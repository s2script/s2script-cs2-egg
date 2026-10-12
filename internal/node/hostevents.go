// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/events"
)

// Producer policy is closed and bound at module registration, never client input.
func operationalType(producer, kind string, nodeScope bool) bool {
	switch producer {
	case "backup":
		return !nodeScope && (kind == "backup" || kind == "restore")
	case "guard":
		return !nodeScope && kind == "guard"
	case "selfupdate":
		return nodeScope && kind == "node.update"
	case "observability":
		return !nodeScope && (kind == "crash.bundle" || kind == "cleanup")
	case "cleanup":
		return !nodeScope && kind == "cleanup"
	}
	return false
}
func (c *Core) publishOperational(ctx context.Context, producer string, r events.OperationalEvent) (events.Admission, error) {
	out := events.Admission{}
	if e := ctx.Err(); e != nil {
		return out, e
	}
	if !c.safeIdentifiers(r.ServerID, r.ActionID, r.EventKey, r.RelatedRunID) {
		return out, errors.New("secret-bearing operational identity refused")
	}
	if !operationalType(producer, r.Type, r.ServerID == "") {
		return out, errors.New("operational producer/type/scope denied")
	}
	if r.ServerID != "" {
		if _, ok := c.servers[r.ServerID]; !ok {
			return out, errors.New("unknown operational target")
		}
	}
	if r.ActionID == "" || len(r.ActionID) > 128 || r.EventKey == "" || len(r.EventKey) > 128 || len(r.RelatedRunID) > 128 || r.At.IsZero() {
		return out, errors.New("invalid operational identity")
	}
	switch r.Severity {
	case "info", "warning", "error":
	default:
		return out, errors.New("invalid operational severity")
	}
	r.At = r.At.UTC()
	b, e := json.Marshal(r)
	if e != nil || len(b) > maxEventBytes {
		return out, errors.New("operational input exceeds bound")
	}
	requestDigest := key(string(b))
	id := key("host-event-v1", c.cfg.ID, producer, r.ActionID, r.EventKey)
	out.EventID = id
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.storageFailed {
		return out, errors.New("durable storage unavailable")
	}
	_, _, old, ok, e := c.lookup("", id)
	if e != nil {
		return out, e
	}
	if ok {
		if old.RequestDigest != requestDigest {
			return out, errors.New("operational identity reused with different input")
		}
		out.Result = "duplicate"
		return out, nil
	}
	v := events.Event{Version: 2, Origin: "host", Producer: producer, ActionID: r.ActionID, RelatedRunID: r.RelatedRunID, ID: id, NodeID: c.cfg.ID, ServerID: r.ServerID, CorrelationID: r.ActionID, Type: r.Type, Severity: r.Severity, At: r.At, Reason: r.Reason, Details: r.Details}
	v = c.cleanEvent(v)
	b, e = json.Marshal(v)
	if e != nil || len(b) > maxEventBytes {
		return out, errors.New("operational envelope exceeds bound")
	}
	replay, e := c.window(id, requestDigest)
	if e != nil {
		return out, e
	}
	c.broadcast(v, "host_module", replay)
	if c.delivery == nil || !c.delivery.Accepts(v) {
		out.Result = "filtered"
		return out, nil
	}
	out.Result, e = c.enqueue(id, deliveryRecord{Host: true, RequestDigest: requestDigest, Digest: key(string(b)), Event: &v})
	return out, e
}

func (c *Core) PublishOperational(context.Context, events.OperationalEvent) (events.Admission, error) {
	return events.Admission{}, errors.New("operational events require a bound module Host")
}
