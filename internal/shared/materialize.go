// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
)

const LocalState = ".s2shared.json"
const PendingState = ".s2shared-pending.json"

type Managed struct {
	File   File   `json:"file"`
	Target string `json:"target,omitempty"`
}
type Local struct {
	Dependencies []string   `json:"dependencies,omitempty"`
	Overrides    []string   `json:"overrides,omitempty"`
	Generation   Generation `json:"generation"`
	Reservation  string     `json:"reservation"`
	Mode         string     `json:"mode"`
	Managed      []Managed  `json:"managed"`
}

func ReadLocal(root string) (Local, error) {
	var l Local
	e := readJSON(root, LocalState, &l)
	return l, e
}
func ReadPending(root string) (Local, error) {
	var l Local
	e := readJSON(root, PendingState, &l)
	return l, e
}
func private(p string) bool {
	if p == "game/csgo/gameinfo.gi" {
		return true
	}
	for _, c := range strings.Split(p, "/") {
		switch c {
		case "cfg", "addons", "data", "steamapps", "Steam", "steamcmd", ".steam":
			return true
		}
	}
	return false
}
func excluded(p string, overrides []string) bool {
	for _, o := range overrides {
		if p == o || strings.HasPrefix(p, o+"/") {
			return true
		}
	}
	return false
}

// Persisted exclusions are operator policy across startup/helper handoffs.
// Removing one config declaration does not revoke protection already recorded.
func mergeOverrides(groups ...[]string) ([]string, error) {
	seen := map[string]bool{}
	for _, group := range groups {
		for _, path := range group {
			if !safePath(path) {
				return nil, errors.New("invalid operator override path")
			}
			seen[path] = true
		}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}
func sharedVPK(p string) bool { return !private(p) && strings.HasSuffix(p, ".vpk") }
func infoAt(root *os.File, p string) (os.FileInfo, string, error) {
	d, n, e := artifact.ParentDirectory(root, p, false)
	if e != nil {
		return nil, "", e
	}
	defer d.Close()
	r, e := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", d.Fd()))
	if e != nil {
		return nil, "", e
	}
	defer r.Close()
	info, e := r.Lstat(n)
	if e != nil {
		return nil, "", e
	}
	target := ""
	if info.Mode()&os.ModeSymlink != 0 {
		target, e = r.Readlink(n)
	}
	return info, target, e
}
func matches(root *os.File, m Managed) bool {
	info, target, e := infoAt(root, m.File.Path)
	if e != nil {
		return false
	}
	if m.Target != "" {
		return info.Mode()&os.ModeSymlink != 0 && target == m.Target
	}
	if !info.Mode().IsRegular() || info.Size() != m.File.Size {
		return false
	}
	f, e := artifact.OpenRegular(root, m.File.Path, syscall.O_RDONLY, 0)
	if e != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == m.File.Digest
}
func copyEntry(source, dest *os.File, f File, name string, mode os.FileMode) error {
	in, e := artifact.OpenRegular(source, f.Path, syscall.O_RDONLY, 0)
	if e != nil {
		return e
	}
	defer in.Close()
	d, _, e := artifact.ParentDirectory(dest, name, true)
	if e != nil {
		return e
	}
	d.Close()
	out, e := artifact.OpenRegular(dest, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, mode)
	if e != nil {
		return e
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, f.Size+1))
	if e == nil && (n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.Digest) {
		e = errors.New("generation file changed or corrupt")
	}
	return errors.Join(e, out.Sync(), out.Close())
}

