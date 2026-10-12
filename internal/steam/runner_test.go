// SPDX-License-Identifier: GPL-3.0-or-later
package steam

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
