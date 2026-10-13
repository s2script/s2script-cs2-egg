// SPDX-License-Identifier: GPL-3.0-or-later
package integration

import (
	"bytes"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/guard"
	"github.com/s2script/s2script-cs2-egg/internal/panel"
	"github.com/s2script/s2script-cs2-egg/internal/plugins"
	"github.com/s2script/s2script-cs2-egg/internal/shared"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func repository(t *testing.T) string {
	t.Helper()
	_, f, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(f), "../.."))
}
func TestGeneratedEggAndSchemasCurrent(t *testing.T) {
	root := repository(t)
	egg, e := panel.Generate(panel.DefaultImage)
	if e != nil {
		t.Fatal(e)
	}
	for p, want := range map[string][]byte{"pterodactyl/s2script-cs2-egg.json": egg, "docs/bundled-plugins.md": panel.CatalogMarkdown()} {
		b, e := os.ReadFile(filepath.Join(root, p))
		if e != nil || !bytes.Equal(b, want) {
			t.Fatalf("stale generated %s: %v", p, e)
		}
	}
	for _, p := range []string{"release-manifest", "events", "actions"} {
		b, e := os.ReadFile(filepath.Join(root, "schemas", p+".schema.json"))
		var v map[string]any
		if e != nil || json.Unmarshal(b, &v) != nil || v["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Fatalf("invalid schema %s: %v", p, e)
		}
	}
	for _, p := range []string{"docs/acceptance.md", "docs/releases.md", "tests/acceptance/compose.yml"} {
		if _, e := os.Stat(filepath.Join(root, p)); e != nil {
			t.Fatal(e)
		}
	}
}
func TestDocumentedExampleConfigsValidate(t *testing.T) {
	root := repository(t)
	if _, e := config.LoadNode(filepath.Join(root, "examples/node.json")); e != nil {
		t.Fatal(e)
	}
	if _, e := plugins.ReadPolicy(filepath.Join(root, "examples/s2script-plugins.json")); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"shared-helper", "operations-helper", "backup-helper"} {
		b, e := os.ReadFile(filepath.Join(root, "examples", name+".json"))
		if e != nil {
			t.Fatal(e)
		}
		var v shared.HelperConfig
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if e = d.Decode(&v); e != nil {
			t.Fatal(e)
		}
		if e = v.ValidateStructure(); e != nil {
			t.Fatalf("%s: %v", name, e)
		}
	}
	b, e := os.ReadFile(filepath.Join(root, "examples/guard-helper.json"))
	if e != nil {
		t.Fatal(e)
	}
	var v guard.HelperConfig
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&v); e != nil {
		t.Fatal(e)
	}
	if e = v.Validate(); e != nil {
		t.Fatal(e)
	}
	serverRoot := t.TempDir()
	os.Mkdir(filepath.Join(serverRoot, "egg"), 0700)
	b, e = os.ReadFile(filepath.Join(root, "examples/server.json"))
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(serverRoot, "egg/server.json"), b, 0600)
	if _, e = config.LoadServer(serverRoot, func(string) (string, bool) { return "", false }); e != nil {
		t.Fatal(e)
	}
}
