// SPDX-License-Identifier: GPL-3.0-or-later
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/framework"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheWireLongDownloadRetryAndPolicy(t *testing.T) {
	payload := []byte(strings.Repeat("archive", 100000))
	hash := storeio.Hash(payload)
	commit := strings.Repeat("a", 40)
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	var downloads atomic.Int32
	var origin string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			downloads.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
				return
			case <-gate:
			}
			w.Write(payload)
			return
		}
		if strings.Contains(r.URL.Path, "/git/ref/") {
			fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, commit)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"v0.8.1","assets":[{"name":"s2script-cs2-linux-0.8.1.zip","browser_download_url":%q,"digest":%q}]}`, origin+"/asset", "sha256:"+hash)
	}))
	defer ts.Close()
	origin = ts.URL
	u, _ := url.Parse(origin)
	s, _ := New(Options{Root: t.TempDir(), Client: ts.Client(), Origins: []string{u.Host}})
	m := &Module{Store: s, Resolver: framework.Resolver{APIBase: origin, Client: ts.Client()}, Policy: Policy{Pins: map[string]string{"s2script": "0.8.1"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e := m.Start(ctx, nil); e != nil {
		t.Fatal(e)
	}
	defer m.Close(context.Background())
	clientConn, serverConn := net.Pipe()
	go protocol.ServeConn(ctx, serverConn, func(ctx context.Context, msg protocol.Message) (any, error) {
		return m.Handle(ctx, node.Server{ID: "one"}, protocol.CapCache, msg)
	})
	client := protocol.NewClient(clientConn)
	defer client.Close()
	call := func(r protocol.CacheRequest) protocol.CacheResult {
		t.Helper()
		var out protocol.CacheResult
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if e := client.Call(c, protocol.CapCache, r, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	a := protocol.ArtifactSelection{Source: "s2script", Version: "0.8.1", SHA256: hash, SourceRevision: commit, Integrity: "github-publisher-sha256"}
	request := protocol.CacheRequest{Operation: "submit", RequestID: "stable", Artifact: a}
	r := call(request)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("download did not start")
	}
	retry := call(request)
	if retry.Handle != r.Handle {
		t.Fatal("ambiguous retry created another job")
	}
	status := call(protocol.CacheRequest{Operation: "status", Handle: r.Handle})
	if status.State == "ready" {
		t.Fatal("unfinished download marked ready")
	}
	close(gate)
	deadline := time.Now().Add(time.Second)
	for status.State != "ready" {
		if status.State == "failed" || time.Now().After(deadline) {
			t.Fatal(status)
		}
		time.Sleep(10 * time.Millisecond)
		status = call(protocol.CacheRequest{Operation: "status", Handle: r.Handle})
	}
	var got []byte
	for int64(len(got)) < status.Size {
		part := call(protocol.CacheRequest{Operation: "read", Handle: r.Handle, Offset: int64(len(got))})
		got = append(got, part.Data...)
	}
	if storeio.Hash(got) != hash || downloads.Load() != 1 {
		t.Fatal("wire content/dedup failure")
	}
	for i, bad := range []protocol.ArtifactSelection{{Source: "evil", Version: "0.8.1", SHA256: hash}, {Source: "s2script", Version: "0.8.2", SHA256: hash}, {Source: "s2script", Version: "0.8.1", SHA256: strings.Repeat("0", 64), SourceRevision: commit, Integrity: "github-publisher-sha256"}} {
		r := call(protocol.CacheRequest{Operation: "submit", RequestID: fmt.Sprint("bad", i), Artifact: bad})
		for range 100 {
			r = call(protocol.CacheRequest{Operation: "status", Handle: r.Handle})
			if r.State == "failed" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if r.State != "failed" {
			b, _ := json.Marshal(r)
			t.Fatal("policy accepted invalid request", string(b))
		}
	}
}
