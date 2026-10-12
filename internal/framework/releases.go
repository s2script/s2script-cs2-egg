// SPDX-License-Identifier: GPL-3.0-or-later
// Package framework resolves official releases and prepares disposable changes.
// Callers hold artifact.Acquire across Recover, Resolve, Prepare and Apply.
package framework

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/catalog"
	"github.com/s2script/s2script-cs2-egg/internal/config"
)

const s2Revision = "86d1de3ba7366b08f7e8bb0db34ca2a1e84415d7"
const mmRevision = "c47debe129df84c0e54d7393347d2a6afa3b9ee5"
const StatePath = "egg/framework-state.json"

type Release struct {
	Version        string `json:"version"`
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	SourceRevision string `json:"source_revision"`
	// Integrity records publisher digest provenance; locally computed digests alone
	// never satisfy release-download verification.
	Integrity string `json:"integrity"`
}
type Selection struct {
	Stale              bool     `json:"-"`
	Warnings           []string `json:"-"`
	Channel            string   `json:"channel"`
	S2Script           Release  `json:"s2script"`
	Metamod            Release  `json:"metamod"`
	HUD                *Release `json:"hud,omitempty"`
	HostAPIMajor       int      `json:"host_api_major"`
	SupportedAPIMajors []int    `json:"supported_api_majors"`
	Protocol2APIMajor  int      `json:"protocol2_api_major"`
	MetamodPLAPI       int      `json:"metamod_plapi"`
	client             *http.Client
}

