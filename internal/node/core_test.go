// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/nodeclient"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
)

type receiptSink struct {
	mu    sync.Mutex
	calls []events.Event
	fail  bool
}

func (s *receiptSink) Publish(context.Context, events.Event) error {
	return errors.New("async Publish is not a receipt")
}
func (s *receiptSink) Accepts(e events.Event) bool {
	return e.Type == "server.up" || e.Type == "server.down"
}
func (s *receiptSink) Deliver(ctx context.Context, e events.Event) (alerts.DeliveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return alerts.Failed, errors.New("uncertain remote receipt")
	}
	s.calls = append(s.calls, e)
	return alerts.Delivered, nil
}
func (s *receiptSink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.calls) }
func fixture(t *testing.T) config.Node {
	t.Helper()
	dir, e := os.MkdirTemp("", "s2n-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	c := config.Node{SchemaVersion: 1, ID: "node", StateDir: filepath.Join(dir, "state"), AdminSocket: filepath.Join(dir, "admin", "admin.sock")}
	for i, id := range []string{"a", "b"} {
		v := filepath.Join(dir, id)
		os.MkdirAll(filepath.Join(v, "egg"), 0700)
		c.Servers = append(c.Servers, config.NodeServer{ID: id, Volume: v, PeerUID: uint32(os.Getuid()), AccessGID: os.Getgid(), Allocation: config.Allocation{IP: "127.0.0.1", Port: 27015 + i}})
	}
	return c
}
func start(t *testing.T, cfg config.Node, s events.Sink, mods ...Module) (*Core, func()) {
	t.Helper()
	c, e := New(cfg, s)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range mods {
		if e = c.AddModule(m, nil); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	select {
	case <-c.Ready():
	case e := <-done:
		t.Fatalf("start: %v", e)
	case <-time.After(2 * time.Second):
		t.Fatal("startup timeout")
	}
	stop := func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(2 * time.Second):
			t.Error("shutdown timeout")
		}
	}
	return c, stop
}
func connect(t *testing.T, cfg config.Node) *nodeclient.Client {
	t.Helper()
	c, e := nodeclient.Connect(context.Background(), filepath.Join(cfg.Servers[0].Volume, "egg/s2node.sock"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func event(id string) events.Event {
	return events.Event{Version: 1, ID: id, Type: "server.up", RunID: "run", CorrelationID: "run", At: time.Now().UTC(), ServerID: "forged"}
}
func TestPeerCannotClaimOtherVolume(t *testing.T) {
	cfg := fixture(t)
	sink := &receiptSink{}
	_, stop := start(t, cfg, sink)
	defer stop()
	c := connect(t, cfg)
	if _, e := c.ClaimRun(context.Background(), "run"); e != nil {
		t.Fatal(e)
	}
	if _, e := c.PublishEvent(context.Background(), event("e")); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return sink.count() == 1 })
	sink.mu.Lock()
	got := sink.calls[0].ServerID
	sink.mu.Unlock()
	if got != "a" {
		t.Fatalf("claimed identity: %s", got)
	}
	conn, e := net.Dial("unix", filepath.Join(cfg.Servers[0].Volume, "egg/s2node.sock"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	p := protocol.NewClient(conn)
	if e = p.Call(context.Background(), "hello", protocol.Hello{}, new(protocol.Welcome)); e != nil {
		t.Fatal(e)
	}
	for _, payload := range []any{map[string]any{"server_id": "b"}, map[string]any{"path": cfg.Servers[1].Volume}} {
		if e = p.Call(context.Background(), "status", payload, new(server.Status)); e == nil {
			t.Fatal("forged selector accepted")
		}
	}
}
func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition timeout")
}

type testModule struct {
	name    string
	fail    bool
	started bool
}

func (m *testModule) Name() string { return m.name }
func (m *testModule) Start(context.Context, Host) error {
	m.started = true
	if m.fail {
		return errors.New("fixture failure")
	}
	return nil
}
func (m *testModule) Health() Health              { return Health{Status: "healthy"} }
func (m *testModule) Close(context.Context) error { return nil }
func TestModuleFailureIsIsolated(t *testing.T) {
	cfg := fixture(t)
	bad, good := &testModule{name: "bad", fail: true}, &testModule{name: "good"}
	c, stop := start(t, cfg, nil, bad, good)
	defer stop()
	if !good.started || c.ModuleHealth()["bad"].Status != "failed" {
		t.Fatal("optional failure stopped core")
	}
	cl := connect(t, cfg)
	if _, e := cl.Status(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestLostContactIsUnconfirmed(t *testing.T) {
	cfg := fixture(t)
	core, stop := start(t, cfg, nil)
	defer stop()
	c := connect(t, cfg)
	if e := c.ReportStatus(context.Background(), server.Status{RunID: "run", State: "ready", Ready: true}); e != nil {
		t.Fatal(e)
	}
	c.Close()
	waitFor(t, func() bool { return core.Status("a").State == "lost_contact" })
	s := core.Status("a")
	if s.Ready || s.State == "stopped" || s.Players.Known {
		t.Fatalf("disconnect inferred exit: %+v", s)
	}
}
func TestDurableRecoveryAndDedup(t *testing.T) {
	cfg := fixture(t)
	sink := &receiptSink{fail: true}
	_, stop := start(t, cfg, sink)
	c := connect(t, cfg)
	if owner, e := c.ClaimRun(context.Background(), "run"); e != nil || owner != "node" {
		t.Fatalf("%s %v", owner, e)
	}
	ev := event("e")
	if _, e := c.PublishEvent(context.Background(), ev); e != nil {
		t.Fatal(e)
	}
	c.Close()
	stop()
	sink.mu.Lock()
	sink.fail = false
	sink.mu.Unlock()
	_, stop = start(t, cfg, sink)
	defer stop()
	c = connect(t, cfg)
	if owner, e := c.ClaimRun(context.Background(), "run"); e != nil || owner != "node" {
		t.Fatalf("lost owner %s %v", owner, e)
	}
	if _, e := c.PublishEvent(context.Background(), ev); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return sink.count() == 1 })
	time.Sleep(30 * time.Millisecond)
	if sink.count() != 1 {
		t.Fatal("duplicate remote delivery")
	}
}
func TestUnselectedTrafficDoesNotExhaustQueue(t *testing.T) {
	cfg := fixture(t)
	cfg.Limits.Pending = 1
	cfg.Limits.Records = 2
	sink := &receiptSink{fail: true}
	_, stop := start(t, cfg, sink)
	defer stop()
	c := connect(t, cfg)
	c.ClaimRun(context.Background(), "run")
	for i := 0; i < 50; i++ {
		ev := event(fmt.Sprint(i))
		ev.Type = "server.console"
		result, e := c.PublishEvent(context.Background(), ev)
		if e != nil || result != "filtered" {
			t.Fatalf("unselected %s %v", result, e)
		}
	}
	if _, e := c.PublishEvent(context.Background(), event("life")); e != nil {
		t.Fatal(e)
	}
	if _, e := c.PublishEvent(context.Background(), event("full")); e == nil {
		t.Fatal("queue exhaustion acknowledged")
	}
}
func TestUnsafeVolumesRejected(t *testing.T) {
	cfg := fixture(t)
	cfg.Servers[1].Volume = filepath.Join(cfg.Servers[0].Volume, "nested")
	os.MkdirAll(cfg.Servers[1].Volume, 0700)
	if _, e := New(cfg, nil); e == nil {
		t.Fatal("overlapping volumes accepted")
	}
	cfg = fixture(t)
	real := cfg.Servers[0].Volume
	cfg.Servers[0].Volume = real + "-link"
	os.Symlink(real, cfg.Servers[0].Volume)
	if _, e := New(cfg, nil); e == nil {
		t.Fatal("symlink volume accepted")
	}
}
func TestPersistedStateIsPrivate(t *testing.T) {
	cfg := fixture(t)
	_, stop := start(t, cfg, &receiptSink{fail: true})
	defer stop()
	c := connect(t, cfg)
	c.ClaimRun(context.Background(), "run")
	c.PublishEvent(context.Background(), event("e"))
	b, e := os.ReadFile(filepath.Join(cfg.StateDir, "delivery.json"))
	if e != nil || !json.Valid(b) {
		t.Fatalf("missing durable state %v", e)
	}
	info, _ := os.Stat(filepath.Join(cfg.StateDir, "delivery.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("state is not private")
	}
}

type localSender struct{ n int }

func (s *localSender) Publish(context.Context, events.Event) error { s.n++; return nil }
func TestOneWebhookOwnerPerRun(t *testing.T) {
	cfg := fixture(t)
	sink := &receiptSink{}
	_, stop := start(t, cfg, sink)
	fallback := &localSender{}
	client := connect(t, cfg)
	route := client.Route(fallback)
	first := event("first")
	if e := route.Publish(context.Background(), first); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return sink.count() == 1 })
	stop()
	if e := route.Publish(context.Background(), event("during-disconnect")); e == nil {
		t.Fatal("disconnected event unexpectedly admitted")
	}
	if fallback.n != 0 {
		t.Fatal("disconnect activated second sender")
	}
	_, stop = start(t, cfg, sink)
	defer stop()
	last := event("after-reconnect")
	if e := route.Publish(context.Background(), last); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return sink.count() == 2 })
	if fallback.n != 0 {
		t.Fatal("acknowledged node events independently sent")
	}
}
func TestExistingNodeOwnershipWithoutDeliveryCannotFilterAwayEvent(t *testing.T) {
	cfg := fixture(t)
	_, stop := start(t, cfg, &receiptSink{})
	cl := connect(t, cfg)
	if _, e := cl.ClaimRun(context.Background(), "run"); e != nil {
		t.Fatal(e)
	}
	cl.Close()
	stop()
	_, stop = start(t, cfg, nil)
	defer stop()
	cl = connect(t, cfg)
	owner, e := cl.ClaimRun(context.Background(), "run")
	if e != nil || owner != "node" {
		t.Fatal("owner changed")
	}
	if _, e = cl.PublishEvent(context.Background(), event("retained")); e == nil {
		t.Fatal("missing delivery interface silently filtered node-owned event")
	}
}
func TestSocketPermissionsAndPeerGate(t *testing.T) {
	cfg := fixture(t)
	cfg.Servers[0].PeerUID = uint32(os.Getuid() + 1)
	_, stop := start(t, cfg, nil)
	defer stop()
	path := filepath.Join(cfg.Servers[0].Volume, "egg/s2node.sock")
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0660 {
		t.Fatalf("server socket mode %v %v", info, e)
	}
	admin, e := os.Stat(cfg.AdminSocket)
	if e != nil || admin.Mode().Perm() != 0600 {
		t.Fatal("admin socket not private")
	}
	if c, e := nodeclient.Connect(context.Background(), path); e == nil {
		c.Close()
		t.Fatal("unexpected peer UID accepted")
	}
}
func TestCoreLockAndDurableQuota(t *testing.T) {
	cfg := fixture(t)
	cfg.Limits.Runs = 1
	_, stop := start(t, cfg, &receiptSink{})
	defer stop()
	second, e := New(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = second.Run(context.Background()); e == nil {
		t.Fatal("second core acquired same durable state")
	}
	c := connect(t, cfg)
	if _, e = c.ClaimRun(context.Background(), "run"); e != nil {
		t.Fatal(e)
	}
	if _, e = c.ClaimRun(context.Background(), "other"); e == nil {
		t.Fatal("ownership quota acknowledged")
	}
	if owner, e := c.ClaimRun(context.Background(), "run"); e != nil || owner != "node" {
		t.Fatal("quota evicted current ownership")
	}
}
func TestNonReceiptSinkNeverOwnsDelivery(t *testing.T) {
	cfg := fixture(t)
	_, stop := start(t, cfg, &localSender{})
	defer stop()
	cl := connect(t, cfg)
	owner, e := cl.ClaimRun(context.Background(), "run")
	if e != nil || owner != "standalone" {
		t.Fatal("generic Publish became delivery receipt")
	}
}
func TestEventIDReuseRejectedAndStatusKnown(t *testing.T) {
	cfg := fixture(t)
	core, stop := start(t, cfg, &receiptSink{fail: true})
	defer stop()
	cl := connect(t, cfg)
	cl.ClaimRun(context.Background(), "run")
	ev := event("stable")
	if _, e := cl.PublishEvent(context.Background(), ev); e != nil {
		t.Fatal(e)
	}
	ev.Reason = "changed"
	if _, e := cl.PublishEvent(context.Background(), ev); e == nil {
		t.Fatal("event identity reused with different content")
	}
	status := event("status")
	status.Type = "server.status"
	status.Reason = "query_ok"
	status.Details = map[string]string{"players": "3", "map": "de_dust2"}
	if _, e := cl.PublishEvent(context.Background(), status); e != nil {
		t.Fatal(e)
	}
	got := core.Status("a")
	if !got.Ready || !got.Players.Known || got.Players.Count != 3 {
		t.Fatalf("status propagation %+v", got)
	}
}
func TestDurableWriteFailureHasNoAcknowledgement(t *testing.T) {
	cfg := fixture(t)
	core, stop := start(t, cfg, &receiptSink{})
	defer stop()
	if e := os.Mkdir(filepath.Join(cfg.StateDir, "delivery.tmp"), 0700); e != nil {
		t.Fatal(e)
	}
	cl := connect(t, cfg)
	if _, e := cl.ClaimRun(context.Background(), "run"); e == nil {
		t.Fatal("failed ownership write acknowledged")
	}
	core.mu.Lock()
	count := len(core.state.Owners)
	core.mu.Unlock()
	if count != 0 {
		t.Fatal("failed ownership write admitted to memory")
	}
}

type actionModule struct {
	testModule
	seen chan Server
}

func (m *actionModule) Capabilities() []string { return []string{protocol.CapPrepare} }
func (m *actionModule) Handle(ctx context.Context, s Server, kind string, msg protocol.Message) (any, error) {
	m.seen <- s
	return protocol.Preparation{Reservation: "reservation", Generation: "generation"}, nil
}
func TestActionDispatcherUsesAcceptedSocketAndModuleGrant(t *testing.T) {
	cfg := fixture(t)
	core, e := New(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	m := &actionModule{testModule: testModule{name: "prepare"}, seen: make(chan Server, 2)}
	if e = core.AddModule(m, []string{protocol.CapPrepare}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- core.Run(ctx) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	<-core.Ready()
	cl := connect(t, cfg)
	if _, e = cl.PrepareGame(ctx, protocol.PrepareRequest{AppID: 730}); e != nil {
		t.Fatal(e)
	}
	if got := <-m.seen; got.ID != "a" || got.Volume != cfg.Servers[0].Volume {
		t.Fatal("action selected another volume")
	}
	conn, e := net.Dial("unix", filepath.Join(cfg.Servers[0].Volume, "egg/s2node.sock"))
	if e != nil {
		t.Fatal(e)
	}
	wire := protocol.NewClient(conn)
	defer wire.Close()
	wire.Call(ctx, "hello", protocol.Hello{}, new(protocol.Welcome))
	for _, forged := range []any{map[string]any{"app_id": 730, "server_id": "b"}, map[string]any{"app_id": 730, "path": cfg.Servers[1].Volume}} {
		if e = wire.Call(ctx, protocol.CapPrepare, forged, new(protocol.Preparation)); e == nil {
			t.Fatal("forged preparation selector accepted")
		}
	}
	if len(m.seen) != 0 {
		t.Fatal("forged request reached module")
	}
	h := moduleHost{core: core, grants: map[string]bool{}}
	if _, e = h.Dispatch(ctx, "b", protocol.CapPrepare, protocol.PrepareRequest{AppID: 730}); e == nil {
		t.Fatal("ungranted module action allowed")
	}
}
func TestServerRecordsAndStatusesAreImmutableCopies(t *testing.T) {
	cfg := fixture(t)
	core, stop := start(t, cfg, nil)
	defer stop()
	records := core.Servers()
	records[0].ID = "changed"
	if core.Servers()[0].ID == "changed" {
		t.Fatal("mutable host record leaked")
	}
	cl := connect(t, cfg)
	s := server.Status{RunID: "run", State: "ready", Ready: true, Versions: map[string]string{"cs2": "1"}}
	if e := cl.ReportStatus(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	snapshot := core.Status("a")
	snapshot.Versions["cs2"] = "changed"
	if core.Status("a").Versions["cs2"] != "1" {
		t.Fatal("mutable status map leaked")
	}
}
func TestDisabledDiscordTransportDoesNotClaimOwnership(t *testing.T) {
	cfg := fixture(t)
	dispatcher, e := alerts.NewDiscord(alerts.Config{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer dispatcher.Close(context.Background())
	_, stop := start(t, cfg, dispatcher)
	defer stop()
	cl := connect(t, cfg)
	if owner, e := cl.ClaimRun(context.Background(), "run"); e != nil || owner != "standalone" {
		t.Fatalf("disabled transport owns delivery: %s %v", owner, e)
	}
}

func (s *receiptSink) Enabled() bool { return true }
func TestStaleRunCannotReplaceCurrentStatus(t *testing.T) {
	cfg := fixture(t)
	core, stop := start(t, cfg, nil)
	defer stop()
	cl := connect(t, cfg)
	cl.ClaimRun(context.Background(), "old")
	cl.ReportStatus(context.Background(), server.Status{RunID: "old", State: "ready", Ready: true})
	cl.ClaimRun(context.Background(), "new")
	cl.ReportStatus(context.Background(), server.Status{RunID: "new", State: "starting"})
	if e := cl.ReportStatus(context.Background(), server.Status{RunID: "old", State: "stopped"}); e == nil {
		t.Fatal("stale run replaced current status")
	}
	if got := core.Status("a"); got.RunID != "new" || got.State == "stopped" {
		t.Fatalf("stale status applied: %+v", got)
	}
}

func TestTerminalStatusSurvivesBackloggedReadyObservation(t *testing.T) {
	for _, kind := range []string{"snapshot", "legacy_status", "up", "direct_report"} {
		t.Run(kind, func(t *testing.T) {
			cfg := fixture(t)
			core, stop := start(t, cfg, nil)
			defer stop()
			runtime := connect(t, cfg)
			ctx := context.Background()
			route := runtime.Route(nil)
			if _, err := runtime.ClaimRun(ctx, "run"); err != nil {
				t.Fatal(err)
			}
			ready := server.Status{RunID: "run", State: "ready", Ready: true, Players: server.Players{Known: true, Count: 3}, Versions: map[string]string{"cs2": "build"}}
			if err := runtime.ReportStatus(ctx, ready); err != nil {
				t.Fatal(err)
			}
			old := event("older")
			old.At = time.Now().Add(-time.Second)
			old.Type = "server.status"
			old.Reason = "query_ok"
			old.Details = map[string]string{"players": "3", "map": "de_dust2"}
			if kind == "snapshot" {
				b, _ := json.Marshal(ready)
				old.Details["runtime_status"] = string(b)
			} else if kind == "up" {
				old.Type = "server.up"
			}
			down := event("down")
			down.Type = "server.down"
			down.Reason = "console_stop"
			if _, err := runtime.PublishEvent(ctx, down); err != nil {
				t.Fatal(err)
			}
			if kind == "direct_report" {
				if err := runtime.ReportStatus(ctx, ready); err != nil {
					t.Fatal(err)
				}
			} else if err := route.Publish(ctx, old); err != nil {
				t.Fatal(err)
			}
			got := core.Status("a")
			if got.State != "stopped" || got.Ready || got.Players.Known {
				t.Fatalf("backlogged observation resurrected terminal run: %+v", got)
			}
			runtime.Close()
			observer := connect(t, cfg)
			got, err := observer.Status(ctx)
			if err != nil || got.State != "stopped" {
				t.Fatalf("terminal status lost on disconnect: %+v %v", got, err)
			}
			if _, err = observer.ClaimRun(ctx, "new-run"); err != nil {
				t.Fatal(err)
			}
			ready.RunID = "new-run"
			if err = observer.ReportStatus(ctx, ready); err != nil {
				t.Fatal(err)
			}
			if got = core.Status("a"); got.RunID != "new-run" || !got.Ready {
				t.Fatalf("new run could not become ready: %+v", got)
			}
		})
	}
}

func TestRuntimeDisconnectWhileStatusObserverRemains(t *testing.T) {
	cfg := fixture(t)
	core, stop := start(t, cfg, nil)
	defer stop()
	runtime := connect(t, cfg)
	observer := connect(t, cfg)
	ctx := context.Background()
	if _, err := runtime.ClaimRun(ctx, "run"); err != nil {
		t.Fatal(err)
	}
	route := runtime.Route(nil)
	snapshot, _ := json.Marshal(server.Status{RunID: "run", State: "ready", Ready: true, Players: server.Players{Known: true, Count: 5}})
	readyEvent := event("ready")
	readyEvent.Type = "server.status"
	readyEvent.Reason = "query_ok"
	readyEvent.Details = map[string]string{"runtime_status": string(snapshot)}
	if err := route.Publish(ctx, readyEvent); err != nil {
		t.Fatal(err)
	}
	if got, err := observer.Status(ctx); err != nil || !got.Ready {
		t.Fatalf("observer missing ready report: %+v %v", got, err)
	}
	runtime.Close()
	waitFor(t, func() bool { return core.Status("a").State == "lost_contact" })
	got, err := observer.Status(ctx)
	if err != nil || got.Ready || got.Players.Known || got.State != "lost_contact" || got.RunID != "run" {
		t.Fatalf("observer masked runtime loss: %+v %v", got, err)
	}
	replacement := connect(t, cfg)
	if _, err = replacement.ClaimRun(ctx, "run"); err != nil {
		t.Fatal(err)
	}
	if got = core.Status("a"); got.State != "lost_contact" {
		t.Fatalf("reconnect fabricated readiness: %+v", got)
	}
	if err = replacement.ReportStatus(ctx, server.Status{RunID: "run", State: "ready", Ready: true}); err != nil {
		t.Fatal(err)
	}
	if got = core.Status("a"); !got.Ready {
		t.Fatal("fresh reporting session did not restore readiness")
	}
}

func TestOnlyCurrentRunReportersRetainContact(t *testing.T) {
	for _, closeOldFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(closeOldFirst), func(t *testing.T) {
			cfg := fixture(t)
			core, stop := start(t, cfg, nil)
			defer stop()
			old := connect(t, cfg)
			current := connect(t, cfg)
			ctx := context.Background()
			old.ClaimRun(ctx, "old")
			old.ReportStatus(ctx, server.Status{RunID: "old", State: "ready", Ready: true})
			current.ClaimRun(ctx, "current")
			if err := current.ReportStatus(ctx, server.Status{RunID: "current", State: "ready", Ready: true, Players: server.Players{Known: true, Count: 2}}); err != nil {
				t.Fatal(err)
			}
			if closeOldFirst {
				old.Close()
				waitFor(t, func() bool { core.mu.Lock(); defer core.mu.Unlock(); return len(core.contacts["a"]) == 1 })
				if got := core.Status("a"); got.RunID != "current" || !got.Ready {
					t.Fatalf("prior run disconnect changed current report: %+v", got)
				}
			}
			current.Close()
			waitFor(t, func() bool { return core.Status("a").State == "lost_contact" })
			if got := core.Status("a"); got.RunID != "current" || got.Ready || got.Players.Known {
				t.Fatalf("old reporter retained current contact: %+v", got)
			}
		})
	}
}

func TestReadOnlySessionLeavesUnknownStatusUnknown(t *testing.T) {
	cfg := fixture(t)
	core, stop := start(t, cfg, nil)
	defer stop()
	observer := connect(t, cfg)
	if _, err := observer.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	observer.Close() // Synchronize via a new server request, then inspect that no reporter was ever registered.
	other := connect(t, cfg)
	if _, err := other.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	contacts := len(core.contacts["a"])
	core.mu.Unlock()
	if got := core.Status("a"); got.State != "unknown" || got.RunID != "" || contacts != 0 {
		t.Fatalf("read-only session fabricated runtime contact: %+v, reporters=%d", got, contacts)
	}
}
