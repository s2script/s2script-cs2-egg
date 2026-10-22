// SPDX-License-Identifier: GPL-3.0-or-later

package conntrace_test

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/conntrace"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

const target = "203.0.113.7"

func newTrace(t *testing.T) (*conntrace.Tracer, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)
	tr, err := conntrace.New(conntrace.Options{Enabled: true, TargetIP: target, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	tr.Drain()
	return tr, &now
}

func connect(ip, name string, uid, port int) string {
	return fmt.Sprintf("CServerSideClientBase::Connect( name='%s', userid=%d, fake=0, connectiontypeflags=4, chan->addr=%s:%d )", name, uid, ip, port)
}

func TestFailedFirstAttemptAndSuccessfulRetry(t *testing.T) {
	tr, now := newTrace(t)
	fixture, err := os.ReadFile("testdata/retry.log")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(fixture)), "\n") {
		tr.Feed(line)
		*now = now.Add(time.Second)
	}
	events := tr.Drain()
	var dropped, joined *conntrace.Event
	for i := range events {
		e := &events[i]
		if e.Stage == "dropped" {
			dropped = e
		}
		if e.Stage == "joined" {
			joined = e
		}
		for _, secret := range []string{target, "Alice", "76561198000000001", ":53071", ":53090"} {
			if strings.Contains(e.Line(), secret) {
				t.Fatalf("trace exposed %q: %s", secret, e.Line())
			}
		}
	}
	if dropped == nil || joined == nil || dropped.Attempt == joined.Attempt || dropped.Attempt == 0 || joined.Attempt == 0 {
		t.Fatalf("expected distinct failed/successful attempts: %+v", events)
	}
	if dropped.Detail != "timeout" || dropped.Signon != "CONNECTED" || joined.Signon != "FULL" || joined.ElapsedMS <= 0 {
		t.Fatalf("missing progression: dropped=%+v joined=%+v", dropped, joined)
	}
	if dropped.Session != joined.Session {
		t.Fatal("retry lost session correlation")
	}
}

func TestDisabledHasNoStateOutputOrNetworkRequests(t *testing.T) {
	tr, err := conntrace.New(conntrace.Options{TargetIP: "invalid"})
	if tr != nil || err != nil {
		t.Fatalf("disabled: %v %v", tr, err)
	}
	tr.Feed(connect(target, "Alice", 7, 1234))
	tr.Tick()
	tr.Network(&proto.ConnectionTraceSample{})
	tr.Block(&proto.GuardBlock{IP: target})
	tr.Close()
	if len(tr.Drain()) != 0 || tr.Query() != nil {
		t.Fatal("disabled trace did work")
	}
	if n := testing.AllocsPerRun(100, func() { tr.Feed("unrelated line") }); n != 0 {
		t.Fatalf("disabled allocations: %v", n)
	}
}

func TestOptInValidationAndExpiry(t *testing.T) {
	for _, ip := range []string{"", "example.com", "203.0.113.7/24", "203.0.113.7:27005", "fe80::1%eth0"} {
		if _, err := conntrace.New(conntrace.Options{Enabled: true, TargetIP: ip}); err == nil {
			t.Fatalf("accepted target %q", ip)
		}
	}
	tr, now := newTrace(t)
	if tr.Query() == nil || tr.Query().IP != target {
		t.Fatal("missing target query")
	}
	*now = now.Add(10 * time.Minute)
	tr.Tick()
	tr.Feed(connect(target, "Alice", 7, 1234))
	if tr.Query() != nil {
		t.Fatal("query after expiry")
	}
	for _, e := range tr.Drain() {
		if e.Stage != "trace_stopped" {
			t.Fatalf("unexpected expiry output: %+v", e)
		}
	}
}

func TestNoiseForgeryAndOtherTargetsIgnored(t *testing.T) {
	tr, _ := newTrace(t)
	for _, line := range []string{
		"[All Chat][Alice (1)]: Receiving C2S_CONNECT [protocol 14178 0 auth 3] from " + target + ":1",
		"prefix Client 7 'Alice' signon state SIGNONSTATE_CONNECTED -> SIGNONSTATE_FULL",
		connect("198.51.100.8", "Receiving C2S_CONNECT from "+target+":1", 8, 1234),
		"Receiving C2S_CONNECT [protocol 14178 0 auth 3] from 999.1.1.1:1",
		strings.Repeat("x", 9000),
	} {
		tr.Feed(line)
	}
	if events := tr.Drain(); len(events) != 0 {
		t.Fatalf("forged/unrelated evidence: %+v", events)
	}
}

