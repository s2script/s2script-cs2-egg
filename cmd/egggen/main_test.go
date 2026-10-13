// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultEggCanBeImportedFromURLWithoutLocalBuild(t *testing.T) {
	target := filepath.Join(t.TempDir(), "egg.json")
	var out, errs bytes.Buffer
	if run([]string{"--output", target}, &out, &errs) != 0 {
		t.Fatal(errs.String())
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var egg struct {
		Meta struct {
			UpdateURL string `json:"update_url"`
		} `json:"meta"`
		Images  map[string]string                                 `json:"docker_images"`
		Scripts struct{ Installation struct{ Container string } } `json:"scripts"`
	}
	if err := json.Unmarshal(b, &egg); err != nil {
		t.Fatal(err)
	}
	if egg.Meta.UpdateURL != "https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/pterodactyl/s2script-cs2-egg.json" {
		t.Errorf("missing panel update URL: %q", egg.Meta.UpdateURL)
	}
	if len(egg.Images) != 2 || egg.Images["S2Script prerelease"] != "ghcr.io/s2script/s2script-cs2-egg:prerelease" || egg.Images["S2Script stable"] != "ghcr.io/s2script/s2script-cs2-egg:stable" {
		t.Errorf("egg needs published image channels: %v", egg.Images)
	}
	if egg.Scripts.Installation.Container != "ghcr.io/pterodactyl/installers:debian" {
		t.Errorf("installation depends on a project runtime image: %q", egg.Scripts.Installation.Container)
	}
}

func TestEgggenCheckNeverRewrites(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "egg.json")
	catalog := filepath.Join(root, "bundled-plugins.md")
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
