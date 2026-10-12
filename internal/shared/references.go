// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
)

type Reference struct {
	Revision   string `json:"revision,omitempty"`
	ID         string `json:"id"`
	Server     string `json:"server"`
	Run        string `json:"run"`
	Request    string `json:"request"`
	Generation string `json:"generation"`
	Committed  bool   `json:"committed"`
}
type references struct {
	Version int         `json:"version"`
	Records []Reference `json:"records"`
}

func (s *Store) refs() (references, error) {
	r := references{Version: 1}
	e := readJSON(s.root, "references.json", &r)
	if errors.Is(e, os.ErrNotExist) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	if r.Version != 1 || len(r.Records) > 8192 {
		return r, errors.New("invalid shared reference snapshot")
	}
	for _, v := range r.Records {
		if v.Revision != "" && !validID.MatchString(v.Revision) || !validID.MatchString(v.ID) || !validID.MatchString(v.Generation) || v.Server == "" || v.Run == "" || v.Request == "" {
			return r, errors.New("invalid shared reference")
		}
	}
	return r, nil
}
func (s *Store) Reserve(ctx context.Context, srv node.Server, run, request string) (Reference, error) {
	return s.reserve(ctx, srv.ID, run, request, "")
}
func (s *Store) reserve(ctx context.Context, server, run, request, generation string) (Reference, error) {
	var zero Reference
	if server == "" || run == "" || request == "" || len(run) > 128 || len(request) > 128 || len(server) > 128 {
		return zero, errors.New("bounded server/run/preparation identity required")
	}
	u, e := artifact.Acquire(s.root)
	if e != nil {
		return zero, e
	}
	defer u()
	if e = ctx.Err(); e != nil {
		return zero, e
	}
	r, e := s.refs()
	if e != nil {
		return zero, e
	}
	for i, v := range r.Records {
		if v.Server == server && v.Request == request {
			if v.Run != run || generation != "" && v.Generation != generation {
				return zero, errors.New("preparation identity conflict")
			}
			return s.renew(&r, i)
		}
	}
	// A stopped administrative attachment can reuse an existing durable pin,
	// allowing safe reconciliation even at quota; ordinary run reservations cannot.
	if generation != "" {
		for i, v := range r.Records {
			if v.Server == server && v.Generation == generation {
				return s.renew(&r, i)
			}
		}
	}
	if len(r.Records) >= 8192 {
		return zero, errors.New("shared reference quota exhausted; perform stopped reconciliation")
	}
	var g Generation
	if generation == "" {
		g, e = s.Current()
	} else {
		var m Manifest
		m, e = LoadManifest(s.root, generation, "")
		g = m.Generation
	}
	if e != nil {
		return zero, e
	}
	v := Reference{Revision: randomID(), ID: randomID(), Server: server, Run: run, Request: request, Generation: g.ID}
	r.Records = append(r.Records, v)
	if e = writeJSON(s.root, "references.json", r, 0600); e != nil {
		return zero, e
	}
	return v, nil
}

// Even an idempotent retry renews the durable revision: it is new evidence of
// use that a previously captured administrative fence must not retire.
func (s *Store) renew(r *references, index int) (Reference, error) {
	r.Records[index].Revision = randomID()
	if e := writeJSON(s.root, "references.json", r, 0600); e != nil {
		return Reference{}, e
	}
	return r.Records[index], nil
}
func (s *Store) Commit(ctx context.Context, server, id string) error {
	u, e := artifact.Acquire(s.root)
	if e != nil {
		return e
	}
	defer u()
	if e = ctx.Err(); e != nil {
		return e
	}
	r, e := s.refs()
	if e != nil {
		return e
	}
	for i, v := range r.Records {
		if v.ID == id && v.Server == server {
			r.Records[i].Committed = true
			_, e := s.renew(&r, i)
			return e
		}
	}
	return errors.New("unknown reservation for this server")
}

// referenceFence is captured before requesting the stopped helper transaction.
// A materialized receipt proves that transaction completed, not that its lease
// remains held when the daemon consumes the receipt. Only unchanged records in
// this snapshot can be retired; later runs/transactions always keep their pins.
type referenceFence struct {
	server, keep string
	eligible     map[string]Reference
}

