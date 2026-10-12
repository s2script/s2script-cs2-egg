// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/framework"
)

func TestRegistryUnavailableRetainsOnlyVerifiedPolicy(t *testing.T) {
	for _, status := range []int{404, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newRegistryFixture(t)
			root := t.TempDir()
			entry := f.entry(t, "hello", "1.0.0", "")
			f.plans["hello"] = plan(entry)
			policy := EmptyPolicy()
			policy.Plugins["hello"] = Entry{Source: "registry"}
			p, e := f.prepare(t, root, policy, nil)
			if e != nil {
				t.Fatal(e)
			}
			applyPrepared(t, root, p)
			items, e := Discover(root)
			if e != nil {
				t.Fatal(e)
			}
			f.status = status
			p, e = f.prepare(t, root, policy, items)
			if status == 404 {
				if e == nil {
					p.Cleanup()
					t.Fatal("404 used fallback")
				}
				return
			}
			if e != nil {
				t.Fatal("verified retained fallback failed", e)
			}
			defer p.Cleanup()
			if p.Installed[0].Version != "1.0.0" || p.Checks[0].Status != "retained-unavailable" {
				t.Fatal(p)
			}
			policy.Plugins["hello"] = Entry{Source: "registry", Version: "2.0.0"}
			if bad, e := f.prepare(t, root, policy, items); e == nil {
				bad.Cleanup()
				t.Fatal("fallback ignored pin")
			}
			path, _ := Target(items[0].Filename, true)
			os.WriteFile(filepath.Join(root, "game/csgo", path), []byte("modified"), 0600)
			if bad, e := f.prepare(t, root, EmptyPolicy(), items); e == nil {
				bad.Cleanup()
				t.Fatal("fallback ignored modified file")
			}
		})
	}
}
func TestBundleOptionalMembersAndRetainedEvidence(t *testing.T) {
	f := newRegistryFixture(t)
	root := t.TempDir()
	a := f.entry(t, "member", "1.0.0", "")
	b := f.entry(t, "extra", "2.0.0", "")
	f.plans["bundle"] = RegistryPlan{Root: &PlanRoot{Name: "bundle", Version: "3.0.0"}, Install: []PlanEntry{a, b}, Skipped: []Skipped{}, Warnings: []string{}, Errors: []string{}}
	policy := EmptyPolicy()
	disabled := false
	policy.Plugins["bundle"] = Entry{Source: "registry", With: []string{"extra"}, Enabled: &disabled}
	p, e := f.prepare(t, root, policy, nil)
	if e != nil {
		t.Fatal(e)
	}
	applyPrepared(t, root, p)
	items, e := Discover(root)
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range items {
		if item.Enabled {
			t.Fatal("disabled bundle member enabled")
		}
	}
	f.status = 503
	p, e = f.prepare(t, root, policy, items)
	if e != nil {
		t.Fatal("bundle retained fallback", e)
	}
	defer p.Cleanup()
	policy.Plugins["bundle"] = Entry{Source: "registry", With: []string{"unknown"}}
	if bad, e := f.prepare(t, root, policy, items); e == nil {
		bad.Cleanup()
		t.Fatal("bundle fallback ignored changed members")
	}
}
func TestRegistryRejectsMalformedAndCrossOrigin(t *testing.T) {
	for _, kind := range []string{"json", "digest", "redirect", "source", "partial"} {
		t.Run(kind, func(t *testing.T) {
			f := newRegistryFixture(t)
			entry := f.entry(t, "hello", "1.0.0", "")
			f.plans["hello"] = plan(entry)
			root := t.TempDir()
			policy := EmptyPolicy()
			policy.Plugins["hello"] = Entry{Source: "registry"}
			p, e := f.prepare(t, root, policy, nil)
			if e != nil {
				t.Fatal(e)
			}
			applyPrepared(t, root, p)
			items, _ := Discover(root)
			next := f.entry(t, "hello", "1.1.0", "")
			f.plans["hello"] = plan(next)
			other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
			defer other.Close()
			original := f.server.Config.Handler
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "json":
					w.Write([]byte(`{"root":`))
					return
				case "redirect":
					http.Redirect(w, r, other.URL, 302)
					return
				case "partial":
					if strings.HasPrefix(r.URL.Path, "/files/") {
						w.Header().Set("Content-Length", "999999")
						w.Write([]byte("short"))
						return
					}
				}
				original.ServeHTTP(w, r)
			})
			if kind == "digest" {
				next.SHA256 = strings.Repeat("0", 64)
				f.plans["hello"] = plan(next)
			}
			if kind == "source" {
				next.URL = other.URL + "/bad"
				f.plans["hello"] = plan(next)
			}
			p, e = f.prepare(t, root, policy, items)
			var unavailable *UnavailableError
			if e == nil || errors.As(e, &unavailable) || len(p.Changes) != 0 {
				p.Cleanup()
				t.Fatal("unsafe response accepted or eligible for fallback", e)
			}
			got, _ := Discover(root)
			if got[0].Version != "1.0.0" {
				t.Fatal("partial plan touched live file")
			}
		})
	}
}
func TestDependencyContractAndCompatibilityFailures(t *testing.T) {
	for _, kind := range []string{"cycle", "version", "hash", "api", "protocol", "unknown-builtin"} {
		t.Run(kind, func(t *testing.T) {
			f := newRegistryFixture(t)
			extraA := `,"pluginDependencies":{"iface":"^1.0.0"}`
			extraB := `,"publishes":{"iface":{"version":"1.2.0","typesSha256":"abc"}}`
			switch kind {
			case "cycle":
				extraA += `,"publishes":{"other":{"version":"1.0.0"}}`
				extraB += `,"pluginDependencies":{"other":"*"}`
			case "version":
				extraB = `,"publishes":{"iface":{"version":"2.0.0"}}`
			case "hash":
				extraA += `,"compiledAgainst":{"iface":"different"}`
			case "unknown-builtin":
				extraA = `,"pluginDependencies":{"@s2script/zones":"*"}`
			}
			a := f.entry(t, "a", "1.0.0", extraA)
			b := f.entry(t, "b", "3.0.0", extraB)
			if kind == "api" || kind == "protocol" {
				data := f.files[strings.TrimPrefix(a.URL, f.server.URL)]
				m, _ := manifestBytes(data)
				var object map[string]any
				json.Unmarshal(m.Raw, &object)
				if kind == "api" {
					object["apiVersion"] = "1.x"
				} else {
					object["apiVersion"] = "2.x"
					object["interfaceProtocol"] = 2
				}
				a = fixtureManifest(t, f, a, object)
			}
			f.plans["a"] = plan(a, b)
			policy := EmptyPolicy()
			policy.Plugins["a"] = Entry{Source: "registry"}
			p, e := f.prepare(t, t.TempDir(), policy, nil)
			if e == nil {
				p.Cleanup()
				t.Fatal("invalid contract accepted", kind)
			}
		})
	}
}
func fixtureManifest(t *testing.T, f *registryFixture, entry PlanEntry, m map[string]any) PlanEntry {
	t.Helper()
	raw, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	data := zipManifest(t, raw)
	f.files[strings.TrimPrefix(entry.URL, f.server.URL)] = data
	entry.SHA256 = fileSHA256(data)
	return entry
}
func TestLegacyAPI2BuiltinsAndSelectedHost(t *testing.T) {
	f := newRegistryFixture(t)
	a := f.entry(t, "legacy", "1.0.0", "")
	a = fixtureManifest(t, f, a, map[string]any{"id": "legacy", "version": "1.0.0", "apiVersion": "2.x", "pluginDependencies": map[string]string{"@s2script/entity": "^0.2.0", "@s2script/math": "^0.1.0"}})
	f.plans["legacy"] = plan(a)
	policy := EmptyPolicy()
	policy.Plugins["legacy"] = Entry{Source: "registry"}
	p, e := f.prepare(t, t.TempDir(), policy, nil)
	if e != nil {
		t.Fatal(e)
	}
	p.Cleanup()
	p, e = f.registry.Prepare(context.Background(), config.Server{Root: t.TempDir()}, policy, nil, framework.Selection{HostAPIMajor: 1, SupportedAPIMajors: []int{1}}, nil)
	if e == nil {
		p.Cleanup()
		t.Fatal("ignored selected host")
	}
}

