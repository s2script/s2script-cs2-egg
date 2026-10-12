// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/backup"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"net"
	"path/filepath"
	"testing"
)

func TestBackupRestoreCapabilityIsPrivateToAdminSocket(t *testing.T) {
	cfg := fixture(t)
	core, e := New(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	m := &backup.Module{Root: filepath.Join(cfg.StateDir, "backup")}
	if e = core.AddModule(m, []string{protocol.CapBackupAdmin, nodetypes.GrantOperationalEvents}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- core.Run(ctx) }()
	select {
	case <-core.Ready():
	case e := <-done:
		t.Fatal(e)
	}
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	for _, path := range []string{filepath.Join(cfg.Servers[0].Volume, "egg/s2node.sock"), cfg.AdminSocket} {
		conn, e := net.Dial("unix", path)
		if e != nil {
			t.Fatal(e)
		}
		client := protocol.NewClient(conn)
		var welcome protocol.Welcome
		if e = client.Call(context.Background(), "hello", protocol.Hello{Capabilities: []string{protocol.CapBackupAdmin}}, &welcome); e != nil {
			t.Fatal(e)
		}
		if path != cfg.AdminSocket {
			if len(welcome.Capabilities) != 0 {
				t.Fatal("backup admin leaked to container")
			}
			var out json.RawMessage
			if e = client.Call(context.Background(), protocol.CapBackupAdmin, backup.Request{Operation: "restore", ServerID: cfg.Servers[0].ID}, &out); e == nil {
				t.Fatal("container invoked restore")
			}
		} else if len(welcome.Capabilities) != 1 {
			t.Fatal("admin missing enabled capability", welcome)
		}
		client.Close()
	}
}
