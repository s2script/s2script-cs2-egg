// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"os"
	"path/filepath"
	"syscall"
)

func OperatorFile(path string, executable bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("operator-owned absolute path required")
	}
	d, e := artifact.OpenDirectory(filepath.Dir(path))
	if e != nil {
		return e
	}
	f, e := artifact.OpenRegular(d, filepath.Base(path), syscall.O_RDONLY, 0)
	d.Close()
	if e != nil {
		return e
	}
	i, e := f.Stat()
	f.Close()
	if e != nil {
		return e
	}
	if i.Sys().(*syscall.Stat_t).Uid != 0 || i.Mode().Perm()&022 != 0 || executable && i.Mode().Perm()&0111 == 0 {
		return errors.New("launcher/policy must be root-owned and not group/world writable")
	}
	for p := filepath.Dir(path); p != "/"; p = filepath.Dir(p) {
		d, e := artifact.OpenDirectory(p)
		if e != nil {
			return e
		}
		i, e := d.Stat()
		d.Close()
		if e != nil || i.Sys().(*syscall.Stat_t).Uid != 0 || i.Mode().Perm()&022 != 0 {
			return errors.New("launcher/policy ancestors must be root-owned and nonwritable")
		}
	}
	return nil
}
func (m *Manager) ManagedExecutable(version string) bool {
	p, e := os.Executable()
	return e == nil && p == m.binary(version) && os.Getenv("S2NODE_READY_FD") == "3" && os.Getenv("S2NODE_TRIAL_ID") != ""
}
