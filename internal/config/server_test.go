// SPDX-License-Identifier: GPL-3.0-or-later
package config_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/config"
)

func lookup(values map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := values[k]; return v, ok }
}
func load(t *testing.T, values map[string]string) config.Server {
	t.Helper()
	if values == nil {
		values = map[string]string{}
	}
	values["ALLOW_TOKENLESS"] = "1"
	s, err := config.LoadServer(t.TempDir(), lookup(values))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Catches accepting malformed port/boolean overrides or silently replacing them with defaults.
func TestServerConfigRejectsInvalidPortAndBoolean(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"SERVER_PORT", "0"}, {"SERVER_PORT", "65536"}, {"SERVER_PORT", "abc"}, {"SERVER_PORT", ""}, {"ALLOW_TOKENLESS", "true"}, {"SRCDS_VALIDATE", "yes"}, {"SRCDS_STOP_UPDATE", "2"}, {"S2SCRIPT_HUD", "false"}, {"SRCDS_MAXPLAYERS", "65"}} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			_, err := config.LoadServer(t.TempDir(), lookup(map[string]string{"ALLOW_TOKENLESS": "1", tc.key: tc.value}))
			if err == nil {
				t.Fatal("accepted invalid override")
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error lacks setting: %v", err)
			}
		})
	}
}

// Catches a secret leaking through JSON, printable structs, or validation errors.
func TestConfigRedactsSecrets(t *testing.T) {
	s := load(t, map[string]string{"STEAM_ACC": "private-steam-token", "DISCORD_WEBHOOK_URL": "https://discord.com/api/webhooks/123/private-webhook-token"})
	if s.Steam.GSLT != "private-steam-token" {
		t.Fatal("runtime lost token")
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{string(b), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s)} {
		for _, secret := range []string{"private-steam-token", "private-webhook-token"} {
			if strings.Contains(out, secret) {
				t.Fatal("secret leaked")
			}
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Fatal("missing redaction")
		}
	}
	_, err = config.LoadServer(t.TempDir(), lookup(map[string]string{"STEAM_ACC": "private-steam-token", "DISCORD_WEBHOOK_URL": "http://evil.test/private-webhook-token"}))
	if err == nil || strings.Contains(err.Error(), "private-webhook-token") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestDefaultsTokenPolicyAndRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := config.LoadServer(root, lookup(nil)); err == nil {
		t.Fatal("accepted missing GSLT without tokenless mode")
	}
	s := load(t, nil)
	if s.Steam.AppID != 730 || s.Launch.Port != 27015 || s.Launch.IP != "0.0.0.0" || s.Launch.Map != "de_dust2" || s.Launch.MaxPlayers != 32 || s.Frameworks.Channel != "stable" || s.Frameworks.S2ScriptVersion != "latest" || s.Frameworks.MetamodVersion != "latest" {
		t.Fatalf("wrong defaults: %+v", s)
	}
	if !filepath.IsAbs(s.Root) {
		t.Fatal("root is not absolute")
	}
	if _, err := config.LoadServer(filepath.Join(root, "absent"), lookup(map[string]string{"ALLOW_TOKENLESS": "1"})); err == nil {
		t.Fatal("accepted missing root")
	}
}

