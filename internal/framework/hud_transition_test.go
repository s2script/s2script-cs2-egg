// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
)

func TestHUDOffRemovesOnlyAddonAndRetainsHelperIntegrity(t *testing.T) {
	ctx := context.Background()
	s := hudFixture(t, selectionServer(t, fixtureZIP(t, "")))
	root := gameRoot(t)
	os.MkdirAll(filepath.Join(root, "steamapps"), 0700)
	os.WriteFile(filepath.Join(root, "steamapps/appmanifest_730.acf"), []byte(`"AppState" { "appid" "730" "buildid" "24934554" }`), 0600)
	cfg := filepath.Join(root, "game/csgo/cfg/multiaddonmanager/multiaddonmanager.cfg")
	os.MkdirAll(filepath.Dir(cfg), 0700)
	original := []byte("// keep\nmm_extra_addons \"123,456\" // operator\nother \"yes\"\n")
	os.WriteFile(cfg, original, 0600)
	apply := func(p Prepared, e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
		defer p.Cleanup()
		if e = artifact.Apply(ctx, root, p.Changes); e != nil {
			t.Fatal(e)
		}
	}
	apply(Prepare(ctx, root, s))
	enabled, _ := os.ReadFile(cfg)
	if !bytes.Contains(enabled, []byte(HUDAddon)) {
		t.Fatal("HUD not enabled")
	}
	hud := s.HUD
	s.HUD = nil
	p, e := Prepare(ctx, root, s)
	if e != nil {
		t.Fatal(e)
	}
	live, _ := os.ReadFile(cfg)
	if !bytes.Equal(live, enabled) {
		t.Fatal("prepare changed live config")
	}
	apply(p, nil)
	got, _ := os.ReadFile(cfg)
	if !bytes.Equal(got, original) {
		t.Fatalf("HUD off did not preserve other addons/settings: %s", got)
	}
	state, e := loadState(root)
	if e != nil {
		t.Fatal(e)
	}
	tracked := 0
	for _, f := range state.Managed {
		if f.Path == "game/csgo/addons/metamod/multiaddonmanager.vdf" || f.Path == "game/csgo/addons/multiaddonmanager/bin/multiaddonmanager.so" {
			tracked++
		}
	}
	if tracked != 2 || state.Selection.HUD != nil {
		t.Fatal("retained helper lost integrity ownership", tracked)
	}
	apply(Prepare(ctx, root, s))
	got, _ = os.ReadFile(cfg)
	if !bytes.Equal(got, original) {
		t.Fatal("repeated off changed settings")
	}
	s.HUD = hud
	apply(Prepare(ctx, root, s))
	got, _ = os.ReadFile(cfg)
	if !bytes.Equal(got, enabled) {
		t.Fatal("re-enabling HUD did not restore only its addon")
	}
	s.HUD = nil
	native := filepath.Join(root, "game/csgo/addons/multiaddonmanager/bin/multiaddonmanager.so")
	os.WriteFile(native, []byte("changed"), 0600)
	if p, e := Prepare(ctx, root, s); e == nil {
		p.Cleanup()
		t.Fatal("modified managed helper silently adopted")
	}
}
func TestHUDOffLeavesUnmanagedAddonSettingsAlone(t *testing.T) {
	root := gameRoot(t)
	s := selectionServer(t, fixtureZIP(t, ""))
	cfg := filepath.Join(root, "game/csgo/cfg/multiaddonmanager/multiaddonmanager.cfg")
	os.MkdirAll(filepath.Dir(cfg), 0700)
	original := []byte("mm_extra_addons \"" + HUDAddon + ",123\"\n")
	os.WriteFile(cfg, original, 0600)
	p, e := Prepare(context.Background(), root, s)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Cleanup()
	if e = artifact.Apply(context.Background(), root, p.Changes); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(cfg)
	if !bytes.Equal(got, original) {
		t.Fatal("unmanaged settings changed")
	}
}
