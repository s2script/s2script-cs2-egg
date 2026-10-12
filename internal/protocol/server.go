// SPDX-License-Identifier: GPL-3.0-or-later
package protocol

import (
	"context"
	"encoding/json"
	"net"
	"time"
)

type Handler func(context.Context, Message) (any, error)

// ServeConn serializes requests; every read/handler/write has an absolute bound.
// Handlers must honor context cancellation. Invalid framing/version closes peer.
func ServeConn(ctx context.Context, c net.Conn, h Handler) {
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	for {
		c.SetDeadline(time.Now().Add(Timeout))
		m, e := Read(c)
		if e != nil || m.Version != Version || m.ID == "" || len(m.ID) > 128 || len(m.Type) > 64 {
			return
		}
		request, cancel := context.WithTimeout(ctx, Timeout)
		v, e := h(request, m)
		cancel()
		kind := "ok"
		if e != nil {
			kind = "error"
			v = Failure{Error: e.Error()}
		}
		b, e := json.Marshal(v)
		if e != nil {
			return
		}
		if Write(c, Message{Version: Version, ID: m.ID, Type: kind, Payload: b}) != nil {
			return
		}
	}
}
