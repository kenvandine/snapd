// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2017 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package userd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
	. "gopkg.in/check.v1"

	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/osutil/sys"
	"github.com/snapcore/snapd/release"
	"github.com/snapcore/snapd/testutil"
	"github.com/snapcore/snapd/usersession/userd"
)

func Test(t *testing.T) { TestingT(t) }

type launcherSuite struct {
	testutil.BaseTest

	launcher    *userd.Launcher
	mockXdgOpen *testutil.MockCmd
	mockXdgMime *testutil.MockCmd
}

var _ = Suite(&launcherSuite{})

func (s *launcherSuite) SetUpTest(c *C) {
	s.BaseTest.SetUpTest(c)

	s.AddCleanup(release.MockOnClassic(true))
	s.launcher = &userd.Launcher{}
	s.mockXdgOpen = testutil.MockCommand(c, "xdg-open", "")
	s.AddCleanup(s.mockXdgOpen.Restore)
	s.mockXdgMime = testutil.MockCommand(c, "xdg-mime", "")
	s.AddCleanup(s.mockXdgMime.Restore)
	s.AddCleanup(userd.MockSnapFromSender(func(*dbus.Conn, dbus.Sender) (string, error) {
		return "some-snap", nil
	}))

	// openURLOnCore resolves handlers under dirs.SnapDesktopFilesDir, so
	// give it a fresh, empty root instead of this machine's real
	// /var/lib/snapd/desktop/applications. Restored on cleanup since
	// dirs.GlobalRootDir is package-global state shared by other suites.
	oldRootDir := dirs.GlobalRootDir
	dirs.SetRootDir(c.MkDir())
	s.AddCleanup(func() { dirs.SetRootDir(oldRootDir) })
	c.Assert(os.MkdirAll(dirs.SnapDesktopFilesDir, 0755), IsNil)
}

func (s *launcherSuite) TestOpenURLWithNotAllowedScheme(c *C) {
	for _, t := range []struct {
		url        string
		errMatcher string
		scheme     string
	}{
		{"tel://049112233445566", `Supplied URL scheme "tel" is not allowed`, "tel"},
		{"aabbccdd0011", "cannot open URL without a scheme", ""},
		{"invälid:%url", dbus.ErrMsgInvalidArg.Error(), ""},
	} {
		s.mockXdgMime.ForgetCalls()
		err := s.launcher.OpenURL(t.url, ":some-dbus-sender")
		c.Assert(err, ErrorMatches, t.errMatcher)
		c.Assert(s.mockXdgOpen.Calls(), IsNil)
		if t.scheme != "" {
			c.Assert(s.mockXdgMime.Calls(), DeepEquals, [][]string{
				{"xdg-mime", "query", "default", "x-scheme-handler/" + t.scheme},
			})
		} else {
			c.Assert(s.mockXdgMime.Calls(), IsNil)
		}
	}
}

func (s *launcherSuite) TestOpenURLWithAllowedSchemeHappy(c *C) {
	for _, schema := range []string{"http", "https", "mailto", "snap", "help", "apt", "zoommtg", "zoomus", "zoomphonecall", "slack", "msteams", "steam"} {
		err := s.launcher.OpenURL(schema+"://snapcraft.io", ":some-dbus-sender")
		c.Assert(err, IsNil)
		c.Assert(s.mockXdgOpen.Calls(), DeepEquals, [][]string{
			{"xdg-open", schema + "://snapcraft.io"},
		})
		s.mockXdgOpen.ForgetCalls()
	}
}

func (s *launcherSuite) testOpenURLWithFallbackHappy(c *C, desktopFileName string) {
	mockXdgMime := testutil.MockCommand(c, "xdg-mime", fmt.Sprintf(`echo "%s"`, desktopFileName))
	defer mockXdgMime.Restore()
	defer s.mockXdgOpen.ForgetCalls()
	err := s.launcher.OpenURL("fallback-scheme://snapcraft.io", ":some-dbus-sender")
	c.Assert(err, IsNil)
	c.Assert(s.mockXdgOpen.Calls(), DeepEquals, [][]string{
		{"xdg-open", "fallback-scheme://snapcraft.io"},
	})
	c.Assert(mockXdgMime.Calls(), DeepEquals, [][]string{
		{"xdg-mime", "query", "default", "x-scheme-handler/fallback-scheme"},
	})
}

