// SPDX-License-Identifier: GPL-3.0-or-later
package guard

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type AdminRequest struct {
	Operation string     `json:"operation"`
	ServerID  string     `json:"server_id,omitempty"`
	ID        string     `json:"id,omitempty"`
	RequestID string     `json:"request_id,omitempty"`
	Address   netip.Addr `json:"address,omitempty"`
	Seconds   int        `json:"seconds,omitempty"`
	Offset    int        `json:"offset,omitempty"`
}
type Record struct {
	ID        string
	Action    Action
	State     string
	Event     events.OperationalEvent
	Published bool
	Error     string
	Manual    bool
	dirty     bool
}
type Module struct {
	Root      string
	Config    config.GuardNode
	Secrets   []string
	host      node.Host
	detector  *Detector
	incidents *Incidents
	sub       *node.Subscription
	mu        sync.Mutex
	records   map[string]*Record
	seen      map[string]time.Time
	seenCount map[string]int
	policies  map[string]Policy
	queue     chan string
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	health    node.Health
}

func (m *Module) Name() string           { return "guard" }
func (m *Module) Capabilities() []string { return []string{protocol.CapGuardAdmin} }
func (m *Module) Health() node.Health    { m.mu.Lock(); defer m.mu.Unlock(); return m.health }
func (m *Module) Start(ctx context.Context, h node.Host) error {
	if e := m.Config.Validate(); e != nil {
		return e
	}
	m.host = h
	m.detector = NewDetector()
	m.incidents = NewIncidents()
	m.records = map[string]*Record{}
	m.seen = map[string]time.Time{}
	m.seenCount = map[string]int{}
	m.policies = map[string]Policy{}
	m.queue = make(chan string, 32)
	m.health = node.Health{Status: "healthy"}
	for _, s := range h.Servers() {
		if redaction.Redact(s.ID, m.Secrets) != s.ID {
			return errors.New("secret-bearing guard identity refused")
		}
		p, configured := m.Config.Policies[s.ID]
		if !configured {
			p.Enabled = true
		}
		if err := p.Validate(); err != nil {
			return err
		}
		addr, err := netip.ParseAddr(s.Allocation.IP)
		if err != nil {
			return err
		}
		a := Allocation{Address: addr, Port: s.Allocation.Port, Protocol: "udp"}
		if external, ok := m.Config.Allocations[s.ID]; ok {
			addr, e := netip.ParseAddr(external.IP)
			if e != nil {
				return e
			}
			a = Allocation{Address: addr, Port: external.Port, Protocol: external.Protocol}
		}
		m.policies[s.ID] = Policy{GuardPolicy: p, ServerID: s.ID, Allocation: a}
	}
	if e := os.MkdirAll(m.Root, 0700); e != nil {
		return e
	}
	entries, e := os.ReadDir(m.Root)
	if e != nil {
		return e
	}
	if len(entries) > 4096 {
		return errors.New("guard audit quota")
	}
	for _, v := range entries {
		var r Record
		if e = storeio.JSON(m.Root, v.Name(), &r); e != nil {
			return e
		}
		if !storeio.Digest(r.ID) || v.Name() != r.ID+".json" || m.policies[r.Action.ServerID].ServerID == "" {
			return errors.New("invalid guard action record")
		}
		if r.State == "queued" || r.State == "executing" {
			m.finish(&r, "uncertain", "interrupted action is never replayed")
		}
		m.incidents.Restore(r.Action)
		m.records[r.ID] = &r
	}
	m.sub, e = h.Subscribe(1024)
	if e != nil {
		return e
	}
	life, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	for _, run := range []func(context.Context){m.observe, m.worker, m.publish} {
		m.wg.Add(1)
		go func(f func(context.Context)) { defer m.wg.Done(); f(life) }(run)
	}
	return nil
}
func (m *Module) Close(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	if m.sub != nil {
		m.sub.Close()
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
func (m *Module) degraded(reason string) {
	m.mu.Lock()
	m.health = node.Health{Status: "degraded", Explanation: reason}
	m.mu.Unlock()
}
func (m *Module) observe(ctx context.Context) {
	var drops uint64
	for {
		select {
		case <-ctx.Done():
			return
		case o, ok := <-m.sub.Events:
			if !ok {
				return
			}
			if n := m.sub.Dropped(); n != drops {
				m.detector.Reset()
				drops = n
				m.degraded("guard observation drops; client correlation reset")
			}
			if o.Server == nil || o.Event.Type != "server.console" || o.Event.Origin == "host" || o.Replay != "first_seen_window" || o.Source != "container_report" && o.Source != "local_runtime" {
				continue
			}
			p, ok := m.policies[o.Server.ID]
			if !ok || !p.Enabled {
				continue
			}
			now := time.Now()
			fresh := time.Duration(p.FreshSeconds) * time.Second
			if now.Sub(o.ReceivedAt) > fresh || o.Event.At.Before(o.ReceivedAt.Add(-fresh)) || o.Event.At.After(o.ReceivedAt.Add(fresh)) {
				continue
			}
			status := m.host.Status(o.Server.ID)
			if status.RunID != o.Event.RunID || status.State != "ready" && status.State != "starting" {
				continue
			}
			key := o.Server.ID + "\x00" + o.Event.ID
			if _, ok = m.seen[key]; ok {
				continue
			}
			for k, at := range m.seen {
				if now.Sub(at) > 5*time.Minute {
					delete(m.seen, k)
					id := strings.SplitN(k, "\x00", 2)[0]
					m.seenCount[id]--
				}
			}
			if len(m.seen) >= 16384 || m.seenCount[o.Server.ID] >= 4096 {
				m.degraded("guard replay capacity; enforcement refused")
				continue
			}
			m.seen[key] = now
			m.seenCount[o.Server.ID]++
			line := redaction.Redact(o.Event.Details["line"], m.Secrets)
			for _, finding := range m.detector.Observe(o.ReceivedAt, o.Server.ID, o.Event.RunID, o.SessionID, line, p) {
				a := m.incidents.Decide(finding, p)
				if a.Kind == "kick" {
					a.Kind = "log"
					a.Reason = "configure typed kick in runtime policy"
				}
				id := storeio.Hash([]byte(key + "\x00" + finding.Rule))
				if _, e := m.admit(id, a, false); e != nil {
					m.degraded("guard action capacity or persistence unavailable")
				}
			}
		}
	}
}
func (m *Module) admit(id string, a Action, manual bool) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.records[id]; old != nil {
		if old.Action.Kind != a.Kind || old.Action.Address != a.Address || old.Action.ServerID != a.ServerID || old.Manual != manual || old.Action.RequestedSeconds != a.RequestedSeconds || old.Action.Allocation != a.Allocation || old.Action.Rule != a.Rule {
			return Record{}, errors.New("guard request identity conflict")
		}
		return *old, nil
	}
	now := time.Now()
	for key, r := range m.records {
		if r.Published && !r.dirty && now.Sub(r.Event.At) > 24*time.Hour && !r.Action.Until.After(now) {
			if os.Remove(filepath.Join(m.Root, key+".json")) == nil {
				delete(m.records, key)
			}
		}
	}
	if len(m.records) >= 4096 || len(m.queue) == cap(m.queue) {
		return Record{}, errors.New("guard action quota")
	}
	r := &Record{ID: id, Action: a, State: "queued", Manual: manual}
	if e := storeio.Save(m.Root, id+".json", r); e != nil {
		return Record{}, e
	}
	m.records[id] = r
	m.queue <- id
	return *r, nil
}
func (m *Module) finish(r *Record, state, why string) {
	r.State = state
	r.Error = why
	r.Event = events.OperationalEvent{ServerID: r.Action.ServerID, ActionID: r.ID, EventKey: "outcome", Type: "guard", Severity: "info", At: time.Now().UTC(), RelatedRunID: r.Action.RunID, Reason: r.Action.Rule, Details: map[string]string{"action": r.Action.Kind, "state": state, "provenance": r.Action.Provenance}}
	if state == "uncertain" || state == "refused" {
		r.Event.Severity = "warning"
	}
	r.dirty = storeio.Save(m.Root, r.ID+".json", r) != nil
}
func (m *Module) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.queue:
			m.mu.Lock()
			r := *m.records[id]
			r.State = "executing"
			if e := storeio.Save(m.Root, id+".json", r); e != nil {
				m.finish(&r, "refused", "guard action persistence unavailable")
				m.records[id] = &r
				m.mu.Unlock()
				m.degraded("guard action persistence unavailable")
				continue
			}
			snapshot := r
			m.records[id] = &snapshot
			m.mu.Unlock()
			p := m.policies[r.Action.ServerID]
			state, why := "observed", ""
			if r.Action.Kind != "log" {
				status := m.host.Status(r.Action.ServerID)
				fresh := !time.Now().Before(r.Action.At) && time.Since(r.Action.At) <= time.Duration(p.FreshSeconds)*time.Second
				if !fresh || !r.Manual && (status.RunID != r.Action.RunID || status.State != "ready" && status.State != "starting" || !m.detector.Valid(r.Action, time.Now(), p)) {
					state = "refused"
					why = "stale or ambiguous action"
				} else if m.Config.HelperSocket == "" {
					state = "refused"
					why = "private guard helper unavailable"
				} else {
					provenance := "console"
					if r.Manual {
						provenance = "manual"
					}
					reply, e := Call(ctx, m.Config.HelperSocket, m.Config.HelperUID, Request{Operation: r.Action.Kind, ServerID: r.Action.ServerID, Address: r.Action.Address, Until: r.Action.Until, Provenance: provenance, Reason: r.Action.Rule})
					if e != nil {
						state = "uncertain"
						why = "helper action failed or outcome unknown"
					} else {
						state = reply.State
						r.Action.Until = reply.Until
					}
				}
			}
			m.mu.Lock()
			m.finish(&r, state, why)
			m.records[id] = &r
			m.mu.Unlock()
		}
	}
}
func (m *Module) publish(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
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
				m.degraded("guard event publication unavailable")
				continue
			}
			m.mu.Lock()
			if current := m.records[r.ID]; current != nil {
				current.Published = true
				current.dirty = storeio.Save(m.Root, current.ID+".json", current) != nil
			}
			m.mu.Unlock()
		}
	}
}
func (m *Module) Handle(ctx context.Context, _ node.Server, _ string, msg protocol.Message) (any, error) {
	var r AdminRequest
	if e := protocol.Decode(msg.Payload, &r); e != nil {
		return nil, e
	}
	for _, v := range []string{r.ServerID, r.ID, r.RequestID} {
		if redaction.Redact(v, m.Secrets) != v {
			return nil, errors.New("secret-bearing guard identity refused")
		}
	}
	if r.Operation == "why" {
		m.mu.Lock()
		defer m.mu.Unlock()
		saved := m.records[r.ID]
		if saved == nil || saved.Action.ServerID != r.ServerID {
			return nil, errors.New("unknown scoped guard action")
		}
		return *saved, nil
	}
	if r.Operation == "preview" {
		var rules Ruleset
		for _, p := range m.policies {
			if p.Enabled && p.Action == "block" {
				rules.Allocations = append(rules.Allocations, p.Allocation)
			}
		}
		text, e := Render(rules, time.Now())
		return struct {
			Rules string
			Limit string
		}{text, "Node preview only; helper root-owned policy and mappings remain authoritative."}, e
	}
	if r.Operation == "blocks" || r.Operation == "rates" || r.Operation == "refresh" || r.Operation == "info" {
		op := r.Operation
		if op == "blocks" {
			op = "list"
		}
		return Call(ctx, m.Config.HelperSocket, m.Config.HelperUID, Request{Operation: op, ServerID: r.ServerID, Offset: r.Offset})
	}
	p, ok := m.policies[r.ServerID]
	if !ok || !p.Enabled || p.Action != "block" || m.Config.HelperSocket == "" {
		return nil, errors.New("configured host block policy/helper required")
	}
	if r.Operation != "block" && r.Operation != "unblock" {
		return nil, errors.New("invalid guard administration")
	}
	if len(r.RequestID) < 1 || len(r.RequestID) > 128 {
		return nil, errors.New("stable bounded request_id required")
	}
	if !Blockable(r.Address, p.Allowlist) {
		return nil, errors.New("protected source")
	}
	seconds := r.Seconds
	if seconds == 0 {
		seconds = p.BlockSeconds
	}
	if seconds < 1 || seconds > p.MaxBlockSeconds {
		return nil, errors.New("guard duration exceeds policy")
	}
	now := time.Now()
	a := Action{RequestedSeconds: seconds, Kind: r.Operation, ServerID: r.ServerID, Allocation: p.Allocation, Address: r.Address.Unmap(), At: now, Until: now.Add(time.Duration(seconds) * time.Second), Rule: "manual", Provenance: "private-admin"}
	return m.admit(storeio.Hash([]byte(r.ServerID+"\x00"+r.RequestID)), a, true)
}
