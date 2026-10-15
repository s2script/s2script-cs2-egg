// SPDX-License-Identifier: GPL-3.0-or-later

package password

import (
	"os"
	"path/filepath"
)

// WriteFile replaces one file and syncs its directory. committed is true once
// rename succeeds, even if durability cannot be confirmed: callers must then
// reconcile with the visible new file rather than pretend the old one remains.
func WriteFile(path string, data []byte, mode os.FileMode) (committed bool, err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(dir, ".password-*")
	if err != nil {
		return false, err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return false, err
	}
	return true, SyncDir(dir)
}

func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
