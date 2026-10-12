// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/s2script/s2script-cs2-egg/internal/catalog"
	"github.com/s2script/s2script-cs2-egg/internal/config"
)

// UnavailableError marks transport failures and HTTP 429/5xx only. Digest,
// source trust, schema, policy and compatibility failures never use this type.
// Cancellation is returned as ctx.Err(), never as an availability failure.
type UnavailableError struct{}

func (*UnavailableError) Error() string { return "official upstream is temporarily unavailable" }

func (r Resolver) Resolve(ctx context.Context, cfg config.Server, pins map[string]string) (Selection, error) {
	if e := ctx.Err(); e != nil {
		return Selection{}, e
	}
	r = r.defaults()
	s, e := r.resolve(ctx, cfg, pins)
	if e == nil {
		return s, nil
	}
	if ctx.Err() != nil {
		return Selection{}, ctx.Err()
	}
	var unavailable *UnavailableError
	if !errors.As(e, &unavailable) {
		return Selection{}, e
	}
	retained, retainedErr := r.Retained(ctx, cfg, pins)
	if retainedErr != nil {
		return Selection{}, fmt.Errorf("upstream unavailable and retained generation cannot satisfy policy: %w", retainedErr)
	}
	return retained, nil
}

// Retained is the only permitted fallback after Prepare returns UnavailableError.
// It verifies installed file facts, source trust, policy and complete cached
// archives. Caller must still Prepare, reconcile bundled plugins and Apply.
func Retained(ctx context.Context, cfg config.Server, pins map[string]string) (Selection, error) {
	return (Resolver{}).Retained(ctx, cfg, pins)
}
func (r Resolver) Retained(ctx context.Context, cfg config.Server, pins map[string]string) (Selection, error) {
	if e := ctx.Err(); e != nil {
		return Selection{}, e
	}
	r = r.defaults()
	state, e := loadState(cfg.Root)
	if e != nil {
		return Selection{}, e
	}
	s := state.Selection
	if s.Channel != cfg.Frameworks.Channel {
		return Selection{}, errors.New("retained generation does not match requested release channel")
	}
	if pin := cfg.Frameworks.S2ScriptVersion; pin != "" && pin != "latest" && normalized(pin) != s.S2Script.Version {
		return Selection{}, errors.New("retained generation does not satisfy S2Script pin")
	}
	if pin := cfg.Frameworks.MetamodVersion; pin != "" && pin != "latest" && normalized(pin) != s.Metamod.Version {
		return Selection{}, errors.New("retained generation does not satisfy Metamod pin")
	}
	known := map[string]bool{}
	for _, p := range catalog.Bundled() {
		known[p.Name] = true
	}
	for name, pin := range pins {
		if !known[name] || !exactVersion.MatchString(pin) || normalized(pin) != s.S2Script.Version {
			return Selection{}, errors.New("retained generation does not satisfy bundled pins")
		}
	}
	if cfg.Frameworks.HUD && s.HUD == nil {
		return Selection{}, errors.New("retained generation has no requested HUD helper")
	}
	if !cfg.Frameworks.HUD {
		s.HUD = nil
	}
	if e = r.validateTrust(s); e != nil {
		return Selection{}, e
	}
	root, e := openDirectory(cfg.Root)
	if e != nil {
		return Selection{}, e
	}
	defer root.Close()
	releases := []Release{s.S2Script, s.Metamod}
	if s.HUD != nil {
		releases = append(releases, *s.HUD)
	}
	for _, release := range releases {
		if e = ctx.Err(); e != nil {
			return Selection{}, e
		}
		b, e := readPinned(root, cachePath(release), downloadLimit)
		if e != nil || sum(b) != release.SHA256 {
			return Selection{}, errors.New("retained archive cache is missing or has invalid digest")
		}
	}
	s.client = r.Client
	s.Stale = true
	s.Warnings = []string{"using verified retained framework generation; official upstream temporarily unavailable"}
	return s, nil
}
func (r Resolver) validateTrust(s Selection) error {
	if e := validateSelection(s); e != nil {
		return e
	}
	releases := []struct {
		release          Release
		repo, tag, asset string
	}{{s.S2Script, "s2script/s2script", "v" + s.S2Script.Version, "s2script-cs2-linux-" + s.S2Script.Version + ".zip"}, {s.Metamod, "alliedmodders/metamod-source", strings.Replace(s.Metamod.Version, "-git", ".", 1), "mmsource-" + s.Metamod.Version + "-linux.tar.gz"}}
	if s.HUD != nil {
		releases = append(releases, struct {
			release          Release
			repo, tag, asset string
		}{*s.HUD, "Source2ZE/MultiAddonManager", "v1.6.2", "MultiAddonManager-v1.6.2-steamrt3.tar.gz"})
	}
	for _, item := range releases {
		u, e := url.Parse(item.release.URL)
		if e != nil || u.Scheme != "https" || u.User != nil {
			return errors.New("retained release has unsafe source URL")
		}
		if r.APIBase == "https://api.github.com" && (u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" || u.Path != "/"+item.repo+"/releases/download/"+item.tag+"/"+item.asset) {
			return errors.New("retained release source is not the official selected asset")
		}
	}
	return nil
}
func cachePath(r Release) string {
	return filepath.ToSlash(filepath.Join("egg/cache/frameworks", r.SHA256+".archive"))
}

// availabilityClient distinguishes a failed request from a redirect policy
// rejection; Client.Do reports both as errors. Only transport failures qualify
// for retained fallback, including when a caller supplies CheckRedirect.
func availabilityClient(base *http.Client) (*http.Client, *availabilityTransport) {
	c := *base
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	tracked := &availabilityTransport{next: next}
	c.Transport = tracked
	policy := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		var err error
		if req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 10 {
			err = errors.New("unsafe upstream redirect")
		} else if policy != nil {
			err = policy(req, via)
		}
		if err != nil {
			tracked.policyRejected = true
		}
		return err
	}
	return &c, tracked
}

func (t *availabilityTransport) temporary() bool { return t.unavailable && !t.policyRejected }

type availabilityTransport struct {
	next           http.RoundTripper
	unavailable    bool
	policyRejected bool
}

func (t *availabilityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, e := t.next.RoundTrip(r)
	if e != nil {
		t.unavailable = true
	} else if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		t.unavailable = true
	}
	// Redirect-body draining errors must not turn a rejected redirect into
	// an outage. Only a successful artifact/metadata response body is consumed.
	if e == nil && resp.StatusCode == http.StatusOK && resp.Body != nil {
		resp.Body = &availabilityBody{ReadCloser: resp.Body, transport: t}
	}
	return resp, e
}

type availabilityBody struct {
	io.ReadCloser
	transport *availabilityTransport
}

func (b *availabilityBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e != nil && e != io.EOF {
		b.transport.unavailable = true
	}
	return n, e
}
