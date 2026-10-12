// SPDX-License-Identifier: GPL-3.0-or-later
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/events"
)

var hookPath = regexp.MustCompile(`^/api(?:/v[0-9]+)?/webhooks/[0-9]+/[A-Za-z0-9_-]+$`)

func NewDiscord(cfg Config, client *http.Client) (*Dispatcher, error) {
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = 64
	}
	if cfg.AttemptTimeout == 0 {
		cfg.AttemptTimeout = 10 * time.Second
	}
	if cfg.QueueCapacity < 1 || cfg.QueueCapacity > 1024 || cfg.AttemptTimeout < time.Millisecond || cfg.AttemptTimeout > 60*time.Second || cfg.MaxRetries < 0 || cfg.MaxRetries > 10 {
		return nil, errors.New("invalid webhook delivery limits")
	}
	if cfg.URL != "" {
		u, e := url.Parse(cfg.URL)
		if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !hookPath.MatchString(u.Path) || (u.Host != "discord.com" && u.Host != "discordapp.com" && u.Host != "canary.discord.com" && u.Host != "ptb.discord.com") {
			return nil, errors.New("webhook must be an HTTPS Discord URL without query or fragment")
		}
	}
	if cfg.Events == nil {
		cfg.Events = []string{"server.up", "server.down", "server.crash", "server.start_failed"}
	}
	selected := map[string]bool{}
	for _, kind := range cfg.Events {
		if kind == "startup.failure" {
			kind = "server.start_failed"
		}
		switch kind {
		case "server.up", "server.down", "server.crash", "server.start_failed", "server.restart", "server.update", "backup", "guard":
			selected[kind] = true
		default:
			return nil, errors.New("unsupported webhook event selection")
		}
	}
	copyClient := http.Client{}
	if client != nil {
		copyClient = *client
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{cfg: cfg, client: &copyClient, selected: selected, queue: make(chan events.Event, cfg.QueueCapacity), ctx: ctx, cancel: cancel, done: make(chan struct{}), seen: map[string]bool{}}
	go d.worker()
	return d, nil
}
func (d *Dispatcher) payload(e events.Event) []byte {
	text := fmt.Sprintf("%s\nServer: %s\nRun: %s\nEvent: %s\nAt: %s\nReason: %s", e.Type, e.ServerID, e.RunID, e.ID, e.At.UTC().Format(time.RFC3339), e.Reason)
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		text += "\n" + k + ": " + e.Details[k]
	}
	text = strings.ReplaceAll(text, d.cfg.URL, "[REDACTED]")
	u, _ := url.Parse(d.cfg.URL)
	token := u.Path[strings.LastIndex(u.Path, "/")+1:]
	if token != "" {
		text = strings.ReplaceAll(text, token, "[REDACTED]")
	}
	chars := []rune(text)
	if len(chars) > 1900 {
		text = string(chars[:1899]) + "…"
	}
	b, _ := json.Marshal(struct {
		Content  string `json:"content"`
		Mentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}{Content: text, Mentions: struct {
		Parse []string `json:"parse"`
	}{Parse: []string{}}})
	return b
}
func (d *Dispatcher) deliver(e events.Event) bool {
	u, _ := url.Parse(d.cfg.URL)
	q := u.Query()
	q.Set("wait", "true")
	u.RawQuery = q.Encode()
	body := d.payload(e)
	for attempt := 0; attempt <= d.cfg.MaxRetries; attempt++ {
		if d.ctx.Err() != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(d.ctx, d.cfg.AttemptTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			cancel()
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "s2script-cs2-egg")
		response, err := d.client.Do(req)
		retry := false
		delay := time.Duration(attempt+1) * 100 * time.Millisecond
		if err != nil {
			retry = true
		} else {
			b, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
			response.Body.Close()
			status := response.StatusCode
			if status == http.StatusNotFound {
				d.dead.Store(true)
			}
			if status >= 200 && status < 300 {
				var ack struct {
					ID string `json:"id"`
				}
				ok := readErr == nil && len(b) <= 64*1024 && json.Unmarshal(b, &ack) == nil && ack.ID != ""
				cancel()
				return ok
			}
			retry = status == 429 || status >= 500
			if retry {
				if after, ok := retryDelay(response.Header.Get("Retry-After"), b, status); ok {
					delay = after
				} else if status == 429 || response.Header.Get("Retry-After") != "" {
					cancel()
					return false
				}
			}
		}
		cancel()
		if !retry || attempt == d.cfg.MaxRetries {
			return false
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-d.ctx.Done():
			timer.Stop()
			return false
		}
	}
	return false
}

// Discord retry_after is fractional seconds. Take the larger valid body/header
// delay, never retry earlier; malformed 429 timing fails rather than guessing.
func retryDelay(header string, body []byte, status int) (time.Duration, bool) {
	seconds := -1.0
	if value, e := strconv.ParseFloat(header, 64); e == nil && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
		seconds = value
	} else if at, e := http.ParseTime(header); e == nil {
		seconds = math.Max(0, time.Until(at).Seconds())
	}
	if status == 429 {
		var data struct {
			RetryAfter *float64 `json:"retry_after"`
		}
		if json.Unmarshal(body, &data) == nil && data.RetryAfter != nil {
			n := *data.RetryAfter
			if n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) && n > seconds {
				seconds = n
			}
		}
	}
	// Refuse excessive timing without violating the requested delay or overflowing.
	if seconds < 0 || seconds > 60 {
		return 0, false
	}
	return time.Duration(math.Ceil(seconds * float64(time.Second))), true
}
