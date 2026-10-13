// SPDX-License-Identifier: GPL-3.0-or-later
package addons_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

type s2Transport func(*http.Request) (*http.Response, error)

func (f s2Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransientReleaseErrorsExcludeTLSIntegrityAndAuthorization(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&addons.APIStatusError{StatusCode: 429}, true},
		{&addons.APIStatusError{StatusCode: 503}, true},
		{&addons.APIStatusError{StatusCode: 403}, false},
		{&addons.APIStatusError{StatusCode: 404}, false},
		{&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{context.DeadlineExceeded, true},
		{context.Canceled, false},
		{x509.UnknownAuthorityError{}, false},
		{&net.OpError{Op: "remote error", Net: "tcp", Err: errors.New("tls: handshake failure")}, false},
		{errors.New("publisher SHA256 mismatch"), false},
		{errors.New("redirect leaves trusted origin"), false},
	} {
		if got := addons.IsTransientReleaseError(tc.err); got != tc.want {
			t.Errorf("IsTransientReleaseError(%v)=%v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestS2ScriptFallsBackOnlyToVerifiedMatchingInstallation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		tamper  bool
		version string
		wantOK  bool
	}{
		{"temporary service", 503, false, "", true},
		{"rate limit", 429, false, "", true},
		{"authorization", 403, false, "", false},
		{"missing release", 404, false, "", false},
		{"tampered retained binary", 503, true, "", false},
		{"different exact pin", 503, false, "0.8.2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := s2fixture(t, s2zip(t, s2files(t, "0.8.1")), "0.8.1", "valid", "/latest", false)
			if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
				t.Fatal(err)
			}
			if tc.tamper {
				s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("tampered"))
			}
			e.HTTP = &http.Client{Transport: s2Transport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
			})}
			err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{Version: tc.version})
			if (err == nil) != tc.wantOK {
				t.Fatalf("fallback result=%v, want successful=%v", err, tc.wantOK)
			}
		})
	}
}

func s2zip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, data := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func s2plugin(t *testing.T, version string) []byte {
	return s2zip(t, map[string][]byte{"manifest.json": []byte(`{"id":"@s2script/clientprefs","version":"` + version + `","apiVersion":"3.x"}`), "plugin.js": []byte("plugin " + version)})
}
func s2files(t *testing.T, version string) map[string][]byte {
	return map[string][]byte{
		"addons/s2script/VERSION":                                []byte(version + "\n"),
		"addons/metamod/s2script.vdf":                            []byte("vdf " + version),
		"addons/s2script/bin/linuxsteamrt64/s2script.so":         []byte("native " + version),
		"addons/s2script/bin/linuxsteamrt64/libs2script_core.so": []byte("core " + version),
		"addons/s2script/configs/admins.json":                    []byte("default admins"),
		"addons/s2script/data/preferences.sqlite":                []byte("default preferences"),
		"addons/s2script/gamedata/core/custom/operator.json":     []byte("default custom"),
		"addons/s2script/gamedata/core/signatures.json":          []byte("gamedata " + version),
		"addons/s2script/plugins/_s2script_clientprefs.s2sp":     s2plugin(t, version),
	}
}
func s2fixture(t *testing.T, archive []byte, version, digest, route string, prerelease bool, loader ...string) *addons.Env {
	t.Helper()
	var srv *httptest.Server
	if digest == "valid" {
		hash := sha256.Sum256(archive)
		digest = "sha256:" + hex.EncodeToString(hash[:])
	}
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/s2script/s2script/commits/v"+version {
			json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat("a", 40)})
			return
		}
		if r.URL.Path == "/repos/s2script/s2script/contents/core/src/loader.rs" {
			if r.URL.Query().Get("ref") != strings.Repeat("a", 40) {
				t.Error("source not pinned to immutable revision")
			}
			source := "pub(crate) const HOST_API_VERSION_MAJOR: u32 = 3; matches!(parse_api_major(api_version), Some(m) if m == 2 || m == HOST_API_VERSION_MAJOR)"
			if len(loader) > 0 {
				source = loader[0]
			}
			json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(source))})
			return
		}
		if r.URL.Path == "/asset.zip" {
			w.Write(archive)
			return
		}
		if r.URL.Path != "/repos/s2script/s2script/releases"+route {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		release := map[string]any{"tag_name": "v" + version, "prerelease": prerelease, "assets": []any{map[string]string{"name": "s2script-cs2-linux-" + version + ".zip", "browser_download_url": srv.URL + "/asset.zip", "digest": digest}}}
		if route == "" {
			json.NewEncoder(w).Encode([]any{release})
		} else {
			json.NewEncoder(w).Encode(release)
		}
	}))
	t.Cleanup(srv.Close)
	old := addons.APIBase
	addons.APIBase = srv.URL
	t.Cleanup(func() { addons.APIBase = old })
	root := t.TempDir()
	return &addons.Env{Root: root, Temp: filepath.Join(root, "scratch"), HTTP: srv.Client(), Versions: addons.Versions{Path: filepath.Join(root, "versions.txt")}, Log: logx.New("test", logx.Debug, io.Discard)}
}