func zipManifest(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("manifest.json")
	w.Write(raw)
	w, _ = z.Create("plugin.js")
	w.Write([]byte("// test"))
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

func TestRegistryReviewedOnlyPolicy(t *testing.T) {
	for _, state := range []string{"pending", "approved", "reviewed"} {
		t.Run(state, func(t *testing.T) {
			f := newRegistryFixture(t)
			a := f.entry(t, "a", "1.0.0", "")
			a.ReviewState = state
			f.plans["a"] = plan(a)
			policy := EmptyPolicy()
			policy.Plugins["a"] = Entry{Source: "registry"}
			p, e := f.prepare(t, t.TempDir(), policy, nil)
			if e != nil {
				t.Fatal("default disallowed unreviewed", e)
			}
			p.Cleanup()
			cfg, e := config.LoadServer(t.TempDir(), func(k string) (string, bool) {
				if k == "ALLOW_TOKENLESS" || k == "S2SCRIPT_REVIEWED_ONLY" {
					return "1", true
				}
				return "", false
			})
			if e != nil {
				t.Fatal(e)
			}
			p, e = f.registry.Prepare(context.Background(), cfg, policy, nil, selected(), nil)
			if state == "reviewed" {
				if e != nil {
					t.Fatal(e)
				}
				p.Cleanup()
			} else if e == nil {
				p.Cleanup()
				t.Fatal("reviewed-only accepted", state)
			}
		})
	}
}
func TestLocalAdoptionRequiresExactVerifiedBytes(t *testing.T) {
	f := newRegistryFixture(t)
	root := t.TempDir()
	a := f.entry(t, "a", "1.0.0", "")
	f.install(t, root, a, false)
	os.Remove(filepath.Join(root, LockFilename))
	items, e := Discover(root)
	if e != nil {
		t.Fatal(e)
	}
	f.plans["a"] = plan(a)
	policy := EmptyPolicy()
	policy.Plugins["a"] = Entry{Source: "registry"}
	p, e := f.prepare(t, root, policy, items)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Cleanup()
	if !p.Installed[0].Managed || p.Installed[0].Enabled || p.Installed[0].SHA256 != a.SHA256 {
		t.Fatal("exact artifact not adopted safely")
	}
}
func TestPlanCanonicalFieldsAndRootArtifactConflict(t *testing.T) {
	for _, raw := range []string{`{"root":{"name":"a","version":"1.0.0"},"install":[],"skipped":[],"warnings":[],"errors"
"io"
"log":[],"Errors":[]}`, `{"root":{"Name":"a","version":"1.0.0"},"install":[],"skipped":[],"warnings":[],"errors"
"io"
"log":[]}`} {
		f := newRegistryFixture(t)
		f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(raw)) })
		if _, e := f.registry.Plan(context.Background(), "a", "*", nil); e == nil {
			t.Fatal("accepted case-aliased plan")
		}
	}
}
func TestRegistryReportsPinnedPrereleaseAvailability(t *testing.T) {
	f := newRegistryFixture(t)
	a := f.entry(t, "a", "2.0.0-rc.1", "")
	f.plans["a"] = plan(a)
	policy := EmptyPolicy()
	policy.Plugins["a"] = Entry{Source: "registry", Version: "2.0.0-rc.1"}
	if p, e := f.prepare(t, t.TempDir(), policy, nil); e == nil {
		p.Cleanup()
		t.Fatal("wildcard response silently accepted prerelease contrary to requested range")
	}
}

