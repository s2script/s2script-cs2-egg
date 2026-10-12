// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type typedFixtureGuard struct {
	valid  bool
	seen   []string
	action ConsoleAction
}

func (g *typedFixtureGuard) Observe(_ time.Time, server, run, line string) []ConsoleAction {
	g.seen = append(g.seen, line)
	a := g.action
	a.ServerID = server
	a.RunID = run
	return []ConsoleAction{a}
}
func (g *typedFixtureGuard) Valid(ConsoleAction, time.Time) bool { return g.valid }
func TestConsoleGuardSeesRedactedFilteredLines(t *testing.T) {
	var mu sync.Mutex
	var shown bytes.Buffer
	var seen []string
	w := &consoleWriter{mu: &mu, out: &shown, redact: func(v string) string { return strings.ReplaceAll(v, "secret", "[redacted]") }, exclude: []string{"filtered"}, emit: func(string, string, map[string]string) {}, observe: func(v string) { seen = append(seen, v) }}
	w.Write([]byte("filtered sec"))
	w.Write([]byte("ret\n"))
	if shown.Len() != 0 || len(seen) != 1 || seen[0] != "filtered [redacted]" {
		t.Fatal("guard missed complete redacted line", shown.String(), seen)
	}
}
func TestTypedKickRefusesStaleAndInvalidUserID(t *testing.T) {
	for _, tc := range []struct {
		uid   int
		valid bool
	}{{42, false}, {0, true}, {-1, true}, {1000000000, true}} {
		g := &typedFixtureGuard{valid: tc.valid, action: ConsoleAction{Kind: "kick", UID: tc.uid, At: time.Now()}}
		var out bytes.Buffer
		emitted := make(chan string, 1)
		loop := newConsoleGuard(context.Background(), g, "one", "run", &out, func(_ string, reason string, _ map[string]string) { emitted <- reason })
		loop.observe("fixture")
		select {
		case reason := <-emitted:
			if reason != "stale_action_refused" {
				t.Fatal(reason)
			}
		case <-time.After(time.Second):
			t.Fatal("no refusal")
		}
		loop.close()
		if out.Len() != 0 {
			t.Fatal("unvalidated kick written", out.String())
		}
	}
}
func TestTypedKickWritesOnlyPositiveNumericCommand(t *testing.T) {
	g := &typedFixtureGuard{valid: true, action: ConsoleAction{Kind: "kick", UID: 42, At: time.Now(), Reason: "untrusted; quit"}}
	r, w := io.Pipe()
	loop := newConsoleGuard(context.Background(), g, "one", "run", w, func(string, string, map[string]string) {})
	defer loop.close()
	defer w.Close()
	defer r.Close()
	loop.observe("fixture")
	b := make([]byte, 10)
	n, e := io.ReadFull(r, b)
	if e != nil || string(b[:n]) != "kickid 42\n" {
		t.Fatal(string(b[:n]), e)
	}
}