func (s *launcherSuite) testOpenURLWithFallbackInvalidDesktopFile(c *C, desktopFileName string) {
	mockXdgMime := testutil.MockCommand(c, "xdg-mime", fmt.Sprintf("echo %s", desktopFileName))
	defer mockXdgMime.Restore()
	err := s.launcher.OpenURL("fallback-scheme://snapcraft.io", ":some-dbus-sender")
	c.Assert(err, ErrorMatches, `Supplied URL scheme "fallback-scheme" is not allowed`)
	c.Assert(s.mockXdgOpen.Calls(), IsNil)
	c.Assert(mockXdgMime.Calls(), DeepEquals, [][]string{
		{"xdg-mime", "query", "default", "x-scheme-handler/fallback-scheme"},
	})
}

func (s *launcherSuite) TestOpenURLWithFallback(c *C) {
	s.testOpenURLWithFallbackHappy(c, "open-this-scheme.desktop")
	s.testOpenURLWithFallbackHappy(c, "open.this.scheme.desktop")
	s.testOpenURLWithFallbackHappy(c, "org._7_zip.Archiver.desktop")
	s.testOpenURLWithFallbackInvalidDesktopFile(c, "1.2.3.4.desktop")
	s.testOpenURLWithFallbackInvalidDesktopFile(c, "1org.foo.bar.desktop")
	s.testOpenURLWithFallbackInvalidDesktopFile(c, "foo bar baz.desktop")
}

func (s *launcherSuite) TestOpenURLWithFailingXdgOpen(c *C) {
	cmd := testutil.MockCommand(c, "xdg-open", "false")
	defer cmd.Restore()

	err := s.launcher.OpenURL("https://snapcraft.io", ":some-dbus-sender")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "cannot open supplied URL")
}

func mockUICommands(c *C, script string) (restore func()) {
	mock := testutil.MockCommand(c, "zenity", script)
	mock.Also("kdialog", script)

	return func() {
		mock.Restore()
	}
}

func (s *launcherSuite) TestOpenFileUserAccepts(c *C) {
	restore := mockUICommands(c, "true")
	defer restore()

	path := filepath.Join(c.MkDir(), "test.txt")
	c.Assert(os.WriteFile(path, []byte("Hello world"), 0644), IsNil)

	file, err := os.Open(path)
	c.Assert(err, IsNil)
	defer file.Close()

	dupFd, err := syscall.Dup(int(file.Fd()))
	c.Assert(err, IsNil)

	err = s.launcher.OpenFile("", dbus.UnixFD(dupFd), ":some-dbus-sender")
	c.Assert(err, IsNil)
	c.Assert(s.mockXdgOpen.Calls(), DeepEquals, [][]string{
		{"xdg-open", path},
	})
}

func (s *launcherSuite) TestOpenFileUserDeclines(c *C) {
	restore := mockUICommands(c, "false")
	defer restore()

	path := filepath.Join(c.MkDir(), "test.txt")
	c.Assert(os.WriteFile(path, []byte("Hello world"), 0644), IsNil)

	file, err := os.Open(path)
	c.Assert(err, IsNil)
	defer file.Close()

	dupFd, err := syscall.Dup(int(file.Fd()))
	c.Assert(err, IsNil)

	err = s.launcher.OpenFile("", dbus.UnixFD(dupFd), ":some-dbus-sender")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "permission denied")
	c.Assert(s.mockXdgOpen.Calls(), IsNil)
}