func TestS2ScriptRejectsUnknownHostAPIBeforeInstalledWrites(t *testing.T) {
	for _, source := range []string{
		"pub(crate) const HOST_API_VERSION_MAJOR: u32 = 999; m == 2 || m == HOST_API_VERSION_MAJOR",
		"pub(crate) const HOST_API_VERSION_MAJOR: u32 = 3; m == HOST_API_VERSION_MAJOR",
	} {
		t.Run("source", func(t *testing.T) {
			e := s2fixture(t, s2zip(t, s2files(t, "0.8.1")), "0.8.1", "valid", "/latest", false, source)
			s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("existing native"))
			if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
				t.Fatal("unreviewed host API family accepted")
			}
			if string(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so")) != "existing native" {
				t.Fatal("host API rejection changed installed files")
			}
		})
	}
}

func TestS2ScriptPreflightsInstalledPluginMetadataBeforeCopies(t *testing.T) {
	for _, bad := range []string{"package", "lock"} {
		t.Run(bad, func(t *testing.T) {
			e := s2fixture(t, s2zip(t, s2files(t, "0.8.1")), "0.8.1", "valid", "/latest", false)
			s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("existing native"))
			if bad == "package" {
				s2write(t, e.Root, "addons/s2script/plugins/operator.s2sp", []byte("malformed ZIP"))
			} else if err := os.WriteFile(filepath.Join(e.Root, "s2script-plugins.lock.json"), []byte("invalid metadata"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
				t.Fatal("invalid installed metadata accepted")
			}
			if string(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so")) != "existing native" {
				t.Fatal("framework copied before installed plugin validation")
			}
		})
	}
}

