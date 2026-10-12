// SPDX-License-Identifier: GPL-3.0-or-later
// Package runtimelease serializes managed game lifetime with offline mutations.
package runtimelease

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

var ErrHeld = errors.New("managed runtime lease is held; stop the server before changing artifacts")

// Acquire takes a nonblocking exclusive cross-process lease. The managed
// supervisor MUST acquire it before artifact.Acquire/Recover/startup and hold it
// until the game process has exited (including cleanup on startup failure).
// CLI offline mutations use the same order: runtimelease then artifact.
// Never delete/replace .runtime-lease; absence of a PID file proves nothing.
// This does not detect games launched outside the managed supervisor.
func Acquire(root string) (func() error, error) {
	abs, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	d, e := os.Open("/")
	if e != nil {
		return nil, e
	}
	for _, part := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if part == "" {
			continue
		}
		fd, e := syscall.Openat(int(d.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		d.Close()
		if e != nil {
			return nil, e
		}
		d = os.NewFile(uintptr(fd), part)
	}
	defer d.Close()
	fd, e := syscall.Openat(int(d.Fd()), ".runtime-lease", syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), "runtime lease")
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("runtime lease must be a regular file")
	}
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		if errors.Is(e, syscall.EWOULDBLOCK) || errors.Is(e, syscall.EAGAIN) {
			return nil, ErrHeld
		}
		return nil, e
	}
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() { result = errors.Join(syscall.Flock(fd, syscall.LOCK_UN), f.Close()) })
		return result
	}, nil
}
