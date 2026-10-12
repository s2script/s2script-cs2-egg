// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
)

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func fixtureZIP(t *testing.T, missing string) []byte {
	t.Helper()
	files := map[string][]byte{
		"addons/s2script/VERSION": []byte("0.8.1\n"), "addons/metamod/s2script.vdf": []byte("vdf"),
		"addons/s2script/bin/linuxsteamrt64/s2script.so": []byte("native"), "addons/s2script/bin/linuxsteamrt64/libs2script_core.so": []byte("core"),
		"addons/s2script/configs/admins.json": []byte("template"), "addons/s2script/data/preferences.sqlite": []byte("template"),
		"addons/s2script/gamedata/core/custom/operator.json": []byte("template"), "addons/s2script/LICENSE": []byte("license"),
	}
	refs := map[string]any{}
	for _, key := range []string{"bootstrap", "gamedata", "trustedFunctions"} {
		p := "game-packages/cs2/" + key
		files["addons/s2script/"+p] = []byte(key)
		refs[key] = map[string]string{"path": p, "sha256": digest([]byte(key))}
	}
	refs["id"] = "@s2script/cs2"
	refs["match"] = map[string]string{"engine": "source2", "game": "csgo"}
	refs["gamedataOwner"] = "cs2"
	files["addons/s2script/game-packages.json"], _ = json.Marshal(map[string]any{"schemaVersion": 2, "packages": []any{refs}})
	for _, name := range []string{"clientprefs", "basecommands"} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		w, _ := z.Create("manifest.json")
		fmt.Fprintf(w, `{"id":"@s2script/%s","version":"0.8.1","apiVersion":"3.x","pluginDependencies":{},"publishes":{"@example/interface":{"version":"1.2.0"}}}`, name)
		w, _ = z.Create("plugin.js")
		w.Write([]byte("code"))
		z.Close()
		files["addons/s2script/plugins/_s2script_"+name+".s2sp"] = b.Bytes()
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for p, data := range files {
		if p == missing {
			continue
		}
		w, _ := z.Create(p)
		w.Write(data)
	}
	z.Close()
	return b.Bytes()
}
func selectionServer(t *testing.T, s2 []byte) Selection {
	t.Helper()
	var mm bytes.Buffer
	z := zip.NewWriter(&mm)
	for _, p := range []string{"addons/metamod/bin/linuxsteamrt64/metamod.2.cs2.so", "addons/metamod/bin/linuxsteamrt64/libserver.so", "addons/metamod/LICENSE.txt"} {
		w, _ := z.Create(p)
		w.Write([]byte(p))
	}
	z.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s2.zip":
			w.Write(s2)
		case "/mm.tar.gz":
			w.Write(mm.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return Selection{Channel: "stable", S2Script: Release{Version: "0.8.1", URL: server.URL + "/s2.zip", SHA256: digest(s2), SourceRevision: s2Revision, Integrity: "github-publisher-sha256"}, Metamod: Release{Version: "2.0.0-git1476", URL: server.URL + "/mm.tar.gz", SHA256: digest(mm.Bytes()), SourceRevision: mmRevision, Integrity: "github-publisher-sha256"}, HostAPIMajor: 3, SupportedAPIMajors: []int{2, 3}, Protocol2APIMajor: 3, MetamodPLAPI: 18, client: server.Client()}
}
func gameRoot(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "game/csgo"), 0700)
	os.WriteFile(filepath.Join(r, "game/csgo/gameinfo.gi"), []byte("\"GameInfo\" { \"FileSystem\" { \"SearchPaths\" { Game csgo } } }\n"), 0600)
	return r
}
func TestNativePairRequired(t *testing.T) {
	s := selectionServer(t, fixtureZIP(t, "addons/s2script/bin/linuxsteamrt64/libs2script_core.so"))
	if p, err := Prepare(context.Background(), gameRoot(t), s); err == nil {
		p.Cleanup()
		t.Fatal("incomplete native pair accepted")
	}
}
func TestFrameworkPreservesOperatorState(t *testing.T) {
	root := gameRoot(t)
	operator := []string{"addons/s2script/configs/admins.json", "addons/s2script/data/preferences.sqlite", "addons/s2script/gamedata/core/custom/operator.json", "addons/s2script/plugins/disabled/_s2script_clientprefs.s2sp"}
	for _, p := range operator {
		f := filepath.Join(root, "game/csgo", p)
		os.MkdirAll(filepath.Dir(f), 0700)
		os.WriteFile(f, []byte("operator bytes"), 0600)
	}
	release, err := artifact.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p, err := Prepare(context.Background(), root, selectionServer(t, fixtureZIP(t, "")))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()
	if len(p.Bundled) != 2 {
		t.Fatalf("bundled=%d", len(p.Bundled))
	}
	for _, b := range p.Bundled {
		if b.Version != "0.8.1" || b.SHA256 == "" || !b.UpstreamEnabled || !bytes.Contains(b.Manifest, []byte("publishes")) {
			t.Fatalf("lost plugin metadata: %+v", b)
		}
	}
	for _, c := range p.Changes {
		if strings.HasSuffix(c.Path, ".s2sp") {
			t.Fatal("framework changed plugin enablement")
		}
	}
	if err := artifact.Apply(context.Background(), root, p.Changes); err != nil {
		t.Fatal(err)
	}
	for _, f := range operator {
		b, _ := os.ReadFile(filepath.Join(root, "game/csgo", f))
		if string(b) != "operator bytes" {
			t.Fatalf("overwrote %s", f)
		}
	}
	for _, f := range []string{"addons/s2script/bin/linuxsteamrt64/s2script.so", "addons/s2script/bin/linuxsteamrt64/libs2script_core.so", "addons/s2script/LICENSE"} {
		if _, err := os.Stat(filepath.Join(root, "game/csgo", f)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestGameInfoPatchIdempotent(t *testing.T) {
	in := []byte("// SearchPaths { ignored }\n\"GameInfo\" { \"FileSystem\" { \"SearchPaths\" { Game csgo } } }\n")
	one, err := PatchGameInfo(in)
	if err != nil {
		t.Fatal(err)
	}
	two, err := PatchGameInfo(one)
	if err != nil || !bytes.Equal(one, two) || bytes.Count(one, []byte("csgo/addons/metamod")) != 1 {
		t.Fatalf("not idempotent: %s, %v", two, err)
	}
	if _, err := PatchGameInfo([]byte("SearchPaths {")); err == nil {
		t.Fatal("malformed accepted")
	}
}
func TestHUDMergePreservesSettings(t *testing.T) {
	b, err := MergeHUDConfig([]byte("// user\nmm_extra_addons \"123,456\" // keep\nother \"yes\"\n"))
	if err != nil || !bytes.Contains(b, []byte("123,456,3790153369")) || !bytes.Contains(b, []byte("// keep")) {
		t.Fatalf("merge=%s err=%v", b, err)
	}
	again, err := MergeHUDConfig(b)
	if err != nil || !bytes.Equal(b, again) {
		t.Fatal("HUD not idempotent")
	}
}
func TestPreparationRejectsUnsafeLivePaths(t *testing.T) {
	root := gameRoot(t)
	os.Remove(filepath.Join(root, "game/csgo/gameinfo.gi"))
	os.Symlink("/etc/passwd", filepath.Join(root, "game/csgo/gameinfo.gi"))
	if p, err := Prepare(context.Background(), root, selectionServer(t, fixtureZIP(t, ""))); err == nil {
		p.Cleanup()
		t.Fatal("live symlink accepted")
	}
}
func TestPreparationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := Prepare(ctx, gameRoot(t), selectionServer(t, fixtureZIP(t, ""))); err == nil {
		p.Cleanup()
		t.Fatal("cancellation ignored")
	}
}
func TestBundledPinConstrainsRuntime(t *testing.T) {
	r, requests := fixtureResolver(t, false, false)
	cfg := config.Server{Frameworks: config.Frameworks{S2ScriptUpdate: true, MetamodUpdate: true, S2ScriptVersion: "latest", MetamodVersion: "latest", Channel: "stable"}}
	s, err := r.Resolve(context.Background(), cfg, map[string]string{"@s2script/clientprefs": "0.8.1"})
	if err != nil || s.S2Script.Version != "0.8.1" {
		t.Fatalf("selection=%+v err=%v", s, err)
	}
	if !strings.Contains(strings.Join(*requests, " "), "/s2script/s2script/releases/tags/v0.8.1") {
		t.Fatal("pin not resolved exactly")
	}
	if !s.SupportsAPI("2.x") || !s.SupportsAPI("3.x") || s.SupportsAPI("1.x") {
		t.Fatal("legacy/current API mapping lost")
	}
}
func TestConflictingPinsFailBeforeDownload(t *testing.T) {
	r, requests := fixtureResolver(t, false, false)
	cfg := config.Server{Frameworks: config.Frameworks{S2ScriptUpdate: true, MetamodUpdate: true, S2ScriptVersion: "latest", MetamodVersion: "latest", Channel: "stable"}}
	if _, err := r.Resolve(context.Background(), cfg, map[string]string{"@s2script/clientprefs": "0.8.1", "@s2script/basechat": "0.8.2"}); err == nil {
		t.Fatal("conflicting pins accepted")
	}
	if len(*requests) != 0 {
		t.Fatal("network contacted before conflict check")
	}
}
func TestResolveFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name          string
		missing, fail bool
	}{{"missing_digest", true, false}, {"network_failure", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := fixtureResolver(t, tc.missing, tc.fail)
			if _, err := r.Resolve(context.Background(), config.Server{Frameworks: config.Frameworks{S2ScriptUpdate: true, MetamodUpdate: true, S2ScriptVersion: "0.8.1", MetamodVersion: "latest", Channel: "stable"}}, nil); err == nil {
				t.Fatal("unsafe release accepted")
			}
		})
	}
}
func fixtureResolver(t *testing.T, missing, fail bool) (Resolver, *[]string) {
	t.Helper()
	requests := []string{}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if fail {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/feed" {
			fmt.Fprint(w, "mmsource-2.0.0-git1476-linux.tar.gz")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/core/src/loader.rs") {
			fmt.Fprintln(w, "pub(crate) const HOST_API_VERSION_MAJOR: u32 = 3; matches!(parse_api_major(api_version), Some(m) if m == 2 || m == HOST_API_VERSION_MAJOR)")
			return
		}
		if strings.Contains(r.URL.Path, "/git/ref/tags/") {
			rev := s2Revision
			if strings.Contains(r.URL.Path, "alliedmodders") {
				rev = mmRevision
			}
			fmt.Fprintf(w, `{"object":{"type":"commit","sha":"%s"}}`, rev)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/core/ISmmPluginExt.h") {
			fmt.Fprintln(w, "#define METAMOD_PLAPI_VERSION 18")
			return
		}
		repo, tag, asset, rev := "s2script/s2script", "v0.8.1", "s2script-cs2-linux-0.8.1.zip", s2Revision
		if strings.Contains(r.URL.Path, "alliedmodders") {
			repo, tag, asset, rev = "alliedmodders/metamod-source", "2.0.0.1476", "mmsource-2.0.0-git1476-linux.tar.gz", mmRevision
		}
		hash := "sha256:" + strings.Repeat("a", 64)
		if missing {
			hash = ""
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "target_commitish": rev, "prerelease": repo != "s2script/s2script", "assets": []any{map[string]any{"name": asset, "browser_download_url": server.URL + "/asset", "digest": hash}}})
	}))
	t.Cleanup(server.Close)
	return Resolver{Client: server.Client(), APIBase: server.URL, MetamodFeed: server.URL + "/feed", SourceBase: server.URL}, &requests
}
