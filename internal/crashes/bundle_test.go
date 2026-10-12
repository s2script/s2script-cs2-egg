// SPDX-License-Identifier: GPL-3.0-or-later
package crashes

import (
	"bytes"
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"os"
	"testing"
)

func TestIntentionalStopNotCrash(t *testing.T) {
	if ShouldCapture(events.Event{Type: "server.down", Reason: "console_stop"}) || ShouldCapture(events.Event{Type: "server.crash", Reason: "console_stop"}) {
		t.Fatal("intentional stop captured")
	}
	if !ShouldCapture(events.Event{Type: "server.crash", Reason: "unexpected_exit"}) {
		t.Fatal("missed crash")
	}
}
func TestCrashBundleHasVersionsWithoutSecrets(t *testing.T) {
	s, e := New(t.TempDir(), Options{}, []string{"secret"})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.Capture(context.Background(), nodetypes.Server{ID: "a"}, server.Status{RunID: "run", Versions: map[string]string{"cs2": "123", "token": "secret"}}, []byte("secret in tail"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte("secret")) || !bytes.Contains(b, []byte("123")) {
		t.Fatal(string(b))
	}
}
