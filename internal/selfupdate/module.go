// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Request struct {
	Operation string `json:"operation"`
	RequestID string `json:"request_id,omitempty"`
	ID        string `json:"id,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}
type Record struct {
	ID        string                  `json:"id"`
	Operation string                  `json:"operation"`
	State     string                  `json:"state"`
	Version   string                  `json:"version,omitempty"`
	Error     string                  `json:"error,omitempty"`
	Event     events.OperationalEvent `json:"event"`
	Published bool                    `json:"published"`
	dirty     bool
}
type Module struct {
	Root           string
	Config         config.ProjectUpdates
	Current        string
	Manager        *Manager
	Secrets        []string
	RequestRestart func()
	host           node.Host
	mu             sync.Mutex
	records        map[string]*Record
	queue          chan string
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	health         node.Health
}

func (m *Module) Name() string           { return "selfupdate" }
func (m *Module) Capabilities() []string { return []string{protocol.CapProjectUpdate} }
func (m *Module) Health() node.Health    { m.mu.Lock(); defer m.mu.Unlock(); return m.health }
func (m *Module) Start(ctx context.Context, h node.Host) error {
	if !m.Config.Enabled {
		return ErrDisabled
	}
	if m.Manager == nil || m.Manager.root() != nil {
		return errors.New("operator-provisioned managed release directory unavailable")
	}
	m.host = h
	m.records = map[string]*Record{}
	m.queue = make(chan string, 8)
	m.health = node.Health{Status: "healthy"}
	if e := os.MkdirAll(m.Root, 0700); e != nil {
		return e
	}
	entries, e := os.ReadDir(m.Root)
	if e != nil {
		return e
	}
	if len(entries) > 130 {
		return errors.New("project update history quota")
	}
	for _, v := range entries {
		if v.Name() == "schedule.json" {
			continue
		}
		var r Record
		if e = storeio.JSON(m.Root, v.Name(), &r); e != nil {
			return e
		}
		if !storeio.Digest(r.ID) || v.Name() != r.ID+".json" {
			return errors.New("invalid project update record")
		}
		if r.State == "queued" || r.State == "running" {
			m.finish(&r, "uncertain", "interrupted action is never replayed")
		}
		m.records[r.ID] = &r
	}
	if e = m.captureOutcome(); e != nil {
		return e
	}
	life, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	for _, f := range []func(context.Context){m.worker, m.maintain} {
		m.wg.Add(1)
		go func(f func(context.Context)) { defer m.wg.Done(); f(life) }(f)
	}
	return nil
}
func (m *Module) Close(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Module) finish(r *Record, state, why string) {
	r.State = state
	r.Error = why
	r.Event = events.OperationalEvent{ActionID: r.ID, EventKey: "outcome", Type: "node.update", Severity: "info", At: time.Now().UTC(), Reason: state, Details: map[string]string{"operation": r.Operation, "version": r.Version, "state": state}}
	if state == "failed" || state == "uncertain" {
		r.Event.Severity = "warning"
	}
	r.dirty = storeio.Save(m.Root, r.ID+".json", r) != nil
}
func (m *Module) admit(op, id string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.records[id]; r != nil {
		if r.Operation != op {
			return Record{}, errors.New("project update request identity conflict")
		}
		return *r, nil
	}
	for id, r := range m.records {
		if r.Published && !r.dirty && time.Since(r.Event.At) > 30*24*time.Hour {
			if os.Remove(filepath.Join(m.Root, id+".json")) == nil {
				delete(m.records, id)
			}
		}
	}
	if len(m.records) >= 128 || len(m.queue) == cap(m.queue) {
		return Record{}, errors.New("project update queue/history quota")
	}
	r := &Record{ID: id, Operation: op, State: "queued"}
	if e := storeio.Save(m.Root, id+".json", r); e != nil {
		return Record{}, e
	}
	m.records[id] = r
	m.queue <- id
	return *r, nil
}
func (m *Module) Handle(ctx context.Context, _ node.Server, _ string, msg protocol.Message) (any, error) {
	var r Request
	if e := protocol.Decode(msg.Payload, &r); e != nil {
		return nil, e
	}
	if r.Operation == "status" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if r.ID != "" {
			v := m.records[r.ID]
			if v == nil {
				return nil, errors.New("unknown project update request")
			}
			return *v, nil
		}
		if r.Offset < 0 {
			return nil, errors.New("invalid status offset")
		}
		var ids []string
		for id := range m.records {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if r.Offset > len(ids) {
			return nil, errors.New("invalid status offset")
		}
		var list []Record
		end := min(r.Offset+8, len(ids))
		for _, id := range ids[r.Offset:end] {
			list = append(list, *m.records[id])
		}
		j, e := m.Manager.Status()
		status := "initialized"
		if e != nil {
			status = "managed selection unavailable"
		}
		next := 0
		if end < len(ids) {
			next = end
		}
		return struct {
			Selection Journal
			Status    string
			Records   []Record
			Next      int
		}{j, status, list, next}, nil
	}
	if r.Operation != "check" && r.Operation != "stage" && r.Operation != "activate" {
		return nil, errors.New("unknown typed project update action")
	}
	if len(r.RequestID) < 1 || len(r.RequestID) > 128 || redaction.Redact(r.RequestID, m.Secrets) != r.RequestID {
		return nil, errors.New("stable bounded non-secret request ID required")
	}
	return m.admit(r.Operation, storeio.Hash([]byte(r.RequestID)))
}
func (m *Module) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.queue:
			m.mu.Lock()
			r := *m.records[id]
			r.State = "running"
			if e := storeio.Save(m.Root, id+".json", r); e != nil {
				m.finish(&r, "failed", "action persistence unavailable")
				m.records[id] = &r
				m.mu.Unlock()
				continue
			}
			snapshot := r
			m.records[id] = &snapshot
			m.mu.Unlock()
			state, why := "checked", ""
			restart := false
			c, cancel := context.WithTimeout(ctx, 5*time.Minute)
			release, e := m.Manager.Client.Check(c, m.Config.Channel, m.Current)
			if e == nil {
				r.Version = release.Version
				if r.Operation == "stage" || r.Operation == "activate" {
					e = m.Manager.Stage(c, release)
					state = "staged"
				}
				if e == nil && r.Operation == "activate" {
					if !m.Config.Activate || m.RequestRestart == nil {
						e = errors.New("automatic activation not provisioned")
					} else if release.Version == m.Current {
						state = "already_current"
					} else {
						_, e = m.Manager.Activate(c, release.Version)
						if e == nil {
							state = "activation_requested"
							restart = true
						}
					}
				}
			}
			cancel()
			if e != nil {
				state = "failed"
				why = redaction.Redact(e.Error(), m.Secrets)
			}
			m.mu.Lock()
			m.finish(&r, state, why)
			m.records[id] = &r
			durable := !r.dirty
			m.mu.Unlock()
			if restart && durable && ctx.Err() == nil {
				m.RequestRestart()
				return
			}
		}
	}
}
func (m *Module) maintain(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var schedule struct{ Next time.Time }
	if m.Config.Schedule {
		if e := storeio.JSON(m.Root, "schedule.json", &schedule); errors.Is(e, os.ErrNotExist) {
			schedule.Next = time.Now().Add(time.Duration(m.Config.IntervalSeconds) * time.Second)
			if storeio.Save(m.Root, "schedule.json", schedule) != nil {
				m.degrade("schedule persistence unavailable")
				schedule.Next = time.Time{}
			}
		} else if e != nil {
			m.degrade("schedule state invalid")
			schedule.Next = time.Time{}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := m.captureOutcome(); err != nil {
			m.degrade("activation outcome persistence unavailable")
		}
		if !schedule.Next.IsZero() && !time.Now().Before(schedule.Next) {
			id := storeio.Hash([]byte("schedule/" + schedule.Next.UTC().Format(time.RFC3339Nano)))
			schedule.Next = time.Now().Add(time.Duration(m.Config.IntervalSeconds) * time.Second)
			if storeio.Save(m.Root, "schedule.json", schedule) == nil {
				op := "stage"
				if m.Config.Activate {
					op = "activate"
				}
				if _, e := m.admit(op, id); e != nil {
					m.degrade("scheduled update admission unavailable")
				}
			} else {
				m.degrade("schedule persistence unavailable")
				schedule.Next = time.Time{}
			}
		}
		m.mu.Lock()
		var out []Record
		for _, r := range m.records {
			if r.dirty {
				r.dirty = storeio.Save(m.Root, r.ID+".json", r) != nil
			}
			if !r.dirty && !r.Published && r.Event.ActionID != "" {
				out = append(out, *r)
			}
		}
		m.mu.Unlock()
		for _, r := range out {
			c, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, e := m.host.PublishOperational(c, r.Event)
			cancel()
			if e != nil {
				m.degrade("project update outcome publication unavailable")
				continue
			}
			m.mu.Lock()
			if v := m.records[r.ID]; v != nil {
				v.Published = true
				v.dirty = storeio.Save(m.Root, v.ID+".json", v) != nil
			}
			m.mu.Unlock()
		}
	}
}
func (m *Module) degrade(why string) {
	m.mu.Lock()
	m.health = node.Health{Status: "degraded", Explanation: why}
	m.mu.Unlock()
}

func (m *Module) captureOutcome() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, e := m.Manager.Status(); e == nil && (j.Outcome == "activated" || j.Outcome == "rolled_back" || j.Outcome == "startup_failed") {
		id := storeio.Hash([]byte(j.Transaction + "/activation-outcome"))
		if m.records[id] == nil {
			if len(m.records) >= 128 {
				return errors.New("project update outcome quota")
			}
			r := &Record{ID: id, Operation: "activation-outcome", State: j.Outcome, Version: j.Current, Event: events.OperationalEvent{ActionID: j.Transaction, EventKey: "activation-outcome", Type: "node.update", Severity: "info", At: j.At, Reason: j.Outcome, Details: map[string]string{"version": j.Current, "state": j.Outcome}}}
			if j.Outcome != "activated" {
				r.Event.Severity = "warning"
			}
			if e = storeio.Save(m.Root, id+".json", r); e != nil {
				return e
			}
			m.records[id] = r
		}
	}
	return nil
}
