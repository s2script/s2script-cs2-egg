// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Store) expirePlans() error {
	entries, e := os.ReadDir(filepath.Join(s.policy.Root, "plans"))
	if e != nil {
		return e
	}
	if len(entries) > 16 {
		return errors.New("restore plan quota exceeded")
	}
	for _, v := range entries {
		if !strings.HasSuffix(v.Name(), ".json") {
			return errors.New("unexpected plan file")
		}
		id := strings.TrimSuffix(v.Name(), ".json")
		p, e := s.loadPlan(id)
		if e != nil {
			return e
		}
		if time.Since(p.CreatedAt) > 24*time.Hour {
			r, e := s.Status(id)
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
			if r.State == "executing" || r.State == "uncertain" {
				continue
			}
			if e = os.Remove(filepath.Join(s.policy.Root, "plans", v.Name())); e != nil {
				return e
			}
		}
	}
	return syncDir(filepath.Join(s.policy.Root, "plans"))
}
func (s *Store) List() (out []Manifest, err error) {
	entries, e := os.ReadDir(filepath.Join(s.policy.Root, "archives"))
	if e != nil {
		return nil, e
	}
	if len(entries) > s.policy.Retention.Count*4+s.policy.Recoveries+16 {
		return nil, errors.New("catalog count exceeds bound")
	}
	for _, v := range entries {
		m, e := s.manifest(v.Name())
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (s *Store) prune(preserve string) error {
	if e := s.expirePlans(); e != nil {
		return e
	}
	pins := map[string]bool{preserve: true}
	entries, e := os.ReadDir(filepath.Join(s.policy.Root, "plans"))
	if e != nil {
		return e
	}
	for _, v := range entries {
		p, e := s.loadPlan(strings.TrimSuffix(v.Name(), ".json"))
		if e != nil {
			return e
		}
		pins[p.BackupID] = true
	}
	list, e := s.List()
	if e != nil {
		return e
	}
	count := 0
	var n int64
	for _, m := range list {
		if strings.HasPrefix(m.BackupID, "backup-") {
			count++
			n += m.ArchiveBytes
		}
	}
	for _, m := range list {
		if !strings.HasPrefix(m.BackupID, "backup-") || pins[m.BackupID] {
			continue
		}
		if count <= s.policy.Retention.Count && n <= s.policy.Retention.Bytes && time.Since(m.CreatedAt) <= time.Duration(s.policy.Retention.AgeDays)*24*time.Hour {
			continue
		}
		if e = s.removeArchive(m); e != nil {
			return e
		}
		count--
		n -= m.ArchiveBytes
	}
	if count > s.policy.Retention.Count || n > s.policy.Retention.Bytes {
		return errors.New("backup published; retention protected by retained backups or restore plans")
	}
	return nil
}
func (s *Store) removeArchive(m Manifest) error {
	current, e := s.manifest(m.BackupID)
	if e != nil {
		return e
	}
	if current.ArchiveSHA256 != m.ArchiveSHA256 {
		return errors.New("catalog changed during retention")
	}
	p := filepath.Join(s.policy.Root, "archives", m.BackupID)
	entries, e := os.ReadDir(p)
	if e != nil {
		return e
	}
	for _, v := range entries {
		if v.Name() != "archive.tar.gz" && v.Name() != "manifest.json" && v.Name() != "owner.json" {
			return errors.New("unexpected archive data; retention refused")
		}
		i, e := v.Info()
		if e != nil || !i.Mode().IsRegular() {
			return errors.New("unexpected archive entry")
		}
	}
	if e = os.RemoveAll(p); e != nil {
		return e
	}
	return syncDir(filepath.Dir(p))
}
func (s *Store) ReleaseRecovery(ctx context.Context, id, digest string) error {
	unlock, e := s.locks(ctx, false)
	if e != nil {
		return e
	}
	defer unlock()
	if !strings.HasPrefix(id, "recovery-") {
		return errors.New("recovery identity required")
	}
	m, e := s.manifest(id)
	if e != nil {
		return e
	}
	if !storeio.Digest(digest) || digest != m.ArchiveSHA256 {
		return errors.New("recovery release requires exact archive digest")
	}
	r, e := s.Status(strings.TrimPrefix(id, "recovery-"))
	if e != nil || r.State != "complete" && r.State != "failed" {
		return errors.New("active or uncertain recovery is pinned")
	}
	entries, e := os.ReadDir(filepath.Join(s.policy.Root, "plans"))
	if e != nil {
		return e
	}
	for _, v := range entries {
		p, e := s.loadPlan(strings.TrimSuffix(v.Name(), ".json"))
		if e != nil {
			return e
		}
		if p.BackupID == id {
			return errors.New("recovery pinned by restore preview")
		}
	}
	return s.removeArchive(m)
}
