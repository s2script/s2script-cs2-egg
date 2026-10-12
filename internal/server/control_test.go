// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"io"
	"sync"
	"testing"
	"time"
)

func TestScheduledStopRequiresAcknowledgedTokenAndPreservesQuit(t *testing.T) {
	n := time.Now()
	g := &stopControl{run: "run"}
	token := "0123456789abcdef0123456789abcdef"
	if handled, reason := g.Handle("s2egg_scheduled_stop " + token); !handled || reason != "" {
		t.Fatal("unacked opcode accepted")
	}
	g.Arm(protocol.UpdateControl{ActionID: token, RunID: "other", ExpiresAt: n.Add(time.Minute)})
	if _, why := g.Handle("s2egg_scheduled_stop " + token); why != "" {
		t.Fatal("wrong run accepted")
	}
	g.Arm(protocol.UpdateControl{ActionID: token, RunID: "run", ExpiresAt: n.Add(time.Minute)})
	if _, why := g.Handle("s2egg_scheduled_stop " + token); why != "scheduled_update" {
		t.Fatal(why)
	}
	g.Handle("quit")
	if _, why := g.Handle("s2egg_scheduled_stop " + token); why != "" {
		t.Fatal("operator veto overridden")
	}
}

type fixtureControl struct {
	ack  chan struct{}
	once sync.Once
}

func (c *fixtureControl) Control(ctx context.Context, s Status) (protocol.UpdateControl, error) {
	token := "0123456789abcdef0123456789abcdef"
	if s.ScheduledActionID == token {
		c.once.Do(func() { close(c.ack) })
	}
	return protocol.UpdateControl{ActionID: token, RunID: s.RunID, Target: "target", ExpiresAt: time.Now().Add(30 * time.Second)}, nil
}
func TestRuntimeScheduledStopReportsCorrelatedTermination(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	control := &fixtureControl{ack: make(chan struct{})}
	o.control = control
	sink := &recordSink{ch: make(chan events.Event, 128)}
	in, write := io.Pipe()
	defer write.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); runWith(ctx, cfg, sink, in, io.Discard, io.Discard, o) }()
	select {
	case <-control.ack:
	case <-ctx.Done():
		t.Fatal("no current-run ack")
	}
	io.WriteString(write, "s2egg_scheduled_stop 0123456789abcdef0123456789abcdef\n")
	event := nextEvent(t, sink, "server.down")
	if event.Reason != "scheduled_update" || event.Details["action_id"] != "0123456789abcdef0123456789abcdef" || event.Details["operator_veto"] != "false" {
		t.Fatal(event)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("scheduled stop did not exit")
	}
}
