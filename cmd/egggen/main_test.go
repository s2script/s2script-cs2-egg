// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEgggenCheckNeverRewrites(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "egg.json")
	catalog := filepath.Join(root, "plugins.md")
	var out, errs bytes.Buffer
	if run([]string{"--output", target, "--catalog-output", catalog}, &out, &errs) != 0 {
		t.Fatal(errs.String())
	}
	before, _ := os.ReadFile(target)
	if run([]string{"--check", "--output", target, "--catalog-output", catalog}, &out, &errs) != 0 {
		t.Fatal(errs.String())
	}
	if err := os.WriteFile(target, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if run([]string{"--check", "--output", target, "--catalog-output", catalog}, &out, &errs) == 0 {
		t.Fatal("accepted stale egg")
	}
	after, _ := os.ReadFile(target)
	if string(after) != "stale" {
		t.Fatal("check rewrote file")
	}
	if doc, err := os.ReadFile(catalog); err != nil || !bytes.Contains(doc, []byte("@s2script/basecommands")) {
		t.Fatal("missing catalog docs")
	}
	if len(before) == 0 {
		t.Fatal("empty generation")
	}
}
