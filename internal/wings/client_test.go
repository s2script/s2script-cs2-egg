// SPDX-License-Identifier: GPL-3.0-or-later
package wings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTLSAndPowerContract(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("incorrect bearer")
		}
		if r.Method == "GET" {
			w.Write([]byte(`{"state":"running"}`))
			return
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["action"] != "restart" {
			t.Error(b)
		}
		w.WriteHeader(202)
	}))
	defer srv.Close()
	c, e := New(Config{Endpoint: srv.URL, Token: "secret"})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Restart(context.Background(), "srv"); e == nil {
		t.Fatal("untrusted TLS accepted")
	}
	pin := sha256.Sum256(srv.Certificate().Raw)
	c, e = New(Config{Endpoint: srv.URL, Token: "secret", CertificateSHA256: hex.EncodeToString(pin[:])})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Restart(context.Background(), "srv"); e != nil {
		t.Fatal(e)
	}
	if s, e := c.State(context.Background(), "srv"); e != nil || s != "running" {
		t.Fatal(s, e)
	}
	if e = c.Restart(context.Background(), "../other"); e == nil {
		t.Fatal("unsafe ID accepted")
	}
}

func TestRedirectTimeoutAndPinFailures(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("never") == "used" {
			t.Fatal("unexpected query")
		}
		w.Header().Set("Location", "https://example.invalid/leak")
		w.WriteHeader(307)
	}))
	defer srv.Close()
	pin := sha256.Sum256(srv.Certificate().Raw)
	c, e := New(Config{Endpoint: srv.URL, Token: "secret", CertificateSHA256: hex.EncodeToString(pin[:])})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Restart(context.Background(), "srv"); e == nil {
		t.Fatal("redirect followed")
	}
	c.http.Timeout = time.Nanosecond
	if e = c.Restart(context.Background(), "srv"); e == nil {
		t.Fatal("timeout accepted")
	}
	c, e = New(Config{Endpoint: srv.URL, Token: "secret", CertificateSHA256: strings.Repeat("0", 64)})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Restart(context.Background(), "srv"); e == nil {
		t.Fatal("wrong pin accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected redirected request count", calls.Load())
	}
}
