// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// An old managed include must not restore the password on a playtest map change.
func TestOpenRemovesLegacyIncludeWithoutChangingUserConfig(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(strings.ReplaceAll(newline, "\r", "CR"), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "game/csgo/cfg/server.cfg")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			original := "hostname \"playtest\"" + newline + "// keep this" + newline
			legacy := strings.ReplaceAll(block, "\n", newline)
			if err := os.WriteFile(path, []byte(original+legacy), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := Open(root, true)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != original {
				t.Fatal("legacy password include remains or user config changed", err)
			}
		})
	}
}

func TestStartupOnlySetsAnEnabledValidAssignment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		password string
		want     string
	}{
		{"disabled", false, "lantern-meadow-copper", "./game/cs2.sh -dedicated"},
		{"unassigned", true, "", "./game/cs2.sh -dedicated"},
		{"invalid", true, "unsafe;quit", "./game/cs2.sh -dedicated"},
		{"enabled", true, "lantern-meadow-copper", `./game/cs2.sh -dedicated +sv_password "lantern-meadow-copper"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{enabled: tc.enabled, current: proto.PasswordUpdate{Password: tc.password}}
			if got := m.Startup("./game/cs2.sh -dedicated"); got != tc.want {
				t.Fatal("wrong startup password policy")
			}
			if m.RestartPending() != "" {
				t.Fatal("initial assignment requested restart")
			}
		})
	}
}

func TestRestartPendingRequiresDurableChangedPassword(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		started  string
		current  string
		unsynced bool
		want     string
	}{
		{"before startup", true, "", "lantern-meadow-copper", false, ""},
		{"duplicate", true, "lantern-meadow-copper", "lantern-meadow-copper", false, ""},
		{"disabled", false, "lantern-meadow-copper", "river-birch-copper", false, ""},
		{"not durable", true, "lantern-meadow-copper", "river-birch-copper", true, ""},
		{"rotated", true, "lantern-meadow-copper", "river-birch-copper", false, "2026-11-15T12:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{enabled: tc.enabled, startedPassword: tc.started, unsynced: tc.unsynced,
				current: proto.PasswordUpdate{Password: tc.current, NextAt: "2026-11-15T12:00:00Z"}}
			if got := m.RestartPending(); got != tc.want {
				t.Fatal("wrong restart policy")
			}
		})
	}
}

func TestDuplicateAssignmentRetriesDirectorySyncBeforeRestart(t *testing.T) {
	u := proto.PasswordUpdate{Password: "river-birch-copper", NextAt: "2026-11-15T12:00:00Z"}
	m := &Manager{root: t.TempDir(), enabled: true, current: u, startedPassword: "lantern-meadow-copper", unsynced: true}
	if err := m.Accept(u); err == nil || m.RestartPending() != "" {
		t.Fatal("failed directory sync allowed a restart")
	}
	if err := os.MkdirAll(filepath.Dir(m.cachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Accept(u); err != nil || m.RestartPending() != "2026-11-15T12:00:00Z" {
		t.Fatal("directory sync was not retried before restart", err)
	}
}
