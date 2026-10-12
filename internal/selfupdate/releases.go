// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrDisabled = errors.New("node self-update is disabled")

const apiBase = "https://api.github.com/repos/" + releasecontract.Repository

// Release can only be staged after Check verified its publisher metadata.
type Release struct {
	Manifest       releasecontract.Manifest `json:"manifest"`
	ManifestSHA256 string                   `json:"manifest_sha256"`
	Version        string                   `json:"version"`
	SourceRevision string                   `json:"source_revision"`
	AssetURL       string                   `json:"asset_url"`
	SHA256         string                   `json:"sha256"`
	Protocol       releasecontract.Range    `json:"protocol"`
	verified       bool
}
type Client struct {
	HTTP           *http.Client
	Enabled        bool
	Version        string
	AllowDowngrade bool
}
type apiAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	State  string `json:"state"`
}
type apiRelease struct {
	Tag        string     `json:"tag_name"`
	Draft      bool       `json:"draft"`
	Prerelease bool       `json:"prerelease"`
	Assets     []apiAsset `json:"assets"`
}

func Check(ctx context.Context, channel, current string) (Release, error) {
	return (Client{Enabled: true, Version: "latest"}).Check(ctx, channel, current)
}
func (c Client) client() *http.Client {
	h := http.Client{Timeout: 2 * time.Minute}
	if c.HTTP != nil {
		h = *c.HTTP
	}
	h.Timeout = 2 * time.Minute
	h.Jar = nil
	h.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("redirect limit")
		}
		u := r.URL
		if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
			return errors.New("unsafe asset redirect")
		}
		original := via[0].URL
		if original.Host == "api.github.com" {
			return errors.New("release API redirect refused")
		}
		if u.Host != "release-assets.githubusercontent.com" && u.Host != "objects.githubusercontent.com" && !(u.Host == "github.com" && u.Path == original.Path && u.RawQuery == "") {
			return errors.New("foreign asset redirect refused")
		}
		r.Header.Del("Authorization")
		r.Header.Del("Cookie")
		return nil
	}
	return &h
}
func (c Client) get(ctx context.Context, raw string, limit int64) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return nil, errors.New("HTTPS release provenance required")
	}
	if u.Host == "api.github.com" {
		if !strings.HasPrefix(raw, apiBase+"/") {
			return nil, errors.New("foreign release API")
		}
	} else if u.Host != "github.com" || !strings.HasPrefix(u.Path, "/"+releasecontract.Repository+"/releases/download/") || u.RawQuery != "" {
		return nil, errors.New("foreign initial asset URL")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "s2script-cs2-egg-updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, errors.New("release request unavailable or untrusted redirect")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("published release unavailable: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, errors.New("release response exceeds size limit")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("release response incomplete or oversized")
	}
	return b, nil
}
func (c Client) api(ctx context.Context, path string, v any) error {
	b, err := c.get(ctx, apiBase+path, 8<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func (c Client) download(ctx context.Context, d releasecontract.Descriptor, limit int64) ([]byte, error) {
	if d.Size < 1 || d.Size > limit || !storeio.Digest(d.SHA256) {
		return nil, errors.New("publisher digest/size missing or invalid")
	}
	b, err := c.get(ctx, d.URL, d.Size)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	if int64(len(b)) != d.Size || hex.EncodeToString(h[:]) != d.SHA256 {
		return nil, errors.New("publisher checksum/length mismatch")
	}
	return b, nil
}
func selectRelease(all []apiRelease, channel, version string) (apiRelease, error) {
	if channel != "stable" && channel != "prerelease" {
		return apiRelease{}, errors.New("invalid release channel")
	}
	if version == "" {
		version = "latest"
	}
	if version != "latest" {
		if _, err := releasecontract.ParseVersion(version); err != nil {
			return apiRelease{}, err
		}
	}
	var selected apiRelease
	seen := map[string]bool{}
	for _, r := range all {
		v, err := releasecontract.ParseVersion(strings.TrimPrefix(r.Tag, "v"))
		if r.Draft || err != nil || !strings.HasPrefix(r.Tag, "v") {
			continue
		}
		if seen[r.Tag] {
			return selected, errors.New("duplicate release tag in bounded listing")
		}
		seen[r.Tag] = true
		if (len(v.Pre) > 0) != r.Prerelease {
			return selected, errors.New("release tag/prerelease mismatch")
		}
		if channel == "stable" && r.Prerelease || version != "latest" && r.Tag != "v"+version {
			continue
		}
		if selected.Tag == "" || releasecontract.Compare(r.Tag[1:], selected.Tag[1:]) > 0 {
			selected = r
		}
	}
	if selected.Tag == "" {
		return selected, errors.New("no compatible published release")
	}
	return selected, nil
}
func (c Client) Check(ctx context.Context, channel, current string) (Release, error) {
	var out Release
	if !c.Enabled {
		return out, ErrDisabled
	}
	if _, err := releasecontract.ParseVersion(current); err != nil {
		return out, errors.New("release baseline unavailable; install a verified release build before automatic updates")
	}
	version := c.Version
	if version == "" {
		version = "latest"
	}
	var all []apiRelease
	if version != "latest" {
		if _, err := releasecontract.ParseVersion(version); err != nil {
			return out, err
		}
		var r apiRelease
		if err := c.api(ctx, "/releases/tags/v"+version, &r); err != nil {
			return out, err
		}
		all = []apiRelease{r}
	} else {
		complete := false
		for page := 1; page <= 10; page++ {
			var r []apiRelease
			if err := c.api(ctx, fmt.Sprintf("/releases?per_page=100&page=%d", page), &r); err != nil {
				return out, err
			}
			if len(r) > 100 {
				return out, errors.New("release page bound")
			}
			all = append(all, r...)
			if len(r) < 100 {
				complete = true
				break
			}
		}
		if !complete {
			return out, errors.New("complete release pagination exceeds 1000 records")
		}
	}
	selected, err := selectRelease(all, channel, version)
	if err != nil {
		return out, err
	}
	if !c.AllowDowngrade && releasecontract.Compare(selected.Tag[1:], current) < 0 {
		return out, errors.New("release downgrade requires administrator policy")
	}
	if len(selected.Assets) > 64 {
		return out, errors.New("release asset bound")
	}
	assets := map[string]apiAsset{}
	for _, a := range selected.Assets {
		if _, ok := assets[a.Name]; ok {
			return out, errors.New("duplicate publisher asset")
		}
		assets[a.Name] = a
	}
	a, ok := assets[releasecontract.ManifestName]
	if !ok || a.State != "uploaded" || a.URL != releasecontract.AssetURL(selected.Tag, releasecontract.ManifestName) || !strings.HasPrefix(a.Digest, "sha256:") {
		return out, errors.New("published manifest provenance unavailable")
	}
	b, err := c.download(ctx, releasecontract.Descriptor{Name: a.Name, URL: a.URL, SHA256: strings.TrimPrefix(a.Digest, "sha256:"), Size: a.Size}, releasecontract.MaxManifest)
	if err != nil {
		return out, err
	}
	m, err := releasecontract.Decode(b)
	if err != nil {
		return out, err
	}
	if m.Tag != selected.Tag || (m.Channel == "prerelease") != selected.Prerelease {
		return out, errors.New("manifest release identity mismatch")
	}
	for _, d := range m.Descriptors() {
		a, ok := assets[d.Name]
		if !ok || a.State != "uploaded" || a.URL != d.URL || a.Size != d.Size || a.Digest != "sha256:"+d.SHA256 {
			return out, errors.New("manifest differs from publisher asset metadata")
		}
	}
	revision, err := c.tagRevision(ctx, m.Tag)
	if err != nil {
		return out, err
	}
	if revision != m.Source.Revision {
		return out, errors.New("source revision differs from immutable tag")
	}
	node := m.Node()
	out = Release{Manifest: m, ManifestSHA256: storeio.Hash(b), Version: m.Version, SourceRevision: m.Source.Revision, AssetURL: node.URL, SHA256: node.SHA256, Protocol: m.Compatibility.NodeContainer.Protocol, verified: true}
	return out, nil
}
func (c Client) tagRevision(ctx context.Context, tag string) (string, error) {
	type object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	}
	var ref struct {
		Object object `json:"object"`
	}
	if err := c.api(ctx, "/git/ref/tags/"+tag, &ref); err != nil {
		return "", err
	}
	for n := 0; n < 8; n++ {
		o := ref.Object
		if len(o.SHA) != 40 {
			return "", errors.New("invalid tag revision")
		}
		for _, b := range o.SHA {
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
				return "", errors.New("invalid tag revision")
			}
		}
		if o.Type == "commit" {
			return o.SHA, nil
		}
		if o.Type != "tag" {
			return "", errors.New("tag does not resolve to commit")
		}
		if err := c.api(ctx, "/git/tags/"+o.SHA, &ref); err != nil {
			return "", err
		}
	}
	return "", errors.New("annotated tag depth bound")
}
