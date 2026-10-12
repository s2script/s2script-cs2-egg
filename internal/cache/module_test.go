// SPDX-License-Identifier: GPL-3.0-or-later
package cache

import (
	"context"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"testing"
)

func TestFleetPinConflictBeforeNetwork(t *testing.T) {
	s, e := New(Options{Root: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	m := &Module{Store: s, Policy: Policy{Pins: map[string]string{"s2script": "0.8.1"}}}
	b, _ := json.Marshal(protocol.ArtifactSelection{Source: "s2script", Version: "0.8.2"})
	if _, e = m.fetch(context.Background(), "server", b); e == nil {
		t.Fatal("conflicting fleet pin accepted")
	}
}
func TestCacheHandlesScopedToServer(t *testing.T) {
	s, _ := New(Options{Root: t.TempDir()})
	m := &Module{Store: s, Policy: Policy{Pins: map[string]string{"s2script": "0.8.1"}}}
	if e := m.Start(context.Background(), nil); e != nil {
		t.Fatal(e)
	}
	defer m.Close(context.Background())
	r, e := m.Request(context.Background(), "one", protocol.CacheRequest{Operation: "submit", RequestID: "id", Artifact: protocol.ArtifactSelection{Source: "s2script", Version: "different"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Request(context.Background(), "two", protocol.CacheRequest{Operation: "status", Handle: r.Handle}); e == nil {
		t.Fatal("cross-server handle exposed")
	}
}
