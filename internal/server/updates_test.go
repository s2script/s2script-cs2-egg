// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestStandaloneUpdateDoesNotExitWithoutRestartAdapter(t *testing.T) {
	cfg := config.Server{Root: t.TempDir(), Updates: config.ServerUpdates{Enabled: true, Policy: config.RestartPolicy{Mode: "immediate", MaxDelaySeconds: 30, FreshnessSeconds: 15}}}
	u := newUpdateSession(cfg, false)
	u.latest = func(context.Context, string) (string, error) { return "200", nil }
	u.installed = "100"
	u.active = true
	u.check(context.Background())
	n := time.Now().UTC()
	s := Status{RunID: "run", Ready: true, State: "ready", SampleAt: n, Players: Players{Known: true}, Versions: map[string]string{"steam_build": "100"}}
	u.observe(s, nil)
	if u.pendingBuild() != "200" {
		t.Fatal("missing pending status")
	}
	select {
	case <-u.request:
		t.Fatal("no adapter stopped game")
	default:
	}
}
func TestStandaloneOperatorVetoPreventsUpdateRequest(t *testing.T) {
	cfg := config.Server{Updates: config.ServerUpdates{Policy: config.RestartPolicy{Mode: "immediate", MaxDelaySeconds: 30, FreshnessSeconds: 15}}}
	u := newUpdateSession(cfg, true)
	u.target = "200"
	u.pending = time.Now().Add(-time.Hour)
	u.veto.Store(true)
	u.observe(Status{RunID: "run", Ready: true, SampleAt: time.Now(), Versions: map[string]string{"steam_build": "100"}}, nil)
	select {
	case <-u.request:
		t.Fatal("operator veto lost")
	default:
	}
}

func TestSupervisorRestartsChildAndKeepsOwnedInput(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	cfg.Updates = config.ServerUpdates{Enabled: true, Adapter: "supervisor", PollSeconds: 10, Policy: config.RestartPolicy{Mode: "immediate", MaxDelaySeconds: 30, FreshnessSeconds: 15}}
	var preparations atomic.Int32
	o.prepare = func(context.Context, config.Server, emitFunc) (map[string]string, error) {
		build := "100"
		if preparations.Add(1) > 1 {
			build = "200"
		}
		return map[string]string{"steam_build": build}, nil
	}
	o.latest = func(context.Context, string) (string, error) { return "200", nil }
	o.updatePoll = 10 * time.Millisecond
	sink := &recordSink{ch: make(chan events.Event, 128)}
	input, write := io.Pipe()
	defer write.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := runSupervisor(ctx, cfg, sink, input, io.Discard, io.Discard, o, nil); done <- e }()
	first := nextEvent(t, sink, "server.up")
	second := nextEvent(t, sink, "server.up")
	if first.RunID == second.RunID || preparations.Load() != 2 {
		t.Fatal("no real child restart", preparations.Load())
	}
	if _, e := io.WriteString(write, "quit\n"); e != nil {
		t.Fatal("input closed across update", e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("supervisor did not exit on operator stop")
	}
	if preparations.Load() != 2 {
		t.Fatal("operator stop restarted child")
	}
}

func TestStandalonePendingOnlyKeepsLiveChild(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	cfg.Updates = config.ServerUpdates{Enabled: true, Adapter: "unsupported", PollSeconds: 10, Policy: config.RestartPolicy{Mode: "immediate", MaxDelaySeconds: 1, FreshnessSeconds: 15}}
	o.prepare = func(context.Context, config.Server, emitFunc) (map[string]string, error) {
		return map[string]string{"steam_build": "100"}, nil
	}
	o.latest = func(context.Context, string) (string, error) { return "200", nil }
	o.updatePoll = 10 * time.Millisecond
	sink := &recordSink{ch: make(chan events.Event, 128)}
	input, write := io.Pipe()
	defer write.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := runSupervisor(ctx, cfg, sink, input, io.Discard, io.Discard, o, nil); done <- e }()
	first := nextEvent(t, sink, "server.up")
	for {
		status, e := Snapshot(ctx, cfg.Root)
		if e == nil && status.UpdatePending == "200" {
			if status.RunID != first.RunID {
				t.Fatal("pending-only adapter restarted")
			}
			break
		}
		select {
		case e := <-done:
			t.Fatal("pending-only runtime exited", e)
		case <-ctx.Done():
			t.Fatal("pending status unavailable")
		case <-time.After(5 * time.Millisecond):
		}
	}
	io.WriteString(write, "quit\n")
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("operator quit blocked")
	}
}

func TestStandalonePreparationUsesActualInstalledTarget(t *testing.T) {
	u := newUpdateSession(config.Server{}, true)
	u.target = "200"
	u.requested = true
	u.prepared(map[string]string{"steam_build": "201"})
	if u.pendingBuild() != "201" {
		t.Fatal("metadata target was not reconciled to installed build")
	}
	u.observe(Status{Ready: true, Versions: map[string]string{"steam_build": "201"}}, nil)
	if u.pendingBuild() != "" {
		t.Fatal("ready prepared build left update permanently pending")
	}
}
