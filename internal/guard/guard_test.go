// SPDX-License-Identifier: GPL-3.0-or-later
package guard

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func fixturePolicy() Policy {
	p := Policy{ServerID: "one", Allocation: Allocation{Address: netip.MustParseAddr("203.0.113.10"), Port: 27015, Protocol: "udp"}}
	p.Enabled = true
	return p
}
func TestGuardDefaultsToObservation(t *testing.T) {
	p := fixturePolicy()
	f := Finding{ServerID: "one", Rule: "reconnect_stuck", Address: netip.MustParseAddr("8.8.8.8"), UID: 42, At: time.Now(), Correlated: true, Provenance: "container-console-correlated"}
	if a := Decide(f, p); a.Kind != "log" {
		t.Fatal(a)
	}
}
func TestBridgePrivateAndAllowlistedAddressesNeverBlocked(t *testing.T) {
	p := fixturePolicy()
	p.Action = "block"
	p.Delegated = true
	p.Allowlist = []string{"8.8.8.0/24", "11.1.0.0/16"}
	for _, ip := range []string{"127.0.0.1", "10.0.0.2", "172.18.0.3", "192.168.0.1", "169.254.1.1", "0.0.0.0", "224.0.0.1", "::ffff:10.0.0.1", "8.8.8.8", "11.1.1.1"} {
		f := Finding{ServerID: "one", Rule: "reconnect_stuck", Address: netip.MustParseAddr(ip), UID: 42, At: time.Now(), Correlated: true, Provenance: "container-console-correlated"}
		if a := Decide(f, p); a.Kind == "block" {
			t.Fatalf("blocked protected %s", ip)
		}
	}
}
func TestReportsCannotSelectAnotherAllocation(t *testing.T) {
	p := fixturePolicy()
	p.Action = "block"
	p.Delegated = true
	f := Finding{ServerID: "other", Rule: "reconnect_stuck", Address: netip.MustParseAddr("8.8.8.8"), UID: 42, At: time.Now(), Correlated: true, Provenance: "container-console-correlated"}
	if a := Decide(f, p); a.Kind != "log" {
		t.Fatal("cross-server action", a)
	}
}
func TestRepeatedOffenceHasBoundedExpiry(t *testing.T) {
	p := fixturePolicy()
	p.Action = "block"
	p.Delegated = true
	p.BlockSeconds = 60
	p.MaxBlockSeconds = 600
	at := time.Now()
	tracker := NewIncidents()
	var first Action
	for i := 0; i < 300; i++ {
		f := Finding{ServerID: "one", Rule: "reconnect_stuck", Address: netip.MustParseAddr("8.8.8.8"), UID: 42, At: at.Add(time.Duration(i) * time.Second), Correlated: true, Provenance: "container-console-correlated"}
		a := tracker.Decide(f, p)
		if i == 0 {
			first = a
		}
		if a.Until.After(at.Add(600 * time.Second)) {
			t.Fatal("sliding expiry extended active incident", a, first)
		}
	}
}
func TestRulesTouchOnlyConfiguredGamePorts(t *testing.T) {
	a := fixturePolicy().Allocation
	r := Ruleset{Allocations: []Allocation{a}, Blocks: []Block{{Allocation: a, Address: netip.MustParseAddr("8.8.8.8"), Until: time.Now().Add(time.Minute)}}}
	text, e := Render(r, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(text, "ip daddr 203.0.113.10 udp dport 27015") || strings.Contains(text, "flush ruleset") || strings.Contains(text, "input policy drop") {
		t.Fatal(text)
	}
	for _, port := range []int{22, 80, 443, 2022, 8080} {
		a.Port = port
		if _, e = Render(Ruleset{Allocations: []Allocation{a}}, time.Now()); e == nil {
			t.Fatalf("accepted infrastructure port%d", port)
		}
	}
}
func TestChatNamesQueriesAndFullSignonCannotAuthorizeAction(t *testing.T) {
	for _, line := range []string{"[All Chat][attacker (12)]: Client 42 'victim' signon state SIGNONSTATE_NEW -> SIGNONSTATE_FULL", "[#99 UDP steamid:123@8.8.8.8:123 'Ignored bad 9.9.9.9:1 Invalid a byte'] 2 reply timeouts", "Receiving C2S_CONNECT junk from 8.8.8.8:1234"} {
		for _, f := range Observe(time.Now(), "one", line) {
			p := fixturePolicy()
			p.Action = "block"
			p.Delegated = true
			if Decide(f, p).Kind != "log" {
				t.Fatal("spoofable evidence acted on")
			}
		}
	}
	d := NewDetector()
	at := time.Now()
	p := fixturePolicy()
	p.Action = "kick"
	for i, line := range []string{"CServerSideClientBase::Connect( name='player', userid=42, chan->addr=8.8.8.8:1234)", "Client 42 'player' signon state SIGNONSTATE_CONNECTED -> SIGNONSTATE_FULL"} {
		d.Observe(at.Add(time.Duration(i)*time.Second), "one", "run", "session", line, p)
	}
	for i := 0; i < 20; i++ {
		findings := d.Observe(at.Add(40*time.Second), "one", "run", "session", "Forcing client reconnect now for client 'player'", p)
		for _, f := range findings {
			if Decide(f, p).Kind != "log" {
				t.Fatal("normal FULL client targeted")
			}
		}
	}
}
func TestFreshNumericCorrelationAndRunFence(t *testing.T) {
	d := NewDetector()
	p := fixturePolicy()
	p.Action = "kick"
	at := time.Now()
	d.Observe(at, "one", "run", "session", "CServerSideClientBase::Connect( name='player', userid=42, chan->addr=8.8.8.8:1234)", p)
	d.Observe(at.Add(time.Second), "one", "run", "session", "Client 42 'player' signon state SIGNONSTATE_CONNECTED -> SIGNONSTATE_NEW", p)
	var found []Finding
	for i := 0; i < 5; i++ {
		found = d.Observe(at.Add(40*time.Second), "one", "run", "session", "Forcing client reconnect now for client 'player'", p)
	}
	if len(found) != 1 || !found[0].Correlated || found[0].UID != 42 {
		t.Fatal("missing numeric attribution", found)
	}
	for i := 0; i < 5; i++ {
		found = d.Observe(at.Add(41*time.Second), "one", "new-run", "session", "Forcing client reconnect now for client 'player'", p)
	}
	for _, f := range found {
		if f.Correlated {
			t.Fatal("old run binding reused")
		}
	}
}
func TestInvalidProtectedEndpointCannotBeSilentlyIgnored(t *testing.T) {
	a := fixturePolicy().Allocation
	for _, p := range []Allocation{{Port: 27015, Protocol: "udpp"}, {Port: -1, Protocol: "udp"}} {
		if _, err := Render(Ruleset{Allocations: []Allocation{a}, Protected: []Allocation{p}}, time.Now()); err == nil {
			t.Fatal("invalid protection silently ignored", p)
		}
	}
}
func TestLateFullSignonEndsPreJoinEligibility(t *testing.T) {
	p := fixturePolicy()
	p.Action = "kick"
	d := NewDetector()
	at := time.Now()
	d.Observe(at, "one", "run", "session", "CServerSideClientBase::Connect( name='player', userid=42, chan->addr=8.8.8.8:1234)", p)
	d.Observe(at.Add(time.Second), "one", "run", "session", "Client 42 'player' signon state SIGNONSTATE_CONNECTED -> SIGNONSTATE_NEW", p)
	d.Observe(at.Add(40*time.Second), "one", "run", "session", "Client 42 'player' signon state SIGNONSTATE_NEW -> SIGNONSTATE_FULL", p)
	for i := 0; i < 5; i++ {
		for _, f := range d.Observe(at.Add(41*time.Second), "one", "run", "session", "Forcing client reconnect now for client 'player'", p) {
			if Decide(f, p).Kind != "log" {
				t.Fatal("late FULL client targeted")
			}
		}
	}
}