// MaterializeLocked requires the managed caller's runtime lease THEN artifact
// lock and completed Recover. It never acquires either lock. Interrupted work
// must be resumed before consumers start. References retain both generations.
// links is a helper-controlled relative stage with daemon-created hardlinks;
// it is empty for normal container symlink/copy preparation.
func MaterializeLocked(ctx context.Context, root, mount string, g Generation, reservation, mode string, overrides []string, links string) error {
	return materializeLocked(ctx, root, mount, g, reservation, mode, overrides, links, mount)
}
func materializeLocked(ctx context.Context, root, mount string, g Generation, reservation, mode string, overrides []string, links, linkMount string) error {
	if mode != "symlink" && mode != "copy" && mode != "hardlink" {
		return errors.New("shared mode must be symlink, copy or hardlink")
	}

	m, e := LoadManifest(mount, g.ID, g.Digest)
	if e != nil {
		return fmt.Errorf("shared read-only mount/generation unavailable: %w", e)
	}
	g = m.Generation
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	defer d.Close()
	source, e := artifact.OpenDirectory(g.Root)
	if e != nil {
		return e
	}
	defer source.Close()
	old, e := ReadLocal(root)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	pending, e := ReadPending(root)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e == nil && pending.Generation.ID != g.ID {
		return errors.New("resume pending shared generation before selecting another")
	}
	overrides, e = mergeOverrides(overrides, old.Overrides, pending.Overrides)
	if e != nil {
		return e
	}
	if mode == "hardlink" && links == "" {
		if old.Generation.ID != g.ID || old.Mode != "hardlink" {
			return errors.New("hardlink generation must be installed by stopped host helper first; choose explicit copy or symlink mode otherwise")
		}
		for _, f := range m.Files {
			if !sharedVPK(f.Path) || excluded(f.Path, overrides) || excluded(f.Path, old.Overrides) {
				continue
			}
			a, e := artifact.OpenRegular(d, f.Path, syscall.O_RDONLY, 0)
			if e != nil {
				return e
			}
			ai, e := a.Stat()
			a.Close()
			b, e2 := artifact.OpenRegular(source, f.Path, syscall.O_RDONLY, 0)
			if e2 != nil {
				return e2
			}
			bi, e2 := b.Stat()
			b.Close()
			if e != nil || e2 != nil || !os.SameFile(ai, bi) || ai.Mode().Perm()&0222 != 0 || ai.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid()) {
				return errors.New("existing hardlink is not an immutable foreign-owned generation inode")
			}
		}
	}
	previous := map[string]Managed{}
	for _, v := range old.Managed {
		previous[v.File.Path] = v
	}
	next := Local{Generation: g, Reservation: reservation, Mode: mode, Overrides: append([]string(nil), overrides...)}
	var needed uint64 = 16 << 20
	for _, f := range m.Files {
		if mode == "copy" || !sharedVPK(f.Path) {
			needed += uint64(f.Size)
		}
	}
	if e = diskSpace(root, needed); e != nil {
		return e
	}
	if e = writeJSON(root, PendingState, next, 0600); e != nil {
		return e
	}
	stage := ".s2shared-stage-" + randomID()
	if e = syscall.Mkdirat(int(d.Fd()), stage, 0700); e != nil {
		return e
	}
	defer os.RemoveAll(filepath.Join(root, stage))
	for index, f := range m.Files {
		if e = ctx.Err(); e != nil {
			return e
		}
		if excluded(f.Path, overrides) {
			continue
		}
		info, _, statErr := infoAt(d, f.Path)
		exists := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if exists && private(f.Path) {
			continue
		}
		if exists && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return errors.New("target is not a regular file or managed symlink")
		}
		entry := Managed{File: f}
		if mode == "symlink" && sharedVPK(f.Path) {
			entry.Target = filepath.Join(linkMount, "generations", g.ID, f.Path)
		}
		if exists {
			already := matches(d, entry)
			if already && sharedVPK(f.Path) && mode == "copy" {
				already = info.Sys().(*syscall.Stat_t).Nlink == 1 && info.Mode().Perm()&0200 != 0
			}
			if already && sharedVPK(f.Path) && mode == "hardlink" {
				original, e := artifact.OpenRegular(source, f.Path, syscall.O_RDONLY, 0)
				if e != nil {
					return e
				}
				oi, e := original.Stat()
				original.Close()
				if e != nil {
					return e
				}
				already = os.SameFile(info, oi) && info.Mode().Perm()&0222 == 0 && info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid())
			}
			if already {
				next.Managed = append(next.Managed, entry)
				continue
			}
			if prev, ok := previous[f.Path]; ok && !matches(d, prev) {
				next.Overrides = append(next.Overrides, f.Path)
				continue
			}
		}
		temp := fmt.Sprintf("%s/%d", stage, index)
		if entry.Target != "" {
			r, e := os.OpenRoot(filepath.Join(root, stage))
			if e != nil {
				return e
			}
			e = r.Symlink(entry.Target, fmt.Sprint(index))
			r.Close()
			if e != nil {
				return e
			}
		} else if mode == "hardlink" && sharedVPK(f.Path) {
			src := fmt.Sprintf("%s/%d", links, index)
			file, e := artifact.OpenRegular(d, src, syscall.O_RDONLY, 0)
			if e != nil {
				return e
			}
			si, e := file.Stat()
			file.Close()
			original, e2 := artifact.OpenRegular(source, f.Path, syscall.O_RDONLY, 0)
			if e2 != nil {
				return e2
			}
			oi, e2 := original.Stat()
			original.Close()
			if e != nil || e2 != nil || !os.SameFile(si, oi) || si.Mode().Perm()&0222 != 0 || si.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid()) {
				return errors.New("hardlink stage does not preserve immutable foreign-owned inode")
			}
			if e = artifact.Rename(d, src, temp); e != nil {
				return e
			}
		} else {
			perm := os.FileMode(0644)
			if f.Mode&0111 != 0 {
				perm = 0755
			}
			if private(f.Path) {
				perm = 0600
			}
			if e = copyEntry(source, d, f, temp, perm); e != nil {
				return e
			}
		}
		if e = artifact.Rename(d, temp, f.Path); e != nil {
			return e
		}
		if !private(f.Path) {
			next.Managed = append(next.Managed, entry)
		}
	}
	present := map[string]bool{}
	for _, f := range m.Files {
		present[f.Path] = true
	}
	for _, v := range old.Managed {
		if !present[v.File.Path] && !private(v.File.Path) && !excluded(v.File.Path, overrides) && matches(d, v) {
			if e = artifact.Remove(d, v.File.Path); e != nil {
				return e
			}
		}
	}
	next.Dependencies, e = collectDependencies(root, d, mount, append(append([]string(nil), old.Dependencies...), old.Generation.ID, g.ID))
	if e != nil {
		return e
	}
	if e = writeJSON(root, LocalState, next, 0600); e != nil {
		return e
	}
	return artifact.Remove(d, PendingState)
}

