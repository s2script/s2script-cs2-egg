// SPDX-License-Identifier: GPL-3.0-or-later
package addons_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
)

type registryFixture struct {
	env         *addons.Env
	payloads    map[string][]byte
	latest      string
	requested   []string
	badHash     bool
	crossOrigin bool
	notFound    map[string]bool
	outage      bool
}

func newRegistryFixture(t *testing.T) *registryFixture {
	t.Helper()
	f := &registryFixture{payloads: map[string][]byte{}, latest: "1.2.3"}
	for _, version := range []string{"1.2.3", "1.3.0"} {
		f.payloads["consumer-"+version+".s2sp"] = registryZIP(t, `{"id":"consumer","version":"`+version+`","apiVersion":"3.x","pluginDependencies":{"@community/service":"^1.0.0"}}`)
	}
	f.payloads["provider.s2sp"] = registryZIP(t, `{"id":"provider","version":"9.0.0","apiVersion":"3.x","publishes":{"@community/service":{"version":"1.1.0"}}}`)
	var base string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		selector := r.URL.Query().Get("range")
		if f.outage {
			http.Error(w, "temporary", 503)
			return
		}
		if f.notFound[name] {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/v1/meta":
			f.requested = append(f.requested, "meta:"+name)
			fmt.Fprint(w, `{"name":`+fmt.Sprintf("%q", name)+`}`)
		case "/api/v1/plan":
			f.requested = append(f.requested, "plan:"+name+":"+selector)
			version := f.latest
			if selector != "*" {
				version = selector
			}
			items := []map[string]string{}
			if name == "consumer" {
				for _, file := range []string{"consumer-" + version + ".s2sp", "provider.s2sp"} {
					data, ok := f.payloads[file]
					if !ok {
						http.Error(w, "not found", 404)
						return
					}
					id, v := "consumer", version
					if file == "provider.s2sp" {
						id, v = "provider", "9.0.0"
					}
					digest := fmt.Sprintf("%x", sha256.Sum256(data))
					if f.badHash {
						digest = strings.Repeat("0", 64)
					}
					url := base + "/files/" + file
					if f.crossOrigin {
						url = "https://untrusted.example/" + file
					}
					items = append(items, map[string]string{"name": id, "version": v, "filename": file, "sha256": digest, "url": url, "reviewState": "reviewed"})
				}
			} else if selector == "*" {
				version = "9.0.0"
			}
			json.NewEncoder(w).Encode(map[string]any{"root": map[string]string{"name": name, "version": version}, "install": items, "skipped": []any{}, "warnings": []string{}, "errors": []string{}})
		default:
			if data, ok := f.payloads[strings.TrimPrefix(r.URL.Path, "/files/")]; ok {
				w.Write(data)
			} else {
				http.NotFound(w, r)
			}
		}
	}))
	base = srv.URL
	old := addons.S2ScriptRegistryURL
	addons.S2ScriptRegistryURL = base
	t.Cleanup(func() { addons.S2ScriptRegistryURL = old; srv.Close() })
	f.env = &addons.Env{Root: t.TempDir(), HTTP: srv.Client()}
	return f
}

func registryZIP(t *testing.T, manifest string) []byte {
	t.Helper()
	file := filepath.Join(t.TempDir(), "plugin.zip")
	makeZip(t, file, map[string]string{"manifest.json": manifest, "plugin.js": "export default {};"})
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *registryFixture) command(args ...string) error {
	return f.env.S2ScriptPluginCommand(context.Background(), args, pluginEnv(nil), io.Discard)
}

func TestS2ScriptRegistryInstallsPinnedClosureAndKeepsDisabledUpdates(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("install", "consumer", "--version", "1.2.3", "--disabled"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"consumer-1.2.3.s2sp", "provider.s2sp"} {
		if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "disabled", file)); err != nil {
			t.Fatal(err)
		}
	}
	f.latest = "1.3.0"
	f.requested = nil
	if err := f.env.CheckS2ScriptPlugins(context.Background(), pluginEnv(nil)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"meta:consumer", "meta:provider", "plan:consumer:*", "plan:consumer:1.2.3"} {
		if !strings.Contains(strings.Join(f.requested, "|"), query) {
			t.Errorf("missing %s from %v", query, f.requested)
		}
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "disabled/consumer-1.2.3.s2sp")); err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Plugins map[string]struct{ Version string } `json:"plugins"`
	}
	b, err := os.ReadFile(filepath.Join(f.env.Root, "s2script-plugins.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Plugins["consumer"].Version != "1.2.3" {
		t.Fatalf("pin mutated: %s", b)
	}
}

