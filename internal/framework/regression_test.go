// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
)

func installedFixture(t *testing.T) (string, Selection) {
	t.Helper()
	root := gameRoot(t)
	s := selectionServer(t, fixtureZIP(t, ""))
	p, e := Prepare(context.Background(), root, s)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Cleanup()
	release, e := artifact.Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = artifact.Apply(context.Background(), root, p.Changes); e != nil {
		t.Fatal(e)
	}
	return root, s
}
func TestDisabledUpdatesRetainVerifiedGeneration(t *testing.T) {
	root, s := installedFixture(t)
	r, requests := fixtureResolver(t, false, true)
	cfg := config.Server{Root: root, Frameworks: config.Frameworks{Channel: "stable", S2ScriptVersion: "latest", MetamodVersion: "latest"}}
	got, e := r.Resolve(context.Background(), cfg, nil)
	if e != nil || got.S2Script.SHA256 != s.S2Script.SHA256 || got.Metamod.Version != "2.0.0-git1476" {
		t.Fatalf("retained=%+v err=%v", got, e)
	}
	if len(*requests) != 0 {
		t.Fatal("disabled updates contacted upstream")
	}
	if _, e = r.Resolve(context.Background(), cfg, map[string]string{"@s2script/clientprefs": "0.8.2"}); e == nil {
		t.Fatal("pin overrode disabled update")
	}
	os.WriteFile(filepath.Join(root, "game/csgo/addons/s2script/bin/linuxsteamrt64/libs2script_core.so"), []byte("corrupt"), 0600)
	if _, e = r.Resolve(context.Background(), cfg, nil); e == nil {
		t.Fatal("corrupt retained native file accepted")
	}
}
func TestDisabledUpdatesRequireState(t *testing.T) {
	r, requests := fixtureResolver(t, false, false)
	if _, e := r.Resolve(context.Background(), config.Server{Root: gameRoot(t), Frameworks: config.Frameworks{Channel: "stable"}}, nil); e == nil {
		t.Fatal("missing retained state accepted")
	}
	if len(*requests) != 0 {
		t.Fatal("network attempted before retained-state preflight")
	}
}
func TestGameInfoRejectsUnbalancedOrConditionalMetamod(t *testing.T) {
	for _, data := range []string{`"GameInfo" { "FileSystem" { "SearchPaths" { Game csgo } } } }`, `"GameInfo" { "FileSystem" { "SearchPaths" { Game csgo/addons/metamod [$WIN32] Game csgo } } }`} {
		if _, e := PatchGameInfo([]byte(data)); e == nil {
			t.Fatalf("unsupported/malformed gameinfo accepted: %s", data)
		}
	}
}
func TestUnknownSourceAPIFailsClosed(t *testing.T) {
	r, _ := fixtureResolver(t, false, false)
	base := r.Client.Transport
	r.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "loader.rs") {
			return &http.Response{StatusCode: 200, Body: ioBody("const HOST_API_VERSION_MAJOR: u32 = 4; m == 2 || m == HOST_API_VERSION_MAJOR"), Header: make(http.Header)}, nil
		}
		return base.RoundTrip(req)
	})
	if _, e := r.Resolve(context.Background(), config.Server{Frameworks: config.Frameworks{Channel: "stable", S2ScriptUpdate: true, MetamodUpdate: true}}, nil); e == nil {
		t.Fatal("unknown API family accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func ioBody(s string) *body                                               { return &body{strings.NewReader(s)} }

type body struct{ *strings.Reader }

func (*body) Close() error { return nil }
func TestUnknownCompatibilitySelectionRejected(t *testing.T) {
	s := selectionServer(t, fixtureZIP(t, ""))
	s.SupportedAPIMajors = append(s.SupportedAPIMajors, 4)
	if p, e := Prepare(context.Background(), gameRoot(t), s); e == nil {
		p.Cleanup()
		t.Fatal("unverified API family enabled")
	}
}
func TestGamePackageDigestMismatch(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "addons/s2script"), 0700)
	os.WriteFile(filepath.Join(root, "addons/s2script/game-packages.json"), []byte(`{"schemaVersion":2,"packages":[{"id":"@s2script/cs2","match":{"engine":"source2","game":"csgo"},"gamedataOwner":"cs2","bootstrap":{"path":"game-packages/cs2/missing","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]}`), 0600)
	dir, e := openDirectory(root)
	if e != nil {
		t.Fatal(e)
	}
	defer dir.Close()
	if e = validateGamePackage(dir); e == nil {
		t.Fatal("missing game-package files accepted")
	}
}
func TestOfficialArchiveLayout(t *testing.T) {
	if os.Getenv("S2SCRIPT_TEST_OFFICIAL_ARCHIVES") != "1" {
		t.Skip("set S2SCRIPT_TEST_OFFICIAL_ARCHIVES=1 for read-only cached upstream archive inspection")
	}
	s2, e := os.ReadFile("/tmp/s2script-release-0.8.1.zip")
	if e != nil {
		t.Fatal(e)
	}
	mm, e := os.ReadFile("/tmp/s2script-upstream-reference/mmsource-2.0.0-git1476-linux.tar.gz")
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/s2" {
			w.Write(s2)
		} else {
			w.Write(mm)
		}
	}))
	defer srv.Close()
	s := Selection{Channel: "stable", S2Script: Release{Version: "0.8.1", URL: srv.URL + "/s2", SHA256: "9ef37564a5546afef0f0fd928b548497fad9d4cee366286fa2c885bc420236d0", SourceRevision: s2Revision, Integrity: "github-publisher-sha256"}, Metamod: Release{Version: "2.0.0-git1476", URL: srv.URL + "/mm", SHA256: "fc8b4dce5b72ee8d3222cecba2c0efab6cd4e46bbfc381aec58f834ed9842f8e", SourceRevision: mmRevision, Integrity: "github-publisher-sha256"}, HostAPIMajor: 3, SupportedAPIMajors: []int{2, 3}, Protocol2APIMajor: 3, MetamodPLAPI: 18, client: srv.Client()}
	p, e := Prepare(context.Background(), gameRoot(t), s)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Cleanup()
	if len(p.Bundled) != 18 {
		t.Fatalf("actual bundled=%d", len(p.Bundled))
	}
	enabled := 0
	for _, b := range p.Bundled {
		if b.UpstreamEnabled {
			enabled++
		}
		if b.Name == "@s2script/clientprefs" && b.Version != "0.8.1" {
			t.Fatal("clientprefs generation mismatch")
		}
	}
	if enabled != 14 {
		t.Fatalf("actual enabled=%d", enabled)
	}
	for _, c := range p.Changes {
		if strings.Contains(c.Path, "dota") || strings.HasSuffix(c.Path, ".s2sp") {
			t.Fatal("unmanaged game/plugin placement")
		}
	}
}
func TestRetainedStateDoesNotRecordOperatorFiles(t *testing.T) {
	root, _ := installedFixture(t)
	b, e := os.ReadFile(filepath.Join(root, StatePath))
	if e != nil {
		t.Fatal(e)
	}
	var s frameworkState
	if e = json.Unmarshal(b, &s); e != nil {
		t.Fatal(e)
	}
	for _, f := range s.Managed {
		if strings.Contains(f.Path, "/configs/") || strings.Contains(f.Path, "/data/") || strings.Contains(f.Path, "/custom/") {
			t.Fatalf("operator file treated as immutable: %s", f.Path)
		}
	}
}
func TestHUDMergeRejectsAmbiguousAssignment(t *testing.T) {
	for _, in := range []string{"mm_extra_addons 123\n", "mm_extra_addons \"1\"\nmm_extra_addons \"2\"\n"} {
		if _, e := MergeHUDConfig([]byte(in)); e == nil {
			t.Fatal("ambiguous HUD assignment accepted")
		}
	}
	b, e := MergeHUDConfig([]byte("other yes"))
	if e != nil || !bytes.Contains(b, []byte("\nmm_extra_addons \"3790153369\"\n")) {
		t.Fatalf("new HUD setting=%s err=%v", b, e)
	}
}
func TestMetamodPinSyntaxAndMissingDigest(t *testing.T) {
	for _, pin := range []string{"2.0.0.1476", "1.12.0-git1227"} {
		r, _ := fixtureResolver(t, false, false)
		_, e := r.Resolve(context.Background(), config.Server{Frameworks: config.Frameworks{S2ScriptUpdate: true, MetamodUpdate: true, Channel: "stable", MetamodVersion: pin}}, nil)
		if e == nil {
			t.Fatalf("unsupported metamod pin %s", pin)
		}
	}
	r, _ := fixtureResolver(t, false, false)
	transport := r.Client.Transport
	r.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, e := transport.RoundTrip(req)
		if e != nil {
			return resp, e
		}
		if strings.Contains(req.URL.Path, "alliedmodders") && strings.Contains(req.URL.Path, "/releases/") {
			resp.Body.Close()
			resp.Body = ioBody(fmt.Sprintf(`{"tag_name":"2.0.0.1476","assets":[{"name":"mmsource-2.0.0-git1476-linux.tar.gz","browser_download_url":"%s/asset"}]}`, r.APIBase))
		}
		return resp, nil
	})
	if _, e := r.Resolve(context.Background(), config.Server{Frameworks: config.Frameworks{S2ScriptUpdate: true, MetamodUpdate: true, Channel: "stable"}}, nil); e == nil {
		t.Fatal("Metamod without publisher digest accepted")
	}
}