// VerifyCopyLocked is the no-container-mount startup path after stopped host
// Attach(copy). It verifies private regular copies against the durable local
// inventory and, when supplied, the node's current immutable descriptor.
func VerifyCopyLocked(root string, want Generation, overrides ...string) (Local, error) {
	var zero Local
	if _, e := ReadPending(root); e == nil {
		return zero, errors.New("copy attachment was interrupted; resume with stopped host helper")
	} else if !errors.Is(e, os.ErrNotExist) {
		return zero, e
	}
	l, e := ReadLocal(root)
	if e != nil || l.Mode != "copy" {
		return zero, errors.New("no shared mount: provision stopped host helper Attach(copy) before startup")
	}
	if want.ID != "" && (l.Generation.ID != want.ID || l.Generation.Digest != want.Digest || l.Generation.BuildID != want.BuildID) {
		return zero, errors.New("new shared generation requires stopped host Attach(copy) before startup without a shared mount")
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return zero, e
	}
	defer d.Close()
	if len(l.Managed) == 0 {
		return zero, errors.New("local copy inventory is empty")
	}
	policy, e := mergeOverrides(l.Overrides, overrides)
	if e != nil {
		return zero, e
	}
	for _, v := range l.Managed {
		if excluded(v.File.Path, policy) {
			continue
		}
		if v.Target != "" || !matches(d, v) {
			return zero, fmt.Errorf("local copy missing or changed: %s; repair using stopped host Attach(copy)", v.File.Path)
		}
		info, _, e := infoAt(d, v.File.Path)
		if e != nil || info.Sys().(*syscall.Stat_t).Nlink != 1 {
			return zero, errors.New("copy startup requires distinct regular inodes")
		}
	}
	if len(policy) != len(l.Overrides) {
		l.Overrides = policy
		if e = writeJSON(root, LocalState, l, 0600); e != nil {
			return zero, e
		}
	}
	return l, nil
}

// Preserve references for operator links too, including immutable hardlinks
// retained from earlier managed generations. Never follow private symlinks.
func collectDependencies(path string, root *os.File, mount string, candidates []string) ([]string, error) {
	ids := map[string]bool{}
	inodes := map[[2]uint64]bool{}
	pattern := regexp.MustCompile(`(?:^|/)generations/([a-f0-9]{32})(?:/|$)`)
	e := filepath.WalkDir(path, func(p string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(path, p)
		if e != nil {
			return e
		}
		info, target, e := infoAt(root, rel)
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if m := pattern.FindStringSubmatch(target); len(m) == 2 {
				ids[m[1]] = true
			}
		} else if info.Mode().IsRegular() && sharedVPK(rel) {
			s := info.Sys().(*syscall.Stat_t)
			if s.Nlink > 1 {
				inodes[[2]uint64{s.Dev, s.Ino}] = true
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	if len(inodes) > 0 {
		for _, id := range candidates {
			if !validID.MatchString(id) {
				continue
			}
			m, e := LoadManifest(mount, id, "")
			if e != nil {
				return nil, e
			}
			d, e := artifact.OpenDirectory(m.Generation.Root)
			if e != nil {
				return nil, e
			}
			for _, f := range m.Files {
				if !sharedVPK(f.Path) {
					continue
				}
				source, e := artifact.OpenRegular(d, f.Path, syscall.O_RDONLY, 0)
				if e != nil {
					d.Close()
					return nil, e
				}
				info, e := source.Stat()
				source.Close()
				if e != nil {
					d.Close()
					return nil, e
				}
				s := info.Sys().(*syscall.Stat_t)
				if inodes[[2]uint64{s.Dev, s.Ino}] {
					ids[id] = true
				}
			}
			d.Close()
		}
	}
	result := []string{}
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}
