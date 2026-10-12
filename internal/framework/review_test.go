// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/config"
)

func TestRedirectPolicyFailureNeverSelectsRetained(t *testing.T) {
	for _, destination := range []string{"http://untrusted.invalid/metadata", "https://untrusted.invalid/metadata"} {
		t.Run(destination, func(t *testing.T) {
			root, _ := installedFixture(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination, http.StatusFound) }))
			defer server.Close()
			client := server.Client()
			client.CheckRedirect = publicClient().CheckRedirect
			resolver := Resolver{Client: client, APIBase: server.URL, MetamodFeed: server.URL + "/feed", SourceBase: server.URL}
			cfg := config.Server{Root: root, Frameworks: config.Frameworks{Channel: "stable", S2ScriptUpdate: true, MetamodUpdate: true}}
			if _, err := resolver.Retained(context.Background(), cfg, nil); err != nil {
				t.Fatalf("retained fixture is not eligible: %v", err)
			}
			_, err := resolver.defaults().get(context.Background(), server.URL+"/metadata")
			var unavailable *UnavailableError
			if err == nil || errors.As(err, &unavailable) {
				t.Errorf("redirect policy rejection classified as outage: %v", err)
			}
			got, err := resolver.Resolve(context.Background(), cfg, nil)
			if err == nil || got.Stale {
				t.Errorf("redirect rejection selected retained install: stale=%v err=%v", got.Stale, err)
			}
		})
	}
}
func TestArchiveRedirectPolicyFailureIsTerminal(t *testing.T) {
	for _, destination := range []string{"http://untrusted.invalid/archive", "https://untrusted.invalid/archive"} {
		t.Run(destination, func(t *testing.T) {
			root, selection := installedFixture(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination, http.StatusFound) }))
			defer server.Close()
			selection.S2Script.URL = server.URL + "/archive"
			selection.S2Script.SHA256 = strings.Repeat("a", 64)
			selection.client = server.Client()
			selection.client.CheckRedirect = publicClient().CheckRedirect
			prepared, err := Prepare(context.Background(), root, selection)
			if err == nil {
				prepared.Cleanup()
				t.Fatal("unsafe archive redirect accepted")
			}
			var unavailable *UnavailableError
			if errors.As(err, &unavailable) {
				t.Fatalf("archive redirect policy rejection classified as outage: %v", err)
			}
		})
	}
}
func TestGameInfoRejectsConditionedSearchPathAncestors(t *testing.T) {
	for _, input := range []string{
		`"GameInfo" { "FileSystem" { "SearchPaths" { Game csgo } } [$WIN32] }`,
		`"GameInfo" { "FileSystem" { "SearchPaths" { Game csgo } } } [$WIN32]`,
	} {
		if _, err := PatchGameInfo([]byte(input)); err == nil {
			t.Errorf("conditioned search-path ancestor accepted: %s", input)
		}
	}
}
func TestDisabledS2UpdateRejectsRetainedChannelMismatch(t *testing.T) {
	root, _ := installedFixture(t)
	path := filepath.Join(root, StatePath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state frameworkState
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state.Selection.Channel = "prerelease"
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	resolver, requests := fixtureResolver(t, false, false)
	cfg := config.Server{Root: root, Frameworks: config.Frameworks{Channel: "stable", S2ScriptUpdate: false, MetamodUpdate: true}}
	got, err := resolver.Resolve(context.Background(), cfg, nil)
	if err == nil {
		t.Errorf("prerelease retained install relabeled %q", got.Channel)
	}
	if len(*requests) != 0 {
		t.Errorf("channel mismatch contacted release service: %q", *requests)
	}
}

func TestRejectedRedirectDrainErrorRemainsTerminal(t *testing.T) {
	for _, kind := range []string{"metadata", "archive"} {
		t.Run(kind, func(t *testing.T) {
			client := &http.Client{CheckRedirect: publicClient().CheckRedirect, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusFound, ContentLength: 1, Body: &interruptedBody{}, Header: http.Header{"Location": []string{"http://untrusted.invalid/redirect"}}}, nil
			})}
			var err error
			if kind == "metadata" {
				_, err = (Resolver{Client: client}).get(context.Background(), "https://github.com/metadata")
			} else {
				selection := selectionServer(t, fixtureZIP(t, ""))
				selection.client = client
				var p Prepared
				p, err = Prepare(context.Background(), gameRoot(t), selection)
				if err == nil {
					p.Cleanup()
				}
			}
			var unavailable *UnavailableError
			if err == nil || errors.As(err, &unavailable) {
				t.Fatalf("policy rejection with redirect-body error became outage: %v", err)
			}
		})
	}
}

func TestArchiveKeepsCallerRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, destination, wantHosts string
		explicit, success            bool
	}{
		{"nil_policy_cross_origin", "https://untrusted.invalid/payload", "github.com", false, false},
		{"nil_policy_same_origin", "https://github.com/payload", "github.com,github.com", false, true},
		{"explicit_trusted_cdn", "https://release-assets.githubusercontent.com/payload", "github.com,release-assets.githubusercontent.com", true, true},
		{"explicit_untrusted_origin", "https://untrusted.invalid/payload", "github.com", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := fixtureZIP(t, "")
			selection := selectionServer(t, payload)
			next := selection.client.Transport
			selection.S2Script.URL = "https://github.com/archive"
			hosts := []string{}
			selection.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "github.com" && req.URL.Host != "release-assets.githubusercontent.com" && req.URL.Host != "untrusted.invalid" {
					return next.RoundTrip(req)
				}
				hosts = append(hosts, req.URL.Host)
				if req.URL.Path == "/archive" {
					return &http.Response{StatusCode: http.StatusFound, Body: ioBody(""), Header: http.Header{"Location": []string{tc.destination}}}, nil
				}
				if req.URL.Host == "untrusted.invalid" {
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: ioBody("unavailable"), Header: make(http.Header)}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Header: make(http.Header)}, nil
			})}
			if tc.explicit {
				selection.client.CheckRedirect = publicClient().CheckRedirect
			}
			prepared, err := Prepare(context.Background(), gameRoot(t), selection)
			if err == nil {
				defer prepared.Cleanup()
			}
			if tc.success && err != nil {
				t.Errorf("allowed archive redirect failed: %v", err)
			}
			if !tc.success {
				var unavailable *UnavailableError
				if err == nil || errors.As(err, &unavailable) {
					t.Errorf("redirect-policy failure accepted or classified as outage: %v", err)
				}
			}
			if got := strings.Join(hosts, ","); got != tc.wantHosts {
				t.Errorf("requested hosts=%s want %s", got, tc.wantHosts)
			}
		})
	}
}
