// SPDX-License-Identifier: GPL-3.0-or-later
// Package server supervises a Linux CS2 process group under a runtime lease.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/processgroup"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
)

type emitFunc func(string, string, map[string]string)
type runOptions struct {
	consoleGuard    ConsoleGuard
	latest          func(context.Context, string) (string, error)
	updatePoll      time.Duration
	control         ControlProvider
	externalLease   bool
	externalSignals bool
	updates         *updateSession

	prepare                 func(context.Context, config.Server, emitFunc) (map[string]string, error)
	poll, timeout, shutdown time.Duration
}

func itoa(n int) string { return strconv.Itoa(n) }
func randomID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("system entropy unavailable")
	}
	return hex.EncodeToString(b[:])
}
func redactFor(cfg config.Server) func(string) string {
	secrets := redaction.Secrets(cfg.Steam.GSLT, cfg.Webhook.URL)
	return func(s string) string { return redaction.Redact(s, secrets) }
}

func launchArgs(c config.Server) []string {
	a := []string{"-dedicated", "-console", "-usercon", "-ip", c.Launch.IP, "-port", itoa(c.Launch.Port), "-maxplayers", itoa(c.Launch.MaxPlayers), "+game_type", itoa(c.Launch.GameType), "+game_mode", itoa(c.Launch.GameMode), "+map", c.Launch.Map}
	a = append(a, c.Launch.ExtraArgs...)
	if c.Steam.GSLT != "" {
		a = append(a, "+sv_setsteamaccount", c.Steam.GSLT)
	}
	return a
}
func homeEnv(root string) []string {
	env := []string{}
	for _, s := range os.Environ() {
		if !strings.HasPrefix(s, "GAME_DEBUGGER=") && !strings.HasPrefix(s, "GDB_DEBUG_PORT=") && !strings.HasPrefix(s, "HOME=") && !strings.HasPrefix(s, "STEAM_ACC=") && !strings.HasPrefix(s, "DISCORD_WEBHOOK_URL=") {
			env = append(env, s)
		}
	}
	return append(env, "HOME="+root)
}

