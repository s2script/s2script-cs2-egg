// SPDX-License-Identifier: GPL-3.0-or-later
package scheduler

import (
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"testing"
	"time"
)

func fresh(now time.Time) server.Status {
	return server.Status{RunID: "run", State: "ready", Ready: true, SampleAt: now, ReceivedAt: now, Players: server.Players{Known: true}}
}
func TestUnknownPlayersNotEmpty(t *testing.T) {
	n := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	s := fresh(n)
	s.Players.Known = false
	p := Policy{Mode: "when_empty", MaximumDelay: time.Hour, Freshness: time.Minute}
	if d := Decide(n, n, s, p); d.Action != "wait" {
		t.Fatal(d)
	}
	s.Players.Known = true
	s.SampleAt = n.Add(-2 * time.Minute)
	if d := Decide(n, n, s, p); d.Action != "wait" {
		t.Fatal(d)
	}
}
func TestWindowSpanningMidnight(t *testing.T) {
	n := time.Date(2026, 10, 12, 0, 30, 0, 0, time.UTC)
	p := Policy{Mode: "maintenance_window", MaximumDelay: 24 * time.Hour, Timezone: "UTC", WindowStart: "23:00", WindowEnd: "01:00"}
	if d := Decide(n, n, fresh(n), p); d.Action != "restart" {
		t.Fatal(d)
	}
	n = n.Add(2 * time.Hour)
	if d := Decide(n, n, fresh(n), p); d.Action != "wait" {
		t.Fatal(d)
	}
}
func TestMaximumDelayAndWarnings(t *testing.T) {
	n := time.Now().UTC()
	s := fresh(n)
	s.Players.Count = 10
	p := Policy{Mode: "immediate", MaximumDelay: time.Hour, WarningOffsets: []time.Duration{time.Minute}}
	if d := Decide(n, n, s, p); d.Action != "warn" {
		t.Fatal(d)
	}
	p.Mode = "when_empty"
	if d := Decide(n.Add(time.Hour), n, s, p); d.Action != "restart" {
		t.Fatal(d)
	}
}
