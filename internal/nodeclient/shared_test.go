// SPDX-License-Identifier: GPL-3.0-or-later
package nodeclient

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/shared"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnavailableMountNeverRunsSteamCMD(t *testing.T) {
	root := t.TempDir()
	c := &Client{caps: map[string]bool{protocol.CapPrepare: true}}
	handled, e := c.Prepare(context.Background(), config.Server{Root: root, Shared: config.SharedClient{Mount: filepath.Join(root, "missing"), Mode: "symlink"}})
	if !handled || e == nil || !strings.Contains(e.Error(), "mount") {
		t.Fatalf("missing mount did not fail closed: %v %v", handled, e)
	}
	if _, e = os.Stat(filepath.Join(root, "steamcmd")); !os.IsNotExist(e) {
		t.Fatal("missing shared mount triggered standalone preparation")
	}
}

func TestCopyPreparationWithoutSharedMount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	storeRoot := t.TempDir()
	store, e := shared.New(shared.Options{Root: storeRoot, ReserveBytes: 1, Update: func(ctx context.Context, root string) (string, error) {
		for name, value := range map[string]string{".steam/sdk64/steamclient.so": "sdk", "steamapps/appmanifest_730.acf": `"appid" "730" "buildid" "100"`, "game/bin/linuxsteamrt64/cs2": "binary", "game/csgo/gameinfo.gi": "gi", "game/csgo/pak01.vpk": "bytes"} {
			p := filepath.Join(root, name)
			if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
				return "", e
			}
			if e := os.WriteFile(p, []byte(value), 0755); e != nil {
				return "", e
			}
		}
		return "100", nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer filepath.WalkDir(storeRoot, func(p string, d os.DirEntry, e error) error {
		if e == nil && d.IsDir() {
			os.Chmod(p, 0755)
		}
		return nil
	})
	g, e := store.Build(ctx)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e = shared.MaterializeLocked(ctx, root, storeRoot, g, "host-install", "copy", nil, ""); e != nil {
		t.Fatal(e)
	}
	socket := filepath.Join(t.TempDir(), "node.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	module := shared.Module{Store: store}
	go func() {
		for {
			conn, e := l.Accept()
			if e != nil {
				return
			}
			go protocol.ServeConn(ctx, conn, func(ctx context.Context, m protocol.Message) (any, error) {
				if m.Type == "hello" {
					return protocol.Welcome{NodeID: "node", ServerID: "one", Capabilities: []string{protocol.CapPrepare}}, nil
				}
				return module.Handle(ctx, nodetypes.Server{ID: "one"}, protocol.CapPrepare, m)
			})
		}
	}()
	client, e := Connect(ctx, socket)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	handled, e := client.Prepare(ctx, config.Server{Root: root, Shared: config.SharedClient{Mode: "copy", Mount: filepath.Join(root, "no-shared-mount")}})
	if !handled || e != nil {
		t.Fatal("host copies could not prepare without shared mount", e)
	}
	if e = os.WriteFile(filepath.Join(root, "game/csgo/pak01.vpk"), []byte("damaged"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Prepare(ctx, config.Server{Root: root, Shared: config.SharedClient{Mode: "copy", Mount: filepath.Join(root, "no-shared-mount")}}); e == nil {
		t.Fatal("damaged local copy accepted without source")
	}
	// An explicit operator exclusion is a persistent policy even without a mount.
	if _, e = client.Prepare(ctx, config.Server{Root: root, Shared: config.SharedClient{Mode: "copy", Mount: filepath.Join(root, "no-shared-mount"), Overrides: []string{"game/csgo/pak01.vpk"}}}); e != nil {
		t.Fatal("explicit copy override rejected", e)
	}
	local, e := shared.ReadLocal(root)
	if e != nil || len(local.Overrides) != 1 || local.Overrides[0] != "game/csgo/pak01.vpk" {
		t.Fatal("startup override policy not persisted", local.Overrides, e)
	}
	if e = shared.MaterializeLocked(ctx, root, storeRoot, g, "next-helper", "copy", nil, ""); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(root, "game/csgo/pak01.vpk"))
	if e != nil || string(data) != "damaged" {
		t.Fatal("helper lost offline startup override", string(data), e)
	}
}
