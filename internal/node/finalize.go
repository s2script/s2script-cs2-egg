// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/privateops"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
)

type runFence struct {
	valid bool
	meta  ownerMeta
	owner string
}

func (c *Core) beginFence(id, run string) (*runFence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.servers[id]; !ok || run == "" || len(run) > 128 || !c.safeIdentifiers(id, run) {
		return nil, errors.New("invalid finalization target")
	}
	k := key(id, run)
	if c.fences[k] != nil {
		return nil, errors.New("reconciliation already in progress")
	}
	owner, meta, _, _, e := c.lookup(k, "")
	if e != nil {
		return nil, e
	}
	if owner == "" {
		return nil, errors.New("unknown run")
	}
	if len(c.fences) >= 256 {
		return nil, errors.New("reconciliation quota")
	}
	f := &runFence{valid: true, meta: meta, owner: owner}
	c.fences[k] = f
	return f, nil
}
func (c *Core) finishFence(id, run string, f *runFence) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(id, run)
	defer delete(c.fences, k)
	owner, meta, _, _, e := c.lookup(k, "")
	if e != nil {
		return e
	}
	if c.fences[k] != f || !f.valid || owner != f.owner || meta != f.meta {
		return errors.New("run activity changed after stopped-reconciliation snapshot")
	}
	s := c.copyState()
	s.Meta[k] = ownerMeta{Server: id, Run: run, Revision: storeio.ID(), Finalized: true}
	if len(s.Meta) > c.cfg.Limits.Runs {
		return errors.New("finalization hot quota; archive first")
	}
	return c.save(s)
}
func (c *Core) FinalizeRun(ctx context.Context, id, run string) error {
	s, ok := c.servers[id]
	if !ok || s.Helper == nil {
		return errors.New("configured UID helper required")
	}
	f, e := c.beginFence(id, run)
	if e != nil {
		return e
	}
	defer func() {
		c.mu.Lock()
		if c.fences[key(id, run)] == f {
			delete(c.fences, key(id, run))
		}
		c.mu.Unlock()
	}()
	return privateops.Stopped(ctx, s.Helper.Socket, s.Helper.UID, storeio.ID(), func() error { return c.finishFence(id, run, f) })
}