func TestRedirectPolicyTimeoutIsTerminal(t *testing.T) {
	f := newRegistryFixture(t)
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, f.server.URL+"/next", 302) })
	f.registry.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return context.DeadlineExceeded }
	_, e := f.registry.Meta(context.Background(), "a")
	var unavailable *UnavailableError
	if e == nil || errors.As(e, &unavailable) {
		t.Fatal("redirect policy treated as outage", e)
	}
}
func TestDownloadOutageRetainsCompleteLock(t *testing.T) {
	f := newRegistryFixture(t)
	root := t.TempDir()
	a := f.entry(t, "a", "1.0.0", "")
	f.plans["a"] = plan(a)
	policy := EmptyPolicy()
	policy.Plugins["a"] = Entry{Source: "registry"}
	p, e := f.prepare(t, root, policy, nil)
	if e != nil {
		t.Fatal(e)
	}
	applyPrepared(t, root, p)
	items, _ := Discover(root)
	f.plans["a"] = plan(f.entry(t, "a", "1.1.0", ""))
	original := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files/") {
			w.WriteHeader(503)
			return
		}
		original.ServeHTTP(w, r)
	})
	p, e = f.prepare(t, root, policy, items)
	if e != nil {
		t.Fatal("download outage did not retain", e)
	}
	defer p.Cleanup()
	if p.Installed[0].Version != "1.0.0" || p.Checks[0].Status != "retained-unavailable" {
		t.Fatal(p)
	}
}
func TestPlanQueryCarriesRangeAndOptionalMembers(t *testing.T) {
	f := newRegistryFixture(t)
	entry := f.entry(t, "bundle", "1.2.3", "")
	original := f.server.Config.Handler
	f.plans["bundle"] = plan(entry)
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/plan" && (r.URL.Query().Get("range") != "^1.0.0" || r.URL.Query().Get("with") != "@x/extra") {
			http.Error(w, "wrong query", 400)
			return
		}
		original.ServeHTTP(w, r)
	})
	if _, e := f.registry.Plan(context.Background(), "bundle", "^1.0.0", []string{"@x/extra"}); e != nil {
		t.Fatal(e)
	}
}
func TestManifestRejectsProtocolCaseAlias(t *testing.T) {
	raw := []byte(`{"id":"a","version":"1.0.0","apiVersion":"2.x","interfaceProtocol":2,"InterfaceProtocol":1}`)
	if _, e := ParseManifest(raw); e == nil {
		t.Fatal("case alias bypassed protocol evidence")
	}
}

