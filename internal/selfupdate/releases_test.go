// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDisabledUpdateMakesNoRequest(t *testing.T) {
	called := false
	c := Client{HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("called") })}}
	if _, err := c.Check(context.Background(), "stable", "1.0.0"); !errors.Is(err, ErrDisabled) || called {
		t.Fatal(err, called)
	}
}
func TestSelfUpdateRejectsBadHashAndForeignRedirect(t *testing.T) {
	d := releasecontract.Descriptor{Name: "node", URL: releasecontract.AssetURL("v1.0.0", "s2node-v1.0.0-linux-amd64"), SHA256: strings.Repeat("a", 64), Size: 3}
	for _, redirect := range []bool{false, true} {
		c := Client{Enabled: true, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if redirect {
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.example/node"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("bad")), Header: http.Header{}, Request: r}, nil
		})}}
		if _, err := c.download(context.Background(), d, 1024); err == nil {
			t.Fatal("untrusted bytes accepted", redirect)
		}
	}
}
func TestSelectHonorsStablePreviewAndExact(t *testing.T) {
	r := []apiRelease{{Tag: "v1.0.0"}, {Tag: "v2.0.0-alpha.1", Prerelease: true}, {Tag: "v0.5.0"}}
	for _, tc := range []struct{ channel, version, want string }{{"stable", "latest", "v1.0.0"}, {"prerelease", "latest", "v2.0.0-alpha.1"}, {"stable", "0.5.0", "v0.5.0"}} {
		got, err := selectRelease(r, tc.channel, tc.version)
		if err != nil || got.Tag != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	if _, err := selectRelease(r, "stable", "2.0.0-alpha.1"); err == nil {
		t.Fatal("stable selected prerelease")
	}
	if _, err := selectRelease(r, "stable", "9.0.0"); err == nil {
		t.Fatal("missing exact selection fell back")
	}
}

func TestPublisherMetadataAndAnnotatedTagAreRequired(t *testing.T) {
	for _, broken := range []string{"", "manifest-hash", "asset-size", "source-tag", "draft"} {
		t.Run(broken, func(t *testing.T) {
			r := fixtureRelease(t, "1.1.0", "binary")
			manifest, _ := json.Marshal(r.Manifest)
			published := apiRelease{Tag: "v1.1.0", Assets: []apiAsset{{Name: releasecontract.ManifestName, URL: releasecontract.AssetURL("v1.1.0", releasecontract.ManifestName), Digest: "sha256:" + storeio.Hash(manifest), Size: int64(len(manifest)), State: "uploaded"}}}
			for _, d := range r.Manifest.Descriptors() {
				published.Assets = append(published.Assets, apiAsset{Name: d.Name, URL: d.URL, Digest: "sha256:" + d.SHA256, Size: d.Size, State: "uploaded"})
			}
			if broken == "manifest-hash" {
				published.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
			}
			if broken == "asset-size" {
				published.Assets[1].Size++
			}
			if broken == "draft" {
				published.Draft = true
			}
			tag := strings.Repeat("d", 40)
			client := Client{Enabled: true, Version: "1.1.0", HTTP: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				var body []byte
				switch req.URL.String() {
				case apiBase + "/releases/tags/v1.1.0":
					body, _ = json.Marshal(published)
				case published.Assets[0].URL:
					body = manifest
				case apiBase + "/git/ref/tags/v1.1.0":
					body = []byte(`{"object":{"type":"tag","sha":"` + tag + `"}}`)
				case apiBase + "/git/tags/" + tag:
					sha := r.SourceRevision
					if broken == "source-tag" {
						sha = strings.Repeat("b", 40)
					}
					body = []byte(`{"object":{"type":"commit","sha":"` + sha + `"}}`)
				default:
					t.Fatal("unexpected fetch", req.URL)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
			})}}
			got, err := client.Check(context.Background(), "stable", "1.0.0")
			if broken == "" {
				if err != nil || got.Version != "1.1.0" || !got.verified {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("invalid publisher provenance accepted")
			}
		})
	}
}
func TestIncompleteReleasePaginationFailsClosed(t *testing.T) {
	calls := 0
	c := Client{Enabled: true, HTTP: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		body, _ := json.Marshal(make([]apiRelease, 100))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}, Request: req}, nil
	})}}
	if _, err := c.Check(context.Background(), "stable", "1.0.0"); err == nil || calls != 10 {
		t.Fatal("incomplete set accepted", err, calls)
	}
}
