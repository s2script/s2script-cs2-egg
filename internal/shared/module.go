// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"errors"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
)

type Module struct{ Store *Store }

func (m *Module) Name() string { return "shared" }
func (m *Module) Start(ctx context.Context, h node.Host) error {
	if m.Store == nil {
		return errors.New("shared store unavailable")
	}
	u, e := artifact.Acquire(m.Store.root)
	if e != nil {
		return e
	}
	defer u()
	_, e = m.Store.refs()
	if e != nil {
		return e
	} // All unknown/disconnected/unregistered references are retained. No status authorizes deletion.
	m.Store.options.State = h
	m.Store.options.Registered = append([]node.Server(nil), h.Servers()...)
	return ctx.Err()
}
func (m *Module) Health() node.Health         { return node.Health{Status: "healthy"} }
func (m *Module) Close(context.Context) error { return nil }
func (m *Module) Capabilities() []string      { return []string{protocol.CapPrepare} }
func (m *Module) Handle(ctx context.Context, s node.Server, cap string, msg protocol.Message) (any, error) {
	if cap != protocol.CapPrepare {
		return nil, errors.New("unsupported shared action")
	}
	var r protocol.PrepareRequest
	if protocol.Decode(msg.Payload, &r) != nil || r.AppID != 730 {
		return nil, errors.New("invalid app730 preparation")
	}
	switch r.Operation {
	case "reserve":
		ref, e := m.Store.Reserve(ctx, s, r.RunID, r.RequestID)
		if e != nil {
			return nil, e
		}
		manifest, e := LoadManifest(m.Store.root, ref.Generation, "")
		if e != nil {
			return nil, e
		}
		g := manifest.Generation
		return protocol.Preparation{Reservation: ref.ID, Generation: g.ID, BuildID: g.BuildID, Digest: g.Digest}, nil
	case "commit":
		if e := m.Store.Commit(ctx, s.ID, r.Reservation); e != nil {
			return nil, e
		}
		return protocol.Preparation{Reservation: r.Reservation}, nil
	default:
		return nil, errors.New("shared preparation requires reserve or commit")
	}
}
