// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"sync/atomic"
	"time"
)

type observer struct {
	ch      chan nodetypes.Observation
	dropped atomic.Uint64
}

func copyServer(s Server) Server {
	if s.Helper != nil {
		h := *s.Helper
		s.Helper = &h
	}
	return s
}
func (c *Core) Subscribe(capacity int) (*nodetypes.Subscription, error) {
	if capacity < 1 || capacity > 1024 {
		return nil, errors.New("observer capacity must be 1..1024")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.observers) >= 16 {
		return nil, errors.New("observer quota exceeded")
	}
	o := &observer{ch: make(chan nodetypes.Observation, capacity)}
	c.observers[o] = struct{}{}
	return &nodetypes.Subscription{Events: o.ch, Dropped: o.dropped.Load, Close: func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.observers[o]; ok {
			delete(c.observers, o)
			close(o.ch)
		}
	}}, nil
}
func (c *Core) cleanEvent(e events.Event) events.Event {
	secrets := redaction.Secrets(c.cfg.Webhook.URL)
	clean := func(v string) string { return redaction.Redact(v, secrets) }
	e.ID = clean(e.ID)
	e.RunID = clean(e.RunID)
	e.CorrelationID = clean(e.CorrelationID)
	e.ActionID = clean(e.ActionID)
	e.RelatedRunID = clean(e.RelatedRunID)
	e.Reason = clean(e.Reason)
	e.Type = clean(e.Type)
	e.Severity = clean(e.Severity)
	d := map[string]string{}
	for k, v := range e.Details {
		d[clean(k)] = clean(v)
	}
	e.Details = d
	return e
}

// Called under Core.mu. Provenance is chosen by the ingress, never event text.
func (c *Core) broadcast(e events.Event, source, replay string) {
	c.sequence++
	for o := range c.observers {
		copy := e
		copy.Details = map[string]string{}
		for k, v := range e.Details {
			copy.Details[k] = v
		}
		v := nodetypes.Observation{Event: copy, ReceivedAt: time.Now().UTC(), Source: source, Replay: replay, Sequence: c.sequence, SessionID: c.sessionID}
		if s, ok := c.servers[e.ServerID]; ok {
			owned := copyServer(s)
			v.Server = &owned
		}
		select {
		case o.ch <- v:
		default:
			o.dropped.Add(1)
		}
	}
}
func (c *Core) window(k, digest string) (string, error) {
	if old, ok := c.observed[k]; ok {
		if old != digest {
			return "", errors.New("observed event identity conflict")
		}
		return "window_duplicate", nil
	}
	c.observed[k] = digest
	c.observedOrder = append(c.observedOrder, k)
	if len(c.observedOrder) > 2048 {
		delete(c.observed, c.observedOrder[0])
		c.observedOrder = c.observedOrder[1:]
	}
	return "first_seen_window", nil
}

// Status is a presentation copy; internal replay/owner keys are never rewritten.
func (c *Core) cleanStatus(s server.Status) server.Status {
	secrets := redaction.Secrets(c.cfg.Webhook.URL)
	clean := func(v string) string { return redaction.Redact(v, secrets) }
	s.RunID = clean(s.RunID)
	s.ScheduledActionID = clean(s.ScheduledActionID)
	s.Map = clean(s.Map)
	s.IntendedStopReason = clean(s.IntendedStopReason)
	s.UpdatePending = clean(s.UpdatePending)
	v := map[string]string{}
	for k, x := range s.Versions {
		v[clean(k)] = clean(x)
	}
	s.Versions = v
	return s
}

func (c *Core) safeIdentifiers(values ...string) bool {
	for _, v := range values {
		if redaction.Redact(v, redaction.Secrets(c.cfg.Webhook.URL)) != v {
			return false
		}
	}
	return true
}
