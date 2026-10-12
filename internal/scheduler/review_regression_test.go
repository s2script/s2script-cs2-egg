// SPDX-License-Identifier: GPL-3.0-or-later
package scheduler

import (
	"context"
	"testing"
	"time"
)

type reviewPower struct {
	*fakePower
	warn func()
}

func (w *reviewPower) Warning(context.Context, string, int) error { w.warn(); return nil }

func TestReviewOperatorStopDuringWarning(t *testing.T) {
	e, w, s, n := jobFixture(t)
	defer e.Close()
	e.o.Power = &reviewPower{w, func() {
		s.OperatorVeto = true
		s.IntendedStopReason = "console_stop"
		s.State = "stopped"
		w.state = "offline"
	}}
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "when_empty", MaximumDelay: time.Hour, WarningOffsets: []time.Duration{0}}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Step(context.Background(), j.ID, n); err != nil {
		t.Fatal(err)
	}
	if w.restarts != 0 {
		t.Fatal("restarted after operator stop became observable during warning")
	}
}

func TestReviewCancelHistoricalJobPreservesCurrentTarget(t *testing.T) {
	e, _, s, n := jobFixture(t)
	defer e.Close()
	selected, pinned := "", ""
	clears := 0
	e.o.Pin = func(_ context.Context, _, generation string) error { pinned = generation; return nil }
	e.o.Clear = func(_ context.Context, _, generation string) error {
		clears++
		if selected == generation {
			selected = ""
		}
		if pinned == generation {
			pinned = ""
		}
		return nil
	}
	e.o.Select = func(_ context.Context, _, generation string) error { selected = generation; return nil }
	a, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "immediate", MaximumDelay: time.Hour}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Step(context.Background(), a.ID, n); err != nil {
		t.Fatal(err)
	}
	s.RunID = "new-run"
	s.Versions = map[string]string{"shared_generation": a.Target.Generation, "steam_build": "200"}
	if err = e.Step(context.Background(), a.ID, n); err != nil {
		t.Fatal(err)
	}
	b, err := e.Schedule(context.Background(), "srv", Target{Generation: "abcdef0123456789abcdef0123456789", Build: "201"}, "", a.Policy, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Step(context.Background(), b.ID, n); err != nil {
		t.Fatal(err)
	}
	if selected != b.Target.Generation {
		t.Fatal("setup failed")
	}
	before := clears
	for i := 0; i < 3; i++ {
		if err = e.Resolve(context.Background(), a.ID, "cancel", false, n); err != nil {
			t.Fatal(err)
		}
	}
	if selected != b.Target.Generation || pinned != b.Target.Generation || clears != before || e.Jobs()[0].Phase != "done" {
		t.Fatal("historical cancellation mutated active target/pin or history", e.Jobs(), clears)
	}
	if err = e.Resolve(context.Background(), b.ID, "cancel", false, n); err != nil {
		t.Fatal(err)
	}
	before = clears
	if err = e.Resolve(context.Background(), b.ID, "cancel", false, n); err != nil {
		t.Fatal(err)
	}
	if clears != before || selected != "" || pinned != "" {
		t.Fatal("repeated canceled cleanup")
	}

}

type boundaryPower struct {
	*fakePower
	stateHook, warningHook   func()
	stops, starts, scheduled int
}

func (w *boundaryPower) State(context.Context, string) (string, error) {
	if w.stateHook != nil {
		w.stateHook()
	}
	return w.state, nil
}
func (w *boundaryPower) Warning(context.Context, string, int) error {
	if w.warningHook != nil {
		w.warningHook()
	}
	return nil
}
func (w *boundaryPower) Stop(context.Context, string) error  { w.stops++; return nil }
func (w *boundaryPower) Start(context.Context, string) error { w.starts++; return nil }
func (w *boundaryPower) RequestScheduledStop(context.Context, string, string) error {
	w.scheduled++
	return nil
}

