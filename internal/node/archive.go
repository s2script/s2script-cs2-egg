// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"strings"
)

type ArchiveUsage struct {
	Segments, HotOwners, HotRecords, Pending int
	OrphanSegments                           int
	DiskUnavailable                          bool
	DiskBytes                                int64
	Bytes                                    int64
}

// lookup validates every manifest member, even if a hot hit exists. Missing cold
// evidence must never be mistaken for permission to elect a new sender.
func (c *Core) lookup(ownerKey, eventKey string) (owner string, meta ownerMeta, record deliveryRecord, found bool, err error) {
	if len(c.state.Archives) > 64 {
		return "", meta, record, false, errors.New("archive segment quota")
	}
	var total int64
	for _, ref := range c.state.Archives {
		if filepath.Base(ref.Name) != ref.Name || !storeio.Digest(ref.Digest) || ref.Bytes < 1 || ref.Bytes > maxStateBytes {
			return "", meta, record, false, errors.New("invalid archive reference")
		}
		total += ref.Bytes
		if total > 256<<20 {
			return "", meta, record, false, errors.New("archive byte quota")
		}
		b, e := storeio.Read(c.cfg.StateDir, ref.Name, maxStateBytes)
		if e != nil || int64(len(b)) != ref.Bytes || storeio.Hash(b) != ref.Digest {
			return "", meta, record, false, errors.New("ownership archive unavailable or corrupt; admission denied")
		}
		var segment deliveryState
		if json.Unmarshal(b, &segment) != nil || segment.Version != 2 || segment.NodeID != c.cfg.ID || len(segment.Archives) != 0 {
			return "", meta, record, false, errors.New("invalid archive segment")
		}
		if v, ok := segment.Owners[ownerKey]; ok {
			owner = v
		}
		if v, ok := segment.Meta[ownerKey]; ok {
			meta = v
		}
		if v, ok := segment.Records[eventKey]; ok {
			if v.Event != nil {
				return "", meta, record, false, errors.New("archive contains pending delivery")
			}
			record = v
			found = true
		}
	}
	if v, ok := c.state.Owners[ownerKey]; ok {
		owner = v
	}
	if v, ok := c.state.Meta[ownerKey]; ok {
		meta = v
	}
	if v, ok := c.state.Records[eventKey]; ok {
		record = v
		found = true
	}
	return
}
func (c *Core) Archive(ctx context.Context) (ArchiveUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return ArchiveUsage{}, e
	}
	if c.storageFailed {
		return ArchiveUsage{}, errors.New("storage unavailable")
	}
	if _, _, _, _, e := c.lookup("", ""); e != nil {
		return ArchiveUsage{}, e
	}
	segment := deliveryState{Version: 2, NodeID: c.cfg.ID, Owners: map[string]string{}, Meta: map[string]ownerMeta{}, Records: map[string]deliveryRecord{}}
	s := c.copyState()
	for k, v := range s.Owners {
		segment.Owners[k] = v
		delete(s.Owners, k)
	}
	for k, v := range s.Meta {
		segment.Meta[k] = v
		delete(s.Meta, k)
	}
	for k, v := range s.Records {
		if v.Event == nil {
			segment.Records[k] = v
			delete(s.Records, k)
		}
	}
	if len(segment.Owners)+len(segment.Meta)+len(segment.Records) == 0 {
		return c.archiveUsage(), nil
	}
	b, e := json.Marshal(segment)
	if e != nil || len(b) > maxStateBytes {
		return ArchiveUsage{}, errors.New("archive segment exceeds bound")
	}
	usage := c.archiveUsage()
	if usage.DiskUnavailable || usage.Segments+usage.OrphanSegments >= 64 || usage.Bytes+int64(len(b)) > 256<<20 {
		return usage, errors.New("cold archive exhausted; evidence cannot be deleted")
	}
	name := "delivery-archive-" + storeio.ID() + ".json"
	if e = storeio.Write(c.cfg.StateDir, name, b, 0600); e != nil {
		return usage, e
	}
	s.Archives = append(s.Archives, archiveRef{Name: name, Digest: storeio.Hash(b), Bytes: int64(len(b))})
	if e = c.save(s); e != nil {
		return usage, e
	}
	return c.archiveUsage(), nil
}
func (c *Core) archiveUsage() ArchiveUsage {
	u := ArchiveUsage{Segments: len(c.state.Archives), HotOwners: len(c.state.Owners), HotRecords: len(c.state.Records)}
	referenced := map[string]bool{}
	for _, v := range c.state.Archives {
		referenced[v.Name] = true
	}
	files, e := os.ReadDir(c.cfg.StateDir)
	if e != nil {
		u.DiskUnavailable = true
		return u
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Name(), "delivery") {
			continue
		}
		info, e := f.Info()
		if e != nil || !info.Mode().IsRegular() {
			u.DiskUnavailable = true
			continue
		}
		u.DiskBytes += info.Size()
		if strings.HasPrefix(f.Name(), "delivery-archive-") && strings.HasSuffix(f.Name(), ".json") {
			u.Bytes += info.Size()
			if !referenced[f.Name()] {
				u.OrphanSegments++
			}
			delete(referenced, f.Name())
		}
	}
	if len(referenced) > 0 {
		u.DiskUnavailable = true
	}
	for _, v := range c.state.Records {
		if v.Event != nil {
			u.Pending++
		}
	}
	return u
}

// MigrateDelivery is explicit offline administration. Newer formats are never
// downgraded. The caller must stop the daemon; openState's lock enforces this.
func (c *Core) MigrateDelivery() error {
	defer func() {
		if c.lock != nil {
			c.lock.Close()
		}
		if c.stateDir != nil {
			c.stateDir.Close()
		}
		c.lockHeld = false
	}()
	e := c.openState()
	if e == nil {
		return nil
	}
	if !c.lockHeld || c.lock == nil || c.stateDir == nil {
		return e
	}
	b, e := storeio.Read(c.cfg.StateDir, "delivery.json", maxStateBytes)
	if e != nil {
		return e
	}
	var old deliveryState
	if json.Unmarshal(b, &old) != nil || old.Version != 1 || old.NodeID != c.cfg.ID || old.Owners == nil || old.Records == nil || len(old.Meta) != 0 || len(old.Archives) != 0 {
		return errors.New("not a valid version 1 delivery state")
	}
	if len(old.Owners) > c.cfg.Limits.Runs || len(old.Records) > c.cfg.Limits.Records {
		return errors.New("legacy state exceeds quota")
	}
	pending := 0
	for k, r := range old.Records {
		if !storeio.Digest(k) || !storeio.Digest(r.Digest) || r.Host || r.RequestDigest != "" {
			return errors.New("invalid legacy record")
		}
		if r.Event != nil {
			pending++
		}
	}
	if pending > c.cfg.Limits.Pending {
		return errors.New("legacy pending quota")
	}
	for k, v := range old.Owners {
		if !storeio.Digest(k) || (v != "node" && v != "standalone") {
			return errors.New("invalid legacy owner")
		}
	}
	backup := "delivery-before-v2.json"
	if previous, e := storeio.Read(c.cfg.StateDir, backup, maxStateBytes); e == nil {
		if string(previous) != string(b) {
			return errors.New("different pre-migration copy already exists")
		}
	} else if errors.Is(e, os.ErrNotExist) {
		if e = storeio.Write(c.cfg.StateDir, backup, b, 0600); e != nil {
			return e
		}
	} else {
		return e
	}
	old.Version = 2
	c.state = old
	if e = c.persist(old); e != nil {
		return fmt.Errorf("queue migration commit: %w", e)
	}
	return nil
}
