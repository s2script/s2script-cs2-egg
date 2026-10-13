// SPDX-License-Identifier: GPL-3.0-or-later

package addons_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
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
)

type nativeFixture struct {
	repo, key, name, binary, vdf, target, config string
}

var nativeFixtures = []nativeFixture{
	{"Source2ZE/MultiAddonManager", "MultiAddonManager", "MultiAddonManager-v1.6.2-steamrt3.tar.gz", "addons/multiaddonmanager/bin/multiaddonmanager.so", "addons/metamod/multiaddonmanager.vdf", "addons/multiaddonmanager/bin/multiaddonmanager", "cfg/multiaddonmanager/multiaddonmanager.cfg"},
	{"SlynxCZ/GameFixes_mm", "GameFixes", "GameFixes_mm_v1.0.14-linux.tar.gz", "addons/game_fixes/bin/linuxsteamrt64/game_fixes.so", "addons/metamod/game_fixes.vdf", "addons/game_fixes/bin/linuxsteamrt64/game_fixes", "addons/game_fixes/game_fixes.ini"},
}

func nativeTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// GameFixes publishes this root-directory entry (tar -C artifacts/linux .).
	if err := tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for path, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "./" + path, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func nativeFiles(f nativeFixture) map[string]string {
	return map[string]string{f.binary: "new native binary", f.vdf: "\"Metamod Plugin\"\n{\n\"alias\" \"test\"\n\"file\" \"" + f.target + "\"\n}\n", f.config: "publisher defaults"}
}

func nativeOptions(f nativeFixture, version string) addons.NativeOptions {
	return addons.NativeOptions{MultiAddonManager: f.key == "MultiAddonManager", GameFixes: f.key == "GameFixes", MultiAddonManagerVersion: version, GameFixesVersion: version}
}

func nativeEnv(t *testing.T) *addons.Env {
	t.Helper()
	root := t.TempDir()
	return &addons.Env{Root: root, Temp: t.TempDir(), Versions: addons.Versions{Path: filepath.Join(root, "versions.txt")}, Log: logx.New("TEST", logx.Error, io.Discard)}
}

func nativeWrite(t *testing.T, root, path, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nativeRead(t *testing.T, root, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func nativeServer(t *testing.T, e *addons.Env, f nativeFixture, archive []byte, digest, wantEndpoint, tag string) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			_, _ = w.Write(archive)
			return
		}
		if r.URL.Path != "/repos/"+f.repo+"/releases/"+wantEndpoint {
			t.Errorf("unexpected release request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		assets := []map[string]string{
			{"name": strings.Replace(f.name, "steamrt3", "steamrt4", 1) + ".windows.zip", "browser_download_url": srv.URL + "/wrong-platform"},
			{"name": f.name, "browser_download_url": srv.URL + "/asset", "digest": digest},
		}
		if f.key == "MultiAddonManager" {
			assets = append([]map[string]string{{"name": strings.Replace(f.name, "steamrt3", "steamrt4", 1), "browser_download_url": srv.URL + "/wrong-runtime"}}, assets...)
		}
		rel := map[string]any{"tag_name": tag, "prerelease": e.Prerelease, "assets": assets}
		if wantEndpoint == "" {
			_ = json.NewEncoder(w).Encode([]any{rel})
		} else {
			_ = json.NewEncoder(w).Encode(rel)
		}
	}))
	t.Cleanup(srv.Close)
	old := addons.APIBase
	addons.APIBase = srv.URL
	t.Cleanup(func() { addons.APIBase = old })
	e.HTTP = srv.Client()
}

func nativeDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestNativeInstallPreservesPrivateConfigAndData(t *testing.T) {
	for _, f := range nativeFixtures {
		t.Run(f.key, func(t *testing.T) {
			e := nativeEnv(t)
			csgo := filepath.Join(e.Root, "game", "csgo")
			nativeWrite(t, csgo, f.config, "private settings")
			nativeWrite(t, csgo, "addons/other/private.txt", "other plugin")
			dataPath := strings.Split(f.binary, "/bin/")[0] + "/data/private.db"
			nativeWrite(t, csgo, dataPath, "private data")
			files := nativeFiles(f)
			files[dataPath] = "publisher data"
			archive := nativeTar(t, files)
			nativeServer(t, e, f, archive, nativeDigest(archive), "latest", "v2.0.0")
			if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err != nil {
				t.Fatal(err)
			}
			if got := nativeRead(t, csgo, f.binary); got != "new native binary" {
				t.Fatalf("binary = %q", got)
			}
			if got := nativeRead(t, csgo, f.config); got != "private settings" {
				t.Fatalf("config overwritten: %q", got)
			}
			if got := nativeRead(t, csgo, dataPath); got != "private data" {
				t.Fatalf("data overwritten: %q", got)
			}
			if got := nativeRead(t, csgo, "addons/other/private.txt"); got != "other plugin" {
				t.Fatal("other plugin changed")
			}
			if got := nativeRead(t, csgo, f.vdf); !strings.Contains(got, f.target) {
				t.Fatalf("missing publisher VDF: %q", got)
			}
			if got := e.Versions.Get(f.key); got != "v2.0.0" {
				t.Fatalf("version = %q", got)
			}
		})
	}
}

func TestNativeFreshInstallIncludesDefaultConfig(t *testing.T) {
	for _, f := range nativeFixtures {
		t.Run(f.key, func(t *testing.T) {
			e := nativeEnv(t)
			archive := nativeTar(t, nativeFiles(f))
			nativeServer(t, e, f, archive, "", "latest", "v2.0.0")
			if err := e.UpdateNative(context.Background(), nativeOptions(f, "")); err != nil {
				t.Fatal(err)
			}
			if got := nativeRead(t, filepath.Join(e.Root, "game", "csgo"), f.config); got != "publisher defaults" {
				t.Fatalf("config = %q", got)
			}
		})
	}
}

func TestNativeExactVersionAllowsDowngrade(t *testing.T) {
	f := nativeFixtures[0]
	e := nativeEnv(t)
	if err := e.Versions.Set(f.key, "v9.0.0"); err != nil {
		t.Fatal(err)
	}
	archive := nativeTar(t, nativeFiles(f))
	nativeServer(t, e, f, archive, nativeDigest(archive), "tags/v1.6.2", "v1.6.2")
	if err := e.UpdateNative(context.Background(), nativeOptions(f, "v1.6.2")); err != nil {
		t.Fatal(err)
	}
	if got := e.Versions.Get(f.key); got != "v1.6.2" {
		t.Fatalf("version = %q", got)
	}
}

func TestNativePrereleaseUsesReleaseList(t *testing.T) {
	f := nativeFixtures[1]
	e := nativeEnv(t)
	e.Prerelease = true
	archive := nativeTar(t, nativeFiles(f))
	// FetchReleases requests /releases without a trailing slash.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/SlynxCZ/GameFixes_mm/releases":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v3.0.0-beta", "prerelease": true, "assets": []map[string]string{{"name": f.name, "browser_download_url": srv.URL + "/asset"}}}})
		case "/asset":
			_, _ = w.Write(archive)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := addons.APIBase
	addons.APIBase = srv.URL
	defer func() { addons.APIBase = old }()
	e.HTTP = srv.Client()
	if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err != nil {
		t.Fatal(err)
	}
	if got := e.Versions.Get(f.key); got != "v3.0.0-beta" {
		t.Fatalf("version = %q", got)
	}
}

