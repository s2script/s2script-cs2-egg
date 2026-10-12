// SPDX-License-Identifier: GPL-3.0-or-later
// Package wings exposes only bounded, host-owned lifecycle operations.
package wings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

type Config struct {
	Endpoint          string `json:"endpoint"`
	Token             string `json:"token"`
	CertificateSHA256 string `json:"certificate_sha256,omitempty"`
}

func (c Config) MarshalJSON() ([]byte, error) {
	type plain Config
	c.Token = "[REDACTED]"
	return json.Marshal(plain(c))
}
func (c Config) Format(f fmt.State, v rune) { b, _ := c.MarshalJSON(); f.Write(b) }

type Client struct {
	endpoint, token string
	http            *http.Client
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var actionID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func New(c Config) (*Client, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || c.Token == "" {
		return nil, errors.New("Wings requires HTTPS origin and host-only bearer token")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CertificateSHA256 != "" {
		pin, e := hex.DecodeString(c.CertificateSHA256)
		if e != nil || len(pin) != 32 {
			return nil, errors.New("Wings certificate pin must be SHA256 hex")
		}
		tr.TLSClientConfig.InsecureSkipVerify = true // Explicit leaf pin replaces PKI validation, never an insecure mode.
		tr.TLSClientConfig.VerifyConnection = func(s tls.ConnectionState) error {
			if len(s.PeerCertificates) == 0 {
				return errors.New("missing Wings certificate")
			}
			got := sha256.Sum256(s.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], pin) != 1 {
				return errors.New("Wings certificate pin mismatch")
			}
			return nil
		}
	}
	return &Client{endpoint: u.Scheme + "://" + u.Host, token: c.Token, http: &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Wings redirects forbidden") }}}, nil
}
func (c *Client) request(ctx context.Context, id, path string, body any, want int, out any) error {
	if !identifier.MatchString(id) {
		return errors.New("invalid Wings server ID")
	}
	method := "GET"
	var r io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		r = bytes.NewReader(b)
		method = "POST"
	}
	req, e := http.NewRequestWithContext(ctx, method, c.endpoint+"/api/servers/"+id+path, r)
	if e != nil {
		return errors.New("invalid Wings request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		return errors.New("Wings request failed or outcome ambiguous")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return errors.New("Wings response unreadable or oversized")
	}
	if resp.StatusCode != want {
		return fmt.Errorf("Wings returned HTTP %d", resp.StatusCode)
	}
	if out != nil && json.Unmarshal(b, out) != nil {
		return errors.New("invalid Wings response")
	}
	return nil
}
func (c *Client) power(ctx context.Context, id, action string) error {
	return c.request(ctx, id, "/power", map[string]string{"action": action}, 202, nil)
}
func (c *Client) Restart(ctx context.Context, id string) error { return c.power(ctx, id, "restart") }
func (c *Client) Stop(ctx context.Context, id string) error    { return c.power(ctx, id, "stop") }
func (c *Client) Start(ctx context.Context, id string) error   { return c.power(ctx, id, "start") }
func (c *Client) State(ctx context.Context, id string) (string, error) {
	var r struct {
		State string `json:"state"`
	}
	e := c.request(ctx, id, "", nil, 200, &r)
	if e == nil {
		switch r.State {
		case "running", "starting", "stopping", "offline":
		default:
			e = errors.New("unknown Wings state")
		}
	}
	return r.State, e
}
func (c *Client) Warning(ctx context.Context, id string, seconds int) error {
	if seconds < 0 || seconds > 604800 {
		return errors.New("invalid countdown")
	}
	return c.request(ctx, id, "/commands", map[string][]string{"commands": {fmt.Sprintf("say Server update restart in %d seconds", seconds)}}, 204, nil)
}
func (c *Client) RequestScheduledStop(ctx context.Context, id, token string) error {
	if !actionID.MatchString(token) {
		return errors.New("invalid scheduled-stop token")
	}
	return c.request(ctx, id, "/commands", map[string][]string{"commands": {"s2egg_scheduled_stop " + token}}, 204, nil)
}
