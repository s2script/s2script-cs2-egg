// SPDX-License-Identifier: GPL-3.0-or-later
package scheduler

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakePower struct {
	restarts int
	state    string
	err      error
}

func (f *fakePower) Restart(context.Context, string) error                      { f.restarts++; return f.err }
func (f *fakePower) Stop(context.Context, string) error                         { return f.err }
func (f *fakePower) Start(context.Context, string) error                        { return f.err }
func (f *fakePower) State(context.Context, string) (string, error)              { return f.state, nil }
func (f *fakePower) Warning(context.Context, string, int) error                 { return nil }
func (f *fakePower) RequestScheduledStop(context.Context, string, string) error { return f.err }
func jobFixture(t *testing.T) (*Engine, *fakePower, *server.Status, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	s := fresh(now)
	s.Versions = map[string]string{"shared_generation": "old"}
	w := &fakePower{state: "running"}
	e, err := Open(Options{Directory: t.TempDir(), Power: w, Status: func(string) server.Status { return s }, Pin: func(context.Context, string, string) error { return nil }, Select: func(context.Context, string, string) error { return nil }, Clear: func(context.Context, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return e, w, &s, now
}
func TestDuplicateUpdateCreatesOneRestartJob(t *testing.T) {
	e, _, s, n := jobFixture(t)
	a, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "immediate", MaximumDelay: time.Hour}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Schedule(context.Background(), "srv", a.Target, "", a.Policy, *s, n)
	if err != nil || a.ID != b.ID || len(e.Jobs()) != 1 {
		t.Fatal(a, b, err)
	}
}
func TestFailedWingsRestartRetainsPendingJob(t *testing.T) {
	e, w, s, n := jobFixture(t)
	w.err = errors.New("timeout")
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "immediate", MaximumDelay: time.Hour}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	_ = e.Step(context.Background(), j.ID, n)
	if got := e.Jobs()[0]; got.Phase == "done" || got.Phase == "waiting" || w.restarts != 1 {
		t.Fatal(got, w.restarts)
	}
	_ = e.Step(context.Background(), j.ID, n.Add(time.Second))
	if w.restarts != 1 {
		t.Fatal("ambiguous restart replayed")
	}
}

func TestRecoveryDoesNotReplayAndRequiresExactTarget(t *testing.T) {
	e, w, s, n := jobFixture(t)
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "immediate", MaximumDelay: time.Hour}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Step(context.Background(), j.ID, n); err != nil {
		t.Fatal(err)
	}
	e.Close()
	again, err := Open(e.o)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if err = again.Step(context.Background(), j.ID, n); err != nil {
		t.Fatal(err)
	}
	if w.restarts != 1 || again.Jobs()[0].Phase != "hold" {
		t.Fatal("replayed accepted action")
	}
	s.RunID = "new-run"
	s.Versions = map[string]string{"shared_generation": "wrong", "steam_build": "200"}
	again.Step(context.Background(), j.ID, n)
	if again.Jobs()[0].Phase == "done" {
		t.Fatal("wrong generation completed")
	}
	s.Versions["shared_generation"] = j.Target.Generation
	again.Step(context.Background(), j.ID, n)
	if again.Jobs()[0].Phase != "done" {
		t.Fatal(again.Jobs())
	}
}
func TestOperatorStopWhileWaitingNeverStarts(t *testing.T) {
	e, w, s, n := jobFixture(t)
	p := Policy{Mode: "when_empty", MaximumDelay: time.Minute}
	s.Players.Count = 3
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "copy", p, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	s.State = "stopped"
	s.IntendedStopReason = "console_stop"
	w.state = "offline"
	if err = e.Step(context.Background(), j.ID, n.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if e.Jobs()[0].Phase != "hold" || w.restarts != 0 {
		t.Fatal(e.Jobs())
	}
	if err = e.Resolve(context.Background(), j.ID, "retry", false, n); err == nil {
		t.Fatal("offline retry lacked explicit authority")
	}
}
func TestHardlinkRequiresCorrelatedTerminationAndHelper(t *testing.T) {
	e, w, s, n := jobFixture(t)
	attached := 0
	e.o.Attach = func(context.Context, string, string, string) error { attached++; return nil }
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "hardlink", Policy{Mode: "immediate", MaximumDelay: time.Hour}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	step := func() {
		t.Helper()
		if err := e.Step(context.Background(), j.ID, n); err != nil {
			t.Fatal(err)
		}
	}
	step()
	armed := e.Jobs()[0]
	s.ScheduledActionID = armed.ActionID
	step()
	w.state = "offline"
	s.State = "stopped"
	s.IntendedStopReason = "console_stop"
	step()
	if attached != 0 {
		t.Fatal("unrelated termination attached")
	}
	s.IntendedStopReason = "scheduled_update"
	step()
	step()
	if attached != 1 || e.Jobs()[0].Phase != "attached" {
		t.Fatal(e.Jobs(), attached)
	}
	step()
	if e.Jobs()[0].Phase != "start_requested" {
		t.Fatal(e.Jobs())
	}
	if w.restarts != 0 {
		t.Fatal("hardlink used blind restart")
	}
}
func TestConcurrentRequestsAndDSTExecuteOnce(t *testing.T) {
	e, w, s, _ := jobFixture(t)
	n := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	s.SampleAt = n
	s.ReceivedAt = n
	p := Policy{Mode: "maintenance_window", Timezone: "America/Chicago", WindowStart: "01:00", WindowEnd: "02:00", MaximumDelay: 4 * time.Hour}
	target := Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Schedule(context.Background(), "srv", target, "", p, *s, n); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(e.Jobs()) != 1 {
		t.Fatal(e.Jobs())
	}
	j := e.Jobs()[0]
	if err := e.Step(context.Background(), j.ID, n); err != nil {
		t.Fatal(err)
	}
	s.SampleAt = n.Add(time.Hour)
	s.ReceivedAt = s.SampleAt
	if err := e.Step(context.Background(), j.ID, n.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if w.restarts != 1 {
		t.Fatal("DST repeated action", w.restarts)
	}
}
func TestCountdownTruncatesAtDeadline(t *testing.T) {
	e, w, s, n := jobFixture(t)
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "immediate", MaximumDelay: 10 * time.Second, WarningOffsets: []time.Duration{time.Minute, 0}}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	e.Step(context.Background(), j.ID, n)
	if w.restarts != 0 || !e.Jobs()[0].ExecuteAt.Equal(n.Add(10*time.Second)) {
		t.Fatal(e.Jobs())
	}
	s.SampleAt = n.Add(10 * time.Second)
	s.ReceivedAt = s.SampleAt
	e.Step(context.Background(), j.ID, s.SampleAt)
	if w.restarts != 1 || len(e.Jobs()[0].Warnings) != 2 {
		t.Fatal(e.Jobs())
	}
}

func TestArmRechecksPlayersAndExplicitOfflineRetryHonorsAdmin(t *testing.T) {
	e, w, s, n := jobFixture(t)
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "copy", Policy{Mode: "when_empty", MaximumDelay: time.Hour}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	e.Step(context.Background(), j.ID, n)
	s.ScheduledActionID = e.Jobs()[0].ActionID
	s.Players.Count = 2
	e.Step(context.Background(), j.ID, n)
	if e.Jobs()[0].Phase != "waiting" {
		t.Fatal("arming ignored newly occupied server", e.Jobs())
	}
	s.State = "stopped"
	s.OperatorVeto = true
	w.state = "offline"
	e.Step(context.Background(), j.ID, n)
	e.o.Attach = func(context.Context, string, string, string) error { return nil }
	if err = e.Resolve(context.Background(), j.ID, "retry", true, n); err != nil {
		t.Fatal(err)
	}
	e.Step(context.Background(), j.ID, n)
	if e.Jobs()[0].Phase != "attached" {
		t.Fatal("explicit admin start authorization did not resume", e.Jobs())
	}
}

func TestInvalidRecoverySnapshotFailsClosed(t *testing.T) {
	e, _, _, _ := jobFixture(t)
	e.Close()
	path := filepath.Join(e.o.Directory, "jobs.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"jobs":[],"unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(e.o); err == nil {
		opened.Close()
		t.Fatal("unknown recovery fields accepted")
	}
}

func TestCompletionRetriesTargetCleanupBeforeDone(t *testing.T) {
	e, _, s, n := jobFixture(t)
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "immediate", MaximumDelay: time.Hour}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	e.Step(context.Background(), j.ID, n)
	s.RunID = "new-run"
	s.Versions = map[string]string{"shared_generation": j.Target.Generation, "steam_build": "200"}
	attempts := 0
	e.o.Clear = func(context.Context, string, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("store busy")
		}
		return nil
	}
	if err = e.Step(context.Background(), j.ID, n); err == nil {
		t.Fatal("cleanup error missing")
	}
	if e.Jobs()[0].Phase == "done" {
		t.Fatal("cleanup failure lost retry")
	}
	if err = e.Step(context.Background(), j.ID, n); err != nil || e.Jobs()[0].Phase != "done" || attempts != 2 {
		t.Fatal(e.Jobs(), attempts, err)
	}
}