// Run owns closable stdin readers until return. File inputs are reopened so the
// caller's descriptor remains untouched. Sink must honor Publish cancellation.
// Linux child-subreaper mode is process-wide; reaping is restricted to this PGID.
func Run(ctx context.Context, cfg config.Server, sink events.Sink, stdin io.Reader, stdout, stderr io.Writer, options Options) (int, error) {
	// Preparation and asynchronous event diagnostics may share an output sink.
	var ioMu sync.Mutex
	if options.PreparationLog == nil {
		options.PreparationLog = stdout
	}
	if options.PreparationLog != nil {
		options.PreparationLog = &lockedWriter{mu: &ioMu, w: options.PreparationLog}
	}
	if stdout != nil {
		stdout = &lockedWriter{mu: &ioMu, w: stdout}
	}
	if stderr != nil {
		stderr = &lockedWriter{mu: &ioMu, w: stderr}
	}
	o := runOptions{prepare: func(ctx context.Context, cfg config.Server, emit emitFunc) (map[string]string, error) {
		return prepareRuntimeOptions(ctx, cfg, emit, options)
	}, consoleGuard: options.ConsoleGuard, control: options.ControlProvider, poll: time.Duration(cfg.Readiness.PollIntervalSeconds) * time.Second, timeout: time.Duration(cfg.Readiness.TimeoutSeconds) * time.Second, shutdown: 10 * time.Second}
	if cfg.Updates.Enabled {
		return runSupervisor(ctx, cfg, sink, stdin, stdout, stderr, o, options.GameProvider)
	}
	return runWith(ctx, cfg, sink, stdin, stdout, stderr, o)
}
func runWith(ctx context.Context, cfg config.Server, sink events.Sink, stdin io.Reader, stdout, stderr io.Writer, o runOptions) (code int, err error) {
	for _, arg := range cfg.Launch.ExtraArgs {
		key, _, _ := strings.Cut(strings.ToLower(arg), "=")
		switch key {
		case "-ip", "+ip", "-port", "+port", "-hostport", "+hostport", "+sv_setsteamaccount", "-sv_setsteamaccount":
			return 1, errors.New("CUSTOM_PARAMS cannot override managed allocation or Steam account arguments")
		}
	}
	if !o.externalSignals {
		signalCtx, cancelSignal := context.WithCancelCause(ctx)
		defer cancelSignal(nil)
		signals := make(chan os.Signal, 2)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
		signalDone := make(chan struct{})
		defer func() { cancelSignal(nil); <-signalDone }()
		go func() {
			defer close(signalDone)
			select {
			case sig := <-signals:
				cancelSignal(signalCause{sig.(syscall.Signal)})
			case <-signalCtx.Done():
			}
		}()
		ctx = signalCtx
	}
	input, closeInput, e := inputReader(stdin)
	if e != nil {
		return 1, e
	}
	defer closeInput()
	if !o.externalLease {
		release, e := runtimelease.Acquire(cfg.Root)
		if e != nil {
			return 1, e
		}
		defer release()
	}
	identity, e := serverIdentity(cfg.Root, cfg.Identity)
	if e != nil {
		return 1, errors.New("cannot establish stable server identity")
	}
	var ioMu sync.Mutex
	if stdout != nil {
		stdout = &lockedWriter{mu: &ioMu, w: stdout}
	}
	if stderr != nil {
		stderr = &lockedWriter{mu: &ioMu, w: stderr}
	}
	queue := newEventQueue(sink, stderr, 2*time.Second, 2*time.Second)
	defer queue.finish()
	runID := randomID()
	control := &stopControl{run: runID}
	redact := redactFor(cfg)
	var eventMu sync.Mutex
	var currentStatus func() Status
	emit := func(kind, reason string, details map[string]string) {
		eventMu.Lock()
		defer eventMu.Unlock()
		if details == nil {
			details = map[string]string{}
		}
		clean := map[string]string{}
		for k, v := range details {
			clean[redact(k)] = redact(v)
		}
		if kind == "server.status" && currentStatus != nil {
			if encoded, e := json.Marshal(currentStatus()); e == nil {
				clean["runtime_status"] = string(encoded)
			}
		}
		event := events.Event{Version: 1, Severity: eventSeverity(kind), CorrelationID: runID, ID: randomID(), Type: kind, ServerID: redact(identity), RunID: runID, At: time.Now().UTC(), Reason: redact(reason), Details: clean}
		if kind == "server.warning" && stderr != nil {
			fmt.Fprintln(stderr, redact("warning: "+reason+" "+clean["message"]))
		}
		queue.publish(event)
	}
	state := &statusStore{status: Status{RunID: runID, State: "preparing", Versions: map[string]string{}}}
	currentStatus = func() Status {
		s := state.snapshot()
		s.ScheduledActionID, s.OperatorVeto = control.Evidence()
		return s
	}
	closeStatus, e := state.serve(cfg.Root)
	if e != nil {
		return 1, errors.New("cannot serve local runtime status")
	}
	defer closeStatus()
	fail := func(reason string, e error) (int, error) {
		emit("server.start_failed", reason, nil)
		return 1, errors.New(redact(e.Error()))
	}
	if cfg.Launch.DebugPort != 0 {
		if _, e := exec.LookPath("gdbserver"); e != nil {
			return fail("debugger_unavailable", errors.New("GDB_DEBUG_PORT requires gdbserver; select the debug image"))
		}
	}
	versions, e := o.prepare(context.WithValue(ctx, preparationRunKey{}, runID), cfg, emit)
	if e != nil {
		return fail("preparation_failed", e)
	}
	if e = ctx.Err(); e != nil {
		return fail("startup_canceled", e)
	}
	safeVersions := map[string]string{}
	for name, version := range versions {
		safeVersions[redact(name)] = redact(version)
	}
	state.update(func(s *Status) { s.Versions = safeVersions; s.State = "starting" })
	// Reject an already-bound allocation before launch; never accept its A2S reply
	// as readiness for this child. Unspecified binds are queried over loopback.
	address := net.JoinHostPort(cfg.Launch.IP, itoa(cfg.Launch.Port))
	probeAddress := address
	if ip := net.ParseIP(cfg.Launch.IP); ip != nil && ip.IsUnspecified() {
		host := "127.0.0.1"
		if ip.To4() == nil {
			host = "::1"
		}
		probeAddress = net.JoinHostPort(host, itoa(cfg.Launch.Port))
	}
	port, e := net.ListenPacket("udp", address)
	if e != nil {
		return fail("allocation_unavailable", errors.New("game allocation is already bound or unavailable"))
	}
	port.Close()
	if stdout != nil {
		fmt.Fprintf(stdout, "Starting CS2 on %s; waiting for an A2S readiness response...\n", address)
	}
	if e = processgroup.EnableReaping(); e != nil {
		return fail("spawn_failed", errors.New("cannot enable child reaping"))
	}
	cmd := exec.Command(filepath.Join(cfg.Root, "game/cs2.sh"), launchArgs(cfg)...)
	cmd.Dir = filepath.Join(cfg.Root, "game")
	cmd.Env = homeEnv(cfg.Root)
	if cfg.Launch.DebugPort != 0 {
		cmd.Env = append(cmd.Env, "GAME_DEBUGGER=gdbserver --no-disable-randomization :"+itoa(cfg.Launch.DebugPort))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	inRead, inWrite, e := os.Pipe()
	if e != nil {
		return fail("spawn_failed", e)
	}
	defer inRead.Close()
	defer inWrite.Close()
	cmd.Stdin = inRead
	commandIn := &commandWriter{f: inWrite}
	guardLoop := newConsoleGuard(ctx, o.consoleGuard, identity, runID, commandIn, emit)
	defer guardLoop.close()
	var outputMu sync.Mutex
	out := &consoleWriter{mu: &outputMu, out: stdout, emit: emit, redact: redact, stream: "stdout", exclude: cfg.Observability.DisplayExclude, observe: guardLoop.observe}
	errs := &consoleWriter{mu: &outputMu, out: stderr, emit: emit, redact: redact, stream: "stderr", exclude: cfg.Observability.DisplayExclude, observe: guardLoop.observe}
	cmd.Stdout = out
	cmd.Stderr = errs
	if e = cmd.Start(); e != nil {
		return fail("spawn_failed", errors.New("cannot launch installed game/cs2.sh"))
	}
	inRead.Close()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopCommands := make(chan string, 1)
	inputDone := make(chan struct{})
	go forwardInput(input, commandIn, stopCommands, life.Done(), inputDone, control.Handle)
	defer func() {
		cancel()
		closeInput()
		inWrite.Close()
		<-inputDone
		out.flush()
		errs.flush()
	}()
	type probeResult struct {
		at time.Time
		s  queryStatus
		e  error
	}
	results := make(chan probeResult, 1)
	var probes sync.WaitGroup
	probe := func() {
		probes.Add(1)
		go func() {
			defer probes.Done()
			s, e := query(life, probeAddress)
			select {
			case results <- probeResult{at: time.Now().UTC(), s: s, e: e}:
			case <-life.Done():
			}
		}()
	}
	defer func() { cancel(); probes.Wait() }()
	probe()
	poll := time.NewTimer(o.poll)
	defer poll.Stop()
	poll.Stop()
	timeout := time.NewTimer(o.timeout)
	defer timeout.Stop()
	var stopping <-chan time.Time
	var shutdownTimer *time.Timer
	defer func() {
		if shutdownTimer != nil {
			shutdownTimer.Stop()
		}
	}()
	ready := false
	reason := ""
	ctxDone := ctx.Done()
	beginStop := func(why string, sig syscall.Signal) {
		if reason != "" {
			return
		}
		reason = why
		state.update(func(s *Status) { s.IntendedStopReason = why; s.State = "stopping" })
		shutdownTimer = time.NewTimer(o.shutdown)
		stopping = shutdownTimer.C
		if sig != 0 {
			syscall.Kill(-cmd.Process.Pid, sig)
		}
	}
	controlResults := make(chan protocol.UpdateControl, 1)
	var controls sync.WaitGroup
	defer func() { cancel(); controls.Wait() }()
	var waitErr error
loop:
	for {
		select {
		case waitErr = <-wait:
			break loop
		case <-ctxDone:
			if context.Cause(ctx) != errUpdateRestart {
				control.Veto()
				if o.updates != nil {
					o.updates.veto.Store(true)
				}
			}
			ctxDone = nil
			why := "context_canceled"
			if context.Cause(ctx) == errUpdateRestart {
				why = "standalone_update"
			}
			sig := syscall.SIGTERM
			var cause signalCause
			if errors.As(context.Cause(ctx), &cause) {
				sig = cause.signal
				why = "signal_term"
				if sig == syscall.SIGINT {
					why = "signal_int"
				}
			}
			beginStop(why, sig)
		case why := <-stopCommands:
			if why == "scheduled_update" {
				beginStop(why, syscall.SIGTERM)
			} else {
				beginStop(why, 0)
			}
		case <-stopping:
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			stopping = nil
		case <-timeout.C:
			if !ready {
				beginStop("readiness_timeout", syscall.SIGTERM)
			}
		case r := <-results:
			if r.e == nil && reason == "" {
				select {
				case waitErr = <-wait:
					break loop
				default:
				}
				state.update(func(s *Status) {
					s.SampleAt = r.at
					s.Map = redact(r.s.Map)
					s.Players = r.s.Players
					s.Versions["cs2"] = redact(r.s.Version)
					s.Ready = true
					s.State = "ready"
				})
				if !ready {
					ready = true
					timeout.Stop()
					if stdout != nil {
						fmt.Fprintln(stdout, "S2EGG_READY")
					}
					emit("server.up", "ready", map[string]string{"map": r.s.Map, "players": itoa(r.s.Players.Count)})
				}
				emit("server.status", "query_ok", map[string]string{"map": r.s.Map, "players": itoa(r.s.Players.Count), "players_known": "true"})
			} else {
				state.update(func(s *Status) {
					s.SampleAt = r.at
					s.Players = Players{}
					if ready && reason == "" {
						s.Ready = false
						s.State = "unresponsive"
					}
				})
				emit("server.status", "query_failed", map[string]string{"players_known": "false"})
			}
			if o.control != nil && reason == "" {
				snapshot := currentStatus()
				controls.Add(1)
				go func() {
					defer controls.Done()
					queryCtx, cancel := context.WithTimeout(life, 2*time.Second)
					defer cancel()
					intent, err := o.control.Control(queryCtx, snapshot)
					if err == nil {
						select {
						case controlResults <- intent:
						case <-life.Done():
						}
					}
				}()
			}
			if o.updates != nil {
				o.updates.observe(currentStatus(), commandIn)
				state.update(func(s *Status) { s.UpdatePending = o.updates.pendingBuild() })
			}
			poll.Reset(o.poll)
		case intent := <-controlResults:
			if reason == "" {
				control.Arm(intent)
				state.update(func(s *Status) { s.ScheduledActionID, s.OperatorVeto = control.Evidence() })
			}
		case <-poll.C:
			probe()
		}
	}
	cancel()
	closeInput()
	inWrite.Close()
	// A launcher may exit before its descendants. Keep the lease until the entire
	// owned group has been killed and orphaned children reaped, after cmd.Wait.
	if e := processgroup.KillAndReap(cmd.Process.Pid); e != nil {
		emit("server.warning", "process_cleanup_failed", map[string]string{"message": "cannot finish owned process-group cleanup"})
	}
	state.update(func(s *Status) { s.State = "stopped"; s.Ready = false; s.Players = Players{} })
	code = cmd.ProcessState.ExitCode()
	if code < 0 {
		code = 128 + int(cmd.ProcessState.Sys().(syscall.WaitStatus).Signal())
	}
	if reason == "" {
		// The reader queues intent before writing quit; the wait/select race
		// must not classify a fast successful console stop as a crash.
		select {
		case reason = <-stopCommands:
		default:
			reason = "unexpected_exit"
		}
	}
	actionID, veto := control.Evidence()
	if o.updates != nil && veto {
		o.updates.veto.Store(true)
	}
	if ready {
		emit("server.down", reason, map[string]string{"exit_code": itoa(code), "action_id": actionID, "operator_veto": strconv.FormatBool(veto)})
		if reason == "unexpected_exit" {
			emit("server.crash", reason, map[string]string{"exit_code": itoa(code)})
		}
	} else {
		emit("server.start_failed", reason, map[string]string{"exit_code": itoa(code)})
		if code == 0 {
			code = 1
		}
		return code, errors.New("game exited before readiness: " + reason)
	}
	if waitErr != nil && reason == "unexpected_exit" {
		return code, errors.New("game exited unexpectedly")
	}
	return code, nil
}

func eventSeverity(kind string) string {
	switch kind {
	case "server.start_failed", "server.crash":
		return "error"
	case "server.warning":
		return "warning"
	case "server.console", "server.status":
		return "debug"
	default:
		return "info"
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(b)
}

type signalCause struct{ signal syscall.Signal }

func (s signalCause) Error() string { return "supervisor received stop signal" }