func TestS2ScriptRejectsAmbiguousOriginalZIPEntries(t *testing.T) {
	for _, kind := range []string{"symlink", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			var archive bytes.Buffer
			z := zip.NewWriter(&archive)
			for name, data := range s2files(t, "0.8.1") {
				header := &zip.FileHeader{Name: name}
				if kind == "symlink" && name == "addons/s2script/bin/linuxsteamrt64/s2script.so" {
					header.SetMode(os.ModeSymlink | 0777)
				}
				w, err := z.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "duplicate" {
				w, err := z.Create("addons/s2script/VERSION")
				if err != nil {
					t.Fatal(err)
				}
				w.Write([]byte("0.8.1\n"))
			}
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			e := s2fixture(t, archive.Bytes(), "0.8.1", "valid", "/latest", false)
			s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("existing native"))
			if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
				t.Fatal("ambiguous signed ZIP accepted")
			}
			if string(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so")) != "existing native" {
				t.Fatal("invalid ZIP changed installed native files")
			}
		})
	}
}
func s2write(t *testing.T, root, name string, data []byte) {
	t.Helper()
	p := filepath.Join(root, "game/csgo", name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
}
func s2read(t *testing.T, root, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "game/csgo", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func s2managed(t *testing.T, root, name string) {
	t.Helper()
	hash := sha256.Sum256(s2read(t, root, name))
	if err := addons.RegisterS2ScriptReleasePlugins(root, map[string]string{"game/csgo/" + name: hex.EncodeToString(hash[:])}); err != nil {
		t.Fatal(err)
	}
}

func TestS2ScriptBundleOverwriteRequiresVerifiedOwnershipOrPublisherBytes(t *testing.T) {
	for _, kind := range []string{"unowned same identity", "modified managed", "exact publisher adoption"} {
		t.Run(kind, func(t *testing.T) {
			files := s2files(t, "0.8.1")
			e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
			name := "addons/s2script/plugins/disabled/renamed-clientprefs.s2sp"
			data := s2plugin(t, "0.8.0")
			if kind == "exact publisher adoption" {
				data = files["addons/s2script/plugins/_s2script_clientprefs.s2sp"]
			}
			s2write(t, e.Root, name, data)
			if kind == "modified managed" {
				s2managed(t, e.Root, name)
				data = s2zip(t, map[string][]byte{"manifest.json": []byte(`{"id":"@s2script/clientprefs","version":"0.8.0","apiVersion":"3.x"}`), "plugin.js": []byte("operator modification")})
				s2write(t, e.Root, name, data)
			}
			s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("installed native"))
			err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{})
			if kind == "exact publisher adoption" {
				if err != nil {
					t.Fatal(err)
				}
				if !addons.S2ScriptHostVerified(e.Root, "0.8.1") {
					t.Fatal("exact publisher adoption not verified")
				}
			} else {
				if err == nil {
					t.Fatal("unowned or modified bundled package overwritten")
				}
				if string(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so")) != "installed native" {
					t.Fatal("bundle ownership rejection changed native bytes")
				}
			}
			if !bytes.Equal(s2read(t, e.Root, name), data) {
				t.Fatal("existing bundle bytes or disabled placement changed")
			}
			if _, err := os.Stat(filepath.Join(e.Root, "game/csgo/addons/s2script/plugins/_s2script_clientprefs.s2sp")); !os.IsNotExist(err) {
				t.Fatal("existing disabled identity duplicated")
			}
		})
	}
}

func TestS2ScriptRejectsRegistryBundleSourceBeforeCopies(t *testing.T) {
	for _, exact := range []bool{false, true} {
		t.Run("registry", func(t *testing.T) {
			files := s2files(t, "0.8.1")
			e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
			name := "addons/s2script/plugins/disabled/clientprefs.s2sp"
			data := s2plugin(t, "0.8.0")
			if exact {
				data = files["addons/s2script/plugins/_s2script_clientprefs.s2sp"]
			}
			s2write(t, e.Root, name, data)
			s2managed(t, e.Root, name)
			lockPath := filepath.Join(e.Root, "s2script-plugins.lock.json")
			raw, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			var lock map[string]any
			if err = json.Unmarshal(raw, &lock); err != nil {
				t.Fatal(err)
			}
			lock["plugins"].([]any)[0].(map[string]any)["source"] = "registry"
			raw, err = json.Marshal(lock)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(lockPath, raw, 0644); err != nil {
				t.Fatal(err)
			}
			s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("installed native"))
			err = e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{})
			if err == nil || !strings.Contains(err.Error(), "registry") {
				t.Fatalf("registry source conflict not rejected: %v", err)
			}
			if string(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so")) != "installed native" {
				t.Fatal("source conflict changed native bytes")
			}
			if !bytes.Equal(s2read(t, e.Root, name), data) {
				t.Fatal("registry package changed")
			}
		})
	}
}

