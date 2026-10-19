// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

func TestCacheConfigPreservationAndOptOut(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "game/csgo/cfg/server.cfg")
	os.MkdirAll(filepath.Dir(cfg), 0755)
	original := "hostname \"my server\"\nsv_password \"original\"\n// keep this\n"
	os.WriteFile(cfg, []byte(original), 0640)
	m, err := Open(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if m.Ready() {
		t.Fatal("new manager unexpectedly ready")
	}
	u := proto.PasswordUpdate{Password: "lantern-meadow-copper", NextAt: "2026-11-15T12:00:00Z"}
	if err := m.Accept(u); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfg)
	if string(data) != original {
		t.Fatal("user config changed")
	}
	reopened, err := Open(root, true)
	if err != nil || !reopened.Ready() {
		t.Fatal("cached restart failed", err)
	}
	if err := reopened.Accept(u); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Startup("./game/cs2.sh -dedicated"); got != `./game/cs2.sh -dedicated +sv_password "lantern-meadow-copper"` {
		t.Fatal("cached password not applied")
	}
	for _, p := range []string{m.cachePath(), filepath.Join(root, "game/csgo/cfg/cs2node-password.cfg")} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatal("secret file not private")
		}
	}
	if _, err := Open(root, false); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(cfg)
	if string(data) != original {
		t.Fatal("opt-out failed to restore original config")
	}
	st, _ := os.Stat(cfg)
	if st.Mode().Perm() != 0640 {
		t.Fatal("user cfg permissions changed")
	}
}
func TestUnsafeUpdateAndDiskFailureRetainKnownPassword(t *testing.T) {
	root := t.TempDir()
	m, _ := Open(root, true)
	first := proto.PasswordUpdate{Password: "lantern-meadow-copper", NextAt: "2026-11-15T12:00:00Z"}
	if err := m.Accept(first); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "apple-pear-plum;quit", "apple-pear-plum\nquit"} {
		if m.Accept(proto.PasswordUpdate{Password: p, NextAt: first.NextAt}) == nil {
			t.Fatal("unsafe update accepted")
		}
	}
	os.Remove(filepath.Join(root, "game/csgo/cfg/cs2node-password.cfg"))
	os.Mkdir(filepath.Join(root, "game/csgo/cfg/cs2node-password.cfg"), 0700)
	if err := m.Accept(proto.PasswordUpdate{Password: "river-lantern-copper", NextAt: first.NextAt}); err == nil {
		t.Fatal("disk error ignored")
	}
	got := m.Startup("cs2")
	if !strings.Contains(got, first.Password) {
		t.Fatal("failed update changed current credential")
	}
}
func TestMaskingAndRotationRequiresRestart(t *testing.T) {
	m, _ := Open(t.TempDir(), true)
	u := proto.PasswordUpdate{Password: "lantern-meadow-copper", NextAt: "2026-11-15T12:00:00Z"}
	m.Accept(u)
	m.Startup("cs2")
	second := "river-birch-copper"
	m.Accept(proto.PasswordUpdate{Password: second, NextAt: "2026-12-15T12:00:00Z"})
	masked := m.Mask("arbitrary echo " + u.Password + " and " + second)
	if strings.Contains(masked, u.Password) || strings.Contains(masked, second) {
		t.Fatal("current/previous password leaked")
	}
	if m.RestartPending() != "2026-12-15T12:00:00Z" {
		t.Fatal("rotation did not request a restart")
	}
}

func TestUnchangedAssignmentDoesNotRestart(t *testing.T) {
	m, _ := Open(t.TempDir(), true)
	u := proto.PasswordUpdate{Password: "hibernating-meadow-copper", NextAt: "2026-11-15T12:00:00Z"}
	m.Accept(u)
	m.Startup("cs2")
	for i := 0; i < 10; i++ {
		if err := m.Accept(u); err != nil {
			t.Fatal(err)
		}
		if m.RestartPending() != "" {
			t.Fatal("unchanged assignment requested a restart")
		}
	}
}

func TestFailedAssignmentIsStillMasked(t *testing.T) {
	root := t.TempDir()
	m, _ := Open(root, true)
	path := m.cachePath()
	os.MkdirAll(path, 0700)
	p := "lantern-meadow-copper"
	if m.Accept(proto.PasswordUpdate{Password: p, NextAt: "2026-11-15T12:00:00Z"}) == nil {
		t.Fatal("expected config error")
	}
	if strings.Contains(m.Mask("engine echoed "+p), p) {
		t.Fatal("partially persisted assignment leaked")
	}
}

func TestCacheFailureRetainsStartupAssignment(t *testing.T) {
	root := t.TempDir()
	m, _ := Open(root, true)
	first := proto.PasswordUpdate{Password: "lantern-meadow-copper", NextAt: "2026-11-15T12:00:00Z"}
	if err := m.Accept(first); err != nil {
		t.Fatal(err)
	}
	m.Startup("cs2")
	os.Remove(m.cachePath())
	os.Mkdir(m.cachePath(), 0700)
	if err := m.Accept(proto.PasswordUpdate{Password: "river-birch-copper", NextAt: first.NextAt}); err == nil {
		t.Fatal("expected cache failure")
	}
	if m.RestartPending() != "" {
		t.Fatal("failed update requested a restart")
	}
	if got := m.Startup("cs2"); got != `cs2 +sv_password "lantern-meadow-copper"` {
		t.Fatal("failed update replaced startup password")
	}
}
