// SPDX-License-Identifier: GPL-3.0-or-later
package integration

import (
	"context"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/node"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/shared"
	"github.com/s2script/s2script-cs2-egg/internal/steam"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedReservationMaterializeRestart(t *testing.T) {
	ctx := context.Background()
	storeRoot := t.TempDir()
	privateRoot := t.TempDir()
	build := "100"
	options := shared.Options{Root: storeRoot, ReserveBytes: 1, Update: func(ctx context.Context, root string) (string, error) {
		for name, value := range map[string]string{".steam/sdk64/steamclient.so": "sdk", "steamapps/appmanifest_730.acf": `"appid" "730" "buildid" "` + build + `"`, "game/bin/linuxsteamrt64/cs2": "executable", "game/csgo/gameinfo.gi": "gameinfo", "game/csgo/pak01.vpk": "vpk-" + build} {
			p := filepath.Join(root, name)
			if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
				return "", e
			}
			if e := os.WriteFile(p, []byte(value), 0755); e != nil {
				return "", e
			}
		}
		return build, nil
	}}
	defer filepath.WalkDir(storeRoot, func(p string, d os.DirEntry, e error) error {
		if e == nil && d.IsDir() {
			os.Chmod(p, 0755)
		}
		return nil
	})
	store, e := shared.New(options)
	if e != nil {
		t.Fatal(e)
	}
	a, e := store.Build(ctx)
	if e != nil {
		t.Fatal(e)
	}
	ref, e := store.Reserve(ctx, node.Server{ID: "one"}, "run-a", "request-a")
	if e != nil {
		t.Fatal(e)
	}
	lease, e := runtimelease.Acquire(privateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer lease()
	unlock, e := artifact.Acquire(privateRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if e = artifact.Recover(privateRoot); e != nil {
		t.Fatal(e)
	}
	if e = shared.MaterializeLocked(ctx, privateRoot, storeRoot, a, ref.ID, "symlink", nil, ""); e != nil {
		t.Fatal(e)
	}
	if e = store.Commit(ctx, "one", ref.ID); e != nil {
		t.Fatal(e)
	}
	build = "200"
	if _, e = store.Build(ctx); e != nil {
		t.Fatal(e)
	}
	restarted, e := shared.New(options)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.Prune(ctx); e != nil {
		t.Fatal(e)
	}
	if got, e := steam.InstalledBuild(privateRoot); e != nil || got != "100" {
		t.Fatalf("private installed generation changed: %s %v", got, e)
	}
	b, e := os.ReadFile(filepath.Join(privateRoot, "game/csgo/pak01.vpk"))
	if e != nil || string(b) != "vpk-100" {
		t.Fatal("active generation was mutated/pruned", e)
	}
}

func TestFailedStandaloneMigrationKeepsSteamGuard(t *testing.T) {
	root := t.TempDir()
	marker, err := json.Marshal(shared.Local{Mode: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, shared.LocalState), marker, 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	source := filepath.Join(outside, "operator-binary")
	if err := os.WriteFile(source, []byte("external source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(root, "operator-binary")); err != nil {
		t.Fatal(err)
	}
	lease, err := runtimelease.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := artifact.Acquire(root)
	if err != nil {
		lease()
		t.Fatal(err)
	}
	err = shared.StandaloneLocked(context.Background(), root)
	unlock()
	lease()
	if err == nil {
		t.Fatal("unsafe migration completed")
	}
	err = server.Prepare(context.Background(), config.Server{Root: root, Steam: config.Steam{AppID: 730, Update: true}}, server.Options{})
	if err == nil || !strings.Contains(err.Error(), "shared install cannot use standalone SteamCMD") {
		t.Fatal("failed migration allowed standalone Steam preparation", err)
	}
	if _, err := os.Stat(filepath.Join(root, "steamcmd")); !os.IsNotExist(err) {
		t.Fatal("standalone bootstrap ran despite remaining dependency", err)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "external source" {
		t.Fatal("external dependency modified", err)
	}
}