func TestConfigPrecedenceAndPluginDefaults(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "egg"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "egg/server.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"schema_version":1,"steam":{"allow_tokenless":true},"launch":{"port":28015,"map":"de_nuke"},"plugins":{"bundled":{"@s2script/basechat":false,"@s2script/nextmap":true}}}`)
	s, err := config.LoadServer(root, lookup(map[string]string{"SERVER_PORT": "29015", "S2SCRIPT_PLUGIN_BASECHAT": "default", "S2SCRIPT_PLUGIN_NEXTMAP": "0"}))
	if err != nil {
		t.Fatal(err)
	}
	if s.Launch.Port != 29015 || s.Launch.Map != "de_nuke" || s.Plugins.Bundled["@s2script/basechat"] || s.Plugins.Bundled["@s2script/nextmap"] {
		t.Fatalf("wrong precedence: %+v", s)
	}
	for _, body := range []string{`{"schema_version":2}`, `{"schema_version":1,"typo":true}`, `{"schema_version":1} {}`, `{"schema_version":1,"steam":{"appid":440}}`} {
		write(body)
		if _, err := config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1"})); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestCustomParametersRemainData(t *testing.T) {
	s := load(t, map[string]string{"CUSTOM_PARAMS": `+hostname "test server" ; $(touch /tmp/never-run)`, "SERVER_IP": "127.0.0.1"})
	want := []string{"+hostname", "test server", ";", "$(touch", "/tmp/never-run)"}
	b, _ := json.Marshal(s.Launch.ExtraArgs)
	w, _ := json.Marshal(want)
	if string(b) != string(w) {
		t.Fatalf("args %s, want %s", b, w)
	}
}

func TestRejectUnsafeSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"SERVER_IP", "bad"}, {"S2SCRIPT_CHANNEL", "nightly"}, {"S2SCRIPT_VERSION", "https://evil.test/v1"}, {"METAMOD_VERSION", "../v1"}, {"S2SCRIPT_PLUGIN_FILE", "../outside.json"}, {"S2SCRIPT_PLUGIN_FILE", "/tmp/outside.json"}, {"S2SCRIPT_PLUGIN_BASECHAT", "yes"}, {"CUSTOM_PARAMS", `"unterminated`}, {"GAME_TYPE", "-1"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			_, err := config.LoadServer(t.TempDir(), lookup(map[string]string{"ALLOW_TOKENLESS": "1", tc.key: tc.value}))
			if err == nil {
				t.Fatal("accepted invalid setting")
			}
		})
	}
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1", "S2SCRIPT_PLUGIN_FILE": "escape/plugins.json"})); err == nil {
		t.Fatal("accepted symlink escape")
	}
}

