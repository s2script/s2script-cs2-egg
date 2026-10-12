// SPDX-License-Identifier: GPL-3.0-or-later
package nodeclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"syscall"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/shared"
)

// Prepare is invoked under the caller-owned runtime/artifact locks. The node
// only reserves immutable host generations; all private writes stay local.
func (c *Client) Prepare(ctx context.Context, cfg config.Server) (bool, error) {
	if !c.Has(protocol.CapPrepare) {
		return false, nil
	}
	mount := cfg.Shared.Mount
	if mount == "" {
		mount = shared.MountRoot
	}
	mode := cfg.Shared.Mode
	if mode == "" {
		mode = "symlink"
	}
	d, e := artifact.OpenDirectory(mount)
	mounted := false
	if e == nil {
		var stat syscall.Statfs_t
		e = syscall.Fstatfs(int(d.Fd()), &stat)
		d.Close()
		mounted = e == nil && stat.Flags&syscall.MS_RDONLY != 0
	}
	offlineCopy := mode == "copy" && !mounted
	if offlineCopy {
		if _, e = shared.VerifyCopyLocked(cfg.Root, shared.Generation{}, cfg.Shared.Overrides...); e != nil {
			return true, e
		}
	} else if !mounted {
		return true, errors.New("shared read-only mount unavailable; configure the Wings read-only mount before preparation")
	}
	commit := func(reservation string) error {
		_, e := c.PrepareGame(ctx, protocol.PrepareRequest{AppID: 730, Operation: "commit", Reservation: reservation})
		return e
	}
	if pending, e := shared.ReadPending(cfg.Root); e == nil {
		if e = shared.MaterializeLocked(ctx, cfg.Root, mount, pending.Generation, pending.Reservation, pending.Mode, cfg.Shared.Overrides, ""); e != nil {
			return true, e
		}
		if e = commit(pending.Reservation); e != nil {
			return true, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return true, e
	}
	var random [16]byte
	if _, e = rand.Read(random[:]); e != nil {
		return true, e
	}
	id := hex.EncodeToString(random[:])
	run := server.PreparationRunID(ctx)
	if run == "" {
		run = "prepare-" + id
	}
	r, e := c.PrepareGame(ctx, protocol.PrepareRequest{AppID: 730, Operation: "reserve", RunID: run, RequestID: id, Validate: cfg.Steam.Validate})
	if e != nil {
		return true, e
	}
	g := shared.Generation{ID: r.Generation, BuildID: r.BuildID, Digest: r.Digest}
	if offlineCopy {
		if _, e = shared.VerifyCopyLocked(cfg.Root, g, cfg.Shared.Overrides...); e != nil {
			return true, e
		}
		return true, commit(r.Reservation)
	}
	if e = shared.MaterializeLocked(ctx, cfg.Root, mount, g, r.Reservation, mode, cfg.Shared.Overrides, ""); e != nil {
		return true, e
	}
	return true, commit(r.Reservation)
}
