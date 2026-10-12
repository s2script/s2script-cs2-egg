// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
