// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func put(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}
func wantFile(t *testing.T, p, s string) {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil || string(b) != s {
		t.Fatalf("%s = %q, %v; want %q", p, b, e, s)
	}
}
func TestFetchRejectsWrongHashAndOversize(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "new generation") }))
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "artifact")
	put(t, dest, "old generation")
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("new generation")))
	for _, tc := range []struct {
		hash  string
		limit int64
	}{{strings.Repeat("0", 64), 100}, {hash, 3}, {hash, 0}} {
		if e := Fetch(context.Background(), s.Client(), s.URL, tc.hash, dest, tc.limit); e == nil {
			t.Fatal("accepted invalid download")
		}
		wantFile(t, dest, "old generation")
	}
	if e := Fetch(context.Background(), s.Client(), s.URL, hash, dest, 100); e != nil {
		t.Fatal(e)
	}
	wantFile(t, dest, "new generation")
}
func TestFetchRejectsUnsafeRedirectsAndSymlink(t *testing.T) {
	hits := 0
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; fmt.Fprint(w, "new") }))
	defer other.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer s.Close()
	d := filepath.Join(t.TempDir(), "artifact")
	h := fmt.Sprintf("%x", sha256.Sum256([]byte("new")))
	if Fetch(context.Background(), s.Client(), s.URL, h, d, 100) == nil || hits != 0 {
		t.Fatal("cross-origin redirect followed")
	}
	if Fetch(context.Background(), nil, "http://example.com/a", h, d, 100) == nil {
		t.Fatal("HTTP accepted")
	}
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(filepath.Dir(d), "link")); e != nil {
		t.Fatal(e)
	}
	if Fetch(context.Background(), s.Client(), s.URL, h, filepath.Join(filepath.Dir(d), "link", "escape"), 100) == nil {
		t.Fatal("symlink parent accepted")
	}
}

func TestFetchTrustedRedirectStripsCredentialsAcrossChain(t *testing.T) {
	var bad atomic.Bool
	var policyCalls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Referer") != "" {
			bad.Store(true)
		}
		if r.URL.Path == "/asset" {
			http.Redirect(w, r, "/final?signed=cdn-token", http.StatusFound)
			return
		}
		fmt.Fprint(w, "new")
	}))
	defer s.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.URL+"/asset?signed=cdn-token", http.StatusFound)
	}))
	defer origin.Close()
	c := origin.Client()
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		policyCalls.Add(1)
		if req.URL.Host != strings.TrimPrefix(s.URL, "https://") {
			return fmt.Errorf("untrusted host")
		}
		req.Header.Set("Authorization", "secret")
		req.Header.Set("Cookie", "secret")
		req.Header.Set("Proxy-Authorization", "secret")
		return nil
	}
	dest := filepath.Join(t.TempDir(), "artifact")
	h := fmt.Sprintf("%x", sha256.Sum256([]byte("new")))
	if e := Fetch(context.Background(), c, origin.URL+"?original=private", h, dest, 100); e != nil {
		t.Fatal(e)
	}
	if bad.Load() || policyCalls.Load() != 2 {
		t.Fatal("redirect leaked credentials or bypassed policy")
	}
	wantFile(t, dest, "new")
}
func TestFetchChecksCallbackMutatedOrigin(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		fmt.Fprint(w, "new")
	}))
	defer other.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/next", http.StatusFound) }))
	defer s.Close()
	c := s.Client()
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		req.URL, _ = url.Parse(other.URL)
		req.Header.Set("Authorization", "secret")
		return nil
	}
	dest := filepath.Join(t.TempDir(), "artifact")
	h := fmt.Sprintf("%x", sha256.Sum256([]byte("new")))
	if e := Fetch(context.Background(), c, s.URL, h, dest, 100); e != nil {
		t.Fatal(e)
	}
	if leaked.Load() {
		t.Fatal("callback-mutated origin received authorization")
	}
}
func TestFetchChunkedOversizeCancellationAndRedaction(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "new generation")
	}))
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "artifact")
	put(t, dest, "old")
	h := fmt.Sprintf("%x", sha256.Sum256([]byte("new generation")))
	if Fetch(context.Background(), s.Client(), s.URL, h, dest, 4) == nil {
		t.Fatal("chunked oversize accepted")
	}
	wantFile(t, dest, "old")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := Fetch(ctx, s.Client(), s.URL+"?secret=PRIVATE", h, dest, 100)
	if e == nil || strings.Contains(e.Error(), "PRIVATE") {
		t.Fatal("cancellation absent or URL leaked")
	}
	wantFile(t, dest, "old")
}
