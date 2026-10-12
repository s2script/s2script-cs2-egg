// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
)

type recordSink struct{ ch chan events.Event }

func (s *recordSink) Publish(ctx context.Context, e events.Event) error {
	select {
	case s.ch <- e:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func nextEvent(t *testing.T, s *recordSink, kind string) events.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-s.ch:
			if e.Type == kind {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s event", kind)
		}
	}
}
func fixtureConfig(t *testing.T, mode string) (config.Server, runOptions) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "game"), 0755)
	exe, _ := os.Executable()
	script := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestGameFixture$ -- \"$@\"\n", exe)
	os.WriteFile(filepath.Join(root, "game/cs2.sh"), []byte(script), 0755)
	t.Setenv("S2EGG_TASK6_FIXTURE", mode)
	// Race-instrumented fixture processes otherwise sleep for one second on a
	// successful exit, which is unrelated to the supervisor's short test grace.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	port, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	p := port.LocalAddr().(*net.UDPAddr).Port
	port.Close()
	cfg, e := config.LoadServer(root, func(k string) (string, bool) { return "1", k == "ALLOW_TOKENLESS" })
	if e != nil {
		t.Fatal(e)
	}
	cfg.Launch.IP = "127.0.0.1"
	cfg.Launch.Port = p
	opts := runOptions{prepare: func(ctx context.Context, c config.Server, emit emitFunc) (map[string]string, error) {
		return map[string]string{"s2script": "3.2.1"}, nil
	}, poll: 20 * time.Millisecond, timeout: 250 * time.Millisecond, shutdown: 100 * time.Millisecond}
	return cfg, opts
}
func TestGameFixture(t *testing.T) {
	mode := os.Getenv("S2EGG_TASK6_FIXTURE")
	if mode == "" {
		return
	}
	root := os.Getenv("HOME")
	cwd, _ := os.Getwd()
	os.WriteFile(filepath.Join(root, "spawned"), []byte(cwd+"\n"+strings.Join(os.Args, "\n")), 0600)
	if mode == "fail" {
		os.Exit(7)
	}
	if mode == "group" {
		c := exec.Command("sh", "-c", "trap '' TERM INT; while :; do sleep 1; done")
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if e := c.Start(); e != nil {
			os.Exit(3)
		}
		os.WriteFile(filepath.Join(root, "grandchild"), []byte(strconv.Itoa(c.Process.Pid)), 0600)
	}
	if mode != "unready" {
		port := ""
		for i, a := range os.Args {
			if a == "-port" && i+1 < len(os.Args) {
				port = os.Args[i+1]
			}
		}
		c, e := net.ListenPacket("udp4", "127.0.0.1:"+port)
		if e != nil {
			os.Exit(4)
		}
		defer c.Close()
		go func() {
			b := make([]byte, 4096)
			for {
				_, addr, e := c.ReadFrom(b)
				if e != nil {
					return
				}
				if _, e := os.Stat(filepath.Join(root, "query-fail")); e == nil {
					c.WriteTo([]byte("bad packet"), addr)
				} else {
					c.WriteTo(infoPacket(), addr)
				}
			}
		}()
	}
	fmt.Print("banner is not readiness\nprivate-")
	fmt.Print("token https://discord.com/api/webhooks/123/private-hook\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println("echo:" + line)
		if line == "flood" {
			for i := 0; i < 10000; i++ {
				fmt.Println("fixture raw console flood")
			}
		}
		if line == "crash" {
			os.Exit(9)
		}
		if line == "quit" {
			os.Exit(0)
		}
	}
	select {}
}
func TestPrepareBeforeProcessSpawn(t *testing.T) {
	cfg, o := fixtureConfig(t, "fail")
	o.prepare = func(ctx context.Context, c config.Server, emit emitFunc) (map[string]string, error) {
		if _, e := os.Stat(filepath.Join(c.Root, "spawned")); !os.IsNotExist(e) {
			t.Error("spawned during prepare")
		}
		if _, e := runtimelease.Acquire(c.Root); !errors.Is(e, runtimelease.ErrHeld) {
			t.Error("runtime lease missing during prepare")
		}
		return nil, errors.New("fixture preparation failure")
	}
	s := &recordSink{make(chan events.Event, 64)}
	code, e := runWith(context.Background(), cfg, s, nil, io.Discard, io.Discard, o)
	if e == nil || code == 0 {
		t.Fatal("preparation failure accepted")
	}
	if _, e = os.Stat(filepath.Join(cfg.Root, "spawned")); !os.IsNotExist(e) {
		t.Fatal("spawned after failed preparation")
	}
	nextEvent(t, s, "server.start_failed")
}
func TestConsoleInputForwarding(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	cfg.Steam.GSLT = "private-token"
	cfg.Webhook.URL = "https://discord.com/api/webhooks/123/private-hook"
	r, w := io.Pipe()
	s := &recordSink{make(chan events.Event, 128)}
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() { code, _ := runWith(context.Background(), cfg, s, r, &out, &out, o); done <- code }()
	up := nextEvent(t, s, "server.up")
	if up.RunID == "" || up.Version != 1 || up.ID == "" {
		t.Fatal("missing event identity")
	}
	status, e := Snapshot(context.Background(), cfg.Root)
	if e != nil || !status.Ready || !status.Players.Known || status.Players.Count != 3 || status.Versions["s2script"] != "3.2.1" {
		t.Fatalf("status %+v %v", status, e)
	}
	fmt.Fprintln(w, "status")
	fmt.Fprintln(w, "quit")
	w.Close()
	down := nextEvent(t, s, "server.down")
	if down.Reason != "console_stop" {
		t.Fatalf("reason %s", down.Reason)
	}
	if code := <-done; code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "echo:status") || strings.Contains(out.String(), "private-token") || strings.Contains(out.String(), "private-hook") {
		t.Fatalf("console output: %s", out.String())
	}
	for len(s.ch) > 0 {
		e := <-s.ch
		if strings.Contains(fmt.Sprint(e), "private-") {
			t.Fatal("event leaked credential")
		}
		if e.Type == "server.up" || e.Type == "server.down" {
			t.Fatal("duplicate lifecycle event")
		}
	}
	b, _ := os.ReadFile(filepath.Join(cfg.Root, "spawned"))
	if !strings.HasPrefix(string(b), filepath.Join(cfg.Root, "game")+"\n") || !strings.Contains(string(b), "-dedicated\n") || !strings.Contains(string(b), "+map\nde_dust2") {
		t.Fatalf("launch contract %s", b)
	}
	if _, e := Snapshot(context.Background(), cfg.Root); !errors.Is(e, ErrNotRunning) {
		t.Fatalf("stale status accepted: %v", e)
	}
}
func TestReadinessGatesUpEvent(t *testing.T) {
	cfg, o := fixtureConfig(t, "unready")
	s := &recordSink{make(chan events.Event, 64)}
	code, e := runWith(context.Background(), cfg, s, nil, io.Discard, io.Discard, o)
	if code == 0 || e == nil {
		t.Fatal("unready startup succeeded")
	}
	failed := nextEvent(t, s, "server.start_failed")
	if failed.Reason != "readiness_timeout" {
		t.Fatal(failed.Reason)
	}
	for len(s.ch) > 0 {
		e := <-s.ch
		if e.Type == "server.up" || e.Type == "server.down" {
			t.Fatal("lifecycle before readiness")
		}
	}
}
func TestFailedStartupEmitsNoUp(t *testing.T) {
	cfg, o := fixtureConfig(t, "fail")
	s := &recordSink{make(chan events.Event, 64)}
	code, e := runWith(context.Background(), cfg, s, nil, io.Discard, io.Discard, o)
	if code != 7 || e == nil {
		t.Fatalf("%d %v", code, e)
	}
	nextEvent(t, s, "server.start_failed")
	for len(s.ch) > 0 {
		if e := <-s.ch; e.Type == "server.up" {
			t.Fatal("up before failure")
		}
	}
}
func TestShutdownReapsProcessGroup(t *testing.T) {
	cfg, o := fixtureConfig(t, "group")
	s := &recordSink{make(chan events.Event, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r, w := io.Pipe()
	defer w.Close()
	go func() { runWith(ctx, cfg, s, r, io.Discard, io.Discard, o); close(done) }()
	nextEvent(t, s, "server.up")
	b, e := os.ReadFile(filepath.Join(cfg.Root, "grandchild"))
	if e != nil {
		t.Fatal(e)
	}
	pid, _ := strconv.Atoi(string(b))
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown hung")
	}
	if e = syscall.Kill(pid, 0); !errors.Is(e, syscall.ESRCH) {
		t.Fatalf("child exists (including zombie): %v", e)
	}
	release, e := runtimelease.Acquire(cfg.Root)
	if e != nil {
		t.Fatal(e)
	}
	release()
}

type endlessReader struct{}

func (endlessReader) Read([]byte) (int, error) { select {} }
func TestUncancelableInputRejectedBeforePreparation(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	o.prepare = func(context.Context, config.Server, emitFunc) (map[string]string, error) {
		t.Fatal("prepared unsupported input")
		return nil, nil
	}
	if code, e := runWith(context.Background(), cfg, nil, endlessReader{}, io.Discard, io.Discard, o); code == 0 || e == nil {
		t.Fatal("accepted uninterruptible reader")
	}
}

func TestStableIdentityAndCorrelation(t *testing.T) {
	cfg, o := fixtureConfig(t, "fail")
	ids := []string{}
	runs := []string{}
	for n := 0; n < 2; n++ {
		s := &recordSink{make(chan events.Event, 64)}
		runWith(context.Background(), cfg, s, nil, io.Discard, io.Discard, o)
		e := nextEvent(t, s, "server.start_failed")
		if e.ServerID == "" || e.CorrelationID != e.RunID || e.Severity != "error" {
			t.Fatalf("incomplete event: %+v", e)
		}
		ids = append(ids, e.ServerID)
		runs = append(runs, e.RunID)
	}
	if ids[0] != ids[1] || runs[0] == runs[1] {
		t.Fatal("identity not stable/run identity reused")
	}
}
func TestStatusDoesNotTrustStaleSocketOrPID(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "server.pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
	c, e := net.Listen("unix", filepath.Join(root, ".s2egg-status.sock"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if _, e := Snapshot(context.Background(), root); !errors.Is(e, ErrNotRunning) {
		t.Fatal("stale socket counted as managed runtime")
	}
}
func TestFileInputCancellationPreservesCallerFD(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	flags, _, _ := syscall.Syscall(syscall.SYS_FCNTL, r.Fd(), syscall.F_GETFL, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s := &recordSink{make(chan events.Event, 64)}
	go func() { runWith(ctx, cfg, s, r, io.Discard, io.Discard, o); close(done) }()
	nextEvent(t, s, "server.up")
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("file input read did not cancel")
	}
	after, _, errno := syscall.Syscall(syscall.SYS_FCNTL, r.Fd(), syscall.F_GETFL, 0)
	if errno != 0 || flags != after {
		t.Fatal("caller descriptor closed or flags changed")
	}
	w.Write([]byte("x"))
	b := make([]byte, 1)
	if _, e = r.Read(b); e != nil || b[0] != 'x' {
		t.Fatal("caller input unavailable after shutdown")
	}
}

func TestReadyMarkerOnlyAfterReadiness(t *testing.T) {
	for _, mode := range []string{"ready", "fail", "unready"} {
		t.Run(mode, func(t *testing.T) {
			cfg, o := fixtureConfig(t, mode)
			var out bytes.Buffer
			s := &recordSink{make(chan events.Event, 64)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { runWith(ctx, cfg, s, nil, &out, io.Discard, o); close(done) }()
			if mode == "ready" {
				nextEvent(t, s, "server.up")
				cancel()
			}
			<-done
			count := strings.Count(out.String(), "S2EGG_READY\n")
			if (mode == "ready" && count != 1) || (mode != "ready" && count != 0) {
				t.Fatalf("marker count %d", count)
			}
		})
	}
}
func TestManagedArgumentsRejectOverridesBeforePreparation(t *testing.T) {
	for _, arg := range []string{"-port", "-PORT=9999", "+hostport", "+hostport=9999", "-ip", "+ip=0.0.0.0", "+sv_setsteamaccount", "+sv_setsteamaccount=secret"} {
		t.Run(arg, func(t *testing.T) {
			cfg, o := fixtureConfig(t, "fail")
			cfg.Launch.ExtraArgs = []string{arg, "9999"}
			o.prepare = func(context.Context, config.Server, emitFunc) (map[string]string, error) {
				t.Error("prepared conflicting allocation")
				return nil, errors.New("stop")
			}
			if code, e := runWith(context.Background(), cfg, nil, nil, io.Discard, io.Discard, o); code == 0 || e == nil {
				t.Fatal("accepted reserved argument")
			}
		})
	}
}
func TestSignalDuringPreparationCancelsAndReleasesLease(t *testing.T) {
	if root := os.Getenv("S2EGG_TASK6_SIGNAL_ROOT"); root != "" {
		cfg := config.Server{Root: root}
		o := runOptions{prepare: func(ctx context.Context, c config.Server, emit emitFunc) (map[string]string, error) {
			os.WriteFile(filepath.Join(root, "preparing"), []byte("yes"), 0600)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		_, e := runWith(context.Background(), cfg, nil, nil, io.Discard, io.Discard, o)
		if e != nil {
			os.Exit(0)
		}
		os.Exit(9)
	}
	root := t.TempDir()
	c := exec.Command(os.Args[0], "-test.run=^TestSignalDuringPreparationCancelsAndReleasesLease$")
	c.Env = append(os.Environ(), "S2EGG_TASK6_SIGNAL_ROOT="+root)
	if e := c.Start(); e != nil {
		t.Fatal(e)
	}
	defer c.Process.Kill()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(root, "preparing")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preparation not entered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.Process.Signal(syscall.SIGTERM)
	if e := c.Wait(); e != nil {
		t.Fatalf("preparation was terminated instead of canceled: %v", e)
	}
	release, e := runtimelease.Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	release()
}

func TestQueryFailureMakesPlayersUnknown(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &recordSink{make(chan events.Event, 64)}
	done := make(chan struct{})
	go func() { runWith(ctx, cfg, s, nil, io.Discard, io.Discard, o); close(done) }()
	nextEvent(t, s, "server.up")
	os.WriteFile(filepath.Join(cfg.Root, "query-fail"), []byte("yes"), 0600)
	for {
		e := nextEvent(t, s, "server.status")
		if e.Reason == "query_failed" {
			break
		}
	}
	status, e := Snapshot(context.Background(), cfg.Root)
	if e != nil || status.Players.Known || status.Ready || status.State != "unresponsive" {
		t.Fatalf("stale status: %+v %v", status, e)
	}
	cancel()
	<-done
}
func TestConsoleRedactsWebhookTokenAndReservedMarker(t *testing.T) {
	cfg := config.Server{}
	cfg.Steam.GSLT = "private-token"
	cfg.Webhook.URL = "https://discord.com/api/webhooks/123/private-hook"
	var out bytes.Buffer
	var mu sync.Mutex
	raw := []string{}
	w := &consoleWriter{mu: &mu, out: &out, redact: redactFor(cfg), emit: func(kind, reason string, d map[string]string) { raw = append(raw, d["line"]) }}
	w.Write([]byte("private-"))
	w.Write([]byte("token private-hook\nS2EGG_READY\n"))
	w.flush()
	if strings.Contains(out.String(), "private-") || strings.Contains(out.String(), "S2EGG_READY") {
		t.Fatalf("unsafe display %q", out.String())
	}
	if len(raw) != 2 || raw[1] != "S2EGG_READY" || strings.Contains(raw[0], "private-") {
		t.Fatalf("raw events %q", raw)
	}
}

func TestLiveStatusRedactsCredentials(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	cfg.Steam.GSLT = "de_dust2"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	s := &recordSink{make(chan events.Event, 64)}
	go func() { runWith(ctx, cfg, s, nil, io.Discard, io.Discard, o); close(done) }()
	nextEvent(t, s, "server.up")
	status, e := Snapshot(context.Background(), cfg.Root)
	if e != nil || status.Map != "[REDACTED]" {
		t.Fatalf("status leaked credential: %+v %v", status, e)
	}
}

func TestUnexpectedExitAfterReadyIsDistinct(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	s := &recordSink{make(chan events.Event, 64)}
	r, w := io.Pipe()
	defer w.Close()
	done := make(chan int, 1)
	go func() { code, _ := runWith(context.Background(), cfg, s, r, io.Discard, io.Discard, o); done <- code }()
	nextEvent(t, s, "server.up")
	fmt.Fprintln(w, "crash")
	down := nextEvent(t, s, "server.down")
	if down.Reason != "unexpected_exit" {
		t.Fatal(down.Reason)
	}
	nextEvent(t, s, "server.crash")
	if code := <-done; code != 9 {
		t.Fatal(code)
	}
}
func TestAllocationConflictNeverSpawns(t *testing.T) {
	cfg, o := fixtureConfig(t, "ready")
	c, e := net.ListenPacket("udp4", net.JoinHostPort(cfg.Launch.IP, strconv.Itoa(cfg.Launch.Port)))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	s := &recordSink{make(chan events.Event, 64)}
	if _, e = runWith(context.Background(), cfg, s, nil, io.Discard, io.Discard, o); e == nil {
		t.Fatal("occupied allocation accepted")
	}
	if _, e = os.Stat(filepath.Join(cfg.Root, "spawned")); !os.IsNotExist(e) {
		t.Fatal("spawned over occupied allocation")
	}
	failure := nextEvent(t, s, "server.start_failed")
	if failure.Reason != "allocation_unavailable" {
		t.Fatal(failure.Reason)
	}
}
func TestShutdownDoesNotReapUnrelatedProcess(t *testing.T) {
	sibling := exec.Command("sh", "-c", "sleep 0.1; exit 7")
	if e := sibling.Start(); e != nil {
		t.Fatal(e)
	}
	cfg, o := fixtureConfig(t, "group")
	s := &recordSink{make(chan events.Event, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runWith(ctx, cfg, s, nil, io.Discard, io.Discard, o); close(done) }()
	nextEvent(t, s, "server.up")
	cancel()
	<-done
	var e *exec.ExitError
	if !errors.As(sibling.Wait(), &e) || e.ExitCode() != 7 {
		t.Fatal("unrelated child reaped by supervisor")
	}
}
