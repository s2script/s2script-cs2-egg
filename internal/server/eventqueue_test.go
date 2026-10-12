// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/events"
)

type slowSink struct {
	entered chan struct{}
	active  atomic.Int32
	mu      sync.Mutex
	seen    map[string]int
}

func (s *slowSink) Publish(ctx context.Context, e events.Event) error {
	s.active.Add(1)
	defer s.active.Add(-1)
	s.mu.Lock()
	s.seen[e.ID]++
	s.mu.Unlock()
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}
func TestSlowSinkFloodHasBoundedDrainAndNoWorkerLeak(t *testing.T) {
	s := &slowSink{entered: make(chan struct{}, 1), seen: map[string]int{}}
	q := newEventQueue(s, io.Discard, time.Second, 30*time.Millisecond)
	start := time.Now()
	for i := 0; i < 10000; i++ {
		q.publish(events.Event{ID: itoa(i), Type: "server.console"})
	}
	q.publish(events.Event{ID: "up", Type: "server.up"})
	q.publish(events.Event{ID: "down", Type: "server.down"})
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("console flood blocked publisher")
	}
	select {
	case <-s.entered:
	case <-time.After(time.Second):
		t.Fatal("no event handed to sink")
	}
	q.finish()
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("final drain was not absolutely bounded")
	}
	if s.active.Load() != 0 {
		t.Fatal("publish worker leaked")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.seen {
		if n > 1 {
			t.Fatal("retried raw event")
		}
	}
}

type gatedSink struct {
	release chan struct{}
	started chan struct{}
	mu      sync.Mutex
	seen    map[string]int
	once    sync.Once
}

func (s *gatedSink) Publish(ctx context.Context, e events.Event) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.seen[e.ID]++
	s.mu.Unlock()
	return nil
}
func TestConsoleFloodCannotDisplaceLifecycle(t *testing.T) {
	s := &gatedSink{release: make(chan struct{}), started: make(chan struct{}), seen: map[string]int{}}
	q := newEventQueue(s, io.Discard, time.Second, time.Second)
	q.publish(events.Event{ID: "first", Type: "server.console"})
	select {
	case <-s.started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no raw event handed to sink")
	}
	for i := 0; i < 10000; i++ {
		q.publish(events.Event{ID: itoa(i), Type: "server.console"})
	}
	q.publish(events.Event{ID: "up", Type: "server.up"})
	q.publish(events.Event{ID: "down", Type: "server.down"})
	close(s.release)
	q.finish()
	for _, id := range []string{"up", "down"} {
		if s.seen[id] != 1 {
			t.Fatalf("%s handed off %d times", id, s.seen[id])
		}
	}
}

type readinessWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *readinessWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), "S2EGG_READY") {
		w.once.Do(func() { close(w.ready) })
	}
	return len(b), nil
}
func TestSlowSinkConsoleFloodDoesNotDelayProcessShutdown(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	s := &slowSink{entered: make(chan struct{}, 1), seen: map[string]int{}}
	out := &readinessWriter{ready: make(chan struct{})}
	r, w := io.Pipe()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { runWith(ctx, cfg, s, r, out, io.Discard, o); close(done) }()
	select {
	case <-out.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("slow sink blocked readiness")
	}
	fmt.Fprintln(w, "flood")
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("console flood blocked process shutdown")
	}
	if time.Since(start) > 3*time.Second || s.active.Load() != 0 {
		t.Fatal("sink worker survived bounded drain")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.seen {
		if n != 1 {
			t.Fatal("duplicate raw-event handoff")
		}
	}
}