func (s *launcherSuite) TestOpenFileSucceedsWithDirectory(c *C) {
	restore := mockUICommands(c, "true")
	defer restore()

	dir := c.MkDir()
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY, 0755)
	c.Assert(err, IsNil)
	defer syscall.Close(fd)

	dupFd, err := syscall.Dup(fd)
	c.Assert(err, IsNil)

	err = s.launcher.OpenFile("", dbus.UnixFD(dupFd), ":some-dbus-sender")
	c.Assert(err, IsNil)
	c.Assert(s.mockXdgOpen.Calls(), DeepEquals, [][]string{
		{"xdg-open", dir},
	})
}

func (s *launcherSuite) TestOpenFileFailsWithDeviceFile(c *C) {
	restore := mockUICommands(c, "true")
	defer restore()

	file, err := os.Open("/dev/null")
	c.Assert(err, IsNil)
	defer file.Close()

	dupFd, err := syscall.Dup(int(file.Fd()))
	c.Assert(err, IsNil)

	err = s.launcher.OpenFile("", dbus.UnixFD(dupFd), ":some-dbus-sender")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "cannot open anything other than regular files or directories")
	c.Assert(s.mockXdgOpen.Calls(), IsNil)
}

func (s *launcherSuite) TestOpenFileFailsWithPathDescriptor(c *C) {
	restore := mockUICommands(c, "true")
	defer restore()

	dir := c.MkDir()
	fd, err := syscall.Open(dir, sys.O_PATH, 0755)
	c.Assert(err, IsNil)
	defer syscall.Close(fd)

	dupFd, err := syscall.Dup(fd)
	c.Assert(err, IsNil)

	err = s.launcher.OpenFile("", dbus.UnixFD(dupFd), ":some-dbus-sender")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "cannot use file descriptors opened using O_PATH")
	c.Assert(s.mockXdgOpen.Calls(), IsNil)
}

func (s *launcherSuite) TestOpenFileFailsOnUbuntuCore(c *C) {
	restore := release.MockOnClassic(false)
	defer restore()

	path := filepath.Join(c.MkDir(), "test.txt")
	c.Assert(os.WriteFile(path, []byte("Hello world"), 0644), IsNil)
	file, err := os.Open(path)
	c.Assert(err, IsNil)
	defer file.Close()
	dupFd, err := syscall.Dup(int(file.Fd()))
	c.Assert(err, IsNil)

	err = s.launcher.OpenFile("", dbus.UnixFD(dupFd), ":some-dbus-sender")
	c.Check(err, ErrorMatches, "not supported on Ubuntu Core")

	c.Check(s.mockXdgOpen.Calls(), HasLen, 0)
}

func (s *launcherSuite) TestOpenURLNoHandlerOnUbuntuCore(c *C) {
	restore := release.MockOnClassic(false)
	defer restore()

	err := s.launcher.OpenURL("https://snapcraft.io", ":some-dbus-sender")
	c.Check(err, ErrorMatches, `no application registered to handle "x-scheme-handler/https"`)
	c.Check(s.mockXdgOpen.Calls(), HasLen, 0)
}

func (s *launcherSuite) TestOpenURLWithHandlerOnUbuntuCore(c *C) {
	restore := release.MockOnClassic(false)
	defer restore()

	mockSystemdRun := testutil.MockCommand(c, "systemd-run", "true")
	defer mockSystemdRun.Restore()

	desktopFile := `[Desktop Entry]
Name=Steam
Exec=/snap/bin/steam %U
MimeType=x-scheme-handler/steam;
Type=Application
`
	c.Assert(os.WriteFile(filepath.Join(dirs.SnapDesktopFilesDir, "steam_steam.desktop"), []byte(desktopFile), 0644), IsNil)

	err := s.launcher.OpenURL("steam://rungameid/945360", ":some-dbus-sender")
	c.Check(err, IsNil)
	c.Check(s.mockXdgOpen.Calls(), HasLen, 0)
	calls := mockSystemdRun.Calls()
	c.Assert(calls, HasLen, 1)
	c.Check(calls[0], DeepEquals, []string{
		"systemd-run", "--user", "--collect", "--",
		"/snap/bin/steam", "steam://rungameid/945360",
	})
}
