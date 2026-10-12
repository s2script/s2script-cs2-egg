// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Receipt struct {
	ID, State, Error string
	CompletedAt      time.Time
	RecoveryID       string
}

func (s *Store) snapshot(p string, dir bool) (Identity, error) {
	root, e := artifact.OpenDirectory(s.volume)
	if e != nil {
		return Identity{}, e
	}
	defer root.Close()
	var f *os.File
	if p == "" {
		f, e = artifact.OpenDirectory(s.volume)
	} else if dir {
		f, _, e = artifact.ParentDirectory(root, p+"/placeholder", false)
	} else {
		f, e = artifact.OpenRegular(root, p, syscall.O_RDONLY, 0)
	}
	if errors.Is(e, os.ErrNotExist) {
		return Identity{}, nil
	}
	if e != nil {
		return Identity{}, e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil {
		return Identity{}, e
	}
	v := statIdentity(i)
	if !dir {
		if i.Size() > s.policy.FileBytes || i.Sys().(*syscall.Stat_t).Nlink != 1 || v.UID != uint32(os.Geteuid()) || i.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return v, errors.New("restore target must be owned single-link nonprivileged file within limit")
		}
		groups, e := os.Getgroups()
		if e != nil {
			return v, e
		}
		group := int(v.GID) == os.Getegid()
		for _, g := range groups {
			group = group || g == int(v.GID)
		}
		if !group {
			return v, errors.New("restore target GID missing from helper supplementary groups")
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(f, s.policy.FileBytes+1))
		if e != nil || n != i.Size() {
			return v, errors.New("target changed or unreadable")
		}
		v.Hash = hex.EncodeToString(h.Sum(nil))
		after, e := f.Stat()
		if e != nil || statIdentity(after) != statIdentity(i) {
			return v, errors.New("target changed during preview")
		}
	}
	return v, nil
}
func (s *Store) Preview(ctx context.Context, id string) (RestorePlan, error) {
	var p RestorePlan
	done, e := s.locks(ctx, false)
	if e != nil {
		return p, e
	}
	defer done()
	if e = s.expirePlans(); e != nil {
		return p, e
	}
	entries, e := os.ReadDir(filepath.Join(s.policy.Root, "plans"))
	if e != nil {
		return p, e
	}
	if len(entries) >= 16 {
		return p, errors.New("restore plan count quota")
	}
	m, e := s.manifest(id)
	if e != nil {
		return p, e
	}
	if _, e = s.verify(ctx, m, ""); e != nil {
		return p, e
	}
	p = RestorePlan{ID: storeio.ID(), ServerID: s.policy.ServerID, RootID: s.policy.RootID, BackupID: id, ArchiveSHA256: m.ArchiveSHA256, CreatedAt: time.Now().UTC(), Parents: map[string]Identity{}}
	p.RecoveryCopy = filepath.Join(s.policy.Root, "archives", "recovery-"+p.ID)
	p.VolumeIdentity, e = s.snapshot("", true)
	if e != nil {
		return p, e
	}
	var planEstimate int64
	for _, f := range m.Files {
		v, e := s.snapshot(f.Path, false)
		if e != nil {
			return p, e
		}
		planEstimate += int64(len(f.Path) + 512)
		if planEstimate > 32<<20 {
			return p, errors.New("restore plan size quota")
		}
		p.Targets = append(p.Targets, Target{f.Path, v})
		for dir := filepath.Dir(f.Path); dir != "."; dir = filepath.Dir(dir) {
			if _, ok := p.Parents[dir]; ok {
				continue
			}
			v, e = s.snapshot(dir, true)
			if e != nil {
				return p, e
			}
			planEstimate += int64(len(dir) + 512)
			if planEstimate > 32<<20 {
				return p, errors.New("restore parent plan quota")
			}
			p.Parents[dir] = v
		}
	}
	b, e := json.Marshal(p)
	if e != nil || len(b) > 32<<20 {
		return p, errors.New("restore plan size quota")
	}
	used, e := treeSize(filepath.Join(s.policy.Root, "plans"))
	if e != nil || used+int64(len(b)) > 64<<20 {
		return p, errors.New("restore plan storage quota")
	}
	p.PlanDigest = storeio.Hash(b)
	b, e = json.Marshal(p)
	if e != nil {
		return p, e
	}
	e = storeio.Write(s.policy.Root, "plans/"+p.ID+".json", b, 0600)
	return p, e
}
func (s *Store) loadPlan(id string) (RestorePlan, error) {
	var p RestorePlan
	if !identifier(id) {
		return p, errors.New("invalid plan ID")
	}
	e := readJSON(s.policy.Root, "plans/"+id+".json", &p, 32<<20)
	if e != nil {
		return p, e
	}
	digest := p.PlanDigest
	p.PlanDigest = ""
	b, e := json.Marshal(p)
	p.PlanDigest = digest
	if e != nil || storeio.Hash(b) != digest || p.ID != id || p.ServerID != s.policy.ServerID || p.RootID != s.policy.RootID || p.RecoveryCopy != filepath.Join(s.policy.Root, "archives", "recovery-"+id) || len(p.Targets) > s.policy.Files {
		return p, errors.New("invalid saved restore plan")
	}
	return p, nil
}
func (s *Store) Status(id string) (Receipt, error) {
	var r Receipt
	if !identifier(id) {
		return r, errors.New("invalid receipt ID")
	}
	e := readJSON(s.policy.Root, "receipts/"+id+".json", &r, 64<<10)
	return r, e
}
func (s *Store) Restore(ctx context.Context, id, digest string) (err error) {
	done, e := s.locks(ctx, false)
	if e != nil {
		return e
	}
	defer done()
	p, e := s.loadPlan(id)
	if e != nil {
		return e
	}
	if !storeio.Digest(digest) || digest != p.PlanDigest {
		return errors.New("restore requires exact preview digest confirmation")
	}
	if time.Since(p.CreatedAt) > 24*time.Hour || time.Until(p.CreatedAt) > time.Minute {
		return errors.New("restore preview expired; preview again")
	}
	if _, e = s.Status(id); !errors.Is(e, os.ErrNotExist) {
		return errors.New("restore already attempted; inspect receipt/recovery and preview again")
	}
	m, e := s.manifest(p.BackupID)
	if e != nil {
		return e
	}
	if m.ArchiveSHA256 != p.ArchiveSHA256 || len(m.Files) != len(p.Targets) {
		return errors.New("archive changed since preview")
	}
	v, e := s.snapshot("", true)
	if e != nil || v != p.VolumeIdentity {
		return errors.New("volume changed since preview")
	}
	for dir, want := range p.Parents {
		v, e = s.snapshot(dir, true)
		if e != nil || v != want {
			return errors.New("restore parent changed since preview")
		}
	}
	var old []selected
	var absent []string
	var budget, rootNew, oldBytes int64
	for i, t := range p.Targets {
		if m.Files[i].Path != t.Path {
			return errors.New("plan members differ")
		}
		v, e = s.snapshot(t.Path, false)
		if e != nil || v != t.Current {
			return errors.New("restore target changed since preview")
		}
		budget += m.Files[i].Size
		rootNew += m.Files[i].Size
		if v.Present {
			budget += v.Size * 2
			oldBytes += v.Size
			old = append(old, selected{t.Path, v})
			old[len(old)-1].identity.Hash = ""
		} else {
			absent = append(absent, t.Path)
		}
	}
	if e = s.recoveryBudget(old); e != nil {
		return e
	}
	if e = s.budget(rootNew+min(oldBytes+oldBytes/100+int64(len(old))*8192+1<<20, s.policy.ArchiveBytes)+1<<20, budget+1<<20); e != nil {
		return e
	}
	stage, e := s.newStage(".stage-restore-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	changes, e := s.verify(ctx, m, stage)
	if e != nil {
		return e
	}
	receipt := Receipt{ID: id, State: "executing", RecoveryID: "recovery-" + id}
	records, e := os.ReadDir(filepath.Join(s.policy.Root, "receipts"))
	if e != nil {
		return e
	}
	if len(records) >= 512 {
		return errors.New("restore receipt quota; operator archival required")
	}
	if e = storeio.Save(s.policy.Root, "receipts/"+id+".json", receipt); e != nil {
		return e
	}
	defer func() {
		receipt.CompletedAt = time.Now().UTC()
		receipt.State = "complete"
		if err != nil {
			receipt.State = "failed"
			receipt.Error = "restore failed; inspect retained recovery and transaction before another preview"
			if _, e := os.Lstat(filepath.Join(s.volume, ".artifact-txn")); !errors.Is(e, os.ErrNotExist) {
				receipt.State = "uncertain"
			}
		}
		if e := storeio.Save(s.policy.Root, "receipts/"+id+".json", receipt); e != nil {
			err = errors.Join(err, errors.New("restore outcome uncertain; saved executing receipt retained"))
		}
	}()
	if _, e = s.seal(ctx, receipt.RecoveryID, "recovery", old, absent); e != nil {
		return e
	}
	return artifact.Apply(ctx, s.volume, changes)
}
func (s *Store) recoveryBudget(files []selected) error {
	entries, e := os.ReadDir(filepath.Join(s.policy.Root, "archives"))
	if e != nil {
		return e
	}
	count := 0
	var n int64
	for _, v := range entries {
		if strings.HasPrefix(v.Name(), "recovery-") {
			count++
			size, e := treeSize(filepath.Join(s.policy.Root, "archives", v.Name()))
			if e != nil {
				return e
			}
			n += size
		}
	}
	var projected int64 = 1 << 20
	for _, v := range files {
		projected += v.identity.Size + 8192
	}
	projected += projected / 100
	if count >= s.policy.Recoveries || n+min(projected, s.policy.ArchiveBytes) > s.policy.RecoveryBytes {
		return errors.New("pinned recovery quota; explicitly release completed recovery before restoring")
	}
	return nil
}