func TestDispatchRechecksBlockingBoundaries(t *testing.T) {
	for _, boundary := range []string{"state", "warning", "selection"} {
		for _, change := range []string{"operator_stop", "offline", "new_run", "occupied", "unknown", "stale", "elapsed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				e, w, s, n := jobFixture(t)
				defer e.Close()
				power := &boundaryPower{fakePower: w}
				e.o.Power = power
				mutate := func() {
					switch change {
					case "operator_stop":
						s.OperatorVeto = true
						s.State = "stopped"
						s.IntendedStopReason = "console_stop"
						w.state = "offline"
					case "offline":
						w.state = "offline"
					case "new_run":
						s.RunID = "replacement"
					case "occupied":
						s.Players.Count = 2
					case "unknown":
						s.Players.Known = false
					case "stale":
						s.SampleAt = n.Add(-time.Hour)
					case "elapsed":
						time.Sleep(150 * time.Millisecond)
					}
				}
				once := false
				hook := func() {
					if !once {
						once = true
						mutate()
					}
				}
				switch boundary {
				case "state":
					power.stateHook = hook
				case "warning":
					power.warningHook = hook
				case "selection":
					e.o.Select = func(context.Context, string, string) error { hook(); return nil }
				}
				p := Policy{Mode: "when_empty", MaximumDelay: time.Hour, WarningOffsets: []time.Duration{0}, Freshness: 100 * time.Millisecond}
				j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", p, *s, n)
				if err != nil {
					t.Fatal(err)
				}
				if err = e.Step(context.Background(), j.ID, n); err != nil {
					t.Fatal(err)
				}
				if !once {
					t.Fatal("boundary not exercised")
				}
				if w.restarts != 0 {
					t.Fatal("dispatched using pre-call evidence", e.Jobs())
				}
			})
		}
	}
}

func TestStopAndStartRecheckStatusAfterState(t *testing.T) {
	for _, phase := range []string{"arming", "stop_requested", "attached"} {
		t.Run(phase, func(t *testing.T) {
			e, w, s, n := jobFixture(t)
			defer e.Close()
			j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "copy", Policy{Mode: "immediate", MaximumDelay: time.Hour}, *s, n)
			if err != nil {
				t.Fatal(err)
			}
			e.jobs[0].Phase = phase
			e.jobs[0].ActionID = "abcdef0123456789abcdef0123456789"
			e.jobs[0].ExpiresAt = n.Add(time.Minute)
			s.ScheduledActionID = e.jobs[0].ActionID
			if phase != "arming" {
				s.State = "stopped"
				s.IntendedStopReason = "scheduled_update"
			}
			if phase == "attached" {
				w.state = "offline"
			}
			power := &boundaryPower{fakePower: w, stateHook: func() { s.OperatorVeto = true; s.IntendedStopReason = "console_stop" }}
			e.o.Power = power
			if err = e.Step(context.Background(), j.ID, n); err != nil {
				t.Fatal(err)
			}
			if power.stops+power.starts+power.scheduled != 0 {
				t.Fatal("dispatched with stale lifecycle evidence")
			}
			if e.Jobs()[0].Phase != "hold" {
				t.Fatal(e.Jobs())
			}
		})
	}
}

// The second State call is the final check, after target selection and durable
// action intent. Reading a fresh status before that HTTP wait is insufficient.
func TestFinalDispatchObservationCanVeto(t *testing.T) {
	for _, phase := range []string{"waiting", "arming", "stop_requested", "attached"} {
		for _, change := range []string{"veto", "run"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				e, w, s, n := jobFixture(t)
				defer e.Close()
				j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "copy", Policy{Mode: "immediate", MaximumDelay: time.Hour}, *s, n)
				if err != nil {
					t.Fatal(err)
				}
				e.jobs[0].Phase = phase
				e.jobs[0].ActionID = "abcdef0123456789abcdef0123456789"
				e.jobs[0].ExpiresAt = n.Add(time.Minute)
				if phase == "waiting" {
					e.jobs[0].AttachMode = ""
				}
				s.ScheduledActionID = e.jobs[0].ActionID
				if phase == "stop_requested" || phase == "attached" {
					s.State = "stopped"
					s.IntendedStopReason = "scheduled_update"
				}
				if phase == "attached" {
					w.state = "offline"
				}
				calls := 0
				power := &boundaryPower{fakePower: w, stateHook: func() {
					calls++
					if calls == 2 {
						if change == "veto" {
							s.OperatorVeto = true
						} else {
							s.RunID = "new-run"
						}
					}
				}}
				e.o.Power = power
				if err = e.Step(context.Background(), j.ID, n); err != nil {
					t.Fatal(err)
				}
				if calls != 2 || w.restarts+power.stops+power.starts+power.scheduled != 0 || e.Jobs()[0].Phase != "hold" {
					t.Fatal("final observation failed", calls, e.Jobs())
				}
			})
		}
	}
}

func TestWarningReceiptSurvivesEligibilityReset(t *testing.T) {
	e, w, s, n := jobFixture(t)
	defer e.Close()
	warnings := 0
	e.o.Power = &boundaryPower{fakePower: w, warningHook: func() { warnings++; s.Players.Count = 1 }}
	j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "when_empty", MaximumDelay: time.Second, WarningOffsets: []time.Duration{0}}, *s, n)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Step(context.Background(), j.ID, n); err != nil {
		t.Fatal(err)
	}
	if w.restarts != 0 || e.Jobs()[0].Phase != "waiting" {
		t.Fatal(e.Jobs())
	}
	// At the UTC maximum deadline, occupied but fresh may proceed. The already
	// delivered warning is not repeated after resetting eligibility.
	s.SampleAt = n.Add(time.Second)
	s.ReceivedAt = s.SampleAt
	if err = e.Step(context.Background(), j.ID, s.SampleAt); err != nil {
		t.Fatal(err)
	}
	if warnings != 1 || w.restarts != 1 {
		t.Fatal(warnings, w.restarts, e.Jobs())
	}
}

