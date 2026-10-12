// SPDX-License-Identifier: GPL-3.0-or-later
package cleanup

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Preview struct {
	ID         string      `json:"id"`
	Candidates []Candidate `json:"candidates"`
	Result     *Result     `json:"result,omitempty"`
}

// PreviewPlan persists the exact reviewable candidate identities. ApplyPlan never
// accepts caller-supplied candidates or root selectors outside its configured root.
func PreviewPlan(ctx context.Context, root string, rules []Rule) (Preview, error) {
	p := Preview{}
	c, e := Plan(root, rules, time.Now())
	if e != nil {
		return p, e
	}
	if e = ctx.Err(); e != nil {
		return p, e
	}
	dir := filepath.Join(root, ".plans")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return p, e
	}
	u, e := storeio.Lock(ctx, dir)
	if e != nil {
		return p, e
	}
	defer u()
	entries, e := os.ReadDir(dir)
	if e != nil {
		return p, e
	}
	if len(entries) >= 128 {
		return p, errors.New("cleanup plan quota exhausted")
	}
	p = Preview{ID: storeio.ID(), Candidates: c}
	return p, storeio.Save(dir, p.ID+".json", p)
}
func ApplyPlan(ctx context.Context, root, id string) (Result, error) {
	var r Result
	if len(id) != 32 || !validName(id) {
		return r, errors.New("invalid cleanup plan ID")
	}
	dir := filepath.Join(root, ".plans")
	u, e := storeio.Lock(ctx, dir)
	if e != nil {
		return r, e
	}
	defer u()
	var p Preview
	if e = storeio.JSON(dir, id+".json", &p); e != nil {
		return r, e
	}
	if p.ID != id {
		return r, errors.New("cleanup plan identity mismatch")
	}
	if p.Result != nil {
		return *p.Result, nil
	}
	r, e = Apply(ctx, root, p.Candidates)
	if e != nil {
		return r, e
	}
	r.CompletedAt = time.Now().UTC()
	p.Result = &r
	return r, storeio.Save(dir, id+".json", p)
}

// Catalog returns verified sealed artifacts only; it does not list arbitrary files.
func Catalog(root string) ([]Candidate, error) {
	u, e := storeio.Lock(context.Background(), root)
	if e != nil {
		return nil, e
	}
	defer u()
	c, e := read(root)
	if e != nil {
		return nil, e
	}
	out := []Candidate{}
	for _, v := range c.Entries {
		cur, e := identity(root, v.Path)
		cur.ID = v.ID
		if e == nil && cur == v {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// PlanResult reads only persisted completion evidence; it never resumes deletion.
func PlanResult(root, id string) (Result, bool, error) {
	var r Result
	if len(id) != 32 || !validName(id) {
		return r, false, errors.New("invalid cleanup plan ID")
	}
	dir := filepath.Join(root, ".plans")
	u, e := storeio.Lock(context.Background(), dir)
	if e != nil {
		return r, false, e
	}
	defer u()
	var p Preview
	if e = storeio.JSON(dir, id+".json", &p); e != nil {
		return r, false, e
	}
	if p.ID != id {
		return r, false, errors.New("cleanup plan identity mismatch")
	}
	if p.Result == nil {
		return r, false, nil
	}
	return *p.Result, true, nil
}
