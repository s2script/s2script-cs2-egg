// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/cleanup"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/crashes"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/nodejobs"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/privateops"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Module struct {
	opMu         sync.Mutex
	pending      map[string]outcome
	writeOutcome func(string, string, any) error
	Root         string
	Config       config.Observability
	Secrets      []string
	host         node.Host
	logs         *Logs
	crashes      *crashes.Store
	sub          *node.Subscription
	jobs         *nodejobs.Pool
	cancel       context.CancelFunc
	done         chan struct{}
	stopMetrics  func(context.Context) error
	mu           sync.Mutex
	health       node.Health
}
type outcome struct {
	StartedAt time.Time                  `json:"started_at,omitempty"`
	Phase     string                     `json:"phase,omitempty"`
	Input     protocol.OperationsRequest `json:"input,omitempty"`
	Failure   string                     `json:"failure,omitempty"`
	Event     events.OperationalEvent    `json:"event"`
	Result    privateops.Reply           `json:"result"`
	Published bool                       `json:"published"`
}

func (m *Module) Name() string           { return "observability" }
func (m *Module) Capabilities() []string { return []string{protocol.CapOperations} }
func (m *Module) Start(ctx context.Context, h node.Host) error {
	if e := m.Config.Validate(); e != nil {
		return e
	}
	m.host = h
	var e error
	m.logs, e = NewLogs(m.Root, LogOptions{m.Config.LogBytes, m.Config.LogFiles, m.Config.TailBytes}, m.Secrets)
	if e != nil {
		return e
	}
	m.crashes, e = crashes.New(m.Root, crashes.Options{Bytes: m.Config.CrashBytes, Retained: m.Config.CrashFiles}, m.Secrets)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(m.Root, "outcomes"), 0700); e != nil {
		return e
	}
	m.sub, e = h.Subscribe(1024)
	if e != nil {
		return e
	}
	life, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.jobs, e = nodejobs.New(filepath.Join(m.Root, "jobs"), m.execute)
	if e == nil {
		e = m.jobs.Start(life)
	}
	if e != nil {
		cancel()
		m.sub.Close()
		return e
	}
	if m.Config.PrivateMetrics != "" {
		m.stopMetrics, e = ServeMetrics(life, m.Config.PrivateMetrics, func() Report { return Snapshot(h) })
		if e != nil {
			cancel()
			m.sub.Close()
			m.jobs.Close()
			return e
		}
	}
	m.health = node.Health{Status: "healthy"}
	m.done = make(chan struct{})
	go m.run(life)
	return nil
}
func (m *Module) Health() node.Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.health
	if m.sub != nil && m.sub.Dropped() > 0 {
		h = node.Health{Status: "degraded", Explanation: "event observation drops; tails are best effort"}
	}
	return h
}
func (m *Module) degraded() {
	m.mu.Lock()
	m.health = node.Health{Status: "degraded", Explanation: "observability work failed or storage quota reached"}
	m.mu.Unlock()
}
func (m *Module) Close(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	if m.sub != nil {
		m.sub.Close()
	}
	if m.jobs != nil {
		m.jobs.Close()
	}
	if m.done != nil {
		select {
		case <-m.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var e error
	if m.stopMetrics != nil {
		e = m.stopMetrics(ctx)
	}
	if m.logs != nil {
		e = errors.Join(e, m.logs.Close())
	}
	return e
}
func (m *Module) run(ctx context.Context) {
	defer close(m.done)
	// Exactly one outbox worker owns the coalescing timer. Long operations may
	// serialize recovery/publication, but cannot block this observation consumer.
	outboxCtx, cancelOutbox := context.WithCancel(ctx)
	outboxDone := make(chan struct{})
	go func() { defer close(outboxDone); m.runOutbox(outboxCtx) }()
	defer func() { cancelOutbox(); <-outboxDone }()
	for {
		select {
		case <-ctx.Done():
			return
		case v, ok := <-m.sub.Events:
			if !ok {
				return
			}
			if v.Server == nil || v.Event.Origin == "host" {
				continue
			}
			if v.Replay != "first_seen_window" {
				continue
			}
			if v.Event.Type == "server.console" {
				if e := m.logs.Append(v.Server.ID, []byte(v.Event.Details["line"]+"\n")); e != nil {
					m.degraded()
				}
			}
			if crashes.ShouldCapture(v.Event) {
				id := storeio.Hash([]byte(v.Server.ID + "\x00" + v.Event.RunID + "\x00" + v.Event.ID))
				var old outcome
				if storeio.JSON(filepath.Join(m.Root, "outcomes"), id+".json", &old) == nil {
					continue
				}
				status := m.host.Status(v.Server.ID)
				if status.RunID != v.Event.RunID {
					status = server.Status{RunID: v.Event.RunID}
				}
				p, e := m.crashes.Capture(ctx, *v.Server, status, m.logs.Tail(v.Server.ID))
				if e != nil {
					m.degraded()
					continue
				}
				r := events.OperationalEvent{ServerID: v.Server.ID, ActionID: id, EventKey: "bundle", Type: "crash.bundle", Severity: "error", At: v.Event.At, RelatedRunID: Redact(v.Event.RunID, m.Secrets), Reason: "unexpected_exit", Details: map[string]string{"artifact": filepath.Base(p), "tail_quality": "bounded best effort"}}
				if e = m.saveOutcome(id, outcome{Event: r}); e != nil {
					m.degraded()
				}
			}
		}
	}
}

// A single worker and timer keep publication/recovery bounded: no goroutine is
// created per tick or observation, and missed ticks coalesce while flush waits.
func (m *Module) runOutbox(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.flush(ctx)
		}
	}
}

