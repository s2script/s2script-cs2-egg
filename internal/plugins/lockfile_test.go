// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLockRejectsIncompatibleAndInvalidFacts(t *testing.T) {
	for _, raw := range []string{`{}`, `{"schema_version":2,"plugins":[]}`, `{"schema_version":1,"plugins":null}`, `{"schema_version":1,"plugins":[],"unknown":1}`} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, LockFilename), []byte(raw), 0600)
		if _, e := ReadLock(root); e == nil {
			t.Fatal("accepted invalid lock", raw)
		}
	}
	root := t.TempDir()
	pluginFile(t, root, "hello.s2sp", "hello", "1.2.3", true)
	p := own(t, root)
	for _, mutate := range []func(*Installed){func(p *Installed) { p.SHA256 = "bad" }, func(p *Installed) { p.Source = "web" }, func(p *Installed) {
		p.Manifest = json.RawMessage(`{"id":"other","version":"1.2.3","apiVersion":"3.x"}`)
	}, func(p *Installed) { p.Source = "local" }, func(p *Installed) { p.Verification = "unverified" }} {
		copy := p
		mutate(&copy)
		if _, e := StageLock(t.TempDir(), []Installed{copy}); e == nil {
			t.Fatal("accepted invalid installed facts", copy)
		}
	}
	if _, e := StageLock(t.TempDir(), []Installed{p, p}); e == nil {
		t.Fatal("accepted duplicate lock identity")
	}
}
func TestModifiedOrRenamedPluginLosesOwnership(t *testing.T) {
	for _, kind := range []string{"modified", "renamed"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := pluginFile(t, root, "hello.s2sp", "hello", "1.2.3", true)
			own(t, root)
			if kind == "renamed" {
				os.Rename(path, filepath.Join(filepath.Dir(path), "renamed.s2sp"))
			} else {
				pluginFile(t, root, "hello.s2sp", "hello", "1.2.4", true)
			}
			got, e := Discover(root)
			if e != nil || len(got) != 1 || got[0].Managed || got[0].Verification != "unverified" || got[0].Source != "local" {
				t.Fatal(got, e)
			}
			if code, msg := cli(t, root, nil, "remove", "hello"); code != 1 || !strings.Contains(msg, "owned") {
				t.Fatalf("%d %s", code, msg)
			}
		})
	}
}
func TestManifestReadIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hello.s2sp")
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	w, _ := z.Create("manifest.json")
	w.Write(make([]byte, maxManifestBytes+1))
	z.Create("plugin.js")
	z.Close()
	f.Close()
	if _, e = ReadManifest(path); e == nil {
		t.Fatal("oversized manifest accepted")
	}
}
func TestPanelOverrideKeepsDefaultInheritance(t *testing.T) {
	if PanelOverride(map[string]bool{}, "hello") != "default" || PanelOverride(map[string]bool{"hello": false}, "hello") != "0" || PanelOverride(map[string]bool{"hello": true}, "hello") != "1" {
		t.Fatal("lost explicit/default distinction")
	}
}

func TestLockRejectsNoncanonicalStructFields(t *testing.T) {
	root := t.TempDir()
	pluginFile(t, root, "hello.s2sp", "hello", "1.2.3", false)
	p := own(t, root)
	b, e := json.Marshal(Lock{SchemaVersion: 1, Plugins: []Installed{p}})
	if e != nil {
		t.Fatal(e)
	}
	canonical := string(b)
	cases := map[string]string{
		"root alias":            `{"schema_version":2,"SCHEMA_VERSION":1,"plugins":[]}`,
		"plugins alias":         `{"schema_version":1,"Plugins":[]}`,
		"conflicting enabled":   strings.Replace(canonical, `"enabled":false`, `"enabled":false,"Enabled":true`, 1),
		"conflicting ownership": strings.Replace(canonical, `"managed":true`, `"managed":true,"Managed":false`, 1),
		"entry alias only":      strings.Replace(canonical, `"source":`, `"Source":`, 1),
		"status alias only":     strings.Replace(canonical, `"last_check_status":`, `"LAST_CHECK_STATUS":`, 1),
		"unknown entry field":   strings.Replace(canonical, `"enabled":false`, `"enabled":false,"Unknown":true`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if e := os.WriteFile(filepath.Join(root, LockFilename), []byte(raw), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := ReadLock(root); e == nil {
				t.Fatalf("accepted noncanonical installed facts: %s", raw)
			}
		})
	}
}
func TestLockPreservesCaseSensitiveRawManifestMaps(t *testing.T) {
	root := t.TempDir()
	raw := json.RawMessage(`{"id":"hello","version":"1.2.3","apiVersion":"3.x","pluginDependencies":{"Enabled":"*","enabled":"*"},"compiledAgainst":{"Source":"A","source":"B"},"extension":{"Enabled":false,"enabled":true,"Source":null}}`)
	p := Installed{Name: "hello", Version: "1.2.3", Source: "local", SHA256: strings.Repeat("a", 64), Filename: "hello.s2sp", Verification: "unverified", LastCheckStatus: "unverified", Manifest: raw}
	writeFacts(t, root, []Installed{p})
	lock, e := ReadLock(root)
	if e != nil {
		t.Fatal(e)
	}
	var got, want any
	if e = json.Unmarshal(lock.Plugins[0].Manifest, &got); e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw upstream map data changed: %s", lock.Plugins[0].Manifest)
	}
}
