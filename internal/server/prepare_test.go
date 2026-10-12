// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/framework"
	"github.com/s2script/s2script-cs2-egg/internal/plugins"
)

func TestDisabledNeverAppearsActiveDuringPrepare(t *testing.T) {
	root := t.TempDir()
	cfg := config.Server{Root: root}
	cfg.Plugins.File = filepath.Join(root, "s2script-plugins.json")
	cfg.Plugins.Overrides = map[string]bool{"@s2script/basecommands": false}
	selection := framework.Selection{HostAPIMajor: 3, SupportedAPIMajors: []int{2, 3}, Protocol2APIMajor: 3, S2Script: framework.Release{Version: "0.8.1", URL: "https://github.com/s2script/s2script/releases/download/v0.8.1/release.zip", SHA256: strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("manifest.json")
	w.Write([]byte(`{"id":"@s2script/basecommands","version":"0.8.1","apiVersion":"3.x"}`))
	w, _ = z.Create("plugin.js")
	w.Write([]byte("// fixture"))
	z.Close()
	stage := t.TempDir()
	path := filepath.Join(stage, "basecommands.s2sp")
	os.WriteFile(path, b.Bytes(), 0600)
	native := filepath.Join(stage, "native")
	os.WriteFile(native, []byte("native selected generation"), 0600)
	bundle := framework.BundledArtifact{Name: "@s2script/basecommands", Version: "0.8.1", SHA256: fmt.Sprintf("%x", sha256.Sum256(b.Bytes())), StagedPath: path, Filename: "basecommands.s2sp", Manifest: []byte(`{"id":"@s2script/basecommands","version":"0.8.1","apiVersion":"3.x"}`), UpstreamEnabled: true}
	registryServer := httptest.NewTLSServer(http.NotFoundHandler())
	defer registryServer.Close()
	registry := plugins.Registry{BaseURL: registryServer.URL, Client: registryServer.Client()}
	o := prepOptions{steam: func(context.Context, config.Server) (string, error) { return "123", nil }, resolve: func(context.Context, config.Server, map[string]string) (framework.Selection, error) {
		return selection, nil
	}, native: func(context.Context, string, framework.Selection) (framework.Prepared, error) {
		return framework.Prepared{Changes: []artifact.Change{{Path: "game/csgo/native", Source: native}}, Selection: selection, Bundled: []framework.BundledArtifact{bundle}}, nil
	}, plugins: registry.Prepare}
	original := o.plugins
	o.plugins = func(ctx context.Context, c config.Server, p plugins.Policy, i []plugins.Installed, s framework.Selection, b []framework.BundledArtifact) (plugins.Prepared, error) {
		if _, e := os.Stat(filepath.Join(root, "game/csgo/native")); !os.IsNotExist(e) {
			t.Fatal("native committed ahead of plugins")
		}
		prepared, e := original(ctx, c, p, i, s, b)
		if _, stat := os.Stat(filepath.Join(root, "game/csgo/addons/s2script/plugins/basecommands.s2sp")); !os.IsNotExist(stat) {
			t.Fatal("disabled briefly active")
		}
		return prepared, e
	}
	if _, e := prepareWith(context.Background(), cfg, func(string, string, map[string]string) {}, o); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(root, "game/csgo/addons/s2script/plugins/disabled/basecommands.s2sp")); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(root, "game/csgo/addons/s2script/plugins/basecommands.s2sp")); !os.IsNotExist(e) {
		t.Fatal("disabled plugin active")
	}
	facts, e := plugins.Discover(root)
	if e != nil || len(facts) != 1 || facts[0].Enabled {
		t.Fatalf("%+v %v", facts, e)
	}
	// Failure in plugin preparation must preserve the last complete generation.
	os.WriteFile(native, []byte("uncommitted replacement"), 0600)
	o.plugins = func(context.Context, config.Server, plugins.Policy, []plugins.Installed, framework.Selection, []framework.BundledArtifact) (plugins.Prepared, error) {
		return plugins.Prepared{}, errors.New("registry integrity failure")
	}
	if _, e = prepareWith(context.Background(), cfg, func(string, string, map[string]string) {}, o); e == nil {
		t.Fatal("partial failure accepted")
	}
	got, _ := os.ReadFile(filepath.Join(root, "game/csgo/native"))
	if string(got) != "native selected generation" {
		t.Fatal("native partially committed")
	}
}

func TestTypedNativeOutageRetriesSamePinsAndCleansStages(t *testing.T) {
	for _, temporary := range []bool{true, false} {
		t.Run(fmt.Sprint(temporary), func(t *testing.T) {
			root := t.TempDir()
			cfg := config.Server{Root: root}
			policy := plugins.EmptyPolicy()
			policy.Plugins["@s2script/basecommands"] = plugins.Entry{Source: "release", Version: "0.8.1"}
			stage := t.TempDir()
			source := filepath.Join(t.TempDir(), "source")
			os.WriteFile(source, []byte("retained generation"), 0600)
			selection := framework.Selection{S2Script: framework.Release{Version: "0.8.1"}}
			attempts := 0
			retained := 0
			warnings := 0
			o := prepOptions{resolve: func(ctx context.Context, c config.Server, pins map[string]string) (framework.Selection, error) {
				if pins["@s2script/basecommands"] != "0.8.1" {
					t.Fatal("bundled pin absent")
				}
				return selection, nil
			}, retained: func(ctx context.Context, c config.Server, pins map[string]string) (framework.Selection, error) {
				retained++
				if pins["@s2script/basecommands"] != "0.8.1" {
					t.Fatal("fallback lost pin")
				}
				selection.Stale = true
				return selection, nil
			}, native: func(ctx context.Context, root string, s framework.Selection) (framework.Prepared, error) {
				attempts++
				if attempts == 1 {
					if temporary {
						return framework.Prepared{StageDir: stage}, &framework.UnavailableError{}
					}
					return framework.Prepared{StageDir: stage}, errors.New("integrity mismatch")
				}
				return framework.Prepared{Selection: s, Changes: []artifact.Change{{Path: "game/csgo/native", Source: source}}}, nil
			}, plugins: func(context.Context, config.Server, plugins.Policy, []plugins.Installed, framework.Selection, []framework.BundledArtifact) (plugins.Prepared, error) {
				return plugins.Prepared{}, nil
			}}
			_, e := reconcileManaged(context.Background(), cfg, policy, nil, func(kind, reason string, d map[string]string) {
				if kind == "server.warning" {
					warnings++
				}
			}, o)
			if temporary {
				if e != nil || attempts != 2 || retained != 1 || warnings == 0 {
					t.Fatalf("retry %d/%d warnings %d: %v", attempts, retained, warnings, e)
				}
				b, _ := os.ReadFile(filepath.Join(root, "game/csgo/native"))
				if string(b) != "retained generation" {
					t.Fatal("retained generation not committed")
				}
			} else if e == nil || attempts != 1 || retained != 0 {
				t.Fatal("integrity error fell back")
			}
			if _, e := os.Stat(stage); !os.IsNotExist(e) {
				t.Fatal("failed native staging leaked")
			}
		})
	}
}
