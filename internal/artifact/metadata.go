// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func permittedMetadata(i os.FileInfo) error {
	st := i.Sys().(*syscall.Stat_t)
	if st.Uid != uint32(os.Geteuid()) || i.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("private restore requires owned nonprivileged target")
	}
	groups, e := os.Getgroups()
	if e != nil {
		return e
	}
	if int(st.Gid) == os.Getegid() {
		return nil
	}
	for _, g := range groups {
		if g == int(st.Gid) {
			return nil
		}
	}
	return errors.New("target GID not provisioned as helper supplementary group")
}
func setMetadata(f *os.File, i os.FileInfo) error {
	if e := permittedMetadata(i); e != nil {
		return e
	}
	st := i.Sys().(*syscall.Stat_t)
	if e := f.Chown(-1, int(st.Gid)); e != nil {
		return e
	}
	return f.Chmod(i.Mode().Perm())
}
func copyPrivate(root, src *os.File, dest string, preserve bool) error {
	if !preserve {
		return copyFile(root, src, dest)
	}
	i, e := src.Stat()
	if e != nil {
		return e
	}
	if e = permittedMetadata(i); e != nil {
		return e
	}
	if e = copyFile(root, src, dest); e != nil {
		return e
	}
	f, e := openFile(root, dest, syscall.O_RDONLY, 0)
	if e != nil {
		return e
	}
	e = setMetadata(f, i)
	return errors.Join(e, f.Sync(), f.Close())
}
func missingParents(root *os.File, p string) ([]string, error) {
	parts := strings.Split(p, "/")
	var out []string
	for i := 1; i < len(parts); i++ {
		dir := strings.Join(parts[:i], "/")
		f, _, e := parent(root, dir+"/placeholder", false)
		if errors.Is(e, os.ErrNotExist) {
			out = append(out, dir)
			continue
		}
		if e != nil {
			return nil, e
		}
		f.Close()
	}
	return out, nil
}
func makePrivateDir(root *os.File, p string) error {
	d, n, e := parent(root, p, false)
	if e != nil {
		return e
	}
	defer d.Close()
	if e = syscall.Mkdirat(int(d.Fd()), n, 0700); e != nil {
		return e
	}
	return d.Sync()
}
func removePrivateDir(root *os.File, p string) error {
	if e := managedPath(p); e != nil {
		return e
	}
	d, _, e := parent(root, p+"/placeholder", false)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	i, e := d.Stat()
	d.Close()
	if e != nil {
		return e
	}
	if i.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || i.Mode().Perm() != 0700 {
		return errors.New("created restore directory identity changed")
	}
	r, e := os.OpenRoot("/proc/self/fd/" + strconv.Itoa(int(root.Fd())))
	if e != nil {
		return e
	}
	defer r.Close()
	e = r.Remove(p)
	if errors.Is(e, syscall.ENOTEMPTY) {
		return nil
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return root.Sync()
}
