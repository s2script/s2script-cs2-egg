// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"context"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/crashes"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/privateops"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type helperProgressHost struct {
	recoveryHost
	helper *node.PrivateHelper
}

func (h *helperProgressHost) Servers() []node.Server {
	return []node.Server{{ID: "a", Helper: h.helper}}
}

func TestObservationsContinueDuringBlockedOperations(t *testing.T) {
	for _, kind := range []string{"cleanup", "helper"} {
		t.Run(kind, func(t *testing.T) {
			m, r, _ := recoveryFixture(t)
			m.logs, _ = NewLogs(m.Root, LogOptions{}, nil)
			defer m.logs.Close()
			m.crashes, _ = crashes.New(m.Root, crashes.Options{}, nil)
			observations := make(chan node.Observation, 8)
			m.sub = &node.Subscription{Events: observations}
			m.done = make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var release func()
			if kind == "cleanup" {
				var e error
				release, e = storeio.Lock(ctx, filepath.Join(m.Root, "a", "logs", ".plans"))
				if e != nil {
					t.Fatal(e)
				}
			} else {
				base, e := os.MkdirTemp("", "s2obs-")
				if e != nil {
					t.Fatal(e)
				}
				defer os.RemoveAll(base)
				socket := filepath.Join(base, "s")
				l, e := net.Listen("unix", socket)
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
				gate := make(chan struct{})
				helperDone := make(chan struct{})
				release = func() { close(gate); <-helperDone }
				go func() {
					defer close(helperDone)
					conn, e := l.Accept()
					if e != nil {
						return
					}
					defer conn.Close()
					if _, e = protocol.Read(conn); e != nil {
						return
					}
					<-gate
					(volumehelper.Session{Conn: conn, Family: "operations"}).Write("result", privateops.Reply{State: "failed", Error: "fixture refused"})
				}()
				m.host = &helperProgressHost{helper: &node.PrivateHelper{Socket: socket, UID: uint32(os.Geteuid())}}
				r.Private = true
			}
			released := false
			defer func() {
				if !released {
					release()
				}
			}()
			executed := make(chan struct{})
			input, _ := json.Marshal(r)
			go func() { defer close(executed); m.execute(ctx, "a", input) }()
			key := storeio.Hash([]byte("a\x00" + r.RequestID))
			deadline := time.Now().Add(time.Second)
			for {
				var o outcome
				if storeio.JSON(filepath.Join(m.Root, "outcomes"), key+".json", &o) == nil && o.Phase == "executing" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("operation did not start")
				}
				time.Sleep(time.Millisecond)
			}
			go m.run(ctx)
			// Let the periodic outbox attempt run while the action still holds opMu.
			time.Sleep(1200 * time.Millisecond)
			observations <- node.Observation{Server: &node.Server{ID: "a"}, Replay: "first_seen_window", Event: events.Event{ID: "console", RunID: "run", Type: "server.console", Details: map[string]string{"line": "console during blocked operation"}}}
			observations <- node.Observation{Server: &node.Server{ID: "a"}, Replay: "first_seen_window", Event: events.Event{ID: "crash", RunID: "run", Type: "server.crash", Reason: "unexpected_exit", At: time.Now().UTC()}}
			deadline = time.Now().Add(2 * time.Second)
			progress := false
			for time.Now().Before(deadline) {
				bundles, _ := filepath.Glob(filepath.Join(m.Root, "a", "crashes", "*.json"))
				captured := false
				for _, bundle := range bundles {
					if !strings.HasPrefix(filepath.Base(bundle), ".") {
						data, _ := os.ReadFile(bundle)
						if strings.Contains(string(data), "console during blocked operation") {
							captured = true
						}
					}
				}
				if strings.Contains(string(m.logs.Tail("a")), "console during blocked operation") && captured {
					progress = true
					break
				}
				time.Sleep(time.Millisecond)
			}
			// Neither observer handling nor recovery may mark the live action terminal.
			var active outcome
			storeio.JSON(filepath.Join(m.Root, "outcomes"), key+".json", &active)
			release()
			released = true
			<-executed
			cancel()
			<-m.done
			if !progress {
				t.Fatal("console/crash consumption stopped behind long operation/outbox")
			}
			if active.Phase != "executing" {
				t.Fatal("outbox recovered a still-active action", active.Phase)
			}
		})
	}
}