func hudFixture(t *testing.T, s Selection) Selection {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for path, data := range map[string]string{"addons/multiaddonmanager/bin/multiaddonmanager.so": "native", "addons/metamod/multiaddonmanager.vdf": "vdf", "cfg/multiaddonmanager/multiaddonmanager.cfg": "mm_extra_addons \"\"\n"} {
		w, _ := z.Create(path)
		w.Write([]byte(data))
	}
	z.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b.Bytes()) }))
	t.Cleanup(server.Close)
	s.HUD = &Release{Version: "1.6.2", URL: server.URL + "/hud", SHA256: digest(b.Bytes()), SourceRevision: mmRevision, Integrity: "github-publisher-sha256"}
	return s
}
func TestHUDRequiresCompatibleInstalledCS2(t *testing.T) {
	s := hudFixture(t, selectionServer(t, fixtureZIP(t, "")))
	root := gameRoot(t)
	if p, e := Prepare(context.Background(), root, s); e == nil {
		p.Cleanup()
		t.Fatal("HUD accepted without compatible installed CS2 build")
	}
	os.MkdirAll(filepath.Join(root, "steamapps"), 0700)
	os.WriteFile(filepath.Join(root, "steamapps/appmanifest_730.acf"), []byte(`"AppState" { "appid" "730" "buildid" "24934553" }`), 0600)
	if p, e := Prepare(context.Background(), root, s); e == nil {
		p.Cleanup()
		t.Fatal("old CS2 build accepted")
	}
}
func TestHUDPreparationMergesExistingSettings(t *testing.T) {
	s := hudFixture(t, selectionServer(t, fixtureZIP(t, "")))
	root := gameRoot(t)
	os.MkdirAll(filepath.Join(root, "steamapps"), 0700)
	os.WriteFile(filepath.Join(root, "steamapps/appmanifest_730.acf"), []byte(`"AppState" { "appid" "730" "buildid" "24934554" }`), 0600)
	path := filepath.Join(root, "game/csgo/cfg/multiaddonmanager/multiaddonmanager.cfg")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("// operator\nmm_extra_addons \"123\"\nother 1\n"), 0600)
	p, e := Prepare(context.Background(), root, s)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Cleanup()
	found := false
	for _, c := range p.Changes {
		if c.Path == "game/csgo/cfg/multiaddonmanager/multiaddonmanager.cfg" {
			found = true
			b, e := os.ReadFile(c.Source)
			if e != nil || string(b) != "// operator\nmm_extra_addons \"123,3790153369\"\nother 1\n" {
				t.Fatalf("HUD merge=%s err=%v", b, e)
			}
		}
	}
	if !found {
		t.Fatal("HUD config change missing")
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), HUDAddon) {
		t.Fatal("Prepare changed live HUD config")
	}
}
func TestSameAPIFamilyNewReleaseResolves(t *testing.T) {
	r, _ := fixtureResolver(t, false, false)
	base := r.Client.Transport
	r.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, e := base.RoundTrip(req)
		if e != nil {
			return resp, e
		}
		if strings.Contains(req.URL.Path, "/s2script/s2script/releases/") {
			resp.Body.Close()
			resp.Body = ioBody(fmt.Sprintf(`{"tag_name":"v0.8.2","target_commitish":"main","assets":[{"name":"s2script-cs2-linux-0.8.2.zip","browser_download_url":"%s/asset","digest":"sha256:%s"}]}`, r.APIBase, strings.Repeat("a", 64)))
		}
		return resp, nil
	})
	s, e := r.Resolve(context.Background(), config.Server{Frameworks: config.Frameworks{Channel: "stable", S2ScriptUpdate: true, MetamodUpdate: true, S2ScriptVersion: "0.8.2"}}, nil)
	if e != nil || s.S2Script.Version != "0.8.2" || s.S2Script.SourceRevision != s2Revision || !s.SupportsAPI("2.x") {
		t.Fatalf("same-family selection=%+v err=%v", s, e)
	}
}
func TestGameInfoPreservesOtherConditions(t *testing.T) {
	in := []byte(`"GameInfo" { ToolsAppId 211 [$WIN32] "FileSystem" { "SearchPaths" { Game csgo } } }`)
	out, e := PatchGameInfo(in)
	if e != nil || !bytes.Contains(out, []byte("211 [$WIN32]")) {
		t.Fatalf("conditional gameinfo=%s err=%v", out, e)
	}
}