// SupportsAPI checks the source-verified family mapping; protocol 2 additionally
// requires Protocol2APIMajor. The plugin reader must enforce that metadata rule.
func (s Selection) SupportsAPI(api string) bool {
	m := regexp.MustCompile(`^[\s^~<>=]*([0-9]+)`).FindStringSubmatch(api)
	if len(m) != 2 {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	for _, n := range s.SupportedAPIMajors {
		if n == major {
			return true
		}
	}
	return false
}

// Resolver permits controlled TLS fixtures and alternate official mirrors in
// embedded uses. Zero values select only the official public upstreams.
type Resolver struct {
	Client                           *http.Client
	APIBase, MetamodFeed, SourceBase string
}

func publicClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("unsafe upstream redirect")
		}
		switch req.URL.Host {
		case "github.com", "api.github.com", "raw.githubusercontent.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "mms.alliedmods.net":
			return nil
		}
		return errors.New("untrusted upstream redirect")
	}}
}
func (r Resolver) defaults() Resolver {
	if r.Client == nil {
		r.Client = publicClient()
	} else {
		c := *r.Client
		if c.Timeout <= 0 {
			c.Timeout = 5 * time.Minute
		}
		r.Client = &c
	}
	if r.APIBase == "" {
		r.APIBase = "https://api.github.com"
	}
	if r.SourceBase == "" {
		r.SourceBase = "https://raw.githubusercontent.com"
	}
	if r.MetamodFeed == "" {
		r.MetamodFeed = "https://mms.alliedmods.net/mmsdrop/2.0/mmsource-latest-linux"
	}
	return r
}
func (r Resolver) get(ctx context.Context, raw string) ([]byte, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil {
		return nil, errors.New("upstream metadata must use HTTPS")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return nil, errors.New("invalid upstream metadata request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client, availability := availabilityClient(r.Client)
	resp, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if availability.temporary() {
			return nil, &UnavailableError{}
		}
		return nil, errors.New("upstream request rejected by client or source policy")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return nil, &UnavailableError{}
	}
	if resp.StatusCode != 200 {
		return nil, errors.New("upstream metadata returned non-200")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &UnavailableError{}
	}
	if len(b) > 4<<20 {
		return nil, errors.New("upstream metadata oversized")
	}
	return b, nil
}

type githubRelease struct {
	Tag        string `json:"tag_name"`
	Target     string `json:"target_commitish"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

var exactVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9][A-Za-z0-9.-]*)?$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func normalized(v string) string   { return strings.TrimPrefix(v, "v") }
func validDigest(hash string) bool { b, e := hex.DecodeString(hash); return e == nil && len(b) == 32 }
func (r Resolver) release(ctx context.Context, repo, pin, channel string, match func(string) bool) (Release, error) {
	endpoint := r.APIBase + "/repos/" + repo + "/releases/"
	if pin == "latest" {
		if channel == "prerelease" {
			endpoint = strings.TrimSuffix(endpoint, "/") + "?per_page=100"
		} else {
			endpoint += "latest"
		}
	} else {
		endpoint += "tags/" + pin
	}
	b, e := r.get(ctx, endpoint)
	if e != nil {
		return Release{}, e
	}
	var rel githubRelease
	if pin == "latest" && channel == "prerelease" {
		var all []githubRelease
		if e = json.Unmarshal(b, &all); e != nil {
			return Release{}, errors.New("invalid releases metadata")
		}
		ok := false
		for _, v := range all {
			if !v.Draft {
				rel = v
				ok = true
				break
			}
		}
		if !ok {
			return Release{}, errors.New("no public release available")
		}
	} else if e = json.Unmarshal(b, &rel); e != nil {
		return Release{}, errors.New("invalid release metadata")
	}
	if rel.Draft || rel.Tag == "" || pin != "latest" && rel.Tag != pin {
		return Release{}, errors.New("release tag mismatch")
	}
	if repo == "s2script/s2script" && (!exactVersion.MatchString(rel.Tag) || channel == "stable" && rel.Prerelease) {
		return Release{}, errors.New("unsupported release version/channel")
	}
	result := Release{Version: normalized(rel.Tag), Integrity: "github-publisher-sha256"}
	count := 0
	for _, a := range rel.Assets {
		if !match(a.Name) {
			continue
		}
		count++
		result.URL = a.URL
		result.SHA256 = strings.TrimPrefix(a.Digest, "sha256:")
		if !strings.HasPrefix(a.Digest, "sha256:") || !validDigest(result.SHA256) {
			return Release{}, errors.New("release asset lacks publisher SHA-256; refusing unverified download")
		}
	}
	if count != 1 {
		return Release{}, errors.New("expected exactly one compatible Linux release asset")
	}
	u, e := url.Parse(result.URL)
	if e != nil || u.Scheme != "https" || u.User != nil {
		return Release{}, errors.New("unsafe release asset URL")
	}
	if r.APIBase == "https://api.github.com" && (u.Host != "github.com" || !strings.HasPrefix(u.Path, "/"+repo+"/releases/download/"+rel.Tag+"/")) {
		return Release{}, errors.New("release asset is not official")
	}
	// target_commitish can be a mutable branch. Resolve the actual selected tag,
	// dereferencing annotated tags, before fetching any compatibility source.
	ref, e := r.get(ctx, r.APIBase+"/repos/"+repo+"/git/ref/tags/"+rel.Tag)
	if e != nil {
		return Release{}, e
	}
	var obj struct {
		Object struct{ Type, SHA string } `json:"object"`
	}
	if json.Unmarshal(ref, &obj) != nil {
		return Release{}, errors.New("invalid release tag reference")
	}
	for n := 0; obj.Object.Type == "tag" && n < 4; n++ {
		if !commitPattern.MatchString(obj.Object.SHA) {
			return Release{}, errors.New("invalid immutable annotated-tag identity")
		}
		tag, e := r.get(ctx, r.APIBase+"/repos/"+repo+"/git/tags/"+obj.Object.SHA)
		if e != nil {
			return Release{}, e
		}
		if json.Unmarshal(tag, &obj) != nil {
			return Release{}, errors.New("invalid annotated tag")
		}
	}
	if obj.Object.Type != "commit" || !commitPattern.MatchString(obj.Object.SHA) {
		return Release{}, errors.New("release tag does not identify an immutable commit")
	}
	result.SourceRevision = obj.Object.SHA
	return result, nil
}
func Resolve(ctx context.Context, cfg config.Server, pins map[string]string) (Selection, error) {
	return (Resolver{}).Resolve(ctx, cfg, pins)
}
func (r Resolver) resolve(ctx context.Context, cfg config.Server, pins map[string]string) (Selection, error) {
	r = r.defaults()
	s := Selection{Channel: cfg.Frameworks.Channel, client: r.Client}
	if s.Channel != "stable" && s.Channel != "prerelease" {
		return s, errors.New("unsupported framework channel")
	}
	s2pin := cfg.Frameworks.S2ScriptVersion
	mmpin := cfg.Frameworks.MetamodVersion
	if s2pin == "" {
		s2pin = "latest"
	}
	if mmpin == "" {
		mmpin = "latest"
	}
	known := map[string]bool{}
	for _, b := range catalog.Bundled() {
		known[b.Name] = true
	}
	for name, pin := range pins {
		if !known[name] || !exactVersion.MatchString(pin) {
			return s, errors.New("bundled pins must name a known plugin and exact release")
		}
		pin = normalized(pin)
		if s2pin != "latest" && normalized(s2pin) != pin {
			return s, errors.New("conflicting bundled/runtime pins")
		}
		s2pin = pin
	}
	var retained frameworkState
	if !cfg.Frameworks.S2ScriptUpdate || !cfg.Frameworks.MetamodUpdate {
		var e error
		retained, e = loadState(cfg.Root)
		if e == nil {
			e = r.validateTrust(retained.Selection)
		}
		if e != nil {
			return s, fmt.Errorf("updates disabled: %w", e)
		}
		if !cfg.Frameworks.S2ScriptUpdate {
			if retained.Selection.Channel != s.Channel {
				return s, errors.New("retained S2Script generation does not match requested release channel")
			}
			if s2pin != "latest" && normalized(s2pin) != retained.Selection.S2Script.Version {
				return s, errors.New("S2Script pin conflicts with retained generation")
			}
			s.S2Script = retained.Selection.S2Script
			s.HostAPIMajor = retained.Selection.HostAPIMajor
			s.SupportedAPIMajors = retained.Selection.SupportedAPIMajors
			s.Protocol2APIMajor = retained.Selection.Protocol2APIMajor
		}
		if !cfg.Frameworks.MetamodUpdate {
			if mmpin != "latest" && normalized(mmpin) != retained.Selection.Metamod.Version {
				return s, errors.New("Metamod pin conflicts with retained generation")
			}
			s.Metamod = retained.Selection.Metamod
			s.MetamodPLAPI = retained.Selection.MetamodPLAPI
		}
	}
	var e error
	if s.S2Script.Version == "" {
		tag := s2pin
		if tag != "latest" {
			if !exactVersion.MatchString(tag) {
				return s, errors.New("invalid S2Script pin")
			}
			tag = "v" + normalized(tag)
		}
		s.S2Script, e = r.release(ctx, "s2script/s2script", tag, s.Channel, func(name string) bool {
			return regexp.MustCompile(`^s2script-cs2-linux-[0-9][A-Za-z0-9.+-]*\.zip$`).MatchString(name)
		})
		if e != nil {
			return s, e
		}
		source, e := r.get(ctx, r.SourceBase+"/s2script/s2script/"+s.S2Script.SourceRevision+"/core/src/loader.rs")
		if e != nil {
			return s, e
		}
		if !regexp.MustCompile(`HOST_API_VERSION_MAJOR\s*:\s*u32\s*=\s*3\s*;`).Match(source) || !regexp.MustCompile(`m\s*==\s*2\s*\|\|\s*m\s*==\s*HOST_API_VERSION_MAJOR`).Match(source) {
			return s, errors.New("unsupported S2Script host API compatibility family")
		}
		s.HostAPIMajor = 3
		s.SupportedAPIMajors = []int{2, 3}
		s.Protocol2APIMajor = 3
	}
	if s.Metamod.Version == "" {
		if mmpin == "latest" {
			feed, e := r.get(ctx, r.MetamodFeed)
			if e != nil {
				return s, e
			}
			m := regexp.MustCompile(`^mmsource-(2\.0\.0-git[0-9]+)-linux\.tar\.gz$`).FindStringSubmatch(strings.TrimSpace(string(feed)))
			if len(m) != 2 {
				return s, errors.New("unsupported Metamod feed artifact")
			}
			mmpin = m[1]
		}
		m := regexp.MustCompile(`^2\.0\.0-git([0-9]+)$`).FindStringSubmatch(normalized(mmpin))
		if len(m) != 2 {
			return s, errors.New("Metamod pin must use 2.0.0-gitBUILD syntax")
		}
		tag := "2.0.0." + m[1]
		asset := "mmsource-2.0.0-git" + m[1] + "-linux.tar.gz"
		s.Metamod, e = r.release(ctx, "alliedmodders/metamod-source", tag, "prerelease", func(name string) bool { return name == asset })
		if e != nil {
			return s, e
		}
		s.Metamod.Version = "2.0.0-git" + m[1]
		header, e := r.get(ctx, r.SourceBase+"/alliedmodders/metamod-source/"+s.Metamod.SourceRevision+"/core/ISmmPluginExt.h")
		if e != nil {
			return s, e
		}
		if !regexp.MustCompile(`(?m)^\s*#define\s+METAMOD_PLAPI_VERSION\s+18\b`).Match(header) {
			return s, errors.New("selected Metamod source does not declare PLAPI 18")
		}
		s.MetamodPLAPI = 18
	}
	if cfg.Frameworks.HUD {
		mam, e := r.release(ctx, "Source2ZE/MultiAddonManager", "v1.6.2", "stable", func(name string) bool { return name == "MultiAddonManager-v1.6.2-steamrt3.tar.gz" })
		if e != nil {
			return s, e
		}
		s.HUD = &mam
	}
	if e = r.validateTrust(s); e != nil {
		return Selection{}, e
	}
	return s, nil
}
