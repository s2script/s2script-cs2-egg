// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/node"
)

type cliReceipt struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *cliReceipt) Publish(context.Context, events.Event) error {
	panic("async Publish is not a transport receipt")
}
func (s *cliReceipt) Accepts(e events.Event) bool { return e.Type == "server.start_failed" }
func (s *cliReceipt) Deliver(ctx context.Context, e events.Event) (alerts.DeliveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return alerts.Delivered, nil
}
func TestCLINodeIdentityReplacesOnlyActiveStandaloneIdentity(t *testing.T) {
	dir, e := os.MkdirTemp("", "s2cli-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	root := filepath.Join(dir, "volume")
	os.MkdirAll(filepath.Join(root, "egg"), 0700)
	const old = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	os.WriteFile(filepath.Join(root, "egg/server-id"), []byte(old), 0600) // Invalid existing installer guarantees an offline startup failure.
	os.MkdirAll(filepath.Join(root, "steamcmd"), 0700)
	os.Symlink("/absent-fixture", filepath.Join(root, "steamcmd/steamcmd.sh"))
	cfg := config.Node{SchemaVersion: 1, ID: "host", StateDir: filepath.Join(dir, "state"), AdminSocket: filepath.Join(dir, "admin/admin.sock"), Servers: []config.NodeServer{{ID: "authoritative-volume", Volume: root, PeerUID: uint32(os.Getuid()), AccessGID: os.Getgid(), Allocation: config.Allocation{IP: "127.0.0.1", Port: 27015}}}}
	sink := &cliReceipt{}
	core, e := node.New(cfg, sink)
	if e != nil {
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
	select {
	case <-core.Ready():
	case <-time.After(time.Second):
		t.Fatal("core not ready")
	}
	var out, errs bytes.Buffer
	code := run([]string{"run", "--root", root}, func(k string) (string, bool) { return "1", k == "ALLOW_TOKENLESS" }, &out, &errs)
	if code == 0 {
		t.Fatal("fixture unexpectedly started")
	}
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		sink.mu.Lock()
		n := len(sink.events)
		sink.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 1 {
		t.Fatalf("events %d: %s", len(sink.events), errs.String())
	}
	ev := sink.events[0]
	if ev.ServerID != "authoritative-volume" || ev.NodeID != "host" || ev.ID == "" || ev.RunID == "" || ev.CorrelationID != ev.RunID {
		t.Fatalf("identity migration: %+v", ev)
	}
	saved, e := os.ReadFile(filepath.Join(root, "egg/server-id"))
	if e != nil || string(saved) != old {
		t.Fatal("standalone identity changed")
	}
}

func (s *cliReceipt) Enabled() bool { return true }