func TestSameIPDifferentPortsAndDuplicateNamesStaySeparate(t *testing.T) {
	tr, _ := newTrace(t)
	tr.Feed(connect(target, "Same", 7, 1234))
	tr.Feed(connect(target, "Same", 8, 1235))
	tr.Feed("Client 8 'Same' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
	tr.Feed("SV:  Dropped client 'Same' from server(29): NETWORK_DISCONNECT_TIMEDOUT")
	events := tr.Drain()
	var joined, ambiguous bool
	for _, e := range events {
		if e.Stage == "joined" {
			joined = e.Attempt == 2
		}
		if e.Stage == "ambiguous_disconnect" {
			ambiguous = e.Attempt == 0
		}
		if e.Stage == "dropped" {
			t.Fatal("name-only drop attributed to one of two clients")
		}
	}
	if !joined || !ambiguous {
		t.Fatalf("events: %+v", events)
	}
}

func TestRepeatConnectIsEvidenceNotInventedRetry(t *testing.T) {
	tr, _ := newTrace(t)
	line := "Receiving C2S_CONNECT [protocol 14178 0 auth 3] from " + target + ":1234"
	tr.Feed(line)
	tr.Feed(line)
	for _, e := range tr.Drain() {
		if e.Attempt != 1 {
			t.Fatalf("invented attempt: %+v", e)
		}
	}
}

func TestOtherSourceWithSameNameDoesNotTerminateTarget(t *testing.T) {
	for _, targetFirst := range []bool{true, false} {
		tr, _ := newTrace(t)
		if targetFirst {
			tr.Feed(connect(target, "Same", 7, 1234))
		}
		tr.Feed(connect("198.51.100.8", "Same", 8, 1234))
		if !targetFirst {
			tr.Feed(connect(target, "Same", 7, 1234))
		}
		tr.Feed("SV:  Dropped client 'Same' from server(29): NETWORK_DISCONNECT_TIMEDOUT")
		tr.Feed("Client 7 'Same' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
		var joined bool
		for _, e := range tr.Drain() {
			if e.Stage == "dropped" {
				t.Fatal("another source's drop attributed to target")
			}
			if e.Stage == "joined" {
				joined = true
			}
		}
		if !joined {
			t.Fatal("ambiguous notice destroyed live target state")
		}
	}
}

func TestNewTransportOnSameEndpointStartsAnotherAttempt(t *testing.T) {
	tr, _ := newTrace(t)
	for _, id := range []string{"123", "123", "456"} {
		tr.Feed("Accepting Steam Net connection #" + id + " UDP steamid:76561198000000001@" + target + ":1234")
	}
	var ids []uint64
	for _, e := range tr.Drain() {
		if e.Stage == "transport_accepted" {
			ids = append(ids, e.Attempt)
		}
	}
	if fmt.Sprint(ids) != "[1 1 2]" {
		t.Fatalf("transport boundaries: %v", ids)
	}
}

func TestDelayedDropFromReplacedTransportCannotEndRetry(t *testing.T) {
	tr, _ := newTrace(t)
	tr.Feed("Accepting Steam Net connection #123 UDP steamid:76561198000000001@" + target + ":1234")
	tr.Feed(connect(target, "Same", 7, 1234))
	tr.Feed("Accepting Steam Net connection #456 UDP steamid:76561198000000001@" + target + ":1234")
	tr.Feed(connect(target, "Same", 8, 1234))
	tr.Feed("SV:  Dropped client 'Same' from server(29): NETWORK_DISCONNECT_TIMEDOUT")
	tr.Feed("Client 8 'Same' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
	var joined, ambiguous bool
	for _, e := range tr.Drain() {
		if e.Stage == "dropped" {
			t.Fatal("delayed old disconnect ended retry")
		}
		if e.Stage == "joined" && e.Attempt == 2 {
			joined = true
		}
		if e.Stage == "ambiguous_disconnect" {
			ambiguous = true
		}
	}
	if !joined || !ambiguous {
		t.Fatal("missing successful retry / ambiguity evidence")
	}
}

func TestForcedReconnectCannotMisattributeItsDelayedDrop(t *testing.T) {
	tr, _ := newTrace(t)
	tr.Feed(connect(target, "Same", 7, 1234))
	tr.Feed("SV:  Forcing client reconnect (SIGNONSTATE_CONNECTED) for client 'Same'")
	tr.Feed(connect(target, "Same", 8, 1234))
	tr.Feed("SV:  Dropped client 'Same' from server(29): NETWORK_DISCONNECT_TIMEDOUT")
	tr.Feed("Client 8 'Same' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
	var joined bool
	for _, e := range tr.Drain() {
		if e.Stage == "dropped" {
			t.Fatal("old forced reconnect drop attributed to retry")
		}
		if e.Stage == "joined" && e.Attempt == 2 {
			joined = true
		}
	}
	if !joined {
		t.Fatal("retry disappeared")
	}
}

func TestIPv6ChannelPrefixAndUIDReuse(t *testing.T) {
	tr, err := conntrace.New(conntrace.Options{Enabled: true, TargetIP: "2001:db8::7"})
	if err != nil {
		t.Fatal(err)
	}
	tr.Drain()
	tr.Feed("[Networking] " + connect("[2001:db8::7]", "A", 7, 1234))
	tr.Feed(connect("[2001:db8::7]", "B", 7, 1235))
	tr.Feed("Client 7 'B' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
	for _, e := range tr.Drain() {
		if e.Stage == "joined" && e.Attempt != 2 {
			t.Fatalf("stale UID mapping: %+v", e)
		}
	}
}

func TestPendingAttemptsEndAsIncompleteNotFailed(t *testing.T) {
	tr, now := newTrace(t)
	tr.Feed("Receiving C2S_CONNECT [protocol 14178 0 auth 3] from " + target + ":1234")
	tr.Drain()
	*now = now.Add(2 * time.Minute)
	tr.Tick()
	events := tr.Drain()
	if len(events) != 1 || events[0].Stage != "incomplete" || events[0].Detail != "observation_gap" {
		t.Fatalf("events: %+v", events)
	}
}

func TestNetworkAndBlockEvidenceIsNotAssignedToAnAttempt(t *testing.T) {
	tr, now := newTrace(t)
	tr.Network(&proto.ConnectionTraceSample{IP: target, Status: "sampled", SampledAt: now.Format(time.RFC3339Nano), Present: true, Packets: 12, Bytes: 600})
	tr.Block(&proto.GuardBlock{IP: target, Rule: "evil secret 76561198000000001", Dry: true})
	tr.Network(&proto.ConnectionTraceSample{IP: "198.51.100.8", Status: "sampled"})
	events := tr.Drain()
	if len(events) != 2 || events[0].Stage != "network_sample" || events[0].Packets != 12 || events[1].Stage != "host_would_block" {
		t.Fatalf("events: %+v", events)
	}
	for _, e := range events {
		if e.Attempt != 0 || strings.Contains(e.Line(), "secret") {
			t.Fatalf("unsafe attribution: %+v", e)
		}
	}
}

func TestStateAndQueueRemainBoundedAndReportLoss(t *testing.T) {
	tr, _ := newTrace(t)
	for i := 0; i < 10000; i++ {
		tr.Feed(connect(target, "A", i+1, 1000+i))
	}
	events := tr.Drain()
	if len(events) > 129 {
		t.Fatalf("unbounded output: %d", len(events))
	}
	var loss bool
	for _, e := range events {
		if e.Stage == "events_lost" {
			loss = true
		}
	}
	if !loss {
		t.Fatal("silently lost evidence")
	}
	tr.Close()
	if len(tr.Drain()) > 33 {
		t.Fatal("unbounded active attempts")
	}
}

func TestConcurrentFeedsAndDrains(t *testing.T) {
	tr, err := conntrace.New(conntrace.Options{Enabled: true, TargetIP: target})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tr.Feed(connect(target, "A", 7, 1234))
				tr.Tick()
				tr.Drain()
				tr.Query()
			}
		}()
	}
	wg.Wait()
	tr.Close()
}

func TestEnvironmentDefaultsAndInvalidDuration(t *testing.T) {
	env := map[string]string{"CONNECTION_TRACE_IP": target}
	get := func(k string) string { return env[k] }
	tr, err := conntrace.FromEnv(get)
	if err != nil || tr != nil {
		t.Fatalf("not opt-in: %v %v", tr, err)
	}
	env["ENABLE_CONNECTION_TRACE"] = "1"
	tr, err = conntrace.FromEnv(get)
	if err != nil || tr == nil {
		t.Fatalf("default window: %v %v", tr, err)
	}
	for _, duration := range []string{"0", "59", "1801", "-60", "huge", "999999999999999999999999999"} {
		env["CONNECTION_TRACE_SECONDS"] = duration
		if got, err := conntrace.FromEnv(get); got != nil || err == nil {
			t.Fatalf("accepted duration %q", duration)
		}
	}
}

func TestRejectionAndForcedReconnectReasonsAreRedacted(t *testing.T) {
	tr, _ := newTrace(t)
	tr.Feed("Rejecting connection from " + target + ":1234: Bad password TOP_SECRET")
	tr.Feed(connect(target, "Alice", 7, 1234))
	tr.Feed("SV:  Forcing client reconnect (SIGNONSTATE_CONNECTED) for client 'Alice'")
	var rejected, forced bool
	for _, e := range tr.Drain() {
		if e.Stage == "rejected" {
			rejected = e.Detail == "password" && e.Attempt == 1
		}
		if e.Stage == "forced_reconnect" {
			forced = e.Attempt == 2
		}
		if strings.Contains(e.Line(), "TOP_SECRET") {
			t.Fatal("reason leaked")
		}
	}
	if !rejected || !forced {
		t.Fatal("missing rejection/reconnect evidence")
	}
}

func TestReplacementAndEvictionDoNotInventFailure(t *testing.T) {
	tr, _ := newTrace(t)
	tr.Feed(connect(target, "A", 7, 1234))
	tr.Feed(connect(target, "B", 8, 1234))
	var replaced bool
	for _, e := range tr.Drain() {
		if e.Attempt == 1 && e.Stage == "incomplete" {
			replaced = true
		}
	}
	if !replaced {
		t.Fatal("unfinished attempt silently replaced")
	}
	tr.Feed("Client 8 'B' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
	for i := 0; i < 33; i++ {
		tr.Feed(connect(target, "C", 100+i, 2000+i))
	}
	for _, e := range tr.Drain() {
		if e.Attempt == 2 && e.Stage == "incomplete" {
			t.Fatal("joined attempt labeled incomplete")
		}
	}
}

func TestIdentityCapacityDisablesNameOnlyAttribution(t *testing.T) {
	tr, _ := newTrace(t)
	tr.Feed(connect(target, "Target", 1, 1234))
	for i := 0; i < 300; i++ {
		tr.Feed(connect("198.51.100.8", fmt.Sprint(i), 100+i, 2000+i))
	}
	tr.Feed("SV:  Dropped client 'Target' from server(29): NETWORK_DISCONNECT_TIMEDOUT")
	tr.Feed("Client 1 'Target' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
	var ambiguous, joined bool
	for _, e := range tr.Drain() {
		if e.Stage == "ambiguous_disconnect" {
			ambiguous = true
		}
		if e.Stage == "joined" {
			joined = true
		}
		if e.Stage == "dropped" {
			t.Fatal("incomplete identity context used for drop attribution")
		}
	}
	if !ambiguous || !joined {
		t.Fatal("bounded identity behavior lost")
	}
}

func BenchmarkDisabled(b *testing.B) {
	var tr *conntrace.Tracer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.Feed("Client 7 'A' signon state SIGNONSTATE_SPAWN -> SIGNONSTATE_FULL")
	}
}

func BenchmarkUnrelatedLine(b *testing.B) {
	tr, _ := conntrace.New(conntrace.Options{Enabled: true, TargetIP: target})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tr.Feed("WorldRenderer: unrelated game message")
	}
}
