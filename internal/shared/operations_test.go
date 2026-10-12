// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/privateops"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOperationsOnlyHelperStopsAndExportsSensitiveDump(t *testing.T) {
	base, e := os.MkdirTemp("", "s2ops-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(base)
	volume := filepath.Join(base, "volume")
	os.MkdirAll(filepath.Join(volume, "dumps"), 0700)
	root := filepath.Join(base, "ops")
	os.MkdirAll(root, 0700)
	data := []byte("unredacted binary sensitive-token\x00\xff")
	os.WriteFile(filepath.Join(volume, "dumps", "fixture.dmp"), data, 0600)
	policy := privateops.Policy{Enabled: true, Root: root, Dumps: true, DumpSource: "dumps"}
	socket := filepath.Join(base, "helper.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeHelper(ctx, l, HelperConfig{Volume: volume, NodeUID: uint32(os.Geteuid()), RuntimeUID: uint32(os.Geteuid()), AccessGID: os.Getgid(), Operations: &policy})
	}()
	defer func() { cancel(); <-done }()
	call := func(r privateops.Request) (privateops.Reply, error) {
		c, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		return privateops.Call(c, socket, uint32(os.Geteuid()), r)
	}
	lease, e := runtimelease.Acquire(volume)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = call(privateops.Request{Operation: "dump-retain", RequestID: "fixture", ID: "fixture.dmp"}); e == nil {
		t.Fatal("live capture allowed")
	}
	lease()
	reply, e := call(privateops.Request{Operation: "dump-retain", RequestID: "fixture", ID: "fixture.dmp"})
	if e != nil || reply.ID == "" || !reply.Sensitive {
		t.Fatal(reply, e)
	}
	read, e := call(privateops.Request{Operation: "read", Namespace: "dumps", ID: reply.ID, Limit: 65536})
	if e != nil || string(read.Data) != string(data) || !read.Sensitive {
		t.Fatal(read, e)
	}
	if _, e = call(privateops.Request{Operation: "read", Namespace: "dumps", ID: "../fixture.dmp", Limit: 65536}); e == nil {
		t.Fatal("unscoped dump read")
	}
	if e = privateops.Stopped(ctx, socket, uint32(os.Geteuid()), "0123456789abcdef0123456789abcdef", func() error {
		u, e := runtimelease.Acquire(volume)
		if u != nil {
			u()
		}
		if !errors.Is(e, runtimelease.ErrHeld) {
			return errors.New("helper did not hold actual stopped lease")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
