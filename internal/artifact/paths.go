// SPDX-License-Identifier: GPL-3.0-or-later
// Package artifact provides bounded staging and recoverable (not atomic multi-file)
// updates. Supported runtime: Linux. The managed tree must not be renamed out of
// its root by another writer; callers serialize writers with Acquire.
package artifact

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validPath(p string) error {
	if p == "" || p == "." || filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\\:\x00") {
		return errors.New("invalid relative artifact path")
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." || s == "." || s == "" {
			return errors.New("invalid artifact component")
		}
	}
	return nil
}

// openDir walks pinned descriptors using O_NOFOLLOW for every component. A
// concurrent symlink replacement can cause an error, never traversal of a link.
func openDir(p string) (*os.File, error) {
	abs, e := filepath.Abs(p)
	if e != nil {
		return nil, e
	}
	f, e := os.Open("/")
	if e != nil {
		return nil, e
	}
	for _, s := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if s == "" {
			continue
		}
		fd, e := syscall.Openat(int(f.Fd()), s, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			syscall.Close(fd)
			return nil, ce
		}
		f = os.NewFile(uintptr(fd), s)
	}
	return f, nil
}
func parent(root *os.File, p string, create bool) (*os.File, string, error) {
	if e := validPath(p); e != nil {
		return nil, "", e
	}
	fd, e := syscall.Dup(int(root.Fd()))
	if e != nil {
		return nil, "", e
	}
	syscall.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), "parent")
	parts := strings.Split(p, "/")
	for _, s := range parts[:len(parts)-1] {
		n, e := syscall.Openat(int(f.Fd()), s, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if e == syscall.ENOENT && create {
			if e = syscall.Mkdirat(int(f.Fd()), s, 0755); e != nil && e != syscall.EEXIST {
				f.Close()
				return nil, "", e
			}
			if e = f.Sync(); e != nil {
				f.Close()
				return nil, "", e
			}
			n, e = syscall.Openat(int(f.Fd()), s, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		}
		ce := f.Close()
		if e != nil {
			return nil, "", e
		}
		if ce != nil {
			syscall.Close(n)
			return nil, "", ce
		}
		f = os.NewFile(uintptr(n), s)
	}
	return f, parts[len(parts)-1], nil
}
func openFile(root *os.File, p string, flags int, mode os.FileMode) (*os.File, error) {
	d, n, e := parent(root, p, false)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	fd, e := syscall.Openat(int(d.Fd()), n, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, uint32(mode.Perm()))
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), p)
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("artifact must be a regular file")
	}
	return f, nil
}
func rename(root *os.File, from, to string) error {
	a, an, e := parent(root, from, false)
	if e != nil {
		return e
	}
	defer a.Close()
	b, bn, e := parent(root, to, true)
	if e != nil {
		return e
	}
	defer b.Close()
	if e = syscall.Renameat(int(a.Fd()), an, int(b.Fd()), bn); e != nil {
		return e
	}
	return errors.Join(a.Sync(), b.Sync())
}
func remove(root *os.File, p string) error {
	d, n, e := parent(root, p, false)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer d.Close()
	e = syscall.Unlinkat(int(d.Fd()), n)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	return d.Sync()
}
func ensureParent(root *os.File, p string) error {
	d, _, e := parent(root, p, true)
	if e != nil {
		return e
	}
	return d.Close()
}
func copyFile(root *os.File, src *os.File, dest string) error {
	info, e := src.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return errors.New("source must be regular")
	}
	if e = ensureParent(root, dest); e != nil {
		return e
	}
	out, e := openFile(root, dest, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, info.Mode().Perm())
	if e != nil {
		return e
	}
	_, e = io.Copy(out, src)
	if e == nil {
		e = out.Chmod(info.Mode().Perm())
	}
	if e == nil {
		e = out.Sync()
	}
	return errors.Join(e, out.Close())
}