func TestNativeLatestDoesNotDowngrade(t *testing.T) {
	f := nativeFixtures[0]
	e := nativeEnv(t)
	csgo := filepath.Join(e.Root, "game", "csgo")
	nativeWrite(t, csgo, f.binary, "installed newer binary")
	nativeWrite(t, csgo, f.vdf, nativeFiles(f)[f.vdf])
	if err := e.Versions.Set(f.key, "v9.0.0"); err != nil {
		t.Fatal(err)
	}
	nativeServer(t, e, f, []byte("invalid archive"), "sha256:invalid", "latest", "v1.6.2")
	if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err != nil {
		t.Fatal(err)
	}
	if got := nativeRead(t, csgo, f.binary); got != "installed newer binary" {
		t.Fatalf("binary changed: %q", got)
	}
	if got := e.Versions.Get(f.key); got != "v9.0.0" {
		t.Fatalf("version changed: %q", got)
	}
}

func TestNativeRejectsExactTagMismatch(t *testing.T) {
	f := nativeFixtures[0]
	e := nativeEnv(t)
	archive := nativeTar(t, nativeFiles(f))
	nativeServer(t, e, f, archive, nativeDigest(archive), "tags/v1.6.2", "v2.0.0")
	if err := e.UpdateNative(context.Background(), nativeOptions(f, "v1.6.2")); err == nil {
		t.Fatal("wrong release tag accepted")
	}
	if got := e.Versions.Get(f.key); got != "" {
		t.Fatalf("version recorded: %q", got)
	}
}

func TestNativeRejectsBadPayloadBeforeChangingInstall(t *testing.T) {
	for _, issue := range []string{"digest", "archive", "gzip-checksum", "layout", "vdf", "unexpected-file", "traversal", "malformed-digest"} {
		t.Run(issue, func(t *testing.T) {
			f := nativeFixtures[1]
			e := nativeEnv(t)
			csgo := filepath.Join(e.Root, "game", "csgo")
			nativeWrite(t, csgo, f.binary, "installed binary")
			nativeWrite(t, csgo, f.config, "private settings")
			if err := e.Versions.Set(f.key, "v1.0.0"); err != nil {
				t.Fatal(err)
			}
			files := nativeFiles(f)
			switch issue {
			case "layout":
				delete(files, f.binary)
			case "vdf":
				files[f.vdf] = "\"Metamod Plugin\" { \"file\" \"addons/other/bin/other\" }"
			case "unexpected-file":
				files["addons/other/bin/other.so"] = "unrelated binary"
			case "traversal":
				files["../outside"] = "escape"
			}
			archive := nativeTar(t, files)
			if issue == "archive" {
				archive = []byte("not an archive")
			}
			if issue == "gzip-checksum" {
				archive[len(archive)-8] ^= 0xff
			}
			digest := nativeDigest(archive)
			if issue == "digest" {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			if issue == "malformed-digest" {
				digest = "sha256:invalid"
			}
			nativeServer(t, e, f, archive, digest, "latest", "v2.0.0")
			if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err == nil {
				t.Fatal("invalid payload accepted")
			}
			if got := nativeRead(t, csgo, f.binary); got != "installed binary" {
				t.Fatalf("binary modified: %q", got)
			}
			if got := nativeRead(t, csgo, f.config); got != "private settings" {
				t.Fatalf("config modified: %q", got)
			}
			if got := e.Versions.Get(f.key); got != "v1.0.0" {
				t.Fatalf("version modified: %q", got)
			}
		})
	}
}

func TestNativeRejectsInstalledDirectorySymlinksBeforeWriting(t *testing.T) {
	f := nativeFixtures[1]
	e := nativeEnv(t)
	csgo := filepath.Join(e.Root, "game", "csgo")
	nativeWrite(t, csgo, "cfg/private/linuxsteamrt64/game_fixes.so", "private file")
	bin := filepath.Join(csgo, "addons", "game_fixes", "bin")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../cfg/private", bin); err != nil {
		t.Fatal(err)
	}
	archive := nativeTar(t, nativeFiles(f))
	nativeServer(t, e, f, archive, nativeDigest(archive), "latest", "v2.0.0")
	if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err == nil {
		t.Fatal("directory symlink accepted")
	}
	if got := nativeRead(t, csgo, "cfg/private/linuxsteamrt64/game_fixes.so"); got != "private file" {
		t.Fatalf("private file overwritten: %q", got)
	}
	if got := e.Versions.Get(f.key); got != "" {
		t.Fatalf("version recorded after unsafe install: %q", got)
	}
}

