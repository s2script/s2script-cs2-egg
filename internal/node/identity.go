// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// openDirectory pins every component without following symlinks. Server volume
// and egg directories must be provisioned by the host before daemon startup.
func openDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("directory must be absolute and clean")
	}
	f, e := os.Open("/")
	if e != nil {
		return nil, e
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		fd, e := syscall.Openat(int(f.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		f.Close()
		if e != nil {
			return nil, errors.New("directory unavailable or contains symlink")
		}
		f = os.NewFile(uintptr(fd), part)
	}
	return f, nil
}
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, e
	}
	var cred *syscall.Ucred
	var inner error
	e = raw.Control(func(fd uintptr) {
		cred, inner = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if e != nil {
		return 0, e
	}
	if inner != nil {
		return 0, inner
	}
	return cred.Uid, nil
}
func groupAllowed(gid int) bool {
	if gid == os.Getegid() {
		return true
	}
	groups, _ := os.Getgroups()
	for _, g := range groups {
		if g == gid {
			return true
		}
	}
	return false
}

type socket struct {
	listener *net.UnixListener
	dir      *os.File
	name     string
	info     os.FileInfo
}

func bindSocket(dir *os.File, name string, gid int, private bool) (*socket, error) {
	path := fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), name)
	if info, e := os.Lstat(path); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("socket path is not a socket")
		} // Never replace a live listener; stale sockets are safe only under core lock.
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
			return nil, errors.New("refusing to replace a socket owned by another user")
		}
		c, e := net.DialTimeout("unix", path, 250*time.Millisecond)
		if e == nil {
			c.Close()
			return nil, errors.New("socket already active")
		}
		if !errors.Is(e, syscall.ECONNREFUSED) {
			return nil, errors.New("existing socket is not confirmed stale")
		}
		if e = os.Remove(path); e != nil {
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return nil, e
	}
	l.SetUnlinkOnClose(false)
	cleanup := func() { l.Close(); os.Remove(path) }
	mode := os.FileMode(0660)
	if private {
		mode = 0600
	} else {
		if !groupAllowed(gid) {
			cleanup()
			return nil, errors.New("daemon does not belong to socket access group")
		}
		if e = os.Lchown(path, -1, gid); e != nil {
			cleanup()
			return nil, e
		}
	}
	confined, openErr := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", dir.Fd()))
	if openErr != nil {
		cleanup()
		return nil, openErr
	}
	defer confined.Close()
	if e = confined.Chmod(name, mode); e != nil {
		cleanup()
		return nil, e
	}
	info, e := os.Lstat(path)
	if e != nil || info.Mode()&os.ModeSocket == 0 {
		cleanup()
		return nil, errors.New("socket path changed during initialization")
	}
	return &socket{listener: l, dir: dir, name: name, info: info}, nil
}
func (s *socket) close() {
	s.listener.Close()
	path := fmt.Sprintf("/proc/self/fd/%d/%s", s.dir.Fd(), s.name)
	if current, e := os.Lstat(path); e == nil && os.SameFile(current, s.info) {
		syscall.Unlinkat(int(s.dir.Fd()), s.name)
	}
	s.dir.Close()
}
