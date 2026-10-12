// SPDX-License-Identifier: GPL-3.0-or-later
// Package cleanup only removes sealed manager-cataloged artifacts. Callers must
// exclude unmanaged writers; private volume callers also hold the runtime lease.
package cleanup

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const catalogName = ".s2-cleanup.json"

type Rule struct {
	Pattern     string        `json:"pattern"`
	Age         time.Duration `json:"age"`
	MaxRetained int           `json:"max_retained"`
}
type Candidate struct {
	Path          string `json:"path"`
	ID            string `json:"id"`
	Device, Inode uint64
	Mode          uint32
	Size          int64
	MTime, CTime  syscall.Timespec
}
type Result struct {
	CompletedAt time.Time         `json:"completed_at,omitempty"`
	Removed     []string          `json:"removed"`
	Skipped     map[string]string `json:"skipped"`
	Bytes       int64             `json:"bytes"`
}
type catalog struct {
	Version   int
	Namespace string
	Entries   map[string]Candidate
}

func validName(s string) bool {
	return s != "" && s == filepath.Base(s) && s != "." && s != ".." && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, "\\\x00:")
}
func read(root string) (catalog, error) {
	var c catalog
	e := storeio.JSON(root, catalogName, &c)
	if e != nil {
		return c, e
	}
	if c.Version != 1 || (c.Namespace != "logs" && c.Namespace != "crashes" && c.Namespace != "dumps") || c.Entries == nil || len(c.Entries) > 4096 {
		return c, errors.New("not a managed cleanup namespace")
	}
	return c, nil
}
func Initialize(root, namespace string) error {
	if namespace != "logs" && namespace != "crashes" && namespace != "dumps" {
		return errors.New("unsupported cleanup namespace")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return e
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	info, e := d.Stat()
	d.Close()
	if e != nil || info.Mode().Perm()&0077 != 0 {
		return errors.New("cleanup root must be private")
	}
	u, e := storeio.Lock(context.Background(), root)
	if e != nil {
		return e
	}
	defer u()
	c, e := read(root)
	if e == nil {
		if c.Namespace != namespace {
			return errors.New("namespace mismatch")
		}
		return nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return storeio.Save(root, catalogName, catalog{1, namespace, map[string]Candidate{}})
}
func identity(root, name string) (Candidate, error) {
	if !validName(name) {
		return Candidate{}, errors.New("invalid managed name")
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return Candidate{}, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, name, syscall.O_RDONLY, 0)
	if e != nil {
		return Candidate{}, e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil {
		return Candidate{}, e
	}
	s := i.Sys().(*syscall.Stat_t)
	if s.Nlink != 1 || s.Uid != uint32(os.Geteuid()) {
		return Candidate{}, errors.New("linked or foreign artifact")
	}
	return Candidate{Path: name, Device: uint64(s.Dev), Inode: s.Ino, Mode: s.Mode, Size: s.Size, MTime: s.Mtim, CTime: s.Ctim}, nil
}
func Seal(root, name string) error {
	u, e := storeio.Lock(context.Background(), root)
	if e != nil {
		return e
	}
	defer u()
	c, e := read(root)
	if e != nil {
		return e
	}
	v, e := identity(root, name)
	if e != nil {
		return e
	}
	if old, ok := c.Entries[name]; ok {
		v.ID = old.ID
		if v == old {
			return nil
		}
		return errors.New("sealed artifact changed")
	}
	if len(c.Entries) >= 4096 {
		return errors.New("managed artifact quota")
	}
	v.ID = storeio.ID()
	c.Entries[name] = v
	return storeio.Save(root, catalogName, c)
}
func Plan(root string, rules []Rule, now time.Time) ([]Candidate, error) {
	u, e := storeio.Lock(context.Background(), root)
	if e != nil {
		return nil, e
	}
	defer u()
	c, e := read(root)
	if e != nil {
		return nil, e
	}
	if len(rules) > 32 {
		return nil, errors.New("rule bound")
	}
	chosen := map[string]Candidate{}
	for _, r := range rules {
		if !validName(r.Pattern) || r.Age < 0 || r.MaxRetained < 0 || r.MaxRetained > 4096 {
			return nil, errors.New("invalid contained cleanup rule")
		}
		if _, e = filepath.Match(r.Pattern, ""); e != nil {
			return nil, e
		}
		var match []Candidate
		for _, v := range c.Entries {
			ok, _ := filepath.Match(r.Pattern, v.Path)
			if !ok {
				continue
			}
			cur, e := identity(root, v.Path)
			cur.ID = v.ID
			if e == nil && cur == v {
				match = append(match, v)
			}
		}
		sort.Slice(match, func(i, j int) bool {
			a, b := match[i], match[j]
			if a.MTime == b.MTime {
				return a.Path > b.Path
			}
			return time.Unix(a.MTime.Sec, a.MTime.Nsec).After(time.Unix(b.MTime.Sec, b.MTime.Nsec))
		})
		for i, v := range match {
			if i >= r.MaxRetained && now.Sub(time.Unix(v.MTime.Sec, v.MTime.Nsec)) >= r.Age {
				chosen[v.Path] = v
			}
		}
	}
	out := []Candidate{}
	for _, v := range chosen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func Apply(ctx context.Context, root string, candidates []Candidate) (Result, error) {
	r := Result{Skipped: map[string]string{}}
	if len(candidates) > 4096 {
		return r, errors.New("candidate bound")
	}
	u, e := storeio.Lock(ctx, root)
	if e != nil {
		return r, e
	}
	defer u()
	c, e := read(root)
	if e != nil {
		return r, e
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return r, e
	}
	defer d.Close()
	for _, v := range candidates {
		if e = ctx.Err(); e != nil {
			return r, e
		}
		old, ok := c.Entries[v.Path]
		cur, err := identity(root, v.Path)
		cur.ID = v.ID
		if !ok || old != v || err != nil || cur != v {
			r.Skipped[v.Path] = "changed, absent, or not sealed"
			continue
		}
		if e = artifact.Remove(d, v.Path); e != nil {
			return r, e
		}
		delete(c.Entries, v.Path)
		r.Removed = append(r.Removed, v.Path)
		r.Bytes += v.Size
	}
	return r, storeio.Save(root, catalogName, c)
}