func TestNativeRejectsSymlinkedGameRoot(t *testing.T) {
	for _, parent := range []string{"game", "game/csgo"} {
		t.Run(parent, func(t *testing.T) {
			f := nativeFixtures[1]
			e := nativeEnv(t)
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(e.Root, parent)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(e.Root, parent)); err != nil {
				t.Fatal(err)
			}
			archive := nativeTar(t, nativeFiles(f))
			nativeServer(t, e, f, archive, nativeDigest(archive), "latest", "v2.0.0")
			if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err == nil {
				t.Fatal("symlinked game root accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("external tree changed: %v %v", entries, err)
			}
		})
	}
}

func TestNativeRepairsSameVersionMissingBinaryOrVDF(t *testing.T) {
	for _, f := range nativeFixtures {
		for _, missing := range []string{f.binary, f.vdf} {
			t.Run(f.key+"/"+missing, func(t *testing.T) {
				e := nativeEnv(t)
				archive := nativeTar(t, nativeFiles(f))
				nativeServer(t, e, f, archive, nativeDigest(archive), "latest", "v2.0.0")
				if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err != nil {
					t.Fatal(err)
				}
				csgo := filepath.Join(e.Root, "game", "csgo")
				if err := os.Remove(filepath.Join(csgo, filepath.FromSlash(missing))); err != nil {
					t.Fatal(err)
				}
				if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err != nil {
					t.Fatal(err)
				}
				if got := nativeRead(t, csgo, missing); got != nativeFiles(f)[missing] {
					t.Fatalf("missing file was not repaired: %q", got)
				}
			})
		}
	}
}

type nativeTransport func(*http.Request) (*http.Response, error)

func (f nativeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func nativeFailRelease(t *testing.T, e *addons.Env, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == 200 {
			_, _ = w.Write([]byte("invalid JSON"))
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	old := addons.APIBase
	addons.APIBase = srv.URL
	t.Cleanup(func() { addons.APIBase = old })
	e.HTTP = srv.Client()
}

func TestNativeRetainsOnlyVerifiedInstallationOnTransientReleaseFailures(t *testing.T) {
	for _, issue := range []string{"429", "503", "403", "404", "invalid-json", "network", "tls", "origin", "tampered-binary", "tampered-vdf", "missing-binary", "missing-vdf", "exact-same", "exact-other"} {
		t.Run(issue, func(t *testing.T) {
			f := nativeFixtures[0]
			e := nativeEnv(t)
			archive := nativeTar(t, nativeFiles(f))
			nativeServer(t, e, f, archive, nativeDigest(archive), "latest", "v2.0.0")
			if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err != nil {
				t.Fatal(err)
			}
			csgo := filepath.Join(e.Root, "game", "csgo")
			status := 503
			switch issue {
			case "429":
				status = 429
			case "403":
				status = 403
			case "404":
				status = 404
			case "invalid-json":
				status = 200
			}
			nativeFailRelease(t, e, status)
			switch issue {
			case "network":
				e.HTTP = &http.Client{Transport: nativeTransport(func(*http.Request) (*http.Response, error) {
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
				})}
			case "tls":
				e.HTTP = &http.Client{Transport: nativeTransport(func(*http.Request) (*http.Response, error) {
					return nil, &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}
				})}
			case "origin":
				e.HTTP = &http.Client{Transport: nativeTransport(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("release origin is not authorized")
				})}
			case "tampered-binary":
				nativeWrite(t, csgo, f.binary, "tampered")
			case "tampered-vdf":
				nativeWrite(t, csgo, f.vdf, "tampered")
			case "missing-binary":
				if err := os.Remove(filepath.Join(csgo, f.binary)); err != nil {
					t.Fatal(err)
				}
			case "missing-vdf":
				if err := os.Remove(filepath.Join(csgo, f.vdf)); err != nil {
					t.Fatal(err)
				}
			}
			want := "latest"
			if issue == "exact-same" {
				want = "v2.0.0"
			}
			if issue == "exact-other" {
				want = "v1.6.2"
			}
			err := e.UpdateNative(context.Background(), nativeOptions(f, want))
			retain := issue == "429" || issue == "503" || issue == "network" || issue == "exact-same"
			if retain && err != nil {
				t.Fatalf("verified installed addon rejected during transient outage: %v", err)
			}
			if !retain && err == nil {
				t.Fatal("unverified installation or permanent failure was retained")
			}
			if got := e.Versions.Get(f.key); got != "v2.0.0" {
				t.Fatalf("version changed: %q", got)
			}
		})
	}
}

