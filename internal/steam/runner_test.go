// SPDX-License-Identifier: GPL-3.0-or-later
package steam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeSteam(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake steamcmd")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSteamUsesApp730AndSeparateArguments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "server with spaces;literal")
	if err := os.MkdirAll(filepath.Join(root, "steamapps"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "steamapps/appmanifest_730.acf"), []byte(`"AppState" { "appid" "730" "buildid" "24934554" }`), 0600)
	f := fakeSteam(t, `printf '%s\n' "$@"`)
	var log bytes.Buffer
	id, err := Update(context.Background(), f, root, true, &log)
	if err != nil || id != "24934554" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	want := []string{"+force_install_dir", root, "+login", "anonymous", "+app_update", "730", "validate", "+quit"}
	if got := strings.Split(strings.TrimSpace(log.String()), "\n"); !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments=%q", got)
	}
}
func TestLatestBuildSelectsPublicBranch(t *testing.T) {
	f := fakeSteam(t, `cat <<'DATA'
"730" { "depots" { "branches" { "beta" { "buildid" "999" } "public" { "buildid" "24934554" "timeupdated" "1" } } } }
DATA`)
	id, err := LatestBuild(context.Background(), f)
	if err != nil || id != "24934554" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}
func TestSteamFailuresAndCancellation(t *testing.T) {
	if _, err := LatestBuild(context.Background(), fakeSteam(t, "exit 7")); err == nil {
		t.Fatal("process failure accepted")
	}
	if _, err := LatestBuild(context.Background(), fakeSteam(t, `echo '"730" { "buildid" "999" }'`)); err == nil {
		t.Fatal("missing public branch accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := LatestBuild(ctx, fakeSteam(t, "exec sleep 30")); err == nil {
		t.Fatal("cancel accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel did not stop process")
	}
}

func TestSteamCancellationStopsDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "orphan-marker")
	f := fakeSteam(t, "sh -c 'sleep 1; echo survived > "+marker+"' &\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := LatestBuild(ctx, f); err == nil {
		t.Fatal("cancel ignored")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("SteamCMD descendant survived cancellation")
	}
}

func TestInstalledBuildRejectsEscapingManifest(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "appmanifest_730.acf"), []byte(`"appid" "730" "buildid" "123"`), 0600)
	os.Symlink(outside, filepath.Join(root, "steamapps"))
	if _, e := InstalledBuild(root); e == nil {
		t.Fatal("read manifest outside root")
	}
}

func TestSteamCommandOwnsDescendantCleanup(t *testing.T) {
	for _, cancelCommand := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelCommand), func(t *testing.T) {
			root := t.TempDir()
			pidFile := filepath.Join(root, "child")
			script := `sh -c 'echo $$ > "` + pidFile + `"; exec sleep 30' >/dev/null 2>&1 &
while [ ! -s "` + pidFile + `" ]; do sleep 0.01; done
`
			if cancelCommand {
				script += "wait\n"
			} else {
				script += `echo '"public" { "buildid" "123" }'` + "\n"
			}
			sibling := exec.Command("sh", "-c", "sleep 0.05; exit 7")
			if e := sibling.Start(); e != nil {
				t.Fatal(e)
			}
			defer sibling.Process.Kill()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			executable := fakeSteam(t, script)
			go func() { _, e := LatestBuild(ctx, executable); done <- e }()
			childPID := 0
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				b, e := os.ReadFile(pidFile)
				if e == nil {
					childPID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
					if childPID > 0 {
						break
					}
				}
				time.Sleep(time.Millisecond)
			}
			if childPID == 0 {
				t.Fatal("Steam fixture descendant did not start")
			}
			defer syscall.Kill(childPID, syscall.SIGKILL)
			if cancelCommand {
				cancel()
			}
			select {
			case e := <-done:
				if cancelCommand && !errors.Is(e, context.Canceled) {
					t.Fatalf("expected cancellation: %v", e)
				}
				if !cancelCommand && e != nil {
					t.Fatal(e)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Steam command did not finish")
			}
			if e := syscall.Kill(childPID, 0); !errors.Is(e, syscall.ESRCH) {
				t.Fatalf("Steam descendant remains after command return: %v", e)
			}
			var exit *exec.ExitError
			if !errors.As(sibling.Wait(), &exit) || exit.ExitCode() != 7 {
				t.Fatal("Steam command reaped unrelated child")
			}
		})
	}
}
