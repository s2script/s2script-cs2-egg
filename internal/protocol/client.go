// SPDX-License-Identifier: GPL-3.0-or-later
package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"
)

type Client struct {
	c    net.Conn
	gate chan struct{}
	next uint64
}

func NewClient(c net.Conn) *Client { return &Client{c: c, gate: make(chan struct{}, 1)} }
func (c *Client) Close() error     { return c.c.Close() }
func (c *Client) Call(ctx context.Context, kind string, in, out any) error {
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.gate }()
	if e := ctx.Err(); e != nil {
		return e
	}
	deadline := time.Now().Add(Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.c.Close() })
	defer stop()
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	c.next++
	id := strconv.FormatUint(c.next, 10)
	if e = Write(c.c, Message{Version: Version, ID: id, Type: kind, Payload: b}); e != nil {
		return e
	}
	m, e := Read(c.c)
	if e != nil {
		return e
	}
	if m.Version != Version || m.ID != id {
		return errors.New("invalid response identity")
	}
	if m.Type == "error" {
		var f Failure
		if Decode(m.Payload, &f) != nil {
			return errors.New("invalid error response")
		}
		return errors.New(f.Error)
	}
	if m.Type != "ok" {
		return errors.New("invalid response type")
	}
	return Decode(m.Payload, out)
}
