// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func trialFixture(t *testing.T) (*Manager, Journal) {
	t.Helper()
	m := fixtureManager(t, "binary")
	ctx := context.Background()
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if err := m.Stage(ctx, fixtureRelease(t, v, "binary")); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Initialize(ctx, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	j, err := m.Activate(ctx, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	return m, j
}
func TestFailedActivationCanRollback(t *testing.T) {
	m, j := trialFixture(t)
	calls := []string{}
	var childExit chan int
	l := Launcher{Notify: func() error { childExit <- 0; return nil }, Manager: m, Timeout: 5 * time.Second, Start: func(ctx context.Context, path, config, tx string) (Child, error) {
		v := filepath.Base(filepath.Dir(path))
		calls = append(calls, v)
		if v == "v1.1.0" {
			saved, _ := m.Status()
			if saved.Phase != "trial-started" {
				t.Fatal("candidate started without durable trial")
			}
			return Child{}, errors.New("candidate startup failed")
		}
		ready := make(chan Readiness, 1)
		ready <- Readiness{Transaction: tx, Version: "1.0.0", Revision: fixtureRelease(t, "1.0.0", "binary").SourceRevision}
		exit := make(chan int, 1)
		childExit = exit
		return Child{Ready: ready, Exit: exit, Stop: func() {}}, nil
	}}
	if code := l.Run(context.Background()); code != 0 {
		t.Fatal(code)
	}
	got, err := m.Status()
	if err != nil || got.Current != "1.0.0" || got.Phase != "good" || got.FailedVersion != "1.1.0" || got.Transaction != j.Transaction || len(calls) != 2 {
		t.Fatal(got, calls, err)
	}
}
func TestInterruptedTrialNeverRetriesCandidate(t *testing.T) {
	m, j := trialFixture(t)
	j.Phase = "trial-started"
	m.save(j)
	calls := 0
	l := Launcher{Manager: m, Timeout: 5 * time.Second, Start: func(_ context.Context, path, _, _ string) (Child, error) {
		calls++
		if filepath.Base(filepath.Dir(path)) != "v1.0.0" {
			t.Fatal("interrupted candidate replayed")
		}
		return Child{}, errors.New("fallback failed")
	}}
	if l.Run(context.Background()) != FailureExit || calls != 1 {
		t.Fatal("fallback attempt count", calls)
	}
	if l.Run(context.Background()) != FailureExit || calls != 1 {
		t.Fatal("failed recovery retried")
	}
}
func TestOperatorStopDoesNotActivateOrFallback(t *testing.T) {
	m, _ := trialFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	l := Launcher{Manager: m, Timeout: 5 * time.Second, Start: func(context.Context, string, string, string) (Child, error) {
		calls++
		cancel()
		return Child{Ready: make(chan Readiness), Exit: make(chan int), Stop: func() {}}, nil
	}}
	if l.Run(ctx) != 0 || calls != 1 {
		t.Fatal("operator stop restarted child", calls)
	}
}
func TestSuccessfulTrialCommitsAndRetainsPredecessor(t *testing.T) {
	m, _ := trialFixture(t)
	exit := make(chan int, 1)
	l := Launcher{Manager: m, Timeout: 5 * time.Second, Notify: func() error { exit <- 0; return nil }, Start: func(_ context.Context, _, _, tx string) (Child, error) {
		ready := make(chan Readiness, 1)
		ready <- Readiness{Transaction: tx, Version: "1.1.0", Revision: fixtureRelease(t, "1.1.0", "binary").SourceRevision}
		return Child{Ready: ready, Exit: exit, Stop: func() {}}, nil
	}}
	if l.Run(context.Background()) != 0 {
		t.Fatal("activation failed")
	}
	j, err := m.Status()
	if err != nil || j.Current != "1.1.0" || j.Previous != "1.0.0" || j.Outcome != "activated" || j.Phase != "good" {
		t.Fatal(j, err)
	}
}
