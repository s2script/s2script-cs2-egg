// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeConfigurationIsBoundedAndStrict(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.json")
	for _, b := range []string{`{"schema_version":1,"id":"n","state_dir":"/tmp/n","unexpected":true}`, `{"schema_version":2,"id":"n","state_dir":"/tmp/n"}`, strings.Repeat(" ", MaxNodeConfig+1)} {
		if e := os.WriteFile(p, []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := LoadNode(p); e == nil {
			t.Fatal("invalid node config accepted")
		}
	}
	os.WriteFile(p, []byte(`{"schema_version":1,"id":"n","state_dir":"/tmp/n"}`), 0600)
	cfg, e := LoadNode(p)
	if e != nil || cfg.AdminSocket != "/run/s2node/admin.sock" || cfg.Limits.Pending != 1024 {
		t.Fatalf("node defaults %+v %v", cfg, e)
	}
}