func TestS2ScriptPreflightsInventoryDestinationBeforeCopies(t *testing.T) {
	e := s2fixture(t, s2zip(t, s2files(t, "0.8.1")), "0.8.1", "valid", "/latest", false)
	s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("installed native"))
	if err := os.MkdirAll(filepath.Join(e.Root, "egg"), 0755); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(t.TempDir(), "private.json")
	if err := os.WriteFile(private, []byte("private"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(e.Root, "egg/installed-list.json")); err != nil {
		t.Fatal(err)
	}
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
		t.Fatal("unsafe inventory destination accepted")
	}
	if string(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so")) != "installed native" {
		t.Fatal("inventory destination rejection happened after native copies")
	}
	raw, err := os.ReadFile(private)
	if err != nil || string(raw) != "private" {
		t.Fatal("private metadata target changed")
	}
}

func TestS2ScriptValidatesFullStagedPluginBeforeCopies(t *testing.T) {
	for _, kind := range []string{"noncanonical manifest", "symlink inner entry"} {
		t.Run(kind, func(t *testing.T) {
			files := s2files(t, "0.8.1")
			if kind == "noncanonical manifest" {
				files["addons/s2script/plugins/_s2script_clientprefs.s2sp"] = s2zip(t, map[string][]byte{"manifest.json": []byte(`{"id":"@s2script/clientprefs","version":"0.8.1","ApiVersion":"3.x"}`), "plugin.js": []byte("code")})
			} else {
				var b bytes.Buffer
				z := zip.NewWriter(&b)
				w, err := z.Create("manifest.json")
				if err != nil {
					t.Fatal(err)
				}
				w.Write([]byte(`{"id":"@s2script/clientprefs","version":"0.8.1","apiVersion":"3.x"}`))
				h := &zip.FileHeader{Name: "plugin.js"}
				h.SetMode(os.ModeSymlink | 0777)
				w, err = z.CreateHeader(h)
				if err != nil {
					t.Fatal(err)
				}
				w.Write([]byte("other.js"))
				if err = z.Close(); err != nil {
					t.Fatal(err)
				}
				files["addons/s2script/plugins/_s2script_clientprefs.s2sp"] = b.Bytes()
			}
			e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
			s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("installed native"))
			if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
				t.Fatal("invalid staged package accepted")
			}
			if string(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so")) != "installed native" {
				t.Fatal("staged package rejection happened after native copies")
			}
		})
	}
}

