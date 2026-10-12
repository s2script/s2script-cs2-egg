// SPDX-License-Identifier: GPL-3.0-or-later
package cache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentArtifactRequestsDownloadOnce(t *testing.T) {
	var hits atomic.Int32
	body := []byte("verified archive")
	h := fmt.Sprintf("%x", sha256.Sum256(body))
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(body) }))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	s, e := New(Options{Root: t.TempDir(), Client: ts.Client(), Origins: []string{u.Host}, MaxBytes: 1024})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := s.Get(context.Background(), ts.URL, h)
			if e != nil {
				t.Error(e)
				return
			}
			b, _ := os.ReadFile(p)
			if string(b) != string(body) {
				t.Error("wrong bytes")
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("downloads %d", hits.Load())
	}
}
func TestBadDigestNeverCached(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("wrong")) }))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	s, e := New(Options{Root: t.TempDir(), Client: ts.Client(), Origins: []string{u.Host}, MaxBytes: 1024})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(context.Background(), ts.URL, fmt.Sprintf("%064d", 0)); e == nil {
		t.Fatal("bad digest published")
	}
	entries, _ := os.ReadDir(s.root + "/objects")
	if len(entries) != 0 {
		t.Fatal("bad entry remained")
	}
}
func TestCrossStoreRequestsDownloadOnce(t *testing.T) {
	var hits atomic.Int32
	body := []byte("one")
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(body) }))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	o := Options{Root: t.TempDir(), Client: ts.Client(), Origins: []string{u.Host}, MaxBytes: 1024}
	a, _ := New(o)
	b, _ := New(o)
	var wg sync.WaitGroup
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			if _, e := s.Get(context.Background(), ts.URL, digest); e != nil {
				t.Error(e)
			}
		}(s)
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatal("cross-store duplicate downloads", hits.Load())
	}
}
func TestCacheRejectsOriginBeforeHit(t *testing.T) {
	s, e := New(Options{Root: t.TempDir(), Origins: []string{"example.org"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(context.Background(), "https://evil.invalid/item", fmt.Sprintf("%064d", 0)); e == nil {
		t.Fatal("unapproved origin accepted")
	}
}
