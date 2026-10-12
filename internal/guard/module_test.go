// SPDX-License-Identifier: GPL-3.0-or-later
package guard

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestManualRequestIdentityIncludesDuration(t *testing.T) {
	p := fixturePolicy()
	p.Action = "block"
	p.GuardPolicy.Validate()
	m := &Module{Root: t.TempDir(), Config: config.GuardNode{HelperSocket: "/private/helper.sock"}, policies: map[string]Policy{"one": p}, records: map[string]*Record{}, queue: make(chan string, 32)}
	r := AdminRequest{Operation: "block", ServerID: "one", RequestID: "stable", Address: netip.MustParseAddr("8.8.8.8"), Seconds: 60}
	call := func() (any, error) {
		b, _ := json.Marshal(r)
		return m.Handle(context.Background(), node.Server{}, "", protocol.Message{Payload: b})
	}
	first, err := call()
	if err != nil {
		t.Fatal(err)
	}
	again, err := call()
	if err != nil || !first.(Record).Action.Until.Equal(again.(Record).Action.Until) || len(m.queue) != 1 {
		t.Fatal("same request replay changed expiry", again, err)
	}
	r.Seconds = 120
	if _, err = call(); err == nil {
		t.Fatal("same identity accepted different duration")
	}
}
func TestUIDReuseAndSessionExpiryRefuseAction(t *testing.T) {
	p := fixturePolicy()
	p.Action = "kick"
	p.GuardPolicy.Validate()
	at := time.Now()
	d := NewDetector()
	d.Observe(at, "one", "run", "session", "CServerSideClientBase::Connect( name='player', userid=42, chan->addr=8.8.8.8:1234)", p)
	d.Observe(at.Add(time.Second), "one", "run", "session", "Client 42 'player' signon state SIGNONSTATE_CONNECTED -> SIGNONSTATE_NEW", p)
	var action Action
	for i := 0; i < 5; i++ {
		for _, f := range d.Observe(at.Add(40*time.Second), "one", "run", "session", "Forcing client reconnect now for client 'player'", p) {
			action = Decide(f, p)
		}
	}
	if action.Kind != "kick" || !d.Valid(action, at.Add(41*time.Second), p) {
		t.Fatal("fixture lacks fresh action", action)
	}
	d.Observe(at.Add(41*time.Second), "one", "run", "session", "CServerSideClientBase::Connect( name='other', userid=42, chan->addr=9.9.9.9:1234)", p)
	if d.Valid(action, at.Add(41*time.Second), p) {
		t.Fatal("UID reuse retained authorization")
	}
	if d.Valid(action, at.Add(121*time.Second), p) {
		t.Fatal("expired session authorized action")
	}
}

type guardHost struct {
	srv          node.Server
	observations chan node.Observation
}

func (h *guardHost) Servers() []node.Server               { return []node.Server{h.srv} }
func (h *guardHost) ModuleHealth() map[string]node.Health { return nil }
func (h *guardHost) Status(string) server.Status {
	return server.Status{RunID: "current", State: "ready"}
}
func (h *guardHost) Publish(context.Context, events.Event) error {
	return errors.New("runtime publish forbidden")
}
func (h *guardHost) PublishOperational(context.Context, events.OperationalEvent) (events.Admission, error) {
	return events.Admission{}, errors.New("offline fixture")
}
func (h *guardHost) Dispatch(context.Context, string, string, any) (any, error) {
	return nil, errors.New("lifecycle forbidden")
}
func (h *guardHost) Subscribe(int) (*node.Subscription, error) {
	return &node.Subscription{Events: h.observations, Close: func() {}, Dropped: func() uint64 { return 0 }}, nil
}
func TestObserverFencesReplayProvenanceRunAndReceiveTime(t *testing.T) {
	p := fixturePolicy()
	p.Action = "block"
	p.Delegated = true
	p.GuardPolicy.Validate()
	h := &guardHost{srv: node.Server{ID: "one", Allocation: config.Allocation{IP: "203.0.113.10", Port: 27015}}, observations: make(chan node.Observation, 64)}
	m := &Module{Root: t.TempDir(), host: h, detector: NewDetector(), incidents: NewIncidents(), records: map[string]*Record{}, seen: map[string]time.Time{}, seenCount: map[string]int{}, policies: map[string]Policy{"one": p}, queue: make(chan string, 32)}
	m.sub, _ = h.Subscribe(64)
	now := time.Now()
	m.detector.Observe(now.Add(-40*time.Second), "one", "current", "session", "CServerSideClientBase::Connect( name='player', userid=42, chan->addr=8.8.8.8:1234)", p)
	m.detector.Observe(now.Add(-39*time.Second), "one", "current", "session", "Client 42 'player' signon state SIGNONSTATE_CONNECTED -> SIGNONSTATE_NEW", p)
	base := node.Observation{Server: &h.srv, ReceivedAt: now, Source: "container_report", Replay: "first_seen_window", SessionID: "session", Event: events.Event{ID: "one", Type: "server.console", RunID: "current", At: now, Details: map[string]string{"line": "Forcing client reconnect now for client 'player'"}}}
	for i := 0; i < 10; i++ {
		stale := base
		stale.Event.ID = "old-run"
		stale.Event.RunID = "old"
		h.observations <- stale
		dup := base
		dup.Replay = "durable_duplicate"
		h.observations <- dup
		old := base
		old.ReceivedAt = now.Add(-time.Minute)
		h.observations <- old
		host := base
		host.Source = "host_module"
		h.observations <- host
	}
	for _, id := range []string{"a", "a", "a", "b", "b", "c", "d", "e"} {
		o := base
		o.Event.ID = id
		h.observations <- o
	}
	close(h.observations)
	m.observe(context.Background())
	if len(m.records) != 1 || len(m.queue) != 1 {
		t.Fatal("replays or untrusted observations entered detector", len(m.records), len(m.queue))
	}
	for _, r := range m.records {
		if r.Action.Kind != "block" || r.Action.UID != 42 || r.Action.Provenance != "container-console-correlated" {
			t.Fatal(r)
		}
	}
}
func TestInterruptedActionRemainsUncertainAfterRestart(t *testing.T) {
	p := fixturePolicy()
	p.GuardPolicy.Validate()
	root := t.TempDir()
	id := strings.Repeat("a", 64)
	r := Record{ID: id, State: "executing", Action: Action{Kind: "block", ServerID: "one", At: time.Now(), Rule: "manual"}, Manual: true}
	if err := storeio.Save(root, id+".json", r); err != nil {
		t.Fatal(err)
	}
	h := &guardHost{srv: node.Server{ID: "one", Allocation: config.Allocation{IP: "203.0.113.10", Port: 27015}}, observations: make(chan node.Observation)}
	m := &Module{Root: root, Config: config.GuardNode{Policies: map[string]config.GuardPolicy{"one": p.GuardPolicy}}}
	if err := m.Start(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	m.mu.Lock()
	saved := *m.records[id]
	queued := len(m.queue)
	m.mu.Unlock()
	if saved.State != "uncertain" || saved.Event.ActionID != id || queued != 0 {
		t.Fatal("interrupted action replayed", saved, queued)
	}
}