func TestFrameworkUpdatePolicyAndFileBounds(t *testing.T) {
	s := load(t, map[string]string{"S2SCRIPT_UPDATE": "0", "METAMOD_UPDATE": "0"})
	if s.Frameworks.S2ScriptUpdate || s.Frameworks.MetamodUpdate {
		t.Fatal("ignored framework update policy")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "egg"), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"schema_version":1}` + strings.Repeat(" ", 1<<20) + `invalid`
	if err := os.WriteFile(filepath.Join(root, "egg/server.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1"})); err == nil {
		t.Fatal("accepted oversized config")
	}
}

func TestNestedSettingsRedactSecrets(t *testing.T) {
	s := load(t, map[string]string{"STEAM_ACC": "nested-private-token", "DISCORD_WEBHOOK_URL": "https://discord.com/api/webhooks/123/nested-private-hook"})
	for _, value := range []any{s.Steam, s.Webhook} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range []string{string(b), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value)} {
			if strings.Contains(out, "nested-private") {
				t.Fatal("nested settings leaked secret")
			}
		}
	}
}

func TestRejectNullAndDirectoryConfigurationPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "egg"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`null`, `{"schema_version":null}`, `{"schema_version":1,"launch":{"port":null}}`} {
		if err := os.WriteFile(filepath.Join(root, "egg/server.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1"})); err == nil {
			t.Fatalf("accepted malformed null config %s", body)
		}
	}
	if err := os.Remove(filepath.Join(root, "egg/server.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1", "S2SCRIPT_PLUGIN_FILE": "egg"})); err == nil {
		t.Fatal("accepted directory as inventory file")
	}
}

// Catches treating an existing dangling symlink as an absent lexical component.
func TestRejectDanglingInventorySymlinks(t *testing.T) {
	for _, tc := range []struct {
		name               string
		ancestor, relative bool
	}{
		{name: "inventory_file_absolute"},
		{name: "inventory_file_relative", relative: true},
		{name: "ancestor_directory_absolute", ancestor: true},
		{name: "ancestor_directory_relative", ancestor: true, relative: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			destination := filepath.Join(outside, "not-created")
			link, inventory := filepath.Join(root, "inventory.json"), "inventory.json"
			if tc.ancestor {
				link = filepath.Join(root, "pending")
				inventory = "pending/nested/inventory.json"
			}
			if tc.relative {
				var err error
				destination, err = filepath.Rel(root, destination)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Fatal(err)
			}
			_, err := config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1", "S2SCRIPT_PLUGIN_FILE": inventory}))
			if err == nil {
				t.Fatal("accepted dangling inventory symlink escaping root")
			}
			if !strings.Contains(err.Error(), "S2SCRIPT_PLUGIN_FILE") {
				t.Fatalf("error omits setting: %v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("validation modified outside directory")
			}
		})
	}
}

func TestAllowMissingInventoryUnderResolvedInRootSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data", filepath.Join(root, "inventory-directory")); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1", "S2SCRIPT_PLUGIN_FILE": "inventory-directory/missing/inventory.json"})); err != nil {
		t.Fatalf("rejected contained missing inventory: %v", err)
	}
}

func TestReviewedOnlyConfigPrecedence(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "egg"), 0700)
	os.WriteFile(filepath.Join(root, "egg/server.json"), []byte(`{"plugins":{"reviewed_only":true}}`), 0600)
	cfg, e := config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1"}))
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(cfg)
	if !strings.Contains(string(raw), `"reviewed_only":true`) {
		t.Fatal("saved reviewed-only lost")
	}
	cfg, e = config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1", "S2SCRIPT_REVIEWED_ONLY": "0"}))
	raw, _ = json.Marshal(cfg)
	if e != nil || !strings.Contains(string(raw), `"reviewed_only":false`) {
		t.Fatal("environment override lost", e)
	}
	if _, e = config.LoadServer(root, lookup(map[string]string{"ALLOW_TOKENLESS": "1", "S2SCRIPT_REVIEWED_ONLY": "true"})); e == nil {
		t.Fatal("invalid reviewed-only accepted")
	}
}

func TestCanonicalStartupFailureEvent(t *testing.T) {
	root := t.TempDir()
	if e := os.MkdirAll(filepath.Join(root, "egg"), 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"server.start_failed", "startup.failure"} {
		os.WriteFile(filepath.Join(root, "egg/server.json"), []byte(`{"webhook":{"events":["`+name+`"]}}`), 0600)
		if _, e := config.LoadServer(root, func(k string) (string, bool) { return "1", k == "ALLOW_TOKENLESS" }); e != nil {
			t.Fatalf("event %s: %v", name, e)
		}
	}
}

func TestDebugPortValidation(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  int
		valid bool
	}{{"0", 0, true}, {"2345", 2345, true}, {"65535", 65535, true}, {"-1", 0, false}, {"65536", 0, false}, {"no", 0, false}, {"27015", 0, false}} {
		t.Run(tt.value, func(t *testing.T) {
			cfg, e := config.LoadServer(t.TempDir(), func(k string) (string, bool) {
				if k == "ALLOW_TOKENLESS" {
					return "1", true
				}
				if k == "GDB_DEBUG_PORT" {
					return tt.value, true
				}
				return "", false
			})
			if tt.valid {
				if e != nil || cfg.Launch.DebugPort != tt.want {
					t.Fatalf("debug=%d err=%v", cfg.Launch.DebugPort, e)
				}
			} else if e == nil {
				t.Fatal("invalid debugger port accepted")
			}
		})
	}
}
func TestWebhookDeliveryLimits(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "egg"), 0755)
	os.WriteFile(filepath.Join(root, "egg/server.json"), []byte(`{"steam":{"allow_tokenless":true},"webhook":{"queue_capacity":8,"max_retries":3}}`), 0600)
	cfg, e := config.LoadServer(root, func(string) (string, bool) { return "", false })
	if e != nil || cfg.Webhook.QueueCapacity != 8 || cfg.Webhook.MaxRetries != 3 {
		t.Fatalf("limits %+v err %v", cfg.Webhook, e)
	}
}

func TestWebhookLimitsRejectMalformedOverrides(t *testing.T) {
	for _, body := range []string{`{"queue_capacity":0}`, `{"queue_capacity":1025}`, `{"max_retries":-1}`, `{"max_retries":11}`} {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "egg"), 0755)
		os.WriteFile(filepath.Join(root, "egg/server.json"), []byte(`{"steam":{"allow_tokenless":true},"webhook":`+body+`}`), 0600)
		if _, e := config.LoadServer(root, func(string) (string, bool) { return "", false }); e == nil {
			t.Fatalf("accepted delivery limits %s", body)
		}
	}
	cfg := load(t, nil)
	if cfg.Launch.DebugPort != 0 || cfg.Webhook.QueueCapacity != 64 || cfg.Webhook.MaxRetries != 2 {
		t.Fatalf("effective defaults %+v", cfg)
	}
}
