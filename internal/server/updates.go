// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/steam"
	"github.com/s2script/s2script-cs2-egg/internal/updatepolicy"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var errUpdateRestart = errors.New("private standalone update request")

type updateSession struct {
	mu                sync.Mutex
	steamMu           sync.Mutex
	active            bool
	cfg               config.Server
	adapter           bool
	latest            func(context.Context, string) (string, error)
	installed, target string
	pending, execute  time.Time
	warnings          map[time.Duration]bool
	request           chan struct{}
	requested         bool
	veto              atomic.Bool
}

func newUpdateSession(c config.Server, valid bool) *updateSession {
	return &updateSession{cfg: c, adapter: valid, latest: steam.LatestBuild, request: make(chan struct{}, 1), warnings: map[time.Duration]bool{}}
}
func (u *updateSession) pendingBuild() string { u.mu.Lock(); defer u.mu.Unlock(); return u.target }
func (u *updateSession) check(ctx context.Context) {
	u.steamMu.Lock()
	defer u.steamMu.Unlock()
	u.mu.Lock()
	active := u.active && u.installed != "" && !u.requested
	u.mu.Unlock()
	if !active {
		return
	}
	build, e := u.latest(ctx, filepath.Join(u.cfg.Root, "steamcmd/steamcmd.sh"))
	if e != nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.installed == "" || build == u.installed {
		return
	}
	if u.target != build {
		u.target = build
		u.pending = time.Now().UTC()
		u.execute = time.Time{}
		u.warnings = map[time.Duration]bool{}
	}
}
func (u *updateSession) observe(s Status, w io.Writer) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.active = true
	u.installed = s.Versions["steam_build"]
	if s.Ready && u.target != "" && u.installed == u.target {
		u.target = ""
		u.requested = false
		u.execute = time.Time{}
	}
	if u.target == "" || !u.adapter || u.veto.Load() || u.requested {
		return
	}
	now := time.Now().UTC()
	p := updatepolicy.FromConfig(u.cfg.Updates.Policy)
	o := updatepolicy.Observation{RunID: s.RunID, Ready: s.Ready, SampleAt: s.SampleAt, ReceivedAt: s.SampleAt, Players: updatepolicy.Players{Known: s.Players.Known, Count: s.Players.Count}}
	if !updatepolicy.Fresh(now, o, p.Freshness) || !updatepolicy.Eligible(now, u.pending, o, p) {
		u.execute = time.Time{}
		return
	}
	if u.execute.IsZero() {
		u.execute = now.Add(updatepolicy.Countdown(p))
		deadline := u.pending.Add(p.MaximumDelay)
		if u.execute.After(deadline) {
			u.execute = deadline
		}
	}
	for _, offset := range p.WarningOffsets {
		if !u.warnings[offset] && !now.Before(u.execute.Add(-offset)) {
			u.warnings[offset] = true
			if w != nil {
				remaining := int(u.execute.Sub(now).Seconds())
				if remaining < 0 {
					remaining = 0
				}
				fmt.Fprintf(w, "say Server update restart in %d seconds\n", remaining)
			}
		}
	}
	if !now.Before(u.execute) && !u.veto.Load() {
		u.requested = true
		select {
		case u.request <- struct{}{}:
		default:
		}
	}
}

// inputBroker owns input once. Child-specific queues are discarded on detach.
// Operator intent is latched before delivery, including while no child is attached.
type inputBroker struct {
	mu         sync.Mutex
	child      *brokerChild
	done       chan struct{}
	closeInput func()
	onStop     func()
}
type brokerChild struct {
	lines    chan string
	done     chan struct{}
	r        *io.PipeReader
	w        *io.PipeWriter
	finished chan struct{}
	once     sync.Once
}

