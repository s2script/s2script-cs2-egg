// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/crashes"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"path/filepath"
)

// LocalSink runs behind the runtime's bounded serial queue, never process I/O.
// Both local and node tails are explicitly best effort if that queue drops facts.
type LocalSink struct {
	Next    events.Sink
	logs    *Logs
	crashes *crashes.Store
	status  server.Status
}

func NewLocal(cfg config.Server, next events.Sink) (*LocalSink, error) {
	o := cfg.Observability
	if e := o.Validate(); e != nil {
		return nil, e
	}
	root := filepath.Join(cfg.Root, "egg", "observability")
	secrets := redaction.Secrets(cfg.Steam.GSLT, cfg.Webhook.URL)
	l, e := NewLogs(root, LogOptions{o.LogBytes, o.LogFiles, o.TailBytes}, secrets)
	if e != nil {
		return nil, e
	}
	c, e := crashes.New(root, crashes.Options{Bytes: o.CrashBytes, Retained: o.CrashFiles}, secrets)
	if e != nil {
		return nil, e
	}
	return &LocalSink{Next: next, logs: l, crashes: c}, nil
}
func (s *LocalSink) Publish(ctx context.Context, e events.Event) error {
	var err error
	if e.Type == "server.console" {
		err = s.logs.Append(e.ServerID, []byte(e.Details["line"]+"\n"))
	}
	if e.Type == "server.status" {
		var status server.Status
		if json.Unmarshal([]byte(e.Details["runtime_status"]), &status) == nil && status.RunID == e.RunID {
			s.status = status
		}
	}
	if crashes.ShouldCapture(e) {
		status := s.status
		if status.RunID != e.RunID {
			status = server.Status{RunID: e.RunID}
		}
		_, ce := s.crashes.Capture(ctx, nodetypes.Server{ID: e.ServerID}, status, s.logs.Tail(e.ServerID))
		err = errors.Join(err, ce)
	}
	if s.Next != nil {
		err = errors.Join(err, s.Next.Publish(ctx, e))
	}
	return err
}
func (s *LocalSink) Close() error { return s.logs.Close() }
