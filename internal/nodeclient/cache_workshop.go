// SPDX-License-Identifier: GPL-3.0-or-later
package nodeclient

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"github.com/s2script/s2script-cs2-egg/internal/workshop"
	"io"
	"os"
	"path/filepath"
	"time"
)

func (c *Client) FetchArtifact(ctx context.Context, a protocol.ArtifactSelection, dest string) error {
	r, e := c.Cache(ctx, protocol.CacheRequest{Operation: "submit", RequestID: storeio.ID(), Artifact: a})
	if e != nil {
		return e
	}
	handle := r.Handle
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = c.Cache(cleanup, protocol.CacheRequest{Operation: "release", Handle: handle})
	}()
	for r.State == "pending" || r.State == "running" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		r, e = c.Cache(ctx, protocol.CacheRequest{Operation: "status", Handle: handle})
		if e != nil {
			return e
		}
	}
	if r.State != "ready" {
		return errors.New("node cache: " + r.Error)
	}
	r, e = c.Cache(ctx, protocol.CacheRequest{Operation: "status", Handle: handle})
	if e != nil {
		return e
	}
	if r.SHA256 != a.SHA256 || r.Size < 0 || r.Size > 2<<30 {
		return errors.New("invalid node artifact identity")
	}
	f, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	for offset := int64(0); offset < r.Size; {
		part, e := c.Cache(ctx, protocol.CacheRequest{Operation: "read", Handle: handle, Offset: offset})
		if e != nil {
			return e
		}
		if len(part.Data) == 0 || int64(len(part.Data)) > r.Size-offset {
			return io.ErrUnexpectedEOF
		}
		if _, e = f.Write(part.Data); e != nil {
			return e
		}
		offset += int64(len(part.Data))
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	hash, n, e := storeio.FileHash(filepath.Dir(dest), filepath.Base(dest), 2<<30)
	if e != nil {
		return e
	}
	if hash != a.SHA256 || n != r.Size {
		return errors.New("node artifact digest mismatch")
	}
	return nil
}
func (c *Client) PrepareWorkshopLocked(ctx context.Context, cfg config.Server) error {
	if !cfg.Workshop.Enabled {
		return nil
	}
	if !c.Has(protocol.CapWorkshop) {
		return errors.New("workshop sharing selected but node capability unavailable")
	}
	_, e := workshop.PrepareLocked(ctx, workshop.LocalOptions{Root: cfg.Root, Items: cfg.Workshop.Items, Mode: cfg.Workshop.Mode, Mount: cfg.Workshop.Mount, Wait: cfg.Workshop.Wait()}, c.Workshop)
	return e
}
func (c *Client) ValidateArtifact(ctx context.Context, a protocol.ArtifactSelection) error {
	r, e := c.Cache(ctx, protocol.CacheRequest{Operation: "validate", RequestID: storeio.ID(), Artifact: a})
	if e != nil {
		return e
	}
	handle := r.Handle
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = c.Cache(cleanup, protocol.CacheRequest{Operation: "release", Handle: handle})
	}()
	for r.State == "pending" || r.State == "running" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		r, e = c.Cache(ctx, protocol.CacheRequest{Operation: "status", Handle: handle})
		if e != nil {
			return e
		}
	}
	if r.State != "ready" {
		return errors.New("node artifact policy: " + r.Error)
	}
	return nil
}
