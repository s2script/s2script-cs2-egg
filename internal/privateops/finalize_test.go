// SPDX-License-Identifier: GPL-3.0-or-later
package privateops

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func stoppedFixture(t *testing.T) (string, string, chan net.Conn) {
	t.Helper()
	base, e := os.MkdirTemp("", "s2final-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	v := filepath.Join(base, "volume")
	os.Mkdir(v, 0700)
	socket := filepath.Join(base, "s")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	connections := make(chan net.Conn, 1)
	t.Cleanup(func() { l.Close() })
	go func() {
		c, e := l.Accept()
		if e != nil {
			return
		}
		connections <- c
		defer c.Close()
		first, e := protocol.Read(c)
		if e == nil {
			Serve(context.Background(), c, v, Policy{Enabled: true, Root: filepath.Join(base, "ops")}, first)
		}
	}()
	return socket, v, connections
}
func TestCancellationBeforeFinishDoesNotCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := Stopped(ctx, "missing", uint32(os.Geteuid()), "0123456789abcdef0123456789abcdef", func() error { called = true; return nil })
	if err == nil || called {
		t.Fatal("canceled request entered finish")
	}
}
func TestCancellationDuringFinishHoldsStoppedAuthority(t *testing.T) {
	socket, v, _ := stoppedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Stopped(ctx, socket, uint32(os.Geteuid()), "0123456789abcdef0123456789abcdef", func() error {
		cancel()
		time.Sleep(30 * time.Millisecond)
		u, e := runtimelease.Acquire(v)
		if u != nil {
			u()
		}
		if !errors.Is(e, runtimelease.ErrHeld) {
			t.Fatal("cancellation released stopped lease during finish")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestHelperLossDuringFinishIsUncertain(t *testing.T) {
	socket, _, connections := stoppedFixture(t)
	err := Stopped(context.Background(), socket, uint32(os.Geteuid()), "0123456789abcdef0123456789abcdef", func() error { (<-connections).Close(); return nil })
	if !errors.Is(err, ErrStoppedUncertain) {
		t.Fatalf("helper loss was not uncertain: %v", err)
	}
}
