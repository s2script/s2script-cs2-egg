// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
)

type Registry struct {
	BaseURL string
	Client  *http.Client
}
type PlanRoot struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type PlanEntry struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	ReviewState string `json:"reviewState"`
	Filename    string `json:"filename"`
}
type Skipped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}
type RegistryPlan struct {
	Root     *PlanRoot   `json:"root"`
	Install  []PlanEntry `json:"install"`
	Skipped  []Skipped   `json:"skipped"`
	Warnings []string    `json:"warnings"`
	Errors   []string    `json:"errors"`
}

// Metadata deliberately preserves the bounded, untyped upstream response. The
// public SDK does not promise a versions/latestVersion metadata schema.
type Metadata struct{ Raw json.RawMessage }
type UnavailableError struct{}

func (*UnavailableError) Error() string { return "registry temporarily unavailable" }

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("registry HTTP %d", e.Status) }
func (r Registry) defaults() Registry {
	if r.BaseURL == "" {
		r.BaseURL = "https://www.s2script.com"
	}
	if r.Client == nil {
		r.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return r
}
func safeRegistryURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("registry URL must be HTTPS without credentials or fragment")
	}
	return u, nil
}
func (r Registry) trust(raw string) error {
	r = r.defaults()
	u, e := safeRegistryURL(raw)
	if e != nil {
		return e
	}
	base, e := safeRegistryURL(r.BaseURL)
	if e != nil {
		return e
	}
	if !strings.EqualFold(u.Host, base.Host) {
		return errors.New("registry source must use configured origin")
	}
	return nil
}

// Every request is public and origin-bound, including redirects. Custom clients
// may supply TLS roots/transports but cannot relax this source policy.
func (r Registry) client() *http.Client {
	r = r.defaults()
	c := *r.Client
	c.Jar = nil
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	callback := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many registry redirects")
		}
		if e := r.trust(req.URL.String()); e != nil {
			return e
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		if callback != nil {
			if e := callback(req, via); e != nil {
				return &redirectPolicyError{}
			}
		}
		return r.trust(req.URL.String())
	}
	return &c
}

type redirectPolicyError struct{}

func (*redirectPolicyError) Error() string { return "registry redirect policy rejected request" }
func transient(e error) bool {
	var policy *redirectPolicyError
	var certificate *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	if errors.As(e, &policy) || errors.As(e, &certificate) || errors.As(e, &authority) || errors.As(e, &invalid) || errors.As(e, &hostname) {
		return false
	}
	var n net.Error
	var op *net.OpError
	var dns *net.DNSError
	return errors.As(e, &dns) || errors.As(e, &op) || errors.As(e, &n) && n.Timeout()
}
func (r Registry) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	r = r.defaults()
	raw := strings.TrimRight(r.BaseURL, "/") + path + "?" + q.Encode()
	if e := r.trust(raw); e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, errors.New("invalid registry request")
	}
	resp, e := r.client().Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if transient(e) {
			return nil, &UnavailableError{}
		}
		return nil, errors.New("registry request rejected")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return nil, &UnavailableError{}
	}
	if resp.StatusCode != 200 {
		return nil, &HTTPError{resp.StatusCode}
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil {
		return nil, errors.New("incomplete registry response")
	}
	if len(b) > 4<<20 {
		return nil, errors.New("registry response exceeds limit")
	}
	return b, nil
}
func (r Registry) Meta(ctx context.Context, name string) (Metadata, error) {
	if !validName(name) {
		return Metadata{}, errors.New("invalid metadata identity")
	}
	b, e := r.get(ctx, "/api/v1/meta", url.Values{"name": {name}})
	if e != nil {
		return Metadata{}, e
	}
	var v any
	if e = decodeDocument(b, &v, true, false); e != nil {
		return Metadata{}, errors.New("malformed registry metadata")
	}
	return Metadata{Raw: b}, nil
}
func (r Registry) Plan(ctx context.Context, name, versionRange string, with []string) (RegistryPlan, error) {
	var p RegistryPlan
	v, e := NormalizeVersion(versionRange)
	if !validName(name) || e != nil {
		return p, errors.New("invalid registry plan request")
	}
	q := url.Values{"name": {name}, "range": {v}}
	seen := map[string]bool{}
	for _, n := range with {
		if !validName(n) || seen[n] {
			return p, errors.New("invalid optional member")
		}
		seen[n] = true
	}
	if len(with) > 0 {
		q.Set("with", strings.Join(with, ","))
	}
	b, e := r.get(ctx, "/api/v1/plan", q)
	if e != nil {
		return p, e
	}
	if e = validatePlanFields(b); e != nil {
		return p, e
	}
	if e = decodeDocument(b, &p, false, false); e != nil {
		return p, errors.New("malformed registry plan")
	}
	if p.Root == nil || p.Root.Name != name || !satisfies(p.Root.Version, v) || p.Install == nil || p.Skipped == nil || p.Warnings == nil || p.Errors == nil || len(p.Errors) > 0 {
		return p, errors.New("registry plan does not satisfy requested root")
	}
	for _, skip := range p.Skipped {
		if !validName(skip.Name) || skip.Reason == "" {
			return p, errors.New("invalid skipped plan member")
		}
	}
	seen = map[string]bool{}
	filenames := map[string]bool{}
	for _, entry := range p.Install {
		if !validName(entry.Name) || !ExactVersion(entry.Version) || seen[entry.Name] || !validHash(entry.SHA256) || entry.ReviewState == "" {
			return p, errors.New("invalid registry plan artifact or missing digest")
		}
		seen[entry.Name] = true
		if _, e = Target(entry.Filename, true); e != nil {
			return p, e
		}
		if filenames[entry.Filename] {
			return p, errors.New("registry filename collision")
		}
		filenames[entry.Filename] = true
		if e = r.trust(entry.URL); e != nil {
			return p, e
		}
		if entry.Name == name && entry.Version != p.Root.Version {
			return p, errors.New("registry root artifact version conflict")
		}
	}
	return p, nil
}
func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func (r Registry) Fetch(ctx context.Context, entry PlanEntry, dest string) error {
	if e := r.trust(entry.URL); e != nil {
		return e
	}
	c := r.client()
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	tracked := &registryTransport{next: transport}
	c.Transport = tracked
	e := artifact.Fetch(ctx, c, entry.URL, entry.SHA256, dest, maxPluginBytes)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e != nil && tracked.unavailable {
		return &UnavailableError{}
	}
	return e
}

type registryTransport struct {
	next        http.RoundTripper
	unavailable bool
}

func (t *registryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, e := t.next.RoundTrip(req)
	if e != nil {
		t.unavailable = transient(e)
	} else if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		t.unavailable = true
	}
	return resp, e
}

func validatePlanFields(raw []byte) error {
	obj, e := canonicalObject[RegistryPlan](raw)
	if e != nil {
		return e
	}
	if _, e = canonicalObject[PlanRoot](obj["root"]); e != nil {
		return e
	}
	for _, key := range []string{"install", "skipped"} {
		var entries []json.RawMessage
		if e = json.Unmarshal(obj[key], &entries); e != nil {
			return e
		}
		for _, entry := range entries {
			if key == "install" {
				_, e = canonicalObject[PlanEntry](entry)
			} else {
				_, e = canonicalObject[Skipped](entry)
			}
			if e != nil {
				return e
			}
		}
	}
	return nil
}
