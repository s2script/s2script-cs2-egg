// SPDX-License-Identifier: GPL-3.0-or-later
package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/server"
)

type routed struct{ target *url.URL }

func (r routed) RoundTrip(req *http.Request) (*http.Response, error) {
	cp := req.Clone(req.Context())
	u := *req.URL
	u.Host = r.target.Host
	u.Scheme = r.target.Scheme
	cp.URL = &u
	return http.DefaultTransport.RoundTrip(cp)
}

// Failing real preparation must never announce up/down, including events before failure.
func TestFailedPreparationWebhookSequence(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "steamcmd/linux32"), 0755)
	for _, name := range []string{"steamcmd", "steamerrorreporter", "libstdc++.so.6", "crashhandler.so"} {
		os.WriteFile(filepath.Join(root, "steamcmd/linux32", name), []byte("fixture"), 0755)
	}
	os.WriteFile(filepath.Join(root, "steamcmd/steamcmd.sh"), []byte("#!/bin/sh\nexit 7\n"), 0755)
	cfg, e := config.LoadServer(root, func(k string) (string, bool) { return "1", k == "ALLOW_TOKENLESS" })
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	var payloads []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct{ Content string }
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		payloads = append(payloads, p.Content)
		mu.Unlock()
		io.WriteString(w, `{"id":"ok"}`)
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	d, e := alerts.NewDiscord(alerts.Config{URL: "https://discord.com/api/webhooks/1/fixture", Events: []string{"server.up", "server.down", "server.crash", "startup.failure", "server.start_failed"}}, &http.Client{Transport: routed{u}})
	if e != nil {
		t.Fatal(e)
	}
	code, e := server.Run(context.Background(), cfg, d, nil, io.Discard, io.Discard, server.Options{})
	if code == 0 || e == nil {
		t.Fatal("failed Steam initialization accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = d.Close(ctx); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 1 {
		t.Fatalf("complete payload sequence: %v", payloads)
	}
	var text = payloads[0]
	if len(text) < len("server.start_failed") || text[:len("server.start_failed")] != "server.start_failed" {
		t.Fatalf("unexpected transition %s", text)
	}
}