func TestS2ScriptRegistryCommandsValidateDependencyPlacement(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("disable", "provider"); err == nil {
		t.Fatal("disabled required provider")
	}
	if err := f.command("remove", "provider"); err == nil {
		t.Fatal("removed required provider")
	}
	if err := f.command("disable", "consumer"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("disable", "provider"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("enable", "consumer"); err == nil {
		t.Fatal("enabled consumer with disabled provider")
	}
	if err := f.command("enable", "provider"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("enable", "consumer"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("remove", "consumer"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "consumer-1.2.3.s2sp")); !os.IsNotExist(err) {
		t.Fatalf("remove: %v", err)
	}
}

func TestS2ScriptRegistryRejectsUnverifiedDownloadWithoutInstalling(t *testing.T) {
	for _, reason := range []string{"hash", "origin"} {
		t.Run(reason, func(t *testing.T) {
			f := newRegistryFixture(t)
			f.badHash = reason == "hash"
			f.crossOrigin = reason == "origin"
			if err := f.command("add", "consumer"); err == nil {
				t.Fatal("accepted bad artifact")
			}
			entries, err := os.ReadDir(filepath.Join(f.env.Root, pluginDir))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".s2sp") {
					t.Fatal("partially installed closure")
				}
			}
		})
	}
}

func TestS2ScriptRegistryPreservesUnownedManualPackages(t *testing.T) {
	f := newRegistryFixture(t)
	b := putPlugin(t, f.env.Root, "my-file.s2sp", "manual", false)
	if err := f.command("check"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("remove", "manual"); err == nil {
		t.Fatal("removed unowned local file")
	}
	got, err := os.ReadFile(filepath.Join(f.env.Root, pluginDir, "disabled/my-file.s2sp"))
	if err != nil || string(got) != string(b) {
		t.Fatalf("manual changed: %v", err)
	}
	if !strings.Contains(strings.Join(f.requested, "|"), "meta:manual") {
		t.Fatalf("manual not checked: %v", f.requested)
	}
}

func TestS2ScriptRegistryChecksAllowPrivateManualPackages(t *testing.T) {
	f := newRegistryFixture(t)
	f.notFound = map[string]bool{"private": true, "consumer": true}
	putPlugin(t, f.env.Root, "private.s2sp", "private", false)
	if err := f.env.CheckS2ScriptPlugins(context.Background(), pluginEnv(nil)); err != nil {
		t.Fatalf("private plugin blocked startup: %v", err)
	}
	if err := f.command("add", "consumer"); err == nil {
		t.Fatal("explicit registry 404 accepted")
	}
}

func TestS2ScriptRegistryDependencyPinsConstrainPlanArtifacts(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("pin", "provider", "--version", "9.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("update"); err == nil {
		t.Fatal("consumer plan bypassed exact dependency pin")
	}
}

func TestS2ScriptRuntimeLeaseExcludesMutationsButAllowsStartupChecks(t *testing.T) {
	f := newRegistryFixture(t)
	release, err := addons.AcquireS2ScriptRuntime(f.env.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = f.command("add", "consumer"); err == nil || !strings.Contains(err.Error(), "stop the server") {
		t.Fatalf("live mutation accepted: %v", err)
	}
	if err = f.env.CheckS2ScriptPlugins(context.Background(), pluginEnv(nil)); err != nil {
		t.Fatalf("startup deadlocked/reacquired lease: %v", err)
	}
}

func TestS2ScriptRegistryOutageReplaysOnlyMatchingVerifiedClosure(t *testing.T) {
	for _, invalid := range []string{"", "pin", "missing", "modified", "with"} {
		t.Run(invalid, func(t *testing.T) {
			f := newRegistryFixture(t)
			if err := f.command("add", "consumer", "--version", "1.2.3", "--disabled"); err != nil {
				t.Fatal(err)
			}
			switch invalid {
			case "pin":
				if err := f.command("pin", "consumer", "--version", "1.3.0"); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(filepath.Join(f.env.Root, pluginDir, "disabled/provider.s2sp")); err != nil {
					t.Fatal(err)
				}
			case "modified":
				if err := os.WriteFile(filepath.Join(f.env.Root, pluginDir, "disabled/provider.s2sp"), pluginZIP(t, "provider", "9.0.0"), 0644); err != nil {
					t.Fatal(err)
				}
			case "with":
				file := filepath.Join(f.env.Root, "s2script-plugins.json")
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(strings.Replace(string(raw), `"version": "1.2.3"`, `"version": "1.2.3", "with":["extra"]`, 1))
				if err = os.WriteFile(file, raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
			f.outage = true
			err := f.env.CheckS2ScriptPlugins(context.Background(), pluginEnv(nil))
			if invalid == "" && err != nil {
				t.Fatalf("verified closure rejected: %v", err)
			}
			if invalid != "" && err == nil {
				t.Fatal("replayed invalid retained closure")
			}
		})
	}
}

func TestS2ScriptPolicyRejectsAmbiguousPinsAndNullEnablement(t *testing.T) {
	for _, entry := range []string{`{"source":"registry","verison":"1.2.3"}`, `{"source":"registry","Version":"1.2.3"}`, `{"source":"registry","enabled":null}`} {
		t.Run(entry, func(t *testing.T) {
			f := newRegistryFixture(t)
			if err := os.WriteFile(filepath.Join(f.env.Root, "s2script-plugins.json"), []byte(`{"schema_version":1,"plugins":{"consumer":`+entry+`}}`), 0644); err != nil {
				t.Fatal(err)
			}
			if err := f.command("update"); err == nil {
				t.Fatal("accepted ambiguous policy")
			}
		})
	}
}

func TestS2ScriptRemoveValidatesMetadataBeforeDeletingPackage(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.env.Root, "egg/installed-list.json")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "operator.json")
	if err := os.WriteFile(outside, []byte("operator"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Fatal(err)
	}
	if err := f.command("remove", "consumer"); err == nil {
		t.Fatal("accepted unsafe metadata")
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "consumer-1.2.3.s2sp")); err != nil {
		t.Fatalf("failed remove deleted package: %v", err)
	}
}

func TestS2ScriptRegistryRepairsMissingTrackedDependencyOnline(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.env.Root, pluginDir, "provider.s2sp")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := f.command("update"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("missing dependency not restored: %v", err)
	}
}

func TestS2ScriptLegacyBuiltinsRequireVerifiedUnchangedHost(t *testing.T) {
	f := newRegistryFixture(t)
	dir := filepath.Join(f.env.Root, pluginDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	legacy := registryZIP(t, `{"id":"legacy","version":"1.0.0","apiVersion":"2.x","pluginDependencies":{"@s2script/entity":"^0.2.0","@s2script/math":"^0.1.0"}}`)
	if err := os.WriteFile(filepath.Join(dir, "legacy.s2sp"), legacy, 0644); err != nil {
		t.Fatal(err)
	}
	if err := f.command("enable", "legacy"); err == nil {
		t.Fatal("granted phantom builtin without verified host")
	}
	native := map[string]string{}
	for _, path := range []string{"game/csgo/addons/metamod/s2script.vdf", "game/csgo/addons/s2script/bin/linuxsteamrt64/s2script.so", "game/csgo/addons/s2script/bin/linuxsteamrt64/libs2script_core.so"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(f.env.Root, path)), 0755); err != nil {
			t.Fatal(err)
		}
		data := []byte("verified native " + path)
		if err := os.WriteFile(filepath.Join(f.env.Root, path), data, 0644); err != nil {
			t.Fatal(err)
		}
		native[path] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	if err := addons.RegisterS2ScriptHost(f.env.Root, "0.8.1", strings.Repeat("a", 40), native); err != nil {
		t.Fatal(err)
	}
	if !addons.S2ScriptHostVerified(f.env.Root, "0.8.1") {
		t.Fatal("verified host not retained")
	}
	if err := f.command("enable", "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.env.Root, "game/csgo/addons/s2script/bin/linuxsteamrt64/libs2script_core.so"), []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	if addons.S2ScriptHostVerified(f.env.Root, "0.8.1") {
		t.Fatal("modified host retained compatibility proof")
	}
	if err := f.command("enable", "legacy"); err == nil {
		t.Fatal("granted builtin on modified host")
	}
}

func TestS2ScriptRegistryRejectsUnsupportedAPIWithoutInstalling(t *testing.T) {
	f := newRegistryFixture(t)
	f.payloads["consumer-1.2.3.s2sp"] = registryZIP(t, `{"id":"consumer","version":"1.2.3","apiVersion":"999.x"}`)
	if err := f.command("add", "consumer"); err == nil {
		t.Fatal("installed unsupported plugin API")
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "consumer-1.2.3.s2sp")); !os.IsNotExist(err) {
		t.Fatalf("installed invalid package: %v", err)
	}
}

func TestS2ScriptRegistryExplicitAdoptionRequiresExactPublisherBytes(t *testing.T) {
	for _, matching := range []bool{true, false} {
		t.Run(fmt.Sprint(matching), func(t *testing.T) {
			f := newRegistryFixture(t)
			dir := filepath.Join(f.env.Root, pluginDir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			data := f.payloads["consumer-1.2.3.s2sp"]
			if !matching {
				data = pluginZIP(t, "consumer", "1.2.3")
			}
			path := filepath.Join(dir, "operator-consumer.s2sp")
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			err := f.command("add", "consumer", "--version", "1.2.3")
			if matching && err != nil {
				t.Fatalf("exact explicit adoption failed: %v", err)
			}
			if !matching && err == nil {
				t.Fatal("adopted modified manual package")
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != string(data) {
				t.Fatalf("manual bytes changed: %v", readErr)
			}
		})
	}
}

type registryRoundTrip func(*http.Request) (*http.Response, error)

func (f registryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestS2ScriptRegistryTLSFailureNeverUsesRetainedFallback(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	f.env.HTTP = &http.Client{Transport: registryRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("remote error: tls: handshake failure")}
	})}
	if err := f.command("update"); err == nil {
		t.Fatal("TLS rejection reused retained closure")
	}
}

func TestS2ScriptRegistryInfersInstalledRootWithoutTreatingDependenciesAsRoots(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.env.Root, "s2script-plugins.json"), []byte(`{"schema_version":1,"plugins":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	f.latest = "1.3.0"
	if err := f.command("update"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "consumer-1.3.0.s2sp")); err != nil {
		t.Fatalf("installed root stopped updating without explicit policy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "consumer-1.2.3.s2sp")); !os.IsNotExist(err) {
		t.Fatalf("previous root version retained: %v", err)
	}
}

func TestS2ScriptRegistryEnablementKeepsVerifiedOutageEvidence(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	if err := f.command("disable", "consumer"); err != nil {
		t.Fatal(err)
	}
	f.outage = true
	if err := f.env.CheckS2ScriptPlugins(context.Background(), pluginEnv(nil)); err != nil {
		t.Fatalf("enablement discarded verified closure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "disabled/consumer-1.2.3.s2sp")); err != nil {
		t.Fatal(err)
	}
}

func TestS2ScriptRegistryOutageAppliesEnablementToVerifiedRequiredClosure(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer", "--disabled"); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(f.env.Root, "s2script-plugins.json")
	raw, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"enabled": false`, `"enabled": true`, 1))
	if err = os.WriteFile(policy, raw, 0644); err != nil {
		t.Fatal(err)
	}
	f.outage = true
	if err = f.env.CheckS2ScriptPlugins(context.Background(), pluginEnv(nil)); err != nil {
		t.Fatalf("verified provider not enabled for consumer: %v", err)
	}
	for _, name := range []string{"consumer-1.2.3.s2sp", "provider.s2sp"} {
		if _, err = os.Stat(filepath.Join(f.env.Root, pluginDir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestS2ScriptRegistryRefusesEmptyRequestedArtifactClosure(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "provider"); err == nil {
		t.Fatal("claimed installation from empty registry plan")
	}
}

func TestS2ScriptRegistryRemovesMissingManagedRootOffline(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.env.Root, pluginDir, "consumer-1.2.3.s2sp")); err != nil {
		t.Fatal(err)
	}
	f.outage = true
	if err := f.command("remove", "consumer"); err != nil {
		t.Fatalf("missing managed root could not be removed offline: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(f.env.Root, "s2script-plugins.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"consumer"`) {
		t.Fatalf("removed root references retained: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "provider.s2sp")); err != nil {
		t.Fatal(err)
	}
}

func TestS2ScriptRegistryStagesMissingRootEnablementForRepair(t *testing.T) {
	f := newRegistryFixture(t)
	if err := f.command("add", "consumer"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.env.Root, pluginDir, "consumer-1.2.3.s2sp")); err != nil {
		t.Fatal(err)
	}
	if err := f.command("disable", "consumer"); err != nil {
		t.Fatalf("missing-root policy could not be staged: %v", err)
	}
	if err := f.command("update"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.env.Root, pluginDir, "disabled/consumer-1.2.3.s2sp")); err != nil {
		t.Fatal(err)
	}
}
