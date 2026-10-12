// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
)

const maxLinkInfoBytes = 256 << 10

// linkInfo returns physical identity only for a pinned, selected exact generation.
// It never accepts a path, follows a symlink or reads private volume files.
func (s *Store) linkInfo(ctx context.Context, server string, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
	var out protocol.WorkshopResult
	if !s.selected(r.ItemID) {
		return out, errors.New("item not selected by node policy")
	}
	unlock, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return out, e
	}
	defer unlock()
	v, e := s.load()
	if e != nil {
		return out, e
	}
	ref, ok := v.References[server+":"+r.ItemID+"/"+r.Revision]
	if !ok || ref.Server != server || ref.Key != r.ItemID+"/"+r.Revision {
		return out, errors.New("generation not reserved")
	}
	g, e := s.Generation(r.ItemID, r.Revision)
	if e != nil {
		return out, e
	}
	if g.Digest != r.Digest {
		return out, errors.New("link generation identity mismatch")
	}
	d, e := artifact.OpenDirectory(filepath.Join(s.root, "generations", g.Key()))
	if e != nil {
		return out, e
	}
	defer d.Close()
	info := &protocol.WorkshopLinkInfo{AppID: g.AppID, ItemID: g.ItemID, Revision: g.Revision, Digest: g.Digest}
	for _, f := range g.Files {
		file, e := artifact.OpenRegular(d, f.Name, syscall.O_RDONLY, 0)
		if e != nil {
			return out, e
		}
		st, e := file.Stat()
		file.Close()
		if e != nil {
			return out, e
		}
		physical := st.Sys().(*syscall.Stat_t)
		if st.Size() != f.Size || st.Mode().Perm() != 0444 || physical.Uid != uint32(os.Geteuid()) {
			return out, errors.New("link source must be immutable daemon-owned generation")
		}
		info.Files = append(info.Files, protocol.WorkshopLink{Name: f.Name, Device: uint64(physical.Dev), Inode: physical.Ino, UID: physical.Uid, Mode: uint32(st.Mode().Perm())})
	}
	out = protocol.WorkshopResult{State: "ready", Links: info}
	b, e := json.Marshal(out)
	if e != nil || len(b) > maxLinkInfoBytes {
		return protocol.WorkshopResult{}, errors.New("link metadata exceeds frame budget")
	}
	return out, nil
}

func verifyExistingHardlinks(ctx context.Context, o LocalOptions, doc *Document, g Generation, call Exchange) error {
	local, e := loadLocal(o.Root)
	if e != nil {
		return e
	}
	old, ok := local.Items[g.ItemID]
	if !ok || old.AppID != 730 || g.AppID != 730 || old.ItemID != g.ItemID || old.Revision != g.Revision || old.Digest != g.Digest {
		return errors.New("local generation identity differs")
	}
	rev, size, e := doc.Revision(g.ItemID)
	if e != nil || rev != g.Revision {
		return errors.New("private ACF revision differs")
	}
	var total int64
	for _, f := range g.Files {
		total += f.Size
	}
	if size != total {
		return errors.New("private ACF size differs")
	}
	if call == nil {
		return errors.New("authenticated source identity unavailable")
	}
	r, e := call(ctx, protocol.WorkshopRequest{Operation: "link-info", ItemID: g.ItemID, Revision: g.Revision, Digest: g.Digest})
	if e != nil {
		return e
	}
	info := r.Links
	if r.State != "ready" || info == nil || info.AppID != 730 || info.ItemID != g.ItemID || info.Revision != g.Revision || info.Digest != g.Digest || len(info.Files) != len(g.Files) {
		return errors.New("source generation identity differs")
	}
	sources := map[string]protocol.WorkshopLink{}
	for _, f := range info.Files {
		if _, ok := sources[f.Name]; ok {
			return errors.New("duplicate link source")
		}
		sources[f.Name] = f
	}
	files, e := snapshot(o, g.ItemID, g)
	if e != nil || manifestDigest(Generation{Files: files}) != g.Digest {
		return errors.New("hardlink content differs")
	}
	d, e := artifact.OpenDirectory(filepath.Join(o.Root, ContentPath, g.ItemID))
	if e != nil {
		return e
	}
	defer d.Close()
	for _, f := range g.Files {
		source, ok := sources[f.Name]
		if !ok {
			return errors.New("missing source identity")
		}
		file, e := artifact.OpenRegular(d, f.Name, syscall.O_RDONLY, 0)
		if e != nil {
			return e
		}
		st, e := file.Stat()
		file.Close()
		if e != nil {
			return e
		}
		physical := st.Sys().(*syscall.Stat_t)
		if source.UID == uint32(os.Geteuid()) || physical.Uid != source.UID || source.Mode != 0444 || st.Mode().Perm() != 0444 || uint64(physical.Dev) != source.Device || physical.Ino != source.Inode || st.Size() != f.Size {
			return errors.New("local file is not the immutable source inode")
		}
	}
	return nil
}
