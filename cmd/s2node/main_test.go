// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/config"
)

func configFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	volume := filepath.Join(dir, "volume")
	os.MkdirAll(filepath.Join(volume, "egg"), 0700)
	cfg := config.Node{SchemaVersion: 1, ID: "node", StateDir: filepath.Join(dir, "state"), AdminSocket: filepath.Join(dir, "admin/admin.sock"), Servers: []config.NodeServer{{ID: "server", Volume: volume, PeerUID: uint32(os.Getuid()), AccessGID: os.Getgid(), Allocation: config.Allocation{IP: "127.0.0.1", Port: 27015}}}}
	b, _ := json.Marshal(cfg)
	p := filepath.Join(dir, "node.json")
	os.WriteFile(p, b, 0600)
	return p
}
func TestDoctorExplainsPrivateAdministrativeAccess(t *testing.T) {
	p := configFixture(t)
	var out, errs bytes.Buffer
	if code := run([]string{"doctor", "--config", p}, &out, &errs); code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	for _, required := range []string{"0600", "separate UID", "unknown", "cooperative"} {
		if !strings.Contains(out.String(), required) {
			t.Fatalf("missing access contract %q: %s", required, out.String())
		}
	}
	if _, e := os.Stat(filepath.Join(filepath.Dir(p), "state")); !os.IsNotExist(e) {
		t.Fatal("doctor created state")
	}
}
func TestInstallStagesWithoutServices(t *testing.T) {
	dir := t.TempDir()
	var out, errs bytes.Buffer
	if code := run([]string{"install", "--directory", dir}, &out, &errs); code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	for _, name := range []string{"s2node", "s2node.service"} {
		if _, e := os.Stat(filepath.Join(dir, name)); e != nil {
			t.Fatal(e)
		}
	}
	if !strings.Contains(out.String(), "not enabled") {
		t.Fatal("install overclaimed host activation")
	}
}
