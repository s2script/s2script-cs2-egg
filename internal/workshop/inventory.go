// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
)

// inventoryKnown is a conservative stopped-volume reconstruction. Every physical
// shared reference must be explained by the inventory. An untracked private,
// single-link regular file is local content, never evidence for a store pin.
func inventoryKnown(ctx context.Context, o LocalOptions, local localState, call Exchange) bool {
	root := filepath.Join(o.Root, ContentPath)
	d, e := artifact.OpenDirectory(root)
	if errors.Is(e, os.ErrNotExist) {
		return len(local.Items) == 0
	}
	if e != nil {
		return false
	}
	defer d.Close()
	entries, e := d.ReadDir(4097)
	if e != nil && e != io.EOF || len(entries) > 4096 {
		return false
	}
	covered := map[string]bool{}
	for _, entry := range entries {
		if ctx.Err() != nil || !number(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return false
		}
		g, tracked := local.Items[entry.Name()]
		if tracked {
			if g.AppID != 730 || g.ItemID != entry.Name() || !number(g.Revision) {
				return false
			}
			files, e := snapshot(o, entry.Name(), g)
			if e != nil || manifestDigest(Generation{Files: files}) != g.Digest {
				return false
			}
			covered[entry.Name()] = true
		}
		dir, e := artifact.OpenDirectory(filepath.Join(root, entry.Name()))
		if e != nil {
			return false
		}
		files, e := dir.ReadDir(MaxFiles + 1)
		if e != nil && e != io.EOF || len(files) > MaxFiles {
			dir.Close()
			return false
		}
		hardlinks := false
		for _, f := range files {
			if f.Type()&os.ModeSymlink != 0 {
				if !tracked {
					dir.Close()
					return false
				}
				continue
			}
			file, e := artifact.OpenRegular(dir, f.Name(), syscall.O_RDONLY, 0)
			if e != nil {
				dir.Close()
				return false
			}
			info, e := file.Stat()
			file.Close()
			if e != nil {
				dir.Close()
				return false
			}
			st := info.Sys().(*syscall.Stat_t)
			if st.Nlink > 1 || st.Uid != uint32(os.Geteuid()) {
				if !tracked {
					dir.Close()
					return false
				}
				hardlinks = true
			}
		}
		dir.Close()
		if hardlinks {
			raw, e := storeio.Read(o.Root, ACFPath, 4<<20)
			if e != nil {
				return false
			}
			doc, e := ParseACF(raw)
			if e != nil || verifyExistingHardlinks(ctx, o, doc, g, call) != nil {
				return false
			}
		}
	}
	return len(covered) == len(local.Items)
}
