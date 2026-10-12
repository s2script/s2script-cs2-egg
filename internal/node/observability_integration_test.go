// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/cleanup"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/observability"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type localWebhook struct{ target *url.URL }

func (r localWebhook) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Host = r.target.Host
	u.Scheme = r.target.Scheme
	copy.URL = &u
	return http.DefaultTransport.RoundTrip(copy)
}
func TestSyntheticCrashBundleAndWebhookWithoutSecrets(t *testing.T) {
	cfg := fixture(t)
	cfg.Observability.Enabled = true
	cfg.Webhook.URL = "https://discord.com/api/webhooks/1/sensitive-token"
	var mu sync.Mutex
	var bodies []string
	httpFixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		io.WriteString(w, `{"id":"fixture"}`)
	}))
	defer httpFixture.Close()
	u, _ := url.Parse(httpFixture.URL)
	dispatch, e := alerts.NewDiscord(alerts.Config{URL: cfg.Webhook.URL, Events: []string{"server.crash", "crash.bundle"}}, &http.Client{Transport: localWebhook{u}})
	if e != nil {
		t.Fatal(e)
	}
	defer dispatch.Close(context.Background())
	core, e := New(cfg, dispatch)
	if e != nil {
		t.Fatal(e)
	}
	module := &observability.Module{Root: filepath.Join(cfg.StateDir, "observability"), Config: core.cfg.Observability, Secrets: redaction.Secrets(cfg.Webhook.URL)}
	if e = core.AddModule(module, []string{protocol.CapOperations, nodetypes.GrantOperationalEvents}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- core.Run(ctx) }()
	select {
	case <-core.Ready():
	case e := <-done:
		t.Fatal(e)
	}
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	client := connect(t, cfg)
	if _, e = client.ClaimRun(context.Background(), "fixture-run"); e != nil {
		t.Fatal(e)
	}
	if e = client.ReportStatus(context.Background(), server.Status{RunID: "fixture-run", State: "ready", Ready: true, Versions: map[string]string{"cs2": "987654"}, SampleAt: time.Now().UTC()}); e != nil {
		t.Fatal(e)
	}
	var output bytes.Buffer
	redactor, e := redaction.NewStream(&output, redaction.Secrets(cfg.Webhook.URL))
	if e != nil {
		t.Fatal(e)
	}
	child := exec.Command("sh", "-c", "printf 'fixture sensitive-'; printf 'token output\\n'; exit 9")
	child.Stdout = redactor
	child.Stderr = redactor
	if e = child.Run(); e == nil {
		t.Fatal("synthetic process did not crash")
	}
	if e = redactor.Close(); e != nil {
		t.Fatal(e)
	}
	console := events.Event{Version: 1, ID: "console", RunID: "fixture-run", CorrelationID: "fixture-run", Type: "server.console", Severity: "debug", At: time.Now().UTC(), Details: map[string]string{"line": output.String(), "stream": "stdout"}}
	if _, e = client.PublishEvent(context.Background(), console); e != nil {
		t.Fatal(e)
	}
	crash := console
	crash.ID = "crash"
	crash.Type = "server.crash"
	crash.Reason = "unexpected_exit"
	crash.Details = map[string]string{"message": "sensitive-token", "exit_code": "9"}
	if _, e = client.PublishEvent(context.Background(), crash); e != nil {
		t.Fatal(e)
	}
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("crash event and bundle event not delivered")
		case <-time.After(10 * time.Millisecond):
		}
	}
	root := filepath.Join(module.Root, "a", "crashes")
	entries, e := cleanup.Catalog(root)
	if e != nil || len(entries) != 1 {
		t.Fatal(entries, e)
	}
	b, e := os.ReadFile(filepath.Join(root, entries[0].Path))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte("sensitive-token")) || !bytes.Contains(b, []byte("987654")) || !bytes.Contains(b, []byte("best effort")) {
		t.Fatal(string(b))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, b := range bodies {
		if strings.Contains(b, "sensitive-token") {
			t.Fatal("webhook leak", b)
		}
	}
}
func TestOperationsCapabilityOnlyOnAdminSocket(t *testing.T) {
	cfg := fixture(t)
	_, stop := start(t, cfg, nil)
	defer stop()
	conn, e := net.Dial("unix", filepath.Join(cfg.Servers[0].Volume, "egg/s2node.sock"))
	if e != nil {
		t.Fatal(e)
	}
	client := protocol.NewClient(conn)
	defer client.Close()
	var welcome protocol.Welcome
	if e = client.Call(context.Background(), "hello", protocol.Hello{Capabilities: []string{protocol.CapOperations}}, &welcome); e != nil {
		t.Fatal(e)
	}
	if len(welcome.Capabilities) != 0 {
		t.Fatal("container got private operations", welcome)
	}
	var out json.RawMessage
	if e = client.Call(context.Background(), protocol.CapOperations, protocol.OperationsRequest{Operation: "archive"}, &out); e == nil {
		t.Fatal("container accessed archive administration")
	}
}
