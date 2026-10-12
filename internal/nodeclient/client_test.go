// SPDX-License-Identifier: GPL-3.0-or-later
package nodeclient

import (
	"context"
	"net"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
)

type counter struct{ n int }

func (c *counter) Publish(context.Context, events.Event) error { c.n++; return nil }
func TestAbsentNodeStandalone(t *testing.T) {
	fallback := &counter{}
	s, close := Optional(context.Background(), t.TempDir()+"/absent.sock", fallback)
	defer close()
	if e := s.Publish(context.Background(), events.Event{RunID: "r"}); e != nil || fallback.n != 1 {
		t.Fatalf("standalone unavailable: %v %d", e, fallback.n)
	}
}
func TestReconnectBeforeFirstClaim(t *testing.T) {
	l, e := net.Listen("unix", t.TempDir()+"/node.sock")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			conn, e := l.Accept()
			if e != nil {
				return
			}
			protocol.ServeConn(context.Background(), conn, func(ctx context.Context, m protocol.Message) (any, error) {
				if m.Type == "hello" {
					return protocol.Welcome{NodeID: "n", ServerID: "s", Capabilities: []string{protocol.CapEvents}}, nil
				}
				return protocol.Ownership{Owner: "node"}, nil
			})
		}
	}() // First hello must complete before deliberately expiring the idle connection.
	c, e := Connect(context.Background(), l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.mu.Lock()
	c.wire.Close()
	c.mu.Unlock()
	if owner, e := c.ClaimRun(context.Background(), "run"); e != nil || owner != "node" {
		t.Fatalf("first lifecycle event lost to idle connection: %s %v", owner, e)
	}
	c.Close()
	<-done
}
func TestAmbiguousClaimNeverActivatesFallback(t *testing.T) {
	l, e := net.Listen("unix", t.TempDir()+"/node.sock")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			conn, e := l.Accept()
			if e != nil {
				return
			}
			protocol.ServeConn(context.Background(), conn, func(ctx context.Context, m protocol.Message) (any, error) {
				if m.Type == "hello" {
					return protocol.Welcome{NodeID: "n", ServerID: "s", Capabilities: []string{protocol.CapEvents}}, nil
				}
				conn.Close()
				return protocol.Ownership{Owner: "node"}, nil
			})
		}
	}()
	c, e := Connect(context.Background(), l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	fallback := &counter{}
	route := c.Route(fallback)
	if e = route.Publish(context.Background(), events.Event{RunID: "run"}); e == nil {
		t.Fatal("lost claim response accepted")
	}
	if fallback.n != 0 {
		t.Fatal("ambiguous claim activated local sender")
	}
	<-done
}