func TestStaleWarningDefersWithoutResettingCountdown(t *testing.T) {
	for _, countdown := range []time.Duration{0, time.Minute} {
		t.Run(countdown.String(), func(t *testing.T) {
			e, w, s, n := jobFixture(t)
			defer e.Close()
			warnings := 0
			e.o.Power = &boundaryPower{fakePower: w, warningHook: func() { warnings++; s.SampleAt = n.Add(-time.Hour) }}
			j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", Policy{Mode: "immediate", MaximumDelay: time.Hour, WarningOffsets: []time.Duration{countdown}}, *s, n)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Step(context.Background(), j.ID, n); err != nil {
				t.Fatal(err)
			}
			waiting := e.Jobs()[0]
			if w.restarts != 0 || waiting.ExecuteAt.IsZero() || waiting.Phase != "waiting" || waiting.Warnings[countdown.String()] != "dispatch_accepted" {
				t.Fatal("stale evidence dispatched or lost countdown/receipt", waiting, warnings)
			}
			if countdown > 0 {
				before := waiting.ExecuteAt.Add(-countdown / 2)
				s.SampleAt = before
				s.ReceivedAt = before
				if err = e.Step(context.Background(), j.ID, before); err != nil {
					t.Fatal(err)
				}
				if w.restarts != 0 || warnings != 1 || e.Jobs()[0].Phase != "waiting" || e.Jobs()[0].Warnings[countdown.String()] != "dispatch_accepted" || !e.Jobs()[0].ExecuteAt.Equal(waiting.ExecuteAt) {
					t.Fatal("fresh evidence bypassed or reset countdown", e.Jobs(), warnings)
				}
			}
			// Step includes time spent observing Wings in its persisted ExecuteAt.
			// Reusing n here can move the test clock behind that instant when this
			// retry is faster than the first call. Retry at the actual saved deadline.
			s.SampleAt = waiting.ExecuteAt
			s.ReceivedAt = waiting.ExecuteAt
			if err = e.Step(context.Background(), j.ID, waiting.ExecuteAt); err != nil {
				t.Fatal(err)
			}
			if w.restarts != 1 || warnings != 1 || e.Jobs()[0].Warnings[countdown.String()] != "dispatch_accepted" || !e.Jobs()[0].ExecuteAt.Equal(waiting.ExecuteAt) {
				t.Fatal("fresh retry changed countdown or warning receipt", w.restarts, warnings, e.Jobs())
			}
		})
	}
}

func TestFinalObservationHonorsArmExpiryAndWindowEnd(t *testing.T) {
	for _, mode := range []string{"arm", "window"} {
		t.Run(mode, func(t *testing.T) {
			e, w, s, _ := jobFixture(t)
			defer e.Close()
			n := time.Date(2026, 11, 1, 0, 59, 59, 900000000, time.UTC)
			s.SampleAt = n
			s.ReceivedAt = n
			p := Policy{Mode: "maintenance_window", Timezone: "UTC", WindowStart: "00:00", WindowEnd: "01:00", MaximumDelay: time.Hour}
			if mode == "arm" {
				p.Mode = "immediate"
			}
			j, err := e.Schedule(context.Background(), "srv", Target{Generation: "0123456789abcdef0123456789abcdef", Build: "200"}, "", p, *s, n)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "arm" {
				e.jobs[0].Phase = "arming"
				e.jobs[0].ActionID = "abcdef0123456789abcdef0123456789"
				e.jobs[0].ExpiresAt = n.Add(100 * time.Millisecond)
				s.ScheduledActionID = e.jobs[0].ActionID
			}
			calls := 0
			power := &boundaryPower{fakePower: w, stateHook: func() {
				calls++
				if calls == 2 {
					time.Sleep(150 * time.Millisecond)
				}
			}}
			e.o.Power = power
			if err = e.Step(context.Background(), j.ID, n); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || w.restarts+power.scheduled != 0 {
				t.Fatal("expired arm/window dispatched", calls, e.Jobs())
			}
			want := "waiting"
			if mode == "arm" {
				want = "hold"
			}
			if e.Jobs()[0].Phase != want {
				t.Fatal(e.Jobs())
			}
		})
	}
}
