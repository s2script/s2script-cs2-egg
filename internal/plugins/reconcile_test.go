// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/framework"
)

type registryFixture struct {
	registry  Registry
	server    *httptest.Server
	plans     map[string]RegistryPlan
	files     map[string][]byte
	status    int
	metaCount map[string]int
}

func newRegistryFixture(t *testing.T) *registryFixture {
	t.Helper()
	f := &registryFixture{plans: map[string]RegistryPlan{}, files: map[string][]byte{}, metaCount: map[string]int{}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		name := r.URL.Query().Get("name")
		switch r.URL.Path {
		case "/api/v1/meta":
			f.metaCount[name]++
			w.Write([]byte(`{"opaque":"upstream metadata"}`))
		case "/api/v1/plan":
			p, ok := f.plans[name+"|"+r.URL.Query().Get("range")]
			if !ok {
				p, ok = f.plans[name]
			}
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(p)
		default:
			b, ok := f.files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		}
	}))
	t.Cleanup(f.server.Close)
	f.registry = Registry{BaseURL: f.server.URL, Client: f.server.Client()}
	return f
}
func (f *registryFixture) entry(t *testing.T, name, version, extra string) PlanEntry {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("manifest.json")
	w.Write([]byte(`{"id":"` + name + `","version":"` + version + `","apiVersion":"3.x"` + extra + `}`))
	w, _ = z.Create("plugin.js")
	w.Write([]byte("// fixture"))
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	filename := strings.NewReplacer("@", "_", "/", "_").Replace(name) + ".s2sp"
	path := "/files/" + filename + "/" + version
	f.files[path] = b.Bytes()
	return PlanEntry{Name: name, Version: version, URL: f.server.URL + path, SHA256: fileSHA256(b.Bytes()), ReviewState: "reviewed", Filename: filename}
}
func plan(root PlanEntry, entries ...PlanEntry) RegistryPlan {
	return RegistryPlan{Root: &PlanRoot{Name: root.Name, Version: root.Version}, Install: append([]PlanEntry{root}, entries...), Skipped: []Skipped{}, Warnings: []string{}, Errors: []string{}}
}
func selected() framework.Selection {
	return framework.Selection{HostAPIMajor: 3, SupportedAPIMajors: []int{2, 3}, Protocol2APIMajor: 3}
}
func (f *registryFixture) prepare(t *testing.T, root string, p Policy, items []Installed) (Prepared, error) {
	t.Helper()
	return f.registry.Prepare(context.Background(), config.Server{Root: root}, p, items, selected(), nil)
}
func (f *registryFixture) install(t *testing.T, root string, e PlanEntry, enabled bool) Installed {
	t.Helper()
	rel, _ := Target(e.Filename, enabled)
	path := filepath.Join(root, "game/csgo", rel)
	os.MkdirAll(filepath.Dir(path), 0700)
	b := f.files[strings.TrimPrefix(e.URL, f.server.URL)]
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := manifestBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	p := Installed{Name: e.Name, Version: e.Version, Source: "registry", SHA256: e.SHA256, Filename: e.Filename, Enabled: enabled, Managed: true, Verification: "verified", SourceURL: e.URL, ReviewState: "reviewed", Manifest: m.Raw}
	writeFacts(t, root, []Installed{p})
	return p
}
func applyPrepared(t *testing.T, root string, p Prepared) {
	t.Helper()
	defer p.Cleanup()
	release, e := artifact.Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = artifact.Apply(context.Background(), root, p.Changes); e != nil {
		t.Fatal(e)
	}
}
func TestDisabledRegistryPluginUpdatesInDisabled(t *testing.T) {
	f := newRegistryFixture(t)
	root := t.TempDir()
	old := f.install(t, root, f.entry(t, "hello", "1.0.0", ""), false)
	new := f.entry(t, "hello", "1.1.0", "")
	f.plans["hello"] = plan(new)
	p, e := f.prepare(t, root, EmptyPolicy(), []Installed{old})
	if e != nil {
		t.Fatal(e)
	}
	applyPrepared(t, root, p)
	got, e := Discover(root)
	if e != nil || len(got) != 1 || got[0].Version != "1.1.0" || got[0].Enabled || len(p.Checks) != 1 || f.metaCount["hello"] != 1 {
		t.Fatalf("%+v %v checks=%+v", got, e, p.Checks)
	}
}
func TestPinnedPluginIsCheckedButNotReplaced(t *testing.T) {
	f := newRegistryFixture(t)
	root := t.TempDir()
	entry := f.entry(t, "hello", "1.0.0", "")
	old := f.install(t, root, entry, false)
	f.plans["hello"] = plan(f.entry(t, "hello", "1.1.0", ""))
	f.plans["hello|1.0.0"] = plan(entry)
	policy := EmptyPolicy()
	policy.Plugins["hello"] = Entry{Source: "registry", Version: "1.0.0"}
	p, e := f.prepare(t, root, policy, []Installed{old})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Cleanup()
	if p.Installed[0].Version != "1.0.0" || p.Installed[0].SHA256 != old.SHA256 || len(p.Checks) != 1 || p.Checks[0].AvailableVersion != "1.1.0" {
		t.Fatalf("%+v", p)
	}
	for _, c := range p.Changes {
		if strings.HasSuffix(c.Path, ".s2sp") {
			t.Fatal("pin replaced", c)
		}
	}
}
func TestConflictingClosuresRejectWholePlan(t *testing.T) {
	f := newRegistryFixture(t)
	a := f.entry(t, "a", "1.0.0", "")
	b := f.entry(t, "b", "1.0.0", "")
	f.plans["a"] = plan(a, f.entry(t, "dep", "1.0.0", ""))
	f.plans["b"] = plan(b, f.entry(t, "dep", "2.0.0", ""))
	policy := EmptyPolicy()
	for _, n := range []string{"a", "b"} {
		policy.Plugins[n] = Entry{Source: "registry"}
	}
	p, e := f.prepare(t, t.TempDir(), policy, nil)
	if e == nil || len(p.Changes) != 0 {
		p.Cleanup()
		t.Fatal("conflicting closures accepted")
	}
}
func TestLocalModifiedPluginNotAdoptedByName(t *testing.T) {
	f := newRegistryFixture(t)
	root := t.TempDir()
	pluginFile(t, root, "hello.s2sp", "hello", "1.0.0", true)
	items, e := Discover(root)
	if e != nil {
		t.Fatal(e)
	}
	f.plans["hello"] = plan(f.entry(t, "hello", "1.1.0", ""))
	p, e := f.prepare(t, root, EmptyPolicy(), items)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Cleanup()
	if p.Installed[0].Managed || p.Installed[0].SHA256 != items[0].SHA256 || len(p.Checks) != 1 {
		t.Fatal("local file adopted", p)
	}
	for _, c := range p.Changes {
		if strings.HasSuffix(c.Path, ".s2sp") {
			t.Fatal("local replaced")
		}
	}
}
func TestMissingHashAndFilenameCollisionAbort(t *testing.T) {
	for _, kind := range []string{"hash", "filename"} {
		t.Run(kind, func(t *testing.T) {
			f := newRegistryFixture(t)
			a := f.entry(t, "a", "1.0.0", "")
			b := f.entry(t, "b", "1.0.0", "")
			if kind == "hash" {
				a.SHA256 = ""
			} else {
				b.Filename = a.Filename
			}
			f.plans["a"] = plan(a, b)
			policy := EmptyPolicy()
			policy.Plugins["a"] = Entry{Source: "registry"}
			p, e := f.prepare(t, t.TempDir(), policy, nil)
			if e == nil || len(p.Changes) > 0 {
				p.Cleanup()
				t.Fatal("invalid closure accepted")
			}
		})
	}
}
func TestDisabledRequiredDependencyFails(t *testing.T) {
	f := newRegistryFixture(t)
	a := f.entry(t, "a", "1.0.0", `,"pluginDependencies":{"iface":"^1.0.0"}`)
	b := f.entry(t, "provider", "3.1.0", `,"publishes":{"iface":{"version":"1.2.0"}}`)
	f.plans["a"] = plan(a, b)
	f.plans["provider"] = plan(b)
	policy := EmptyPolicy()
	disabled := false
	policy.Plugins["a"] = Entry{Source: "registry"}
	policy.Plugins["provider"] = Entry{Source: "registry", Enabled: &disabled}
	p, e := f.prepare(t, t.TempDir(), policy, nil)
	if e == nil || len(p.Changes) > 0 {
		p.Cleanup()
		t.Fatal("enabled consumer accepted disabled provider")
	}
}
func TestDisabledRootDependencyPlacement(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "exclusive", true: "shared"}[shared], func(t *testing.T) {
			f := newRegistryFixture(t)
			a := f.entry(t, "a", "1.0.0", `,"pluginDependencies":{"iface":"^1.0.0"}`)
			b := f.entry(t, "provider", "3.1.0", `,"publishes":{"iface":{"version":"1.2.0"}}`)
			f.plans["a"] = plan(a, b)
			policy := EmptyPolicy()
			disabled := false
			policy.Plugins["a"] = Entry{Source: "registry", Enabled: &disabled}
			if shared {
				c := f.entry(t, "c", "1.0.0", `,"pluginDependencies":{"iface":"^1.0.0"}`)
				f.plans["c"] = plan(c, b)
				policy.Plugins["c"] = Entry{Source: "registry"}
			}
			p, e := f.prepare(t, t.TempDir(), policy, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer p.Cleanup()
			for _, item := range p.Installed {
				if item.Name == "provider" && item.Enabled != shared {
					t.Fatalf("provider enabled %v, want %v", item.Enabled, shared)
				}
			}
		})
	}
}

