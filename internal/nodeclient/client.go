// SPDX-License-Identifier: GPL-3.0-or-later
// Package nodeclient exposes only typed negotiated operations to the egg.
package nodeclient

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
)

type Client struct {
	mu       sync.Mutex
	gate     chan struct{}
	path     string
	wire     *protocol.Client
	caps     map[string]bool
	identity protocol.Welcome
	closed   bool
}

func Connect(ctx context.Context, socketPath string) (*Client, error) {
	c := &Client{path: socketPath, gate: make(chan struct{}, 1)}
	if e := c.connect(ctx); e != nil {
		return nil, e
	}
	return c, nil
}
func (c *Client) connect(ctx context.Context) error {
	conn, e := (&net.Dialer{}).DialContext(ctx, "unix", c.path)
	if e != nil {
		return e
	}
	p := protocol.NewClient(conn)
	var w protocol.Welcome
	if e = p.Call(ctx, "hello", protocol.Hello{}, &w); e != nil {
		p.Close()
		return e
	}
	if c.identity.NodeID != "" && (c.identity.NodeID != w.NodeID || c.identity.ServerID != w.ServerID) {
		p.Close()
		return errors.New("node socket identity changed")
	}
	c.identity = w
	c.caps = map[string]bool{}
	for _, cap := range w.Capabilities {
		c.caps[cap] = true
	}
	c.wire = p
	return nil
}
func (c *Client) Has(cap string) bool { c.mu.Lock(); defer c.mu.Unlock(); return c.caps[cap] }
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.wire != nil {
		return c.wire.Close()
	}
	return nil
}
func (c *Client) call(ctx context.Context, cap, kind string, in, out any) error {
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.gate }()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("node client closed")
	}

	attempts := 1
	// These operations have stable run/event IDs or replace a snapshot. Retry
	// ambiguous transport failure once. Shared reserve/commit use durable IDs;
	// other mutating module actions are never retried.
	switch kind {
	case "run.claim", "event", "status", "status.report", protocol.CapPrepare:
		attempts = 2
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.wire == nil {
			if e := c.connect(ctx); e != nil {
				return e
			}
		}
		if !c.caps[cap] {
			return errors.New("capability unavailable")
		}
		if last = c.wire.Call(ctx, kind, in, out); last == nil {
			return nil
		}
		c.wire.Close()
		c.wire = nil
	}
	return last
}
func (c *Client) Status(ctx context.Context) (server.Status, error) {
	var s server.Status
	e := c.call(ctx, protocol.CapStatus, "status", struct{}{}, &s)
	return s, e
}
func (c *Client) ReportStatus(ctx context.Context, s server.Status) error {
	return c.call(ctx, protocol.CapStatus, "status.report", s, &struct{}{})
}
func (c *Client) ClaimRun(ctx context.Context, run string) (string, error) {
	var r protocol.Ownership
	e := c.call(ctx, protocol.CapEvents, "run.claim", protocol.Claim{RunID: run}, &r)
	if e == nil && r.Owner != "node" && r.Owner != "standalone" {
		e = errors.New("invalid delivery owner")
	}
	return r.Owner, e
}
func (c *Client) PublishEvent(ctx context.Context, event events.Event) (string, error) {
	var r protocol.Receipt
	e := c.call(ctx, protocol.CapEvents, "event", event, &r)
	if e == nil && r.Result != "acknowledged" && r.Result != "filtered" {
		e = errors.New("invalid event receipt")
	}
	return r.Result, e
}
func (c *Client) PrepareGame(ctx context.Context, r protocol.PrepareRequest) (protocol.Preparation, error) {
	var result protocol.Preparation
	e := c.call(ctx, protocol.CapPrepare, protocol.CapPrepare, r, &result)
	return result, e
}
func (c *Client) Cache(ctx context.Context, r protocol.CacheRequest) (protocol.CacheResult, error) {
	var result protocol.CacheResult
	e := c.call(ctx, protocol.CapCache, protocol.CapCache, r, &result)
	return result, e
}
func (c *Client) Workshop(ctx context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
	var result protocol.WorkshopResult
	e := c.call(ctx, protocol.CapWorkshop, protocol.CapWorkshop, r, &result)
	return result, e
}
func (c *Client) ScheduleRestart(ctx context.Context, r protocol.RestartRequest) (protocol.RestartResult, error) {
	var result protocol.RestartResult
	e := c.call(ctx, protocol.CapRestart, protocol.CapRestart, r, &result)
	return result, e
}

// RoutedSink is scoped to ONE runtime invocation. Owner is immutable once known;
// a lost claim response is retried and can never activate the fallback sender.
type RoutedSink struct {
	gate       chan struct{}
	client     *Client
	fallback   events.Sink
	run, owner string
}

func (c *Client) Route(fallback events.Sink) events.Sink {
	owner := ""
	if !c.Has(protocol.CapEvents) {
		owner = "standalone"
	}
	return &RoutedSink{client: c, fallback: fallback, owner: owner, gate: make(chan struct{}, 1)}
}
func (s *RoutedSink) Publish(ctx context.Context, e events.Event) error {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.gate }()
	if s.run == "" {
		s.run = e.RunID
	}
	if e.RunID == "" || s.run != e.RunID {
		return errors.New("delivery route belongs to one run")
	}
	if s.owner == "" {
		owner, err := s.client.ClaimRun(ctx, e.RunID)
		if err != nil {
			return err
		}
		s.owner = owner
	}
	if s.owner == "standalone" {
		var err error
		if s.fallback != nil {
			err = s.fallback.Publish(ctx, e)
		}
		if s.client.Has(protocol.CapEvents) {
			_, _ = s.client.PublishEvent(ctx, e)
		}
		return err
	}
	_, err := s.client.PublishEvent(ctx, e)
	return err
}

// Optional chooses standalone only when initial connection is known unavailable.
// Once connected, any uncertain ownership claim fails closed for that run.
func Optional(ctx context.Context, path string, fallback events.Sink) (events.Sink, func()) {
	c, e := Connect(ctx, path)
	if e != nil {
		return fallback, func() {}
	}
	return c.Route(fallback), func() { c.Close() }
}

// ServerID is authoritative only for this accepted, configured socket. The CLI
// uses it for the active run without rewriting the saved standalone identity.
func (c *Client) ServerID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.identity.ServerID }

// Control reports current evidence before requesting a run-bound host intent.
func (c *Client) Control(ctx context.Context, s server.Status) (protocol.UpdateControl, error) {
	var out protocol.UpdateControl
	if !c.Has(protocol.CapUpdateControl) {
		return out, nil
	}
	if err := c.ReportStatus(ctx, s); err != nil {
		return out, err
	}
	err := c.call(ctx, protocol.CapUpdateControl, protocol.CapUpdateControl, protocol.ControlRequest{RunID: s.RunID}, &out)
	return out, err
}
