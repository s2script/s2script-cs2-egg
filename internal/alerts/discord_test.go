// SPDX-License-Identifier: GPL-3.0-or-later
package alerts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/events"
)

const hook = "https://discord.com/api/webhooks/123/private-token"

type routeTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (r routeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cp := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.target.Scheme
	u.Host = r.target.Host
	cp.URL = &u
	return r.base.RoundTrip(cp)
}
func fixture(t *testing.T, h http.HandlerFunc) *http.Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	return &http.Client{Transport: routeTransport{http.DefaultTransport, u}}
}
func event(id, kind string) events.Event {
	return events.Event{Version: 1, ID: id, Type: kind, ServerID: "server1", RunID: "run1", CorrelationID: "run1", At: time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC), Reason: "fixture"}
}
func closeOK(t *testing.T, d *alerts.Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := d.Close(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestWebhookUpDownAndCrashPayloads(t *testing.T) {
	var mu sync.Mutex
	var payloads []string
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("wait") != "true" {
			t.Error("missing acknowledgement")
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		payloads = append(payloads, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"message"}`)
	})
	d, e := alerts.NewDiscord(alerts.Config{URL: hook}, client)
	if e != nil {
		t.Fatal(e)
	}
	for i, k := range []string{"server.up", "server.crash", "server.down", "server.console"} {
		ev := event(fmt.Sprint(i), k)
		ev.Reason = "@everyone " + hook + strings.Repeat("é", 3000)
		if e = d.Publish(context.Background(), ev); e != nil {
			t.Fatal(e)
		}
	}
	closeOK(t, d)
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 3 {
		t.Fatalf("requests %d", len(payloads))
	}
	for i, b := range payloads {
		var p struct {
			Content string
			Allowed struct {
				Parse []string `json:"parse"`
			} `json:"allowed_mentions"`
		}
		if e = json.Unmarshal([]byte(b), &p); e != nil {
			t.Fatal(e)
		}
		if p.Allowed.Parse == nil || len(p.Allowed.Parse) != 0 || len([]rune(p.Content)) > 2000 || strings.Contains(b, "private-token") {
			t.Fatalf("unsafe payload %s", b)
		}
		if !strings.Contains(p.Content, []string{"server.up", "server.crash", "server.down"}[i]) || !strings.Contains(p.Content, "server1") || !strings.Contains(p.Content, "run1") {
			t.Fatal("missing lifecycle identity")
		}
	}
}
func TestRetryHonors429AndDeduplicatesEventID(t *testing.T) {
	var count atomic.Int32
	var first time.Time
	var elapsed time.Duration
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			first = time.Now()
			w.WriteHeader(429)
			fmt.Fprint(w, `{"retry_after":0.06}`)
			return
		}
		elapsed = time.Since(first)
		fmt.Fprint(w, `{"id":"ok"}`)
	})
	d, e := alerts.NewDiscord(alerts.Config{URL: hook, MaxRetries: 2, AttemptTimeout: time.Second}, client)
	if e != nil {
		t.Fatal(e)
	}
	ev := event("same", "server.up")
	d.Publish(context.Background(), ev)
	d.Publish(context.Background(), ev)
	closeOK(t, d)
	if count.Load() != 2 || elapsed < 55*time.Millisecond || d.Stats().Duplicates != 1 {
		t.Fatalf("attempts=%d elapsed=%v stats=%+v", count.Load(), elapsed, d.Stats())
	}
}
func TestWebhookTimeoutDoesNotBlockServerStop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	d, e := alerts.NewDiscord(alerts.Config{URL: hook, AttemptTimeout: time.Second, MaxRetries: 1}, client)
	if e != nil {
		t.Fatal(e)
	}
	d.Publish(context.Background(), event("one", "server.down"))
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if e = d.Close(ctx); e == nil {
		t.Fatal("missing deadline error")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("shutdown blocked")
	}
}
func TestNoWebhookWhenUnconfigured(t *testing.T) {
	var count atomic.Int32
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1) })
	d, e := alerts.NewDiscord(alerts.Config{}, client)
	if e != nil {
		t.Fatal(e)
	}
	d.Publish(context.Background(), event("one", "server.up"))
	closeOK(t, d)
	if count.Load() != 0 {
		t.Fatal("unconfigured webhook sent")
	}
}
func TestBoundedQueueAndAliasSelection(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var count atomic.Int32
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			close(started)
			<-release
		}
		fmt.Fprint(w, `{"id":"ok"}`)
	})
	d, e := alerts.NewDiscord(alerts.Config{URL: hook, Events: []string{"startup.failure", "server.start_failed"}, QueueCapacity: 1}, client)
	if e != nil {
		t.Fatal(e)
	}
	d.Publish(context.Background(), event("one", "server.start_failed"))
	<-started
	d.Publish(context.Background(), event("two", "server.start_failed"))
	if e = d.Publish(context.Background(), event("three", "server.start_failed")); e == nil {
		t.Fatal("queue overflow accepted")
	}
	d.Publish(context.Background(), event("up", "server.up"))
	close(release)
	closeOK(t, d)
	if count.Load() != 2 || d.Stats().Dropped != 1 {
		t.Fatalf("requests %d stats %+v", count.Load(), d.Stats())
	}
	if e = d.Publish(context.Background(), event("four", "server.start_failed")); e == nil {
		t.Fatal("publish after close accepted")
	}
}
func TestPermanentFailureAndRedirectAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var count atomic.Int32
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Location", hook)
				w.WriteHeader(status)
				fmt.Fprint(w, hook)
			})
			d, e := alerts.NewDiscord(alerts.Config{URL: hook, MaxRetries: 2}, client)
			if e != nil {
				t.Fatal(e)
			}
			d.Publish(context.Background(), event("one", "server.up"))
			closeOK(t, d)
			if count.Load() != 1 || d.Stats().Failed != 1 {
				t.Fatalf("count=%d stats=%+v", count.Load(), d.Stats())
			}
			if status == 404 {
				d.Publish(context.Background(), event("two", "server.down"))
				if count.Load() != 1 {
					t.Fatal("retried dead endpoint")
				}
			}
		})
	}
}
func TestRetryAfterHeaderAndServerErrors(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var count atomic.Int32
			var first time.Time
			var elapsed time.Duration
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if count.Add(1) == 1 {
					first = time.Now()
					w.Header().Set("Retry-After", "0.05")
					w.WriteHeader(status)
					return
				}
				elapsed = time.Since(first)
				fmt.Fprint(w, `{"id":"ok"}`)
			})
			d, e := alerts.NewDiscord(alerts.Config{URL: hook, MaxRetries: 1}, client)
			if e != nil {
				t.Fatal(e)
			}
			d.Publish(context.Background(), event("one", "server.up"))
			closeOK(t, d)
			if count.Load() != 2 || elapsed < 45*time.Millisecond {
				t.Fatalf("count=%d elapsed=%v", count.Load(), elapsed)
			}
		})
	}
}
func TestDispatcherValidationAndRedaction(t *testing.T) {
	for _, cfg := range []alerts.Config{{URL: "http://discord.com/api/webhooks/1/x"}, {URL: hook + "?thread_id=1"}, {URL: hook, QueueCapacity: -1}, {URL: hook, AttemptTimeout: 61 * time.Second}, {URL: hook, MaxRetries: 11}, {URL: hook, Events: []string{"server.console"}}} {
		if _, e := alerts.NewDiscord(cfg, nil); e == nil || strings.Contains(e.Error(), "private-token") {
			t.Fatalf("unsafe validation %v", e)
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", alerts.Config{URL: hook}), "private-token") {
		t.Fatal("config formatting leaks URL")
	}
}

func TestExcessiveRetryAfterDoesNotRetryEarly(t *testing.T) {
	var count atomic.Int32
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(503)
	})
	d, e := alerts.NewDiscord(alerts.Config{URL: hook, MaxRetries: 1}, client)
	if e != nil {
		t.Fatal(e)
	}
	d.Publish(context.Background(), event("one", "server.up"))
	closeOK(t, d)
	if count.Load() != 1 {
		t.Fatal("retried earlier than server requested")
	}
}
func TestDeadEndpointDisablesQueuedDelivery(t *testing.T) {
	var count atomic.Int32
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1); w.WriteHeader(404) })
	d, e := alerts.NewDiscord(alerts.Config{URL: hook, MaxRetries: 2}, client)
	if e != nil {
		t.Fatal(e)
	}
	d.Publish(context.Background(), event("one", "server.up"))
	d.Publish(context.Background(), event("two", "server.down"))
	closeOK(t, d)
	if count.Load() != 1 || d.Stats().Failed != 2 {
		t.Fatalf("count=%d stats=%+v", count.Load(), d.Stats())
	}
}

func TestAcknowledgementRequiredAndBodyBounded(t *testing.T) {
	for _, body := range []string{`{}`, `not JSON`, strings.Repeat("x", 70*1024)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			d, e := alerts.NewDiscord(alerts.Config{URL: hook}, client)
			if e != nil {
				t.Fatal(e)
			}
			d.Publish(context.Background(), event("one", "server.up"))
			closeOK(t, d)
			if d.Stats().Delivered != 0 || d.Stats().Failed != 1 {
				t.Fatalf("unacknowledged message accepted %+v", d.Stats())
			}
		})
	}
}
func TestCanceledPublishAndCopiedEvent(t *testing.T) {
	var payload string
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		payload = string(b)
		io.WriteString(w, `{"id":"ok"}`)
	})
	d, e := alerts.NewDiscord(alerts.Config{URL: hook}, client)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d.Publish(ctx, event("canceled", "server.up")) == nil {
		t.Fatal("canceled publish accepted")
	}
	ev := event("one", "server.up")
	ev.Details = map[string]string{"state": "original"}
	d.Publish(context.Background(), ev)
	ev.Details["state"] = "mutated"
	closeOK(t, d)
	if !strings.Contains(payload, "original") || strings.Contains(payload, "mutated") {
		t.Fatal("queued event map was not copied")
	}
}
