// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Fetch verifies a bounded HTTPS body before replacing dest. No URL is included
// in network errors. Cross-origin redirects require an explicit CheckRedirect
// callback on client; HTTPS, no userinfo, ten hops, and credential stripping are
// always enforced. A custom Transport must not inject origin credentials.
func Fetch(ctx context.Context, client *http.Client, raw, hash, dest string, limit int64) (err error) {
	expected, e := hex.DecodeString(hash)
	if e != nil || len(expected) != 32 || limit <= 0 || limit == int64(^uint64(0)>>1) {
		return errors.New("invalid digest or download limit")
	}
	u, e := url.Parse(raw)
	if e != nil || !safeURL(u) {
		return errors.New("artifact URL must be HTTPS without userinfo")
	}
	d, e := openDir(filepath.Dir(dest))
	if e != nil {
		return e
	}
	defer d.Close()
	name := filepath.Base(dest)
	if e = validPath(name); e != nil {
		return e
	}
	c := http.Client{Timeout: 5 * time.Minute}
	if client != nil {
		c = *client
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Minute
	}
	callback := c.CheckRedirect
	c.Jar = nil
	crossed := false
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !safeURL(req.URL) {
			return errors.New("unsafe artifact redirect")
		}
		if !strings.EqualFold(req.URL.Host, u.Host) {
			crossed = true
			if callback == nil {
				return errors.New("cross-origin redirect requires explicit policy")
			}
		}
		if crossed {
			stripCredentials(req)
		}
		if callback != nil {
			if e := callback(req, via); e != nil {
				return e
			}
		}
		if !safeURL(req.URL) {
			return errors.New("unsafe artifact redirect")
		}
		// A policy callback may replace the URL; reassess the final origin.
		if !strings.EqualFold(req.URL.Host, u.Host) {
			crossed = true
		}
		if crossed {
			stripCredentials(req)
		}
		return nil
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return errors.New("invalid artifact request")
	}
	resp, e := c.Do(req)
	if e != nil {
		return errors.New("artifact request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("artifact server returned non-200 response")
	}
	if resp.ContentLength > limit {
		return errors.New("artifact exceeds download limit")
	}
	temp := ".artifact-download-" + randomName()
	f, e := openFile(d, temp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, remove(d, temp)) }()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if e == nil && n > limit {
		e = errors.New("artifact exceeds download limit")
	}
	if e == nil && subtle.ConstantTimeCompare(h.Sum(nil), expected) != 1 {
		e = errors.New("artifact SHA-256 mismatch")
	}
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	return rename(d, temp, name)
}
func safeURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
func stripCredentials(r *http.Request) {
	r.Header.Del("Authorization")
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Cookie")
	r.Header.Del("Referer")
}
