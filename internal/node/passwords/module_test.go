// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/nodeclient"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/alerts"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

type docker struct{ containers []core.Container }

func (d docker) Running(context.Context, []string) ([]core.Container, error) {
	return d.containers, nil
}
func (d docker) Inspect(_ context.Context, id string) (core.Container, error) {
	for _, c := range d.containers {
		if c.ID == id {
			return c, nil
		}
	}
	return core.Container{}, os.ErrNotExist
}
func (d docker) Events(ctx context.Context, _ []string) (<-chan core.Event, <-chan error) {
	return make(chan core.Event), make(chan error)
}

func TestSocketOptInAndIdentity(t *testing.T) {
	cfg := testConfig(t, "unique")
	n := nodeconfig.Default()
	n.SetSection("passwords", cfg)
	vol, err := os.MkdirTemp("/tmp", "pwtest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(vol)
	var containers []core.Container
	for _, name := range []string{"one", "two", "off"} {
		p := filepath.Join(vol, name)
		os.MkdirAll(p, 0700)
		containers = append(containers, core.Container{ID: name, Name: name, Volume: p, Owner: core.Owner{UID: os.Getuid(), GID: os.Getgid()}})
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := core.New(docker{containers}, n, []string{"passwords"}, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(log, &http.Client{})
	go m.Run(ctx, c)
	go c.Run(ctx)
	query := func(name string, opt bool) *proto.PasswordUpdate {
		t.Helper()
		cl, err := nodeclient.Connect(ctx, core.SocketPath(filepath.Join(vol, name)), 3*time.Second, proto.Hello{RotatingPassword: opt})
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		msg, err := cl.Request(&proto.PasswordQuery{}, 3*time.Second, func(m proto.Message) bool { _, ok := m.(*proto.PasswordUpdate); return ok })
		if err != nil {
			t.Fatal(err)
		}
		return msg.(*proto.PasswordUpdate)
	}
	a := query("one", true)
	b := query("two", true)
	off := query("off", false)
	if !ValidPassword(a.Password) || !ValidPassword(b.Password) || a.Password == b.Password {
		t.Fatal("assignments missing or not isolated")
	}
	if off.Password != "" || off.Err == "" {
		t.Fatal("unopted server obtained an assignment")
	}
	raw, _ := json.Marshal(c.Status("test", "test"))
	if strings.Contains(string(raw), a.Password) || strings.Contains(string(raw), b.Password) {
		t.Fatal("status leaked password")
	}
}

func TestRotationRestartUsesOnlyItsEnrolledSocketServer(t *testing.T) {
	requests := make(chan string, 4)
	wings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || string(body) != `{"action":"restart"}` || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("invalid Wings restart request")
		}
		select {
		case requests <- r.URL.Path:
		default:
			t.Error("too many restart requests")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer wings.Close()
	configPath := filepath.Join(t.TempDir(), "wings.yml")
	_, port, _ := strings.Cut(strings.TrimPrefix(wings.URL, "http://"), ":")
	if err := os.WriteFile(configPath, []byte("token: test-token\napi:\n  host: 127.0.0.1\n  port: "+port+"\n  ssl:\n    enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, "shared")
	n := nodeconfig.Default()
	n.SetSection("passwords", cfg)
	n.SetSection("vpksync", map[string]string{"wings_config": configPath})
	volume, err := os.MkdirTemp("/tmp", "pwrestart")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(volume)
	var containers []core.Container
	for _, server := range []string{"one", "off"} {
		path := filepath.Join(volume, server)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		containers = append(containers, core.Container{ID: server, Name: server, Volume: path, Owner: core.Owner{UID: os.Getuid(), GID: os.Getgid()}})
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := core.New(docker{containers}, n, []string{"passwords"}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(logger, &http.Client{})
	go m.Run(ctx, c)
	go c.Run(ctx)
	one, err := nodeclient.Connect(ctx, core.SocketPath(filepath.Join(volume, "one")), 3*time.Second, proto.Hello{RotatingPassword: true})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	off, err := nodeclient.Connect(ctx, core.SocketPath(filepath.Join(volume, "off")), 3*time.Second, proto.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()
	update, err := one.Request(&proto.PasswordQuery{}, 3*time.Second, func(m proto.Message) bool { _, ok := m.(*proto.PasswordUpdate); return ok })
	if err != nil {
		t.Fatal(err)
	}
	nextAt := update.(*proto.PasswordUpdate).NextAt
	if nextAt == "" {
		t.Fatal("missing assignment")
	}
	if err := off.Send(&proto.PasswordRestart{NextAt: nextAt}); err != nil {
		t.Fatal(err)
	}
	if err := one.Send(&proto.PasswordRestart{NextAt: nextAt}); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-requests:
		if path != "/api/servers/one/power" {
			t.Fatal("restart escaped its opted-in socket server")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wings never received the rotation restart")
	}
	for i := 0; i < 10; i++ {
		if err := one.Send(&proto.PasswordRestart{NextAt: nextAt}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case path := <-requests:
		t.Fatal("unenrolled or duplicate restart reached Wings:", path)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDiscordFirstRotationRetryAndRestart(t *testing.T) {
	cfg := testConfig(t, "shared")
	cfg.Interval = "0 4 * * *"
	cfg.Timezone = "America/Chicago"
	s, _ := Open(cfg)
	now := time.Now()
	a, _, _ := s.Get("one", now)
	var mu sync.Mutex
	fail := true
	var bodies []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, string(b))
		if fail {
			w.WriteHeader(500)
			w.Write([]byte(a.Password))
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	logbuf := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logbuf, nil))
	m := New(log, srv.Client())
	m.store = s
	m.cfg = cfg
	n := nodeconfig.Default()
	n.SetSection("alerts", alerts.Config{Enabled: true, Webhook: srv.URL, Events: []string{"crash"}, Name: "test-node"})
	c := core.New(nil, n, nil, log)
	m.notify(context.Background(), c)
	if len(s.Pending()) != 1 {
		t.Fatal("failed post must remain pending")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	s, _ = Open(cfg)
	m.store = s
	m.notify(context.Background(), c)
	if len(s.Pending()) != 0 {
		t.Fatal("successful retry not persisted")
	}
	m.notify(context.Background(), c)
	b, _, _ := s.Get("two", a.NextAt)
	m.notify(context.Background(), c)
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 || !strings.Contains(bodies[1], a.Password) || !strings.Contains(bodies[2], b.Password) {
		t.Fatal("first/retry/rotation posts missing or duplicated")
	}
	if !strings.Contains(bodies[1], "0 4 * * * (America/Chicago)") {
		t.Fatal("notification omitted the cron timezone")
	}
	if strings.Contains(logbuf.String(), a.Password) {
		t.Fatal("webhook failure leaked credential")
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func TestAssignmentDeliveryCannotOvertakeOlderReply(t *testing.T) {
	cfg := testConfig(t, "unique")
	s, _ := Open(cfg)
	now := time.Now()
	a, _, _ := s.Get("one", now)
	m := New(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	m.store = s
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	secondDone := make(chan struct{})
	var mu sync.Mutex
	var sent []string
	go func() {
		m.withAssignment("one", now, func(r Record, err error) {
			close(started)
			<-release
			mu.Lock()
			sent = append(sent, r.Password)
			mu.Unlock()
		})
		close(done)
	}()
	<-started
	go func() {
		m.withAssignment("one", a.NextAt, func(r Record, err error) { mu.Lock(); sent = append(sent, r.Password); mu.Unlock() })
		close(secondDone)
	}()
	select {
	case <-secondDone:
		close(release)
		t.Fatal("new reply overtook the paused old reply")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	<-secondDone
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || sent[0] != a.Password || sent[1] == a.Password {
		t.Fatal("reply order is incorrect")
	}
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