func newInputBroker(in io.Reader, onStop func()) (*inputBroker, error) {
	reader, closeInput, e := inputReader(in)
	if e != nil {
		return nil, e
	}
	b := &inputBroker{done: make(chan struct{}), closeInput: closeInput, onStop: onStop}
	go func() {
		defer close(b.done)
		if reader == nil {
			return
		}
		scan := bufio.NewScanner(reader)
		scan.Buffer(make([]byte, 4096), 64<<10)
		for scan.Scan() {
			line := scan.Text()
			if strings.EqualFold(strings.TrimSpace(line), "quit") || strings.EqualFold(strings.TrimSpace(line), "exit") {
				onStop()
			}
			b.mu.Lock()
			c := b.child
			b.mu.Unlock()
			if c != nil {
				select {
				case c.lines <- line:
				case <-c.done:
				default:
				}
			}
		}
	}()
	return b, nil
}
func (b *inputBroker) attach() (*io.PipeReader, func()) {
	r, w := io.Pipe()
	c := &brokerChild{lines: make(chan string, 32), done: make(chan struct{}), r: r, w: w, finished: make(chan struct{})}
	b.mu.Lock()
	b.child = c
	b.mu.Unlock()
	go func() {
		defer close(c.finished)
		for {
			select {
			case <-c.done:
				return
			case line := <-c.lines:
				if _, e := io.WriteString(w, line+"\n"); e != nil {
					return
				}
			}
		}
	}()
	return r, func() {
		c.once.Do(func() {
			b.mu.Lock()
			if b.child == c {
				b.child = nil
			}
			b.mu.Unlock()
			close(c.done)
			r.Close()
			w.Close()
			<-c.finished
		})
	}
}
func (b *inputBroker) close() { b.closeInput(); <-b.done }
func runSupervisor(ctx context.Context, cfg config.Server, sink events.Sink, stdin io.Reader, stdout, stderr io.Writer, o runOptions, provider GameProvider) (int, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	signalDone := make(chan struct{})
	go func() {
		defer close(signalDone)
		select {
		case s := <-signals:
			cancel(signalCause{s.(syscall.Signal)})
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(nil); <-signalDone }()
	release, e := runtimelease.Acquire(cfg.Root)
	if e != nil {
		return 1, e
	}
	defer release()
	valid := cfg.Updates.Adapter == "supervisor" && provider == nil
	sharedTree := provider != nil
	for _, name := range []string{".s2shared.json", ".s2shared-pending.json"} {
		if _, e := os.Lstat(filepath.Join(cfg.Root, name)); !errors.Is(e, os.ErrNotExist) {
			sharedTree = true
		}
	}
	valid = valid && !sharedTree
	u := newUpdateSession(cfg, valid)
	if o.latest != nil {
		u.latest = o.latest
	}
	prepare := o.prepare
	o.prepare = func(ctx context.Context, c config.Server, emit emitFunc) (map[string]string, error) {
		u.steamMu.Lock()
		defer u.steamMu.Unlock()
		versions, err := prepare(ctx, c, emit)
		if err == nil {
			u.prepared(versions)
		}
		return versions, err
	}
	broker, e := newInputBroker(stdin, func() { u.veto.Store(true); cancel(errors.New("operator console stop")) })
	if e != nil {
		return 1, e
	}
	defer broker.close()
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		if sharedTree {
			return
		}
		interval := time.Duration(cfg.Updates.PollSeconds) * time.Second
		if o.updatePoll > 0 {
			interval = o.updatePoll
		}
		if interval <= 0 {
			interval = time.Second
		}
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				queryCtx, stop := context.WithTimeout(ctx, 30*time.Second)
				u.check(queryCtx)
				stop()
			}
		}
	}()
	defer func() { cancel(nil); <-pollDone }()
	o.externalLease = true
	o.externalSignals = true
	o.updates = u
	for {
		if e := ctx.Err(); e != nil {
			return 0, nil
		}
		input, detach := broker.attach()
		runCtx, stopRun := context.WithCancelCause(ctx)
		requestDone := make(chan struct{})
		go func() {
			defer close(requestDone)
			select {
			case <-u.request:
				if !u.veto.Load() && ctx.Err() == nil {
					stopRun(errUpdateRestart)
				}
			case <-runCtx.Done():
			}
		}()
		code, err := runWith(runCtx, cfg, sink, input, stdout, stderr, o)
		u.mu.Lock()
		u.active = false
		u.mu.Unlock()
		cause := context.Cause(runCtx)
		stopRun(nil)
		<-requestDone
		detach()
		if cause != errUpdateRestart || u.veto.Load() || ctx.Err() != nil {
			return code, err
		}
		cfg.Steam.Update = true
	}
}

// Steam may advance between metadata polling and installation. Preparation's
// verified installed manifest becomes the target before the new child starts.
func (u *updateSession) prepared(versions map[string]string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.requested && versions["steam_build"] != "" {
		u.target = versions["steam_build"]
	}
}