func TestBundledPlacementPinsAndConfigsSurvive(t *testing.T) {
	for _, override := range []string{"inherit", "enabled", "disabled"} {
		t.Run(override, func(t *testing.T) {
			f := newRegistryFixture(t)
			root := t.TempDir()
			entry := f.entry(t, "@s2script/basecommands", "0.8.1", "")
			data := f.files[strings.TrimPrefix(entry.URL, f.server.URL)]
			m, _ := manifestBytes(data)
			source := filepath.Join(t.TempDir(), "bundle.s2sp")
			os.WriteFile(source, data, 0600)
			selection := selected()
			selection.S2Script = framework.Release{URL: "https://github.com/s2script/s2script/releases/download/v0.8.1/release.zip", SHA256: strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
			bundle := framework.BundledArtifact{Name: entry.Name, Version: entry.Version, SHA256: entry.SHA256, StagedPath: source, Filename: entry.Filename, Manifest: m.Raw, UpstreamEnabled: true}
			old := f.install(t, root, entry, false)
			old.Source = "release"
			old.SourceURL = selection.S2Script.URL
			writeFacts(t, root, []Installed{old})
			configPath := filepath.Join(root, "game/csgo/addons/s2script/configs/operator.json")
			os.MkdirAll(filepath.Dir(configPath), 0700)
			os.WriteFile(configPath, []byte("user configuration"), 0600)
			cfg := config.Server{Root: root}
			if override != "inherit" {
				cfg.Plugins.Overrides = map[string]bool{entry.Name: override == "enabled"}
			}
			p, e := f.registry.Prepare(context.Background(), cfg, EmptyPolicy(), []Installed{old}, selection, []framework.BundledArtifact{bundle})
			if e != nil {
				t.Fatal(e)
			}
			applyPrepared(t, root, p)
			got, _ := Discover(root)
			if got[0].Enabled != (override == "enabled") || got[0].Source != "release" || got[0].Generation != selection.S2Script.SHA256 {
				t.Fatal(got)
			}
			b, _ := os.ReadFile(configPath)
			if string(b) != "user configuration" {
				t.Fatal("user config overwritten")
			}
			policy := EmptyPolicy()
			policy.Plugins[entry.Name] = Entry{Source: "release", Version: "0.8.0"}
			bad, e := f.registry.Prepare(context.Background(), cfg, policy, got, selection, []framework.BundledArtifact{bundle})
			if e == nil {
				bad.Cleanup()
				t.Fatal("bundled pin ignored")
			}
		})
	}
}
func TestMissingRegistryArtifactReinstalled(t *testing.T) {
	f := newRegistryFixture(t)
	root := t.TempDir()
	entry := f.entry(t, "a", "1.0.0", "")
	p := f.install(t, root, entry, false)
	target, _ := Target(p.Filename, false)
	os.Remove(filepath.Join(root, "game/csgo", target))
	items, e := Discover(root)
	if e != nil {
		t.Fatal(e)
	}
	f.plans["a"] = plan(entry)
	prepared, e := f.prepare(t, root, EmptyPolicy(), items)
	if e != nil {
		t.Fatal(e)
	}
	applyPrepared(t, root, prepared)
	got, _ := Discover(root)
	if len(got) != 1 || got[0].LastCheckStatus == "missing" || got[0].Enabled {
		t.Fatal(got)
	}
}

func TestPrepareRejectsNilPolicyAndUnsupportedSelectedAPI(t *testing.T) {
	t.Run("nil-policy", func(t *testing.T) {
		f := newRegistryFixture(t)
		if p, e := f.prepare(t, t.TempDir(), Policy{SchemaVersion: 1}, nil); e == nil {
			p.Cleanup()
			t.Fatal("nil policy accepted")
		}
	})
	t.Run("unsupported-api", func(t *testing.T) {
		f := newRegistryFixture(t)
		selection := selected()
		selection.SupportedAPIMajors = append(selection.SupportedAPIMajors, 1)
		if p, e := f.registry.Prepare(context.Background(), config.Server{Root: t.TempDir()}, EmptyPolicy(), nil, selection, nil); e == nil {
			p.Cleanup()
			t.Fatal("unverified host API mapping accepted")
		}
	})
}

// A newer standalone provider must be reported without replacing the version
// selected by its actual pinned root, on every reconciliation and placement.
func TestRepeatedReconcileImplicitProviderFollowsPinnedRoot(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			f := newRegistryFixture(t)
			root := t.TempDir()
			provider := f.entry(t, "a-provider", "1.0.0", `,"publishes":{"iface":{"version":"1.0.0"}}`)
			consumer := f.entry(t, "z-consumer", "1.0.0", `,"pluginDependencies":{"iface":"^1.0.0"}`)
			f.plans[consumer.Name] = plan(consumer, provider)
			f.plans[consumer.Name+"|1.0.0"] = plan(consumer, provider)
			policy := EmptyPolicy()
			policy.Plugins[consumer.Name] = Entry{Source: "registry", Version: "1.0.0", Enabled: &enabled}
			prepared, e := f.prepare(t, root, policy, nil)
			if e != nil {
				t.Fatal(e)
			}
			applyPrepared(t, root, prepared)
			newer := f.entry(t, provider.Name, "2.0.0", `,"publishes":{"iface":{"version":"2.0.0"}}`)
			f.plans[provider.Name] = plan(newer)
			for iteration := 0; iteration < 2; iteration++ {
				installed, e := Discover(root)
				if e != nil {
					t.Fatal(e)
				}
				prepared, e = f.prepare(t, root, policy, installed)
				if e != nil {
					t.Fatalf("repeat %d: %v", iteration, e)
				}
				for _, p := range prepared.Installed {
					if p.Version != "1.0.0" || p.Enabled != enabled {
						prepared.Cleanup()
						t.Fatalf("root closure changed: %+v", p)
					}
					if p.Name == provider.Name && p.SHA256 != provider.SHA256 {
						prepared.Cleanup()
						t.Fatal("implicit provider bytes changed")
					}
				}
				checked := false
				for _, check := range prepared.Checks {
					if check.Name == provider.Name {
						checked = true
						if check.AvailableVersion != "2.0.0" {
							prepared.Cleanup()
							t.Fatal("provider availability lost", check)
						}
					}
				}
				if !checked {
					prepared.Cleanup()
					t.Fatal("implicit provider was not checked")
				}
				for _, change := range prepared.Changes {
					if strings.HasSuffix(change.Path, ".s2sp") {
						prepared.Cleanup()
						t.Fatal("unchanged pinned closure staged replacement", change)
					}
				}
				applyPrepared(t, root, prepared)
				lock, e := ReadLock(root)
				if e != nil {
					t.Fatal(e)
				}
				if len(lock.RegistryRoots) != 1 || lock.RegistryRoots[0].Name != consumer.Name {
					t.Fatalf("implicit provider became durable root: %+v", lock.RegistryRoots)
				}
			}
			if f.metaCount[provider.Name] != 2 {
				t.Fatal("each repeat must check provider metadata")
			}
			f.status = 503
			installed, e := Discover(root)
			if e != nil {
				t.Fatal(e)
			}
			prepared, e = f.prepare(t, root, policy, installed)
			if e != nil {
				t.Fatal("retained parent closure unavailable", e)
			}
			prepared.Cleanup()
			f.status = 0
			policy.Plugins[provider.Name] = Entry{Source: "registry", Version: "2.0.0"}
			prepared, e = f.prepare(t, root, policy, installed)
			if e == nil {
				prepared.Cleanup()
				t.Fatal("explicit conflicting provider root was ignored")
			}
		})
	}
}