func TestS2ScriptInstallsReleaseAndPreservesOperatorState(t *testing.T) {
	files := s2files(t, "0.8.1")
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	keep := map[string][]byte{
		"addons/s2script/configs/admins.json":                []byte("operator state"),
		"addons/s2script/data/preferences.sqlite":            []byte("operator state"),
		"addons/s2script/gamedata/core/custom/operator.json": []byte("operator state"),
		"addons/s2script/plugins/my-plugin.s2sp":             s2zip(t, map[string][]byte{"manifest.json": []byte(`{"id":"@example/operator-plugin","version":"1.0.0","apiVersion":"3.x"}`), "plugin.js": []byte("operator code")}),
	}
	for name, data := range keep {
		s2write(t, e.Root, name, data)
	}
	oldPlugin := "addons/s2script/plugins/disabled/renamed-clientprefs.s2sp"
	s2write(t, e.Root, oldPlugin, s2plugin(t, "0.8.0"))
	s2managed(t, e.Root, oldPlugin)
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, data := range keep {
		if !bytes.Equal(s2read(t, e.Root, name), data) {
			t.Errorf("overwrote %s", name)
		}
	}
	for _, name := range []string{"addons/s2script/VERSION", "addons/metamod/s2script.vdf", "addons/s2script/bin/linuxsteamrt64/s2script.so", "addons/s2script/bin/linuxsteamrt64/libs2script_core.so", "addons/s2script/gamedata/core/signatures.json"} {
		if !bytes.Equal(s2read(t, e.Root, name), files[name]) {
			t.Errorf("did not install %s", name)
		}
	}
	if !bytes.Equal(s2read(t, e.Root, oldPlugin), files["addons/s2script/plugins/_s2script_clientprefs.s2sp"]) {
		t.Fatal("disabled bundle not refreshed")
	}
	if _, err := os.Stat(filepath.Join(e.Root, "game/csgo/addons/s2script/plugins/_s2script_clientprefs.s2sp")); !os.IsNotExist(err) {
		t.Fatal("bundled identity duplicated or enabled")
	}
	if e.Versions.Get("S2Script") != "v0.8.1" {
		t.Fatalf("version not recorded: %q", e.Versions.Get("S2Script"))
	}
	data, err := os.ReadFile(filepath.Join(e.Root, "s2script-plugins.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Plugins []struct {
			Name, Source, Verification, Path string
			Managed, Enabled                 bool
		}
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	var recorded bool
	for _, plugin := range lock.Plugins {
		if plugin.Name == "@s2script/clientprefs" {
			recorded = plugin.Managed && plugin.Source == "release" && plugin.Verification == "verified" && !plugin.Enabled && plugin.Path == "game/csgo/"+oldPlugin
		}
	}
	if !recorded {
		t.Fatal("verified release ownership or disabled placement not recorded")
	}
}

func TestS2ScriptRejectsParentSymlinksBeforeAnyInstalledWrite(t *testing.T) {
	files := s2files(t, "0.8.1")
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	s2write(t, e.Root, "cfg/linuxsteamrt64/s2script.so", []byte("private operator file"))
	s2write(t, e.Root, "addons/metamod/s2script.vdf", []byte("existing loader"))
	dir := filepath.Join(e.Root, "game/csgo/addons/s2script")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../cfg", filepath.Join(dir, "bin")); err != nil {
		t.Fatal(err)
	}
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
		t.Fatal("in-tree parent symlink accepted")
	}
	if !bytes.Equal(s2read(t, e.Root, "cfg/linuxsteamrt64/s2script.so"), []byte("private operator file")) {
		t.Fatal("private file overwritten through parent symlink")
	}
	if !bytes.Equal(s2read(t, e.Root, "addons/metamod/s2script.vdf"), []byte("existing loader")) {
		t.Fatal("installed loader changed before target validation")
	}
}
func TestS2ScriptSelectsPrereleaseAndExactVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version, route string
		options              addons.S2ScriptOptions
		pre                  bool
	}{
		{"prerelease", "0.9.0-rc.1", "", addons.S2ScriptOptions{Prerelease: true}, true},
		{"exact downgrade", "0.8.0", "/tags/v0.8.0", addons.S2ScriptOptions{Version: "0.8.0"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := s2files(t, tc.version)
			e := s2fixture(t, s2zip(t, files), tc.version, "valid", tc.route, tc.pre)
			s2write(t, e.Root, "addons/s2script/VERSION", []byte("0.8.1"))
			if err := e.UpdateS2Script(context.Background(), tc.options); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(s2read(t, e.Root, "addons/s2script/VERSION"), files["addons/s2script/VERSION"]) {
				t.Fatal("selected release not installed")
			}
		})
	}
}
func TestS2ScriptRejectsInvalidReleaseBeforeChangingInstalledFiles(t *testing.T) {
	for _, kind := range []string{"digest mismatch", "malformed digest", "missing digest", "missing native", "missing vdf", "version mismatch", "path traversal"} {
		t.Run(kind, func(t *testing.T) {
			files := s2files(t, "0.8.1")
			digest := "valid"
			switch kind {
			case "digest mismatch":
				digest = "sha256:" + strings.Repeat("0", 64)
			case "malformed digest":
				digest = "sha256:bad"
			case "missing digest":
				digest = ""
			case "missing native":
				delete(files, "addons/s2script/bin/linuxsteamrt64/libs2script_core.so")
			case "missing vdf":
				delete(files, "addons/metamod/s2script.vdf")
			case "version mismatch":
				files["addons/s2script/VERSION"] = []byte("0.7.0")
			case "path traversal":
				files["../escape"] = []byte("bad")
			}
			e := s2fixture(t, s2zip(t, files), "0.8.1", digest, "/latest", false)
			s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("existing native"))
			if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
				t.Fatal("invalid release accepted")
			}
			if string(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so")) != "existing native" {
				t.Fatal("changed installed files before validation")
			}
			if e.Versions.Get("S2Script") != "" {
				t.Fatal("recorded invalid release")
			}
		})
	}
}

