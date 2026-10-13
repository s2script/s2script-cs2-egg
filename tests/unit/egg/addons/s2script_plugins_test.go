// SPDX-License-Identifier: GPL-3.0-or-later
package addons_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
)

const pluginDir = "game/csgo/addons/s2script/plugins"

func pluginZIP(t *testing.T, id, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, err := z.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(w, `{"id":%q,"version":%q,"apiVersion":"3.x"}`, id, version)
	w, err = z.Create("plugin.js")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(w, "export default {};")
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func putPlugin(t *testing.T, root, file, id string, enabled bool) []byte {
	t.Helper()
	dir := filepath.Join(root, pluginDir)
	if !enabled {
		dir = filepath.Join(dir, "disabled")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	b := pluginZIP(t, id, "0.8.1")
	if err := os.WriteFile(filepath.Join(dir, file), b, 0644); err != nil {
		t.Fatal(err)
	}
	return b
}

func pluginEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestS2ScriptControlsMoveManifestIdentitiesAndWriteInventory(t *testing.T) {
	root := t.TempDir()
	b := putPlugin(t, root, "operator-renamed.s2sp", "@s2script/basechat", true)
	putPlugin(t, root, "vote.s2sp", "@s2script/nextmap", false)
	err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": "0", "S2SCRIPT_PLUGIN_NEXTMAP": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, pluginDir, "disabled/operator-renamed.s2sp")); err != nil || !bytes.Equal(got, b) {
		t.Fatalf("moved original bytes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, pluginDir, "operator-renamed.s2sp")); !os.IsNotExist(err) {
		t.Fatalf("source retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, pluginDir, "vote.s2sp")); err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		SchemaVersion int `json:"schema_version"`
		Plugins       []struct {
			Name, Version, Path, SHA256 string
			Enabled                     bool
		} `json:"plugins"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "egg/installed-list.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.SchemaVersion != 1 || len(inventory.Plugins) != 2 {
		t.Fatalf("inventory %s", raw)
	}
	p := inventory.Plugins[0]
	if p.Name != "@s2script/basechat" || p.Version != "0.8.1" || p.Enabled || p.Path != pluginDir+"/disabled/operator-renamed.s2sp" || p.SHA256 != fmt.Sprintf("%x", sha256.Sum256(b)) {
		t.Fatalf("wrong facts: %+v", p)
	}
}

func TestS2ScriptDefaultsPreserveActualPlacementAndManualPlugins(t *testing.T) {
	root := t.TempDir()
	putPlugin(t, root, "chat.s2sp", "@s2script/basechat", false)
	putPlugin(t, root, "vote.s2sp", "@s2script/nextmap", true)
	putPlugin(t, root, "custom.s2sp", "@operator/custom", false)
	for i := 0; i < 2; i++ {
		if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(nil)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"disabled/chat.s2sp", "vote.s2sp", "disabled/custom.s2sp"} {
		if _, err := os.Stat(filepath.Join(root, pluginDir, path)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestS2ScriptControlsRejectDuplicateIdentityBeforeMoves(t *testing.T) {
	root := t.TempDir()
	putPlugin(t, root, "one.s2sp", "@s2script/basechat", true)
	putPlugin(t, root, "two.s2sp", "@s2script/basechat", false)
	if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": "0"})); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, pluginDir, "one.s2sp")); err != nil {
		t.Fatal(err)
	}
}

func TestS2ScriptControlsRejectInvalidValueBeforeWriting(t *testing.T) {
	root := t.TempDir()
	if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": "yes"})); err == nil {
		t.Fatal("accepted invalid control")
	}
	if _, err := os.Stat(filepath.Join(root, "game")); !os.IsNotExist(err) {
		t.Fatalf("invalid input mutated tree: %v", err)
	}
}

func TestS2ScriptControlsRejectSymlinkPaths(t *testing.T) {
	for _, component := range []string{"plugins", "package", "inventory", "egg"} {
		t.Run(component, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			putPlugin(t, root, "chat.s2sp", "@s2script/basechat", true)
			var link, target string
			switch component {
			case "plugins":
				link = filepath.Join(root, pluginDir)
				target = outside
				if err := os.RemoveAll(link); err != nil {
					t.Fatal(err)
				}
			case "package":
				link = filepath.Join(root, pluginDir, "chat.s2sp")
				target = filepath.Join(outside, "chat.s2sp")
				if err := os.WriteFile(target, pluginZIP(t, "@s2script/basechat", "0.8.1"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			case "egg":
				link = filepath.Join(root, "egg")
				target = outside
			case "inventory":
				if err := os.MkdirAll(filepath.Join(root, "egg"), 0755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(root, "egg/installed-list.json")
				target = filepath.Join(outside, "inventory.json")
				if err := os.WriteFile(target, []byte("operator"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": "0"})); err == nil {
				t.Fatal("followed symlink")
			}
		})
	}
}

func TestS2ScriptCatalogReturnsIndependentReviewedDefaults(t *testing.T) {
	all := addons.S2ScriptPlugins()
	enabled := 0
	for _, p := range all {
		if p.DefaultEnabled {
			enabled++
		}
		if !strings.HasPrefix(p.Name, "@s2script/") || !strings.HasPrefix(p.Variable, "S2SCRIPT_PLUGIN_") {
			t.Fatalf("invalid catalog %+v", p)
		}
	}
	if len(all) != 18 || enabled != 14 {
		t.Fatalf("catalog: %d total, %d enabled", len(all), enabled)
	}
	all[0].Name = "changed"
	if addons.S2ScriptPlugins()[0].Name == "changed" {
		t.Fatal("shared catalog")
	}
}

func TestS2ScriptControlsInheritSavedPolicyBeforePanelOverride(t *testing.T) {
	root := t.TempDir()
	putPlugin(t, root, "chat.s2sp", "@s2script/basechat", true)
	policy := `{"schema_version":1,"plugins":{"@s2script/basechat":{"source":"local","version":"0.8.1","enabled":false}}}`
	if err := os.WriteFile(filepath.Join(root, "s2script-plugins.json"), []byte(policy), 0644); err != nil {
		t.Fatal(err)
	}
	if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, pluginDir, "disabled/chat.s2sp")); err != nil {
		t.Fatal(err)
	}
	if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": "1"})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, pluginDir, "chat.s2sp")); err != nil {
		t.Fatal(err)
	}
}