func (s *Store) captureReconciliation(server, keep string) (referenceFence, error) {
	fence := referenceFence{server: server, keep: keep, eligible: map[string]Reference{}}
	unlock, e := artifact.Acquire(s.root)
	if e != nil {
		return fence, e
	}
	defer unlock()
	r, e := s.refs()
	if e != nil {
		return fence, e
	}
	for _, v := range r.Records {
		if v.Server == server {
			fence.eligible[v.ID] = v
		}
	}
	if _, ok := fence.eligible[keep]; !ok {
		return fence, errors.New("attachment reservation missing before helper transaction")
	}
	return fence, nil
}

// reconcile requires the store lock. The fence must predate helper begin, never
// be reconstructed from a delayed receipt, and is not reused across Attach calls.
func (s *Store) reconcile(fence referenceFence, dependencies []string) error {
	server, keep := fence.server, fence.keep
	r, e := s.refs()
	if e != nil {
		return e
	}
	deps := map[string]bool{}
	for _, id := range dependencies {
		if _, e := LoadManifest(s.root, id, ""); e != nil {
			return e
		}
		deps[id] = true
	}
	keptGenerations := map[string]bool{}
	out := r.Records[:0]
	for _, v := range r.Records {
		eligible, captured := fence.eligible[v.ID]
		if v.Server != server || v.ID == keep || !captured || v != eligible || deps[v.Generation] && !keptGenerations[v.Generation] {
			if v.Server == server {
				keptGenerations[v.Generation] = true
			}
			out = append(out, v)
		}
	}
	for id := range deps {
		if !keptGenerations[id] {
			out = append(out, Reference{Revision: randomID(), ID: randomID(), Server: server, Run: "operator-dependency", Request: "dependency-" + id, Generation: id, Committed: true})
		}
	}
	if len(out) > 8192 {
		return errors.New("shared dependency reference quota exhausted")
	}
	r.Records = out
	return writeJSON(s.root, "references.json", r, 0600)
}
func (s *Store) Prune(ctx context.Context) ([]string, error) {
	u, e := artifact.Acquire(s.root)
	if e != nil {
		return nil, e
	}
	defer u()
	r, e := s.refs()
	if e != nil {
		return nil, e
	}
	g, e := s.Current()
	if e != nil {
		return nil, e
	}
	keep := map[string]bool{g.ID: true}
	for _, v := range r.Records {
		keep[v.Generation] = true
	}
	registered := s.options.Registered
	if s.options.State != nil {
		registered = s.options.State.Servers()
	}
	activeBuilds := map[string]bool{}
	for _, srv := range registered {
		known := false
		for _, v := range r.Records {
			if v.Server == srv.ID {
				known = true
				break
			}
		}
		if !known {
			return nil, errors.New("registered server has no durable shared reference; complete shared preparation or stopped helper attachment before pruning")
		}
		if s.options.State != nil {
			status := s.options.State.Status(srv.ID)
			if status.Versions["steam_build"] != "" {
				activeBuilds[status.Versions["steam_build"]] = true
			}
		}
	}
	entries, e := os.ReadDir(filepath.Join(s.root, "generations"))
	if e != nil {
		return nil, e
	}
	var removed []string
	for _, entry := range entries {
		if e = ctx.Err(); e != nil {
			return removed, e
		}
		if !entry.IsDir() || !validID.MatchString(entry.Name()) {
			return removed, errors.New("unexpected generation entry")
		}
		manifest, e := LoadManifest(s.root, entry.Name(), "")
		if e != nil {
			return removed, e
		}
		if activeBuilds[manifest.Generation.BuildID] {
			continue
		}
		if keep[entry.Name()] {
			continue
		}
		if e = removeTree(filepath.Join(s.root, "generations", entry.Name())); e != nil {
			return removed, e
		}
		removed = append(removed, entry.Name())
	}
	d, e := artifact.OpenDirectory(filepath.Join(s.root, "generations"))
	if e != nil {
		return removed, e
	}
	return removed, errors.Join(d.Sync(), d.Close())
}