func TestS2ScriptLatestDoesNotDowngradeAnInstalledRelease(t *testing.T) {
	installed := s2fixture(t, s2zip(t, s2files(t, "0.9.0")), "0.9.0", "valid", "/latest", false)
	if err := installed.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	e := s2fixture(t, s2zip(t, s2files(t, "0.8.1")), "0.8.1", "valid", "/latest", false)
	e.Root, e.Versions = installed.Root, installed.Versions
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s2read(t, e.Root, "addons/s2script/VERSION"), []byte("0.9.0\n")) {
		t.Fatal("latest release downgraded installed framework")
	}
	s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("modified"))
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
		t.Fatal("newer modified installation bypassed verification")
	}
}

func TestS2ScriptOutageCannotBypassResolvedNodePin(t *testing.T) {
	installed := s2fixture(t, s2zip(t, s2files(t, "0.8.2")), "0.8.2", "valid", "/latest", false)
	if err := installed.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	e := s2fixture(t, s2zip(t, s2files(t, "0.8.1")), "0.8.1", "valid", "/latest", false)
	e.Root, e.Versions = installed.Root, installed.Versions
	e.Node = func(*proto.AddonQuery) *proto.AddonRelease {
		return &proto.AddonRelease{Tag: "v0.8.1", Pinned: true, Assets: []proto.AddonAsset{{Name: "s2script-cs2-linux-0.8.1.zip", URL: addons.APIBase + "/asset.zip", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	}
	e.HTTP = &http.Client{Transport: s2Transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
	})}
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
		t.Fatal("outage retained a generation different from node pin")
	}
}

func TestS2ScriptRepairsAnIncompleteCurrentInstallation(t *testing.T) {
	files := s2files(t, "0.8.1")
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	s2write(t, e.Root, "addons/s2script/VERSION", []byte("0.8.1\n"))
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so"), files["addons/s2script/bin/linuxsteamrt64/s2script.so"]) {
		t.Fatal("incomplete current install was skipped")
	}
}

func TestS2ScriptReverifiesCurrentInstallationWithoutHostProof(t *testing.T) {
	files := s2files(t, "0.8.1")
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	for name, data := range files {
		s2write(t, e.Root, name, data)
	}
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	if !addons.S2ScriptHostVerified(e.Root, "0.8.1") {
		t.Fatal("unverified same-version installation was skipped instead of registering host proof")
	}
}

func TestS2ScriptHonorsVerifiedNodePin(t *testing.T) {
	files := s2files(t, "0.8.1")
	archive := s2zip(t, files)
	hash := sha256.Sum256(archive)
	e := s2fixture(t, archive, "0.8.1", "valid", "/latest", false)
	for name, data := range s2files(t, "0.9.0") {
		s2write(t, e.Root, name, data)
	}
	s2managed(t, e.Root, "addons/s2script/plugins/_s2script_clientprefs.s2sp")
	e.Node = func(q *proto.AddonQuery) *proto.AddonRelease {
		if q.Repo == "" {
			return nil
		}
		return &proto.AddonRelease{Tag: "v0.8.1", Pinned: true, Assets: []proto.AddonAsset{{Name: "s2script-cs2-linux-0.8.1.zip", URL: addons.APIBase + "/asset.zip", Digest: "sha256:" + hex.EncodeToString(hash[:])}}}
	}
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s2read(t, e.Root, "addons/s2script/VERSION"), []byte("0.8.1\n")) {
		t.Fatal("verified node pin was not installed")
	}
}

