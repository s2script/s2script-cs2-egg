// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Console actions are typed. Implementations cannot submit arbitrary commands.
type ConsoleAction struct {
	Kind, ServerID, RunID, SessionID, Reason string
	UID                                      int
	At                                       time.Time
}
type ConsoleGuard interface {
	Observe(time.Time, string, string, string) []ConsoleAction
	Valid(ConsoleAction, time.Time) bool
}
type commandWriter struct {
	mu sync.Mutex
	f  *os.File
}

func (w *commandWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.f.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer w.f.SetWriteDeadline(time.Time{})
	return w.f.Write(b)
}

type consoleGuardLoop struct {
	guard       ConsoleGuard
	server, run string
	queue       chan ConsoleAction
	cancel      context.CancelFunc
	done        chan struct{}
	emit        emitFunc
}

func newConsoleGuard(ctx context.Context, g ConsoleGuard, server, run string, w io.Writer, emit emitFunc) *consoleGuardLoop {
	if g == nil {
		return nil
	}
	life, cancel := context.WithCancel(ctx)
	c := &consoleGuardLoop{guard: g, server: server, run: run, queue: make(chan ConsoleAction, 32), cancel: cancel, done: make(chan struct{}), emit: emit}
	go func() {
		defer close(c.done)
		for {
			select {
			case <-life.Done():
				return
			case a := <-c.queue:
				if a.Kind != "kick" || a.RunID != run || a.ServerID != server || a.UID < 1 || a.UID > 999999999 || !g.Valid(a, time.Now()) {
					emit("guard", "stale_action_refused", map[string]string{"action": "kick"})
					continue
				}
				_, e := fmt.Fprintf(w, "kickid %d\n", a.UID)
				state := "submitted"
				if e != nil {
					state = "failed_or_uncertain"
				}
				emit("guard", a.Reason, map[string]string{"action": "kick", "state": state, "userid": itoa(a.UID)})
			}
		}
	}()
	return c
}
func (c *consoleGuardLoop) observe(line string) {
	if c == nil {
		return
	}
	for _, a := range c.guard.Observe(time.Now(), c.server, c.run, line) {
		if a.Kind == "log" {
			c.emit("guard", a.Reason, map[string]string{"action": "log"})
			continue
		}
		select {
		case c.queue <- a:
		default:
			c.emit("guard", "action_queue_full", map[string]string{"action": "refused"})
		}
	}
}
func (c *consoleGuardLoop) close() {
	if c != nil {
		c.cancel()
		<-c.done
	}
}
