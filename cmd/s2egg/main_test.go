// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
)

func TestReadOnlyDiagnostics(t *testing.T) {
	root := t.TempDir()
	lookup := func(k string) (string, bool) {
		switch k {
		case "STEAM_ACC":
			return "private-token", true
		case "DISCORD_WEBHOOK_URL":
			return "https://discord.com/api/webhooks/123/private-hook", true
		}
		return "", false
	}
	for _, command := range [][]string{{"config", "check", "--root", root}, {"config", "show", "--root", root}, {"doctor", "--root", root}} {
		var out, errs bytes.Buffer
		code := run(command, lookup, &out, &errs)
		if command[0] != "doctor" && code != 0 {
			t.Fatalf("code %d: %s", code, errs.String())
		}
		if strings.Contains(out.String()+errs.String(), "private-token") || strings.Contains(out.String()+errs.String(), "private-hook") {
			t.Fatal("leaked secret")
		}
		if command[0] == "doctor" {
			if code != 1 || !strings.Contains(out.String(), "missing") || !strings.Contains(out.String(), "readiness is not assessed") {
				t.Fatalf("dishonest diagnostics: %s", out.String())
			}
		} else if command[1] == "show" {
			var data map[string]any
			if err := json.Unmarshal(out.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
			if data["root"] != root {
				t.Fatal("show omitted root")
			}
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("diagnostics changed server root")
	}
	if _, err := os.Stat(filepath.Join(root, "egg")); !os.IsNotExist(err) {
		t.Fatal("created config directory")
	}
}
func TestCLIRejectsInvalidConfigAndUnknownCommands(t *testing.T) {
	for _, args := range [][]string{{"config", "check", "--root", t.TempDir()}, {"run"}, {"config", "nope"}, {"config", "check", "extra"}} {
		var out, errs bytes.Buffer
		if run(args, func(string) (string, bool) { return "", false }, &out, &errs) == 0 {
			t.Fatalf("accepted %v", args)
		}
		if errs.Len() == 0 {
			t.Fatal("missing actionable error")
		}
	}
}

func TestStartupBannerPrecedesConfigurationFailure(t *testing.T) {
	var out, errs bytes.Buffer
	code := run([]string{"run", "--root", t.TempDir()}, func(string) (string, bool) { return "", false }, &out, &errs)
	if code != 1 || !strings.Contains(out.String(), "Starting S2Script CS2") || !strings.Contains(errs.String(), "STEAM_ACC") {
		t.Fatalf("startup failure must identify the running launcher and cause: code=%d stdout=%q stderr=%q", code, out.String(), errs.String())
	}
}

func TestPluginCommandsAreRouted(t *testing.T) {
	root := t.TempDir()
	lookup := func(k string) (string, bool) { return "1", k == "ALLOW_TOKENLESS" }
	var out, errs bytes.Buffer
	if code := run([]string{"plugins", "list", "--root", root}, lookup, &out, &errs); code != 0 || !strings.Contains(out.String(), `"installed"`) {
		t.Fatalf("%d %s %s", code, out.String(), errs.String())
	}
	out.Reset()
	errs.Reset()
	if code := run([]string{"plugins", "add", "hello-world", "--version", "1.2.3", "--disabled", "--root", root}, lookup, &out, &errs); code != 1 || !strings.Contains(out.String(), "policy saved") || !strings.Contains(errs.String(), "unavailable") {
		t.Fatalf("%d %s %s", code, out.String(), errs.String())
	}
}

func TestRuntimeCommandsAreRouted(t *testing.T) {
	root := t.TempDir()
	lookup := func(k string) (string, bool) { return "1", k == "ALLOW_TOKENLESS" }
	for _, command := range []string{"status", "prepare"} {
		var out, errs bytes.Buffer
		code := run([]string{command, "--root", root}, lookup, &out, &errs)
		if code == 2 || strings.Contains(errs.String(), "usage:") || strings.Contains(errs.String(), "not implemented") {
			t.Fatalf("runtime command unavailable: %d %s", code, errs.String())
		}
		if code != 1 {
			t.Fatalf("empty root unexpectedly ready/prepared: %d %s", code, out.String())
		}
	}
}

// Exercise the real CLI entrypoint in a separate process so a default signal
// action cannot terminate the test runner. SteamCMD is a harmless shell fixture.
func TestPrepareCLISignalsStopSteamGroupBeforeUnlock(t *testing.T) {
	if root := os.Getenv("S2EGG_TASK6_PREPARE_SIGNAL_ROOT"); root != "" {
		os.Args = []string{"s2egg", "prepare", "--root", root}
		main()
		return
	}
	// Adopt leaked fixtures on RED, so the regression itself can clean them up.
	if _, _, e := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); e != 0 {
		t.Fatal(e)
	}
	for _, phase := range []string{"bootstrap", "update"} {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
			t.Run(phase+"/"+sig.String(), func(t *testing.T) {
				root := t.TempDir()
				steamRoot := filepath.Join(root, "steamcmd")
				if e := os.MkdirAll(filepath.Join(steamRoot, "linux32"), 0700); e != nil {
					t.Fatal(e)
				}
				for _, name := range []string{"steamcmd", "steamerrorreporter", "libstdc++.so.6", "crashhandler.so"} {
					if e := os.WriteFile(filepath.Join(steamRoot, "linux32", name), []byte("fixture"), 0700); e != nil {
						t.Fatal(e)
					}
				}
				if phase == "update" {
					os.MkdirAll(filepath.Join(steamRoot, "linux64"), 0700)
					os.WriteFile(filepath.Join(steamRoot, "linux64/steamclient.so"), []byte("fixture"), 0600)
				}
				script := `#!/bin/sh
trap '' INT TERM
printf '%s' "$$" > "$HOME/steam-parent"
printf '%s\n' "$*" > "$HOME/steam-arguments"
sh -c 'trap "" INT TERM; printf "%s" "$$" > "$HOME/steam-child"; while :; do sleep 1; done' &
wait
`
				if e := os.WriteFile(filepath.Join(steamRoot, "steamcmd.sh"), []byte(script), 0700); e != nil {
					t.Fatal(e)
				}
				c := exec.Command(os.Args[0], "-test.run=^TestPrepareCLISignalsStopSteamGroupBeforeUnlock$")
				c.Env = append(os.Environ(), "S2EGG_TASK6_PREPARE_SIGNAL_ROOT="+root, "ALLOW_TOKENLESS=1", "STEAM_ACC=", "DISCORD_WEBHOOK_URL=", "SRCDS_STOP_UPDATE=0")
				var output bytes.Buffer
				c.Stdout = &output
				c.Stderr = &output
				if e := c.Start(); e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() { done <- c.Wait() }()
				steamPID := 0
				defer func() {
					c.Process.Kill()
					if steamPID == 0 {
						b, _ := os.ReadFile(filepath.Join(root, "steam-parent"))
						steamPID, _ = strconv.Atoi(string(b))
					}
					if steamPID > 0 {
						syscall.Kill(-steamPID, syscall.SIGKILL)
						deadline := time.Now().Add(time.Second)
						for time.Now().Before(deadline) {
							var status syscall.WaitStatus
							pid, e := syscall.Wait4(-steamPID, &status, syscall.WNOHANG, nil)
							if errors.Is(e, syscall.ECHILD) {
								break
							}
							if pid == 0 {
								time.Sleep(time.Millisecond)
							}
						}
					}
				}()
				readPID := func(name string) int {
					t.Helper()
					deadline := time.Now().Add(3 * time.Second)
					for time.Now().Before(deadline) {
						b, e := os.ReadFile(filepath.Join(root, name))
						if e == nil {
							if pid, e := strconv.Atoi(string(b)); e == nil && pid > 0 {
								return pid
							}
						}
						time.Sleep(5 * time.Millisecond)
					}
					t.Fatalf("fixture did not start: %s", name)
					return 0
				}
				steamPID = readPID("steam-parent")
				childPID := readPID("steam-child")
				if pg, e := syscall.Getpgid(steamPID); e != nil || pg != steamPID {
					t.Fatalf("SteamCMD lacks own group: %d %v", pg, e)
				}
				if pg, e := syscall.Getpgid(childPID); e != nil || pg != steamPID {
					t.Fatalf("descendant not in fixture group: %d %v", pg, e)
				}
				args, _ := os.ReadFile(filepath.Join(root, "steam-arguments"))
				if (phase == "bootstrap" && string(args) != "+quit\n") || (phase == "update" && !strings.Contains(string(args), "+app_update 730")) {
					t.Fatalf("wrong phase: %s", args)
				}
				if release, e := runtimelease.Acquire(root); e == nil {
					release()
					t.Fatal("runtime lease missing while SteamCMD runs")
				}
				if release, e := artifact.Acquire(root); e == nil {
					release()
					t.Fatal("artifact lock missing while SteamCMD runs")
				}
				monitorStop := make(chan struct{})
				monitorDone := make(chan struct{})
				violations := make(chan string, 2)
				go func() {
					defer close(monitorDone)
					for {
						for _, lock := range []struct {
							name    string
							acquire func(string) (func() error, error)
						}{{"runtime", runtimelease.Acquire}, {"artifact", artifact.Acquire}} {
							if release, e := lock.acquire(root); e == nil {
								if syscall.Kill(-steamPID, 0) == nil {
									select {
									case violations <- lock.name:
									default:
									}
								}
								release()
							}
						}
						select {
						case <-monitorStop:
							return
						case <-time.After(time.Millisecond):
						}
					}
				}()
				var stopMonitorOnce sync.Once
				stopMonitor := func() {
					stopMonitorOnce.Do(func() {
						close(monitorStop)
						<-monitorDone
						close(violations)
						for name := range violations {
							t.Errorf("%s lock became available while owned SteamCMD group existed", name)
						}
					})
				}
				defer stopMonitor()
				if e := c.Process.Signal(sig); e != nil {
					t.Fatal(e)
				}
				var waitErr error
				select {
				case waitErr = <-done:
				case <-time.After(4 * time.Second):
					t.Fatal("interrupted preparation did not return")
				}
				stopMonitor()
				var exit *exec.ExitError
				if !errors.As(waitErr, &exit) || exit.ExitCode() != 1 {
					t.Errorf("prepare terminated instead of cancelling: %v; output=%s", waitErr, output.String())
				}
				if e := syscall.Kill(-steamPID, 0); !errors.Is(e, syscall.ESRCH) {
					t.Errorf("SteamCMD group survives CLI exit: %v", e)
				}
				if e := syscall.Kill(childPID, 0); !errors.Is(e, syscall.ESRCH) {
					t.Errorf("SteamCMD descendant survives CLI exit: %v", e)
				}
				release, e := runtimelease.Acquire(root)
				if e != nil {
					t.Fatal(e)
				}
				release()
				release, e = artifact.Acquire(root)
				if e != nil {
					t.Fatal(e)
				}
				release()
			})
		}
	}
}