func TestNativeOutageDoesNotTrustVersionRecordAlone(t *testing.T) {
	f := nativeFixtures[0]
	e := nativeEnv(t)
	csgo := filepath.Join(e.Root, "game", "csgo")
	for name, body := range nativeFiles(f) {
		nativeWrite(t, csgo, name, body)
	}
	if err := e.Versions.Set(f.key, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	nativeFailRelease(t, e, 503)
	if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err == nil {
		t.Fatal("installation without trusted hash evidence retained")
	}
}

func TestNativeIntegrityFailureDoesNotRetainVerifiedInstall(t *testing.T) {
	f := nativeFixtures[1]
	e := nativeEnv(t)
	archive := nativeTar(t, nativeFiles(f))
	nativeServer(t, e, f, archive, nativeDigest(archive), "latest", "v2.0.0")
	if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err != nil {
		t.Fatal(err)
	}
	nativeServer(t, e, f, archive, "sha256:"+strings.Repeat("0", 64), "latest", "v3.0.0")
	if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err == nil {
		t.Fatal("publisher integrity failure was suppressed using the installed addon")
	}
	if got := e.Versions.Get(f.key); got != "v2.0.0" {
		t.Fatalf("version changed: %q", got)
	}
	if got := nativeRead(t, filepath.Join(e.Root, "game", "csgo"), f.binary); got != "new native binary" {
		t.Fatalf("installed binary changed: %q", got)
	}
}

func TestNativeAssetOutageRetainsVerifiedInstallUnlessPinChanges(t *testing.T) {
	for _, want := range []string{"latest", "v3.0.0"} {
		t.Run(want, func(t *testing.T) {
			f := nativeFixtures[1]
			e := nativeEnv(t)
			archive := nativeTar(t, nativeFiles(f))
			nativeServer(t, e, f, archive, nativeDigest(archive), "latest", "v2.0.0")
			if err := e.UpdateNative(context.Background(), nativeOptions(f, "latest")); err != nil {
				t.Fatal(err)
			}
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/asset" {
					w.WriteHeader(503)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v3.0.0", "assets": []map[string]string{{"name": f.name, "browser_download_url": srv.URL + "/asset", "digest": nativeDigest(archive)}}})
			}))
			defer srv.Close()
			old := addons.APIBase
			addons.APIBase = srv.URL
			defer func() { addons.APIBase = old }()
			e.HTTP = srv.Client()
			err := e.UpdateNative(context.Background(), nativeOptions(f, want))
			if want == "latest" && err != nil {
				t.Fatal(err)
			}
			if want != "latest" && err == nil {
				t.Fatal("different exact pin retained during download outage")
			}
			if got := e.Versions.Get(f.key); got != "v2.0.0" {
				t.Fatalf("version changed: %q", got)
			}
		})
	}
}
