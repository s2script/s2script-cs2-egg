// SPDX-License-Identifier: GPL-3.0-or-later
package addons_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
)

const nativeManifest = "game/csgo/addons/s2script/plugins.json"

func writeNativeManifest(t *testing.T, root, body string) {
	t.Helper()
	file := filepath.Join(root, nativeManifest)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRegistryStartupDefersToS2Script(t *testing.T) {
	root := t.TempDir()
	putPlugin(t, root, "private.s2sp", "private-plugin", true)
	body := `{"dependencies":{"community-plugin":"^1.2"},"autoUpdate":false,"reviewedOnly":true,"custom":{"keep":true}}`
	writeNativeManifest(t, root, body)
	if err := os.WriteFile(filepath.Join(root, "s2script-plugins.json"), []byte("obsolete policy"), 0644); err != nil {
		t.Fatal(err)
	}
	env := &addons.Env{Root: root, HTTP: &http.Client{Transport: s2Transport(func(*http.Request) (*http.Response, error) {
		t.Fatal("egg contacted registry despite native manifest")
		return nil, nil
	})}}
	getenv := pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_FILE": "../obsolete.json"})
	if err := env.CheckS2ScriptPlugins(context.Background(), getenv); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, nativeManifest))
	if err != nil || string(got) != body {
		t.Fatalf("native settings changed: %s, %v", got, err)
	}
}

func TestNativeRegistryBundledOverridesPreserveSettingsAndLock(t *testing.T) {
	root := t.TempDir()
	putPlugin(t, root, "chat.s2sp", "@s2script/basechat", true)
	writeNativeManifest(t, root, `{"name":"server","dependencies":{"@s2script/basechat":"^0.8.0","community-plugin":"*"},"disabled":["community-plugin"],"autoUpdate":false,"with":{"community-plugin":["optional"]},"custom":{"keep":true}}`)
	lock := filepath.Join(root, "game/csgo/addons/s2script/plugins-lock.json")
	lockBody := `{"lockfileVersion":3,"packages":{"":{}}}`
	if err := os.WriteFile(lock, []byte(lockBody), 0644); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "1"} {
		if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": value})); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, nativeManifest))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Disabled   []string
			AutoUpdate bool
			Custom     map[string]bool
			With       map[string][]string
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(manifest.Disabled, ","), "@s2script/basechat") != (value == "0") || !strings.Contains(strings.Join(manifest.Disabled, ","), "community-plugin") || manifest.AutoUpdate || !manifest.Custom["keep"] || len(manifest.With["community-plugin"]) != 1 {
			t.Fatalf("incorrect native manifest: %s", data)
		}
		dir := ""
		if value == "0" {
			dir = "disabled"
		}
		if _, err := os.Stat(filepath.Join(root, pluginDir, dir, "chat.s2sp")); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(lock); err != nil || string(data) != lockBody {
		t.Fatalf("egg changed native ownership lock: %s, %v", data, err)
	}
}

func TestNativeRegistryFilesSurviveFrameworkUpdate(t *testing.T) {
	files := s2files(t, "0.8.1")
	files["addons/s2script/plugins.json"] = []byte(`{"dependencies":{}}`)
	files["addons/s2script/plugins-lock.json"] = []byte("default lock")
	env := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	writeNativeManifest(t, env.Root, `{"dependencies":{"private":"1.0.0"},"autoUpdate":false}`)
	lock := filepath.Join(env.Root, "game/csgo/addons/s2script/plugins-lock.json")
	lockBody := `{"lockfileVersion":3,"packages":{"":{"dependencies":{"private":"1.0.0"}}}}`
	if err := os.WriteFile(lock, []byte(lockBody), 0644); err != nil {
		t.Fatal(err)
	}
	if err := env.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(env.Root, nativeManifest)); err != nil || !strings.Contains(string(data), "private") {
		t.Fatalf("manifest overwritten: %s, %v", data, err)
	}
	if data, err := os.ReadFile(lock); err != nil || string(data) != lockBody {
		t.Fatalf("lock overwritten: %s, %v", data, err)
	}
}