type webhookRoute struct{ target *url.URL }

func (r webhookRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	cp := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.target.Scheme
	u.Host = r.target.Host
	cp.URL = &u
	return (&http.Transport{}).RoundTrip(cp)
}
func TestRunCLIFlushesStartupFailureWebhook(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "steamcmd/linux32"), 0755)
	for _, name := range []string{"steamcmd", "steamerrorreporter", "libstdc++.so.6", "crashhandler.so"} {
		os.WriteFile(filepath.Join(root, "steamcmd/linux32", name), []byte("fixture"), 0755)
	}
	os.WriteFile(filepath.Join(root, "steamcmd/steamcmd.sh"), []byte("#!/bin/sh\nexit 7\n"), 0755)
	var count atomic.Int32
	var payload string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		payload = string(b)
		count.Add(1)
		io.WriteString(w, `{"id":"ok"}`)
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	old := http.DefaultTransport
	http.DefaultTransport = webhookRoute{u}
	defer func() { http.DefaultTransport = old }()
	var out, errs bytes.Buffer
	code := run([]string{"run", "--root", root}, func(k string) (string, bool) {
		if k == "ALLOW_TOKENLESS" {
			return "1", true
		}
		if k == "DISCORD_WEBHOOK_URL" {
			return "https://discord.com/api/webhooks/1/private-hook", true
		}
		return "", false
	}, &out, &errs)
	if code == 0 || count.Load() != 1 || !strings.Contains(payload, "server.start_failed") {
		t.Fatalf("code %d requests %d payload %s errors %s", code, count.Load(), payload, errs.String())
	}
	if strings.Contains(errs.String(), "private-hook") {
		t.Fatal("secret in CLI error")
	}
}