func (m *Module) saveOutcome(id string, o outcome) error {
	for _, v := range []string{o.Event.ServerID, o.Event.ActionID, o.Event.EventKey, o.Event.RelatedRunID} {
		if Redact(v, m.Secrets) != v {
			return errors.New("secret-bearing outcome identity refused")
		}
	}
	if !m.safeRequest(o.Input) {
		return errors.New("secret-bearing outcome input refused")
	}
	root := filepath.Join(m.Root, "outcomes")
	unlock, e := storeio.Lock(context.Background(), root)
	if e != nil {
		return e
	}
	defer unlock()
	entries, e := os.ReadDir(root)
	if e != nil {
		return e
	}
	_, existing := os.Stat(filepath.Join(root, id+".json"))
	if errors.Is(existing, os.ErrNotExist) && len(entries) >= 1024 {
		return errors.New("operational outcome quota")
	}
	return m.persistOutcome(root, id+".json", o)
}
func (m *Module) flush(ctx context.Context) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	for id, o := range m.pending {
		if m.saveOutcome(id, o) == nil {
			delete(m.pending, id)
		}
	}
	root := filepath.Join(m.Root, "outcomes")
	files, e := os.ReadDir(root)
	if e != nil {
		m.degraded()
		return
	}
	for _, f := range files {
		if ctx.Err() != nil {
			return
		}
		if filepath.Ext(f.Name()) != ".json" {
			continue
		}
		var o outcome
		if storeio.JSON(root, f.Name(), &o) != nil {
			m.degraded()
			continue
		}
		if o.Phase == "executing" || o.Phase == "reserved" {
			id := strings.TrimSuffix(f.Name(), ".json")
			if pending, ok := m.pending[id]; ok {
				o = pending
			} else {
				o = m.recoverOutcome(ctx, o)
			}
			if e = m.saveOutcome(id, o); e != nil {
				m.remember(id, o)
				m.degraded()
				continue
			}
		}
		if o.Published {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, e := m.host.PublishOperational(c, o.Event)
		cancel()
		if e != nil {
			m.degraded()
			continue
		}
		o.Published = true
		if m.persistOutcome(root, f.Name(), o) != nil {
			m.degraded()
		}
	}
}
func (m *Module) server(id string) (node.Server, error) {
	for _, s := range m.host.Servers() {
		if s.ID == id {
			return s, nil
		}
	}
	return node.Server{}, errors.New("unknown registered server")
}
func (m *Module) Handle(ctx context.Context, _ node.Server, _ string, msg protocol.Message) (any, error) {
	var r protocol.OperationsRequest
	if e := protocol.Decode(msg.Payload, &r); e != nil {
		return nil, e
	}
	if !m.safeRequest(r) {
		return nil, errors.New("secret-bearing operation identifier refused")
	}
	s, e := m.server(r.ServerID)
	if e != nil {
		return nil, e
	}
	switch r.Operation {
	case "job":
		return m.jobView(s.ID, r.ID)
	case "cleanup-apply", "dump-retain":
		if r.RequestID == "" || len(r.RequestID) > 128 {
			return nil, errors.New("stable request_id required")
		}
		return m.jobs.Submit(s.ID, r.RequestID, r)
	default:
		return m.perform(ctx, s, r)
	}
}
func (m *Module) execute(ctx context.Context, id string, input json.RawMessage) (value any, returnedErr error) {
	defer func() {
		if returnedErr != nil {
			returnedErr = errors.New(Redact(returnedErr.Error(), m.Secrets))
		}
	}()
	m.opMu.Lock()
	defer m.opMu.Unlock()
	var r protocol.OperationsRequest
	if protocol.Decode(input, &r) != nil || r.ServerID != id || r.RequestID == "" {
		return nil, errors.New("invalid saved operation")
	}
	if !m.safeRequest(r) {
		return nil, errors.New("secret-bearing saved operation refused")
	}
	key := storeio.Hash([]byte(id + "\x00" + r.RequestID))
	root := filepath.Join(m.Root, "outcomes")
	var o outcome
	e := storeio.JSON(root, key+".json", &o)
	if e == nil {
		if o.Phase != "" {
			a, _ := json.Marshal(o.Input)
			if string(a) != string(input) {
				return nil, errors.New("operational request conflict")
			}
		}
		if pending, ok := m.pending[key]; ok {
			o = pending
		}
		if o.Phase == "executing" {
			o = m.recoverOutcome(ctx, o)
		}
		if o.Phase == "terminal" || o.Phase == "" {
			if e = m.saveOutcome(key, o); e != nil {
				m.remember(key, o)
				return nil, errors.New("completion awaiting durable recovery")
			}
			return outcomeResult(o)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	} else {
		o = outcome{Phase: "reserved", StartedAt: time.Now().UTC(), Input: r, Event: events.OperationalEvent{ServerID: id, ActionID: key, EventKey: "complete", Type: "cleanup", Severity: "info", Reason: r.Operation}}
		if e = m.saveOutcome(key, o); e != nil {
			return nil, e
		}
	}
	// No action may start until its reservation and executing phase are durable.
	o.Phase = "executing"
	if e = m.saveOutcome(key, o); e != nil {
		return nil, e
	}
	srv, e := m.server(id)
	var result privateops.Reply
	if e == nil {
		result, e = m.perform(ctx, srv, r)
	}
	if e != nil && srv.Helper != nil && (r.Private || r.Operation == "dump-retain") && result.State != "failed" {
		// A transport/cancellation error is not proof the helper did no work.
		o = m.recoverOutcome(ctx, o)
	} else {
		o = m.terminalOutcome(o, result, e)
	}
	if e = m.saveOutcome(key, o); e != nil {
		m.remember(key, o)
		return nil, errors.New("completion awaiting durable recovery; inspect operation outcome")
	}
	return outcomeResult(o)
}

func (m *Module) perform(ctx context.Context, s node.Server, r protocol.OperationsRequest) (privateops.Reply, error) {
	if r.Private || r.Operation == "dump-retain" {
		if s.Helper == nil {
			return privateops.Reply{}, errors.New("configured private UID helper required")
		}
		return privateops.Call(ctx, s.Helper.Socket, s.Helper.UID, privateops.Request{RequestID: r.RequestID, Operation: r.Operation, ID: r.ID, Namespace: r.Namespace, Offset: r.Offset, Limit: r.Limit, Retain: r.Retain, AgeSeconds: r.AgeSeconds})
	}
	if r.Namespace != "logs" && r.Namespace != "crashes" {
		return privateops.Reply{}, errors.New("node namespace must be logs or crashes")
	}
	root := filepath.Join(m.Root, s.ID, r.Namespace)
	switch r.Operation {
	case "tail":
		if r.Namespace != "logs" {
			break
		}
		return privateops.Reply{Data: m.logs.Tail(s.ID)}, nil
	case "list":
		list, e := cleanup.Catalog(root)
		if e != nil {
			return privateops.Reply{}, e
		}
		if r.Offset < 0 || r.Limit < 0 || r.Limit > 128 {
			return privateops.Reply{}, errors.New("invalid list bounds")
		}
		start := int(r.Offset)
		if start > len(list) {
			start = len(list)
		}
		n := r.Limit
		if n == 0 {
			n = 128
		}
		end := start + n
		if end > len(list) {
			end = len(list)
		}
		return privateops.Reply{Entries: list[start:end]}, nil
	case "read":
		return privateops.ReadArtifact(root, privateops.Request{ID: r.ID, Offset: r.Offset, Limit: r.Limit}, false)
	case "cleanup-plan":
		if r.Retain < 0 || r.Retain > 4096 || r.AgeSeconds < 0 || r.AgeSeconds > 315360000 {
			return privateops.Reply{}, errors.New("invalid cleanup rule")
		}
		plan, e := cleanup.PreviewPlan(ctx, root, []cleanup.Rule{{Pattern: "*", MaxRetained: r.Retain, Age: time.Duration(r.AgeSeconds) * time.Second}})
		return privateops.Reply{Plan: &plan}, e
	case "cleanup-apply":
		result, e := cleanup.ApplyPlan(ctx, root, r.ID)
		return privateops.Reply{Result: &result}, e
	}
	return privateops.Reply{}, errors.New("unsupported observability operation")
}

func (m *Module) safeRequest(r protocol.OperationsRequest) bool {
	for _, v := range []string{r.Operation, r.ServerID, r.RunID, r.ID, r.RequestID, r.Namespace} {
		if Redact(v, m.Secrets) != v {
			return false
		}
	}
	return true
}
