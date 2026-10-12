// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/cleanup"
	"github.com/s2script/s2script-cs2-egg/internal/privateops"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"path/filepath"
	"time"
)

func (m *Module) persistOutcome(root, name string, o any) error {
	if m.writeOutcome != nil {
		return m.writeOutcome(root, name, o)
	}
	return storeio.Save(root, name, o)
}
func (m *Module) remember(id string, o outcome) {
	if m.pending == nil {
		m.pending = map[string]outcome{}
	}
	m.pending[id] = o
}
func outcomeResult(o outcome) (any, error) {
	if o.Failure != "" {
		return o.Result, errors.New(o.Failure)
	}
	return o.Result, nil
}
func (m *Module) terminalOutcome(o outcome, r privateops.Reply, e error) outcome {
	o.Phase = "terminal"
	o.Event.At = time.Now().UTC()
	if !r.CompletedAt.IsZero() {
		o.Event.At = r.CompletedAt
	}
	if r.Result != nil && !r.Result.CompletedAt.IsZero() {
		o.Event.At = r.Result.CompletedAt
	}
	r.Error = Redact(r.Error, m.Secrets)
	o.Result = r
	o.Event.Details = map[string]string{"outcome": "completed", "sensitive_dump": "false"}
	if r.Sensitive {
		o.Event.Details["sensitive_dump"] = "true"
	}
	if e != nil {
		o.Failure = Redact(e.Error(), m.Secrets)
		o.Event.Severity = "error"
		o.Event.Details["outcome"] = "failed"
		o.Event.Details["error"] = o.Failure
	}
	return o
}

// Recovery only queries durable evidence. It never invokes the mutation again.
func (m *Module) recoverOutcome(ctx context.Context, o outcome) outcome {
	if o.Phase == "reserved" {
		return m.terminalOutcome(o, privateops.Reply{}, errors.New("operation was not executed before interruption"))
	}
	srv, e := m.server(o.Input.ServerID)
	if e != nil {
		return m.terminalOutcome(o, privateops.Reply{}, e)
	}
	r := o.Input
	var result privateops.Reply
	if r.Private || r.Operation == "dump-retain" {
		if srv.Helper == nil {
			return m.terminalOutcome(o, result, errors.New("completion uncertain: original helper unavailable; work will not be replayed"))
		}
		op := "cleanup-result"
		if r.Operation == "dump-retain" {
			op = "dump-result"
		}
		result, e = privateops.Call(ctx, srv.Helper.Socket, srv.Helper.UID, privateops.Request{Operation: op, RequestID: r.RequestID, ID: r.ID, Namespace: r.Namespace, Offset: r.Offset, Limit: r.Limit, Retain: r.Retain, AgeSeconds: r.AgeSeconds})
	} else {
		if r.Namespace != "logs" && r.Namespace != "crashes" {
			return m.terminalOutcome(o, result, errors.New("invalid recovery namespace"))
		}
		var done bool
		var res cleanup.Result
		res, done, e = cleanup.PlanResult(filepath.Join(m.Root, srv.ID, r.Namespace), r.ID)
		result.Result = &res
		if done {
			result.State = "complete"
		} else {
			result.State = "pending"
		}
	}
	if result.State == "failed" {
		return m.terminalOutcome(o, result, errors.New(result.Error))
	}
	if e != nil || result.State != "complete" {
		return m.terminalOutcome(o, privateops.Reply{}, errors.New("completion uncertain: no durable terminal evidence; inspect artifact and request journal; work will not be replayed"))
	}
	return m.terminalOutcome(o, result, e)
}

// jobView exposes recovered terminal evidence even if the generic worker recorded
// a persistence failure before the independent operational outbox recovered.
func (m *Module) jobView(server, id string) (any, error) {
	j, e := m.jobs.Get(server, id)
	if e != nil {
		return nil, e
	}
	var o outcome
	if storeio.JSON(filepath.Join(m.Root, "outcomes"), j.ID+".json", &o) == nil && o.Phase == "terminal" {
		j.State = "ready"
		j.Error = ""
		if o.Failure != "" {
			j.State = "failed"
			j.Error = o.Failure
		} else {
			j.Output, _ = json.Marshal(o.Result)
		}
	}
	return j, nil
}