func TestNativeRegistryOwnsBundledDependenciesDuringFrameworkUpdate(t *testing.T) {
	env := s2fixture(t, s2zip(t, s2files(t, "0.8.2")), "0.8.2", "valid", "/latest", false)
	original := putPlugin(t, env.Root, "renamed.s2sp", "@s2script/clientprefs", false)
	writeNativeManifest(t, env.Root, `{"dependencies":{"@s2script/clientprefs":"0.8.1"},"disabled":["@s2script/clientprefs"]}`)
	if err := env.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(env.Root, pluginDir, "disabled/renamed.s2sp"))
	if err != nil || string(got) != string(original) {
		t.Fatalf("framework overwrote native registry package: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Root, pluginDir, "_s2script_clientprefs.s2sp")); !os.IsNotExist(err) {
		t.Fatalf("framework created duplicate native registry package: %v", err)
	}
}

func TestNativeRegistryOwnsTransitiveBundledPackagesDuringFrameworkUpdate(t *testing.T) {
	env := s2fixture(t, s2zip(t, s2files(t, "0.8.2")), "0.8.2", "valid", "/latest", false)
	original := putPlugin(t, env.Root, "renamed.s2sp", "@s2script/clientprefs", true)
	writeNativeManifest(t, env.Root, `{"dependencies":{"community-plugin":"*"}}`)
	lock := filepath.Join(env.Root, "game/csgo/addons/s2script/plugins-lock.json")
	if err := os.WriteFile(lock, []byte(`{"lockfileVersion":3,"packages":{"":{},"plugins/renamed.s2sp":{"name":"@s2script/clientprefs"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := env.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(env.Root, pluginDir, "renamed.s2sp")); err != nil || string(data) != string(original) {
		t.Fatalf("framework overwrote native transitive dependency: %v", err)
	}
}

func TestNativeRegistryRejectsUnsafeOrInvalidManifestBeforeMovingPlugins(t *testing.T) {
	for _, body := range []string{
		`null`,
		`{"dependencies":null}`,
		`{"dependencies":{"plugin":"*"},"disabled":["unknown"]}`,
		`{"dependencies":{},"disabled":null}`,
		`{"dependencies":{},"dependencies":{"plugin":"*"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			putPlugin(t, root, "chat.s2sp", "@s2script/basechat", true)
			writeNativeManifest(t, root, body)
			if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": "0"})); err == nil {
				t.Fatal("invalid native manifest accepted")
			}
			if _, err := os.Stat(filepath.Join(root, pluginDir, "chat.s2sp")); err != nil {
				t.Fatalf("plugin moved before validating manifest: %v", err)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		writeNativeManifest(t, root, `{}`)
		file := filepath.Join(root, nativeManifest)
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{}`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, file); err != nil {
			t.Fatal(err)
		}
		if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(nil)); err == nil {
			t.Fatal("native manifest symlink followed")
		}
	})
}

func TestNativeRegistryCreatesManifestWithoutLegacyFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "s2script-plugins.json"), []byte("obsolete policy"), 0644); err != nil {
		t.Fatal(err)
	}
	env := &addons.Env{Root: root, HTTP: &http.Client{Transport: s2Transport(func(*http.Request) (*http.Response, error) {
		t.Fatal("egg contacted the old registry manager")
		return nil, nil
	})}}
	if err := env.CheckS2ScriptPlugins(context.Background(), pluginEnv(nil)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, nativeManifest))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Dependencies map[string]string
		AutoUpdate   bool
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Dependencies == nil || !manifest.AutoUpdate {
		t.Fatalf("native defaults not created: %s, %v", data, err)
	}
}

func TestS2ScriptRuntimeLeaseExcludesCompetingEggProcesses(t *testing.T) {
	root := t.TempDir()
	release, err := addons.AcquireS2ScriptRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if competingRelease, err := addons.AcquireS2ScriptRuntime(root); err == nil {
		competingRelease()
		t.Fatal("competing runtime lease accepted")
	}
	release()
	nextRelease, err := addons.AcquireS2ScriptRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	nextRelease()
}

func TestNativeRegistryRollsBackManifestWhenPanelMoveFails(t *testing.T) {
	root := t.TempDir()
	plugin := putPlugin(t, root, "chat.s2sp", "@s2script/basechat", true)
	body := `{"dependencies":{"@s2script/basechat":"*"},"disabled":[]}`
	writeNativeManifest(t, root, body)
	hash := sha256.Sum256(plugin)
	lockBody := fmt.Sprintf(`{"lockfileVersion":3,"packages":{"":{},"plugins/chat.s2sp":{"name":"@s2script/basechat","version":"0.8.1","integrity":"sha256-%s"}}}`, base64.StdEncoding.EncodeToString(hash[:]))
	lock := filepath.Join(root, "game/csgo/addons/s2script/plugins-lock.json")
	if err := os.WriteFile(lock, []byte(lockBody), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "egg/installed-list.json"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": "0"})); err == nil {
		t.Fatal("unsafe inventory path accepted")
	}
	if data, err := os.ReadFile(filepath.Join(root, nativeManifest)); err != nil || string(data) != body {
		t.Fatalf("manifest not rolled back: %s, %v", data, err)
	}
	if data, err := os.ReadFile(lock); err != nil || string(data) != lockBody {
		t.Fatalf("native lock not rolled back: %s, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, pluginDir, "chat.s2sp")); err != nil {
		t.Fatalf("plugin moved despite failed preparation: %v", err)
	}
}

func TestNativeRegistryPanelMoveRelocatesVerifiedLockEntry(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(fmt.Sprint(tampered), func(t *testing.T) {
			root := t.TempDir()
			data := putPlugin(t, root, "renamed.s2sp", "@s2script/basechat", true)
			body := `{"dependencies":{"@s2script/basechat":"*"}}`
			writeNativeManifest(t, root, body)
			hash := sha256.Sum256(data)
			if tampered {
				hash[0] ^= 1
			}
			lock := filepath.Join(root, "game/csgo/addons/s2script/plugins-lock.json")
			lockBody := fmt.Sprintf(`{"name":"server","lockfileVersion":3,"packages":{"":{"dependencies":{"@s2script/basechat":"*"}},"plugins/renamed.s2sp":{"name":"@s2script/basechat","version":"0.8.1","integrity":"sha256-%s","resolved":"https://example.test/plugin","custom":true}}}`, base64.StdEncoding.EncodeToString(hash[:]))
			if err := os.WriteFile(lock, []byte(lockBody), 0644); err != nil {
				t.Fatal(err)
			}
			err := addons.ConfigureS2ScriptPlugins(root, pluginEnv(map[string]string{"S2SCRIPT_PLUGIN_BASECHAT": "0"}))
			if tampered {
				if err == nil {
					t.Fatal("changed native lock bytes accepted")
				}
				if got, err := os.ReadFile(filepath.Join(root, nativeManifest)); err != nil || string(got) != body {
					t.Fatalf("manifest not restored: %s, %v", got, err)
				}
				if got, err := os.ReadFile(lock); err != nil || string(got) != lockBody {
					t.Fatalf("unverified lock changed: %s, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(lock)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Packages map[string]map[string]any
			}
			if err := json.Unmarshal(got, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Packages["plugins/renamed.s2sp"] != nil || parsed.Packages["plugins/disabled/renamed.s2sp"]["custom"] != true {
				t.Fatalf("native ownership not relocated: %s", got)
			}
			if _, err := os.Stat(filepath.Join(root, pluginDir, "disabled/renamed.s2sp")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
