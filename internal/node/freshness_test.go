// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"testing"
	"time"
)

func TestConsoleDoesNotRefreshPlayerSample(t *testing.T) {
	n := time.Now().UTC()
	c := &Core{statuses: map[string]server.Status{"srv": {RunID: "run", State: "ready", SampleAt: n.Add(-time.Minute), ReceivedAt: n.Add(-time.Minute), Players: server.Players{Known: true}}}, contacts: map[string]map[*runtimeContact]struct{}{}}
	c.observe("srv", events.Event{RunID: "run", Type: "server.console"}, &runtimeContact{})
	if got := c.Status("srv"); !got.ReceivedAt.Equal(n.Add(-time.Minute)) {
		t.Fatal(got)
	}
	reported := server.Status{RunID: "run", State: "ready", SampleAt: n.Add(-2 * time.Minute), Players: server.Players{Known: true}}
	raw, _ := json.Marshal(reported)
	c.observe("srv", events.Event{RunID: "run", Type: "server.status", Details: map[string]string{"runtime_status": string(raw)}}, &runtimeContact{})
	if got := c.Status("srv"); !got.SampleAt.Equal(n.Add(-time.Minute)) {
		t.Fatal("old queued report replaced sample", got)
	}
}
func TestTerminationRetainsActionAndOperatorIntent(t *testing.T) {
	c := &Core{statuses: map[string]server.Status{}, contacts: map[string]map[*runtimeContact]struct{}{}}
	c.observe("srv", events.Event{RunID: "run", Type: "server.down", Reason: "scheduled_update", Details: map[string]string{"action_id": "0123456789abcdef0123456789abcdef", "operator_veto": "true"}}, &runtimeContact{})
	s := c.Status("srv")
	if s.IntendedStopReason != "scheduled_update" || !s.OperatorVeto || s.ScheduledActionID == "" {
		t.Fatal(s)
	}
}

func TestExpiredPlayerSampleIsExposedAsUnknown(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	c := &Core{statuses: map[string]server.Status{"srv": {RunID: "run", State: "ready", Ready: true, SampleAt: old, ReceivedAt: old, Players: server.Players{Known: true}}}}
	if s := c.Status("srv"); s.Ready || s.Players.Known {
		t.Fatal("stale player/readiness evidence exposed as current", s)
	}
}

func TestBackloggedLegacyZeroCannotReplaceFreshOccupiedSample(t *testing.T) {
	now := time.Now().UTC()
	c := &Core{statuses: map[string]server.Status{"srv": {RunID: "run", State: "ready", Ready: true, SampleAt: now, ReceivedAt: now, Players: server.Players{Known: true, Count: 5}}}, contacts: map[string]map[*runtimeContact]struct{}{}}
	c.observe("srv", events.Event{RunID: "run", Type: "server.status", At: now.Add(-time.Minute), Reason: "query_ok", Details: map[string]string{"players": "0"}}, &runtimeContact{})
	if got := c.Status("srv"); !got.Players.Known || got.Players.Count != 5 {
		t.Fatal("backlogged zero gained newer sample freshness", got)
	}
}
