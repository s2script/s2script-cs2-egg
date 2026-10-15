// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"errors"
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
	if !strings.HasPrefix(string(data), original) || strings.Count(string(data), "exec cs2node-password.cfg") != 1 {
		t.Fatal("user config changed or include duplicated")
	}
	reopened, err := Open(root, true)
	if err != nil || !reopened.Ready() {
		t.Fatal("cached restart failed", err)
	}
	if err := reopened.Accept(u); err != nil {
		t.Fatal(err)
	}
	var command string
	if err := reopened.Apply(func(s string) error { command = s; return nil }); err != nil || command != `sv_password "lantern-meadow-copper"` {
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
	var got string
	m.Apply(func(s string) error { got = s; return nil })
	if !strings.Contains(got, first.Password) {
		t.Fatal("failed update changed current credential")
	}
}
func TestMaskingAndMapReapplication(t *testing.T) {
	m, _ := Open(t.TempDir(), true)
	u := proto.PasswordUpdate{Password: "lantern-meadow-copper", NextAt: "2026-11-15T12:00:00Z"}
	m.Accept(u)
	second := "river-birch-copper"
	m.Accept(proto.PasswordUpdate{Password: second, NextAt: "2026-12-15T12:00:00Z"})
	masked := m.Mask("arbitrary echo " + u.Password + " and " + second)
	if strings.Contains(masked, u.Password) || strings.Contains(masked, second) {
		t.Fatal("current/previous password leaked")
	}
	calls := 0
	send := func(s string) error {
		calls++
		if s != `sv_password "river-birch-copper"` {
			t.Fatal("wrong command")
		}
		return nil
	}
	m.Observe("ordinary output", send)
	m.Observe("Host activate", send)
	m.Observe("Host activate", send)
	if calls != 2 {
		t.Fatal("map changes did not reapply")
	}
	if err := m.Apply(func(string) error { return errors.New("not running") }); err == nil {
		t.Fatal("send failure ignored")
	}
}

func TestPasswordEchoDoesNotTriggerApplyLoop(t *testing.T) {
	m, _ := Open(t.TempDir(), true)
	m.Accept(proto.PasswordUpdate{Password: "hibernating-meadow-copper", NextAt: "2026-11-15T12:00:00Z"})
	calls := 0
	m.Observe(`sv_password "hibernating-meadow-copper"`, func(string) error { calls++; return nil })
	if calls != 0 {
		t.Fatal("echo triggered another password command")
	}
}

func TestFailedAssignmentIsStillMasked(t *testing.T) {
	root := t.TempDir()
	m, _ := Open(root, true)
	path := filepath.Join(root, "game/csgo/cfg/server.cfg")
	os.MkdirAll(path, 0700)
	p := "lantern-meadow-copper"
	if m.Accept(proto.PasswordUpdate{Password: p, NextAt: "2026-11-15T12:00:00Z"}) == nil {
		t.Fatal("expected config error")
	}
	if strings.Contains(m.Mask("engine echoed "+p), p) {
		t.Fatal("partially persisted assignment leaked")
	}
}

func TestIncludeFailureDoesNotPublishAssignmentFiles(t *testing.T) {
	root := t.TempDir()
	m, _ := Open(root, true)
	first := proto.PasswordUpdate{Password: "lantern-meadow-copper", NextAt: "2026-11-15T12:00:00Z"}
	if err := m.Accept(first); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "game/csgo/cfg/server.cfg")
	os.Remove(cfg)
	os.Mkdir(cfg, 0700)
	if err := m.Accept(proto.PasswordUpdate{Password: "river-birch-copper", NextAt: first.NextAt}); err == nil {
		t.Fatal("expected include failure")
	}
	for _, p := range []string{m.cachePath(), filepath.Join(root, "game/csgo/cfg/cs2node-password.cfg")} {
		data, _ := os.ReadFile(p)
		if !strings.Contains(string(data), first.Password) {
			t.Fatal("failed update replaced assignment file")
		}
	}
}