func TestSelectionRequiresPublisherIntegrityPolicy(t *testing.T) {
	s := selectionServer(t, fixtureZIP(t, ""))
	s.Metamod.Integrity = ""
	if p, e := Prepare(context.Background(), gameRoot(t), s); e == nil {
		p.Cleanup()
		t.Fatal("selection without publisher integrity provenance accepted")
	}
}

func TestOfflineFallbackRequiresVerifiedStateCacheAndPolicy(t *testing.T) {
	root, s := installedFixture(t)
	r, _ := fixtureResolver(t, false, true)
	cfg := config.Server{Root: root, Frameworks: config.Frameworks{Channel: "stable", S2ScriptUpdate: true, MetamodUpdate: true, S2ScriptVersion: "latest", MetamodVersion: "latest"}}
	got, e := r.Resolve(context.Background(), cfg, nil)
	if e != nil || !got.Stale || len(got.Warnings) == 0 || got.S2Script.SHA256 != s.S2Script.SHA256 {
		t.Fatalf("offline fallback=%+v err=%v", got, e)
	}
	got.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("network must not be used") })}
	p, e := Prepare(context.Background(), root, got)
	if e != nil {
		t.Fatal(e)
	}
	p.Cleanup()
	if _, e := r.Resolve(context.Background(), cfg, map[string]string{"@s2script/clientprefs": "0.8.2"}); e == nil {
		t.Fatal("offline fallback violated bundled pin")
	}
	cfg.Frameworks.Channel = "prerelease"
	if _, e := r.Resolve(context.Background(), cfg, nil); e == nil {
		t.Fatal("offline fallback ignored channel")
	}
	cfg.Frameworks.Channel = "stable"
	cache := filepath.Join(root, "egg/cache/frameworks", s.S2Script.SHA256+".archive")
	if e = os.WriteFile(cache, []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Resolve(context.Background(), cfg, nil); e == nil {
		t.Fatal("corrupt archive cache used for fallback")
	}
}
func TestIntegrityFailureNeverFallsBack(t *testing.T) {
	root, _ := installedFixture(t)
	r, _ := fixtureResolver(t, true, false)
	cfg := config.Server{Root: root, Frameworks: config.Frameworks{Channel: "stable", S2ScriptUpdate: true, MetamodUpdate: true}}
	if _, e := r.Resolve(context.Background(), cfg, nil); e == nil {
		t.Fatal("publisher digest failure silently fell back")
	}
}