func TestS2ScriptBundledPinSelectsMatchingFrameworkRelease(t *testing.T) {
	files := s2files(t, "0.8.1")
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/tags/v0.8.1", false)
	options := addons.S2ScriptOptions{PluginPins: map[string]string{"@s2script/clientprefs": "0.8.1"}}
	if err := e.UpdateS2Script(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s2read(t, e.Root, "addons/s2script/VERSION"), []byte("0.8.1\n")) {
		t.Fatal("bundled pin did not select matching runtime")
	}
}

func TestS2ScriptRejectsConflictingOrInvalidBundledPinsBeforeNetwork(t *testing.T) {
	for _, options := range []addons.S2ScriptOptions{
		{Version: "0.8.2", PluginPins: map[string]string{"@s2script/clientprefs": "0.8.1"}},
		{PluginPins: map[string]string{"@s2script/clientprefs": "0.8.1", "@s2script/basechat": "0.8.2"}},
		{PluginPins: map[string]string{"@example/private": "0.8.1"}},
		{PluginPins: map[string]string{"@s2script/clientprefs": "latest"}},
	} {
		t.Run("pins", func(t *testing.T) {
			e := s2fixture(t, nil, "0.8.1", "valid", "/unreachable", false)
			s2write(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so", []byte("installed native"))
			if err := e.UpdateS2Script(context.Background(), options); err == nil {
				t.Fatal("invalid or conflicting bundled pin accepted")
			}
			if !bytes.Equal(s2read(t, e.Root, "addons/s2script/bin/linuxsteamrt64/s2script.so"), []byte("installed native")) {
				t.Fatal("pin conflict changed installed native file")
			}
		})
	}
}

func TestS2ScriptDoesNotOverwriteUserPluginAtBundledFilename(t *testing.T) {
	files := s2files(t, "0.8.1")
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	name := "addons/s2script/plugins/_s2script_clientprefs.s2sp"
	user := s2zip(t, map[string][]byte{"manifest.json": []byte(`{"id":"@example/operator-plugin","version":"1.0.0","apiVersion":"3.x"}`), "plugin.js": []byte("operator code")})
	s2write(t, e.Root, name, user)
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
		t.Fatal("silently overwrote a different plugin identity")
	}
	if !bytes.Equal(s2read(t, e.Root, name), user) {
		t.Fatal("operator plugin was changed")
	}
	if _, err := os.Stat(filepath.Join(e.Root, "game/csgo/addons/s2script/bin/linuxsteamrt64/s2script.so")); !os.IsNotExist(err) {
		t.Fatal("native files changed before plugin validation")
	}
}

func TestS2ScriptDefaultsAreCreatedAndBundlePlacementIsPreserved(t *testing.T) {
	files := s2files(t, "0.8.1")
	const active = "addons/s2script/plugins/_s2script_clientprefs.s2sp"
	const disabled = "addons/s2script/plugins/disabled/_s2script_clientprefs.s2sp"
	files[disabled] = files[active]
	delete(files, active)
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	s2write(t, e.Root, active, s2plugin(t, "0.8.0"))
	s2managed(t, e.Root, active)
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"addons/s2script/configs/admins.json", "addons/s2script/data/preferences.sqlite"} {
		if !bytes.Equal(s2read(t, e.Root, name), files[name]) {
			t.Fatalf("fresh default not installed: %s", name)
		}
	}
	if !bytes.Equal(s2read(t, e.Root, active), files[disabled]) {
		t.Fatal("previously enabled plugin not refreshed")
	}
	if _, err := os.Stat(filepath.Join(e.Root, "game/csgo", disabled)); !os.IsNotExist(err) {
		t.Fatal("update reset manually enabled bundled plugin")
	}
}
