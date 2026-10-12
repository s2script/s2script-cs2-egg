// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import "os"

// OpenDirectory pins an absolute directory without traversing any symlink.
func OpenDirectory(path string) (*os.File, error) { return openDir(path) }

// OpenRegular opens a relative regular file without following symlinks.
func OpenRegular(root *os.File, path string, flags int, mode os.FileMode) (*os.File, error) {
	return openFile(root, path, flags, mode)
}

// ParentDirectory pins a relative path's parent, optionally creating directories.
// The caller must close the returned descriptor. It never follows symlinks.
func ParentDirectory(root *os.File, path string, create bool) (*os.File, string, error) {
	return parent(root, path, create)
}

// Rename durably renames a relative entry between pinned, no-symlink parents.
func Rename(root *os.File, from, to string) error { return rename(root, from, to) }

// Remove durably unlinks an entry without following it.
func Remove(root *os.File, path string) error { return remove(root, path) }