func TestTLSRejectionCannotUseRetainedFallback(t *testing.T) {
	f := newRegistryFixture(t)
	f.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	f.registry.Client = &http.Client{}
	_, e := f.registry.Meta(context.Background(), "a")
	var unavailable *UnavailableError
	if e == nil || errors.As(e, &unavailable) {
		t.Fatal("TLS rejection became outage", e)
	}
}
func TestCanceledReconcileReturnsNoChanges(t *testing.T) {
	f := newRegistryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, e := f.registry.Prepare(ctx, config.Server{Root: t.TempDir()}, EmptyPolicy(), nil, selected(), nil)
	if !errors.Is(e, context.Canceled) || len(p.Changes) != 0 || p.StageDir != "" {
		t.Fatal(p, e)
	}
}
func TestPlanRejectsMissingReviewStateAndNullFields(t *testing.T) {
	f := newRegistryFixture(t)
	entry := f.entry(t, "a", "1.0.0", "")
	raw, _ := json.Marshal(plan(entry))
	for _, bad := range []string{strings.Replace(string(raw), `"reviewState":"reviewed",`, "", 1), strings.Replace(string(raw), `"reviewState":"reviewed"`, `"reviewState":null`, 1)} {
		f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(bad)) })
		if _, e := f.registry.Plan(context.Background(), "a", "*", nil); e == nil {
			t.Fatal("accepted incomplete PlanEntry")
		}
	}
}
