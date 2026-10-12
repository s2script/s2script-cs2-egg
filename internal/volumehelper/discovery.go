// SPDX-License-Identifier: GPL-3.0-or-later
package volumehelper

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"time"
)

// Discover authenticates the configured peer and never requests a stopped lease.
func Discover(ctx context.Context, socket string, uid uint32) (releasecontract.Helper, error) {
	var h releasecontract.Helper
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, e := Dial(ctx, socket, uid)
	if e != nil {
		return h, e
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	c.SetDeadline(deadline)
	b, _ := json.Marshal(struct{}{})
	if e = protocol.Write(c, protocol.Message{Version: protocol.Version, ID: "discovery", Type: "info", Payload: b}); e != nil {
		return h, e
	}
	m, e := protocol.Read(c)
	if e != nil {
		return h, e
	}
	if m.Version != protocol.Version || m.ID != "discovery" || m.Type != "info" {
		return h, errors.New("helper discovery unsupported")
	}
	if e = protocol.Decode(m.Payload, &h); e != nil {
		return h, e
	}
	if h.DiscoveryVersion != 1 || h.Protocol.Min != 1 || h.Protocol.Max != 1 {
		return h, errors.New("unknown helper discovery contract")
	}
	return h, nil
}
