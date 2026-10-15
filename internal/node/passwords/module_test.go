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

func TestDiscordFirstRotationRetryAndRestart(t *testing.T) {
	cfg := testConfig(t, "shared")
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