func TestPrepareClassifiesOnlyTemporaryDownloadFailures(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          int
		badHash         bool
		wantUnavailable bool
	}{{"temporary_503", 503, false, true}, {"missing_asset", 404, false, false}, {"wrong_digest", 0, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := selectionServer(t, fixtureZIP(t, ""))
			if tc.badHash {
				s.S2Script.SHA256 = strings.Repeat("0", 64)
			}
			if tc.status != 0 {
				s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: tc.status, Body: ioBody("unavailable"), Header: make(http.Header)}, nil
				})}
			}
			p, e := Prepare(context.Background(), gameRoot(t), s)
			if e == nil {
				p.Cleanup()
				t.Fatal("download failure accepted")
			}
			var unavailable *UnavailableError
			if errors.As(e, &unavailable) != tc.wantUnavailable {
				t.Fatalf("wrong outage classification: %v", e)
			}
		})
	}
}
func TestRetainedSourceTrustAndCancellation(t *testing.T) {
	root, _ := installedFixture(t)
	if _, e := Retained(context.Background(), config.Server{Root: root, Frameworks: config.Frameworks{Channel: "stable"}}, nil); e == nil {
		t.Fatal("nonofficial retained URL trusted by public resolver")
	}
	r, _ := fixtureResolver(t, false, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := r.Resolve(ctx, config.Server{Root: root, Frameworks: config.Frameworks{Channel: "stable", S2ScriptUpdate: true, MetamodUpdate: true}}, nil); !errors.Is(e, context.Canceled) {
		t.Fatalf("canceled startup selected fallback: %v", e)
	}
}
func TestDisabledUpdatesStillValidateSourceTrust(t *testing.T) {
	root, _ := installedFixture(t)
	if _, e := Resolve(context.Background(), config.Server{Root: root, Frameworks: config.Frameworks{Channel: "stable"}}, nil); e == nil {
		t.Fatal("disabled updates bypassed source trust")
	}
}

type interruptedBody struct{}

func (*interruptedBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (*interruptedBody) Close() error             { return nil }
func TestInterruptedDownloadIsTemporary(t *testing.T) {
	s := selectionServer(t, fixtureZIP(t, ""))
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: &interruptedBody{}, Header: make(http.Header)}, nil
	})}
	p, e := Prepare(context.Background(), gameRoot(t), s)
	if e == nil {
		p.Cleanup()
		t.Fatal("interrupted download accepted")
	}
	var unavailable *UnavailableError
	if !errors.As(e, &unavailable) {
		t.Fatalf("interrupted transfer did not allow verified fallback: %v", e)
	}
}
func TestOfficialHUDArchiveLayout(t *testing.T) {
	if os.Getenv("S2SCRIPT_TEST_OFFICIAL_ARCHIVES") != "1" {
		t.Skip("set S2SCRIPT_TEST_OFFICIAL_ARCHIVES=1 for cached upstream helper archive")
	}
	path := "/tmp/s2script-upstream-reference/MultiAddonManager-v1.6.2-steamrt3.tar.gz"
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if sum(b) != "21176ec47b45b1c7b480ad17cd765ac2d9ab7f8626f9959b1c49b0f8a74a1412" {
		t.Fatal("official helper archive digest mismatch")
	}
	stage := t.TempDir()
	if e = artifact.Extract(path, stage, expandedLimit); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"addons/multiaddonmanager/bin/multiaddonmanager.so", "addons/metamod/multiaddonmanager.vdf"} {
		if info, e := os.Stat(filepath.Join(stage, p)); e != nil || info.Size() == 0 {
			t.Fatalf("missing helper member %s", p)
		}
	}
	cfg, e := os.ReadFile(filepath.Join(stage, "cfg/multiaddonmanager/multiaddonmanager.cfg"))
	if e != nil {
		t.Fatal(e)
	}
	merged, e := MergeHUDConfig(cfg)
	if e != nil || !bytes.Contains(merged, []byte(HUDAddon)) {
		t.Fatalf("official HUD configuration merge failed: %v", e)
	}
}
