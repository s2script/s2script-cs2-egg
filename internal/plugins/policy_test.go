// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boolp(v bool) *bool { return &v }
func pluginFile(t *testing.T, root, filename, id, version string, enabled bool) string {
	t.Helper()
	rel, e := Target(filename, enabled)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "game/csgo", rel)
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	m, _ := json.Marshal(map[string]any{"id": id, "version": version, "apiVersion": "3.x", "pluginDependencies": map[string]string{}, "optionalPluginDependencies": map[string]string{}})
	w, _ := z.Create("manifest.json")
	w.Write(m)
	w, _ = z.Create("plugin.js")
	w.Write([]byte("throw new Error('must never execute');"))
	z.Close()
	f.Close()
	return path
}
func TestDiscoverActiveAndDisabled(t *testing.T) {
	root := t.TempDir()
	pluginFile(t, root, "renamed.s2sp", "@s2script/clientprefs", "0.8.1", true)
	pluginFile(t, root, "other.s2sp", "hello-world", "1.2.3", false)
	got, e := Discover(root)
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || got[0].Name != "@s2script/clientprefs" || !got[0].Enabled || got[1].Enabled || got[1].Source != "local" || got[1].Verification != "unverified" || len(got[0].SHA256) != 64 {
		t.Fatalf("inventory: %+v", got)
	}
}
func TestFirstInstallDisabled(t *testing.T) {
	p, e := Target("hello.s2sp", false)
	if e != nil || p != "addons/s2script/plugins/disabled/hello.s2sp" {
		t.Fatalf("%s %v", p, e)
	}
	for _, p := range []string{"../x.s2sp", "a/b.s2sp", "x.zip", "\\x.s2sp"} {
		if _, e = Target(p, true); e == nil {
			t.Fatalf("unsafe target %s", p)
		}
	}
}
func TestPolicyPinsSurviveRediscovery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s2script-plugins.json")
	data := []byte(`{"schema_version":1,"plugins":{"hello-world":{"source":"registry","version":"1.2.3","enabled":false},"other":{"source":"registry"}}}`)
	os.WriteFile(path, data, 0600)
	p, e := ReadPolicy(path)
	if e != nil {
		t.Fatal(e)
	}
	if p.Plugins["other"].Version != "*" {
		t.Fatal(p)
	}
	pluginFile(t, root, "hello.s2sp", "hello-world", "1.2.4", true)
	if _, e = Discover(root); e != nil {
		t.Fatal(e)
	}
	p, e = ReadPolicy(path)
	if e != nil || p.Plugins["hello-world"].Version != "1.2.3" {
		t.Fatalf("pin changed %+v %v", p, e)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("discovery rewrote policy")
	}
}
func TestPanelDefaultInheritsPolicy(t *testing.T) {
	for _, tc := range []struct {
		panel          string
		desired        *bool
		upstream, want bool
	}{{"default", boolp(false), true, false}, {"1", boolp(false), false, true}, {"0", boolp(true), true, false}, {"default", nil, false, false}, {"", nil, true, true}} {
		v, e := ResolveEnabled("hello-world", tc.desired, tc.upstream, tc.panel)
		if e != nil || v != tc.want {
			t.Fatalf("%+v: %v %v", tc, v, e)
		}
	}
	if _, e := ResolveEnabled("hello-world", nil, true, "yes"); e == nil {
		t.Fatal("accepted invalid override")
	}
}
func TestDuplicateIdentityRejected(t *testing.T) {
	root := t.TempDir()
	pluginFile(t, root, "a.s2sp", "hello", "1.2.3", true)
	pluginFile(t, root, "b.s2sp", "hello", "1.2.3", false)
	if _, e := Discover(root); e == nil {
		t.Fatal("accepted duplicate identity under different filenames")
	}
}
func TestPolicyRejectsInvalid(t *testing.T) {
	for _, s := range []string{`{"schema_version":2,"plugins":{}}`, `{"schema_version":1,"plugins":{"bad/name":{"source":"registry"}}}`, `{"schema_version":1,"plugins":{"hello":{"source":"web"}}}`, `{"schema_version":1,"plugins":{"hello":{"source":"registry","version":"1.2"}}}`, `{"schema_version":1,"plugins":{"hello":{"source":"registry","enabled":null}}}`, `{"schema_version":1,"plugins":{"hello":{"source":"local"},"hello":{"source":"registry"}}}`} {
		p := filepath.Join(t.TempDir(), "policy.json")
		os.WriteFile(p, []byte(s), 0600)
		if _, e := ReadPolicy(p); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestManifestPreservesInterfacesAndRejectsUnsafeZIP(t *testing.T) {
	root := t.TempDir()
	path := pluginFile(t, root, "p.s2sp", "provider", "3.1.0", true)
	f, _ := os.Create(path)
	z := zip.NewWriter(f)
	w, _ := z.Create("manifest.json")
	w.Write([]byte(`{"id":"provider","version":"3.1.0","apiVersion":"2.x","pluginDependencies":{"@community/mapchooser":"^1.2.0"},"publishes":{"@community/service":{"version":"1.2.0","typesSha256":"abc"}},"compiledAgainst":{"x":1}}`))
	w, _ = z.Create("plugin.js")
	w.Write([]byte("code"))
	w, _ = z.Create("gamedata/file.json")
	w.Write([]byte("{}"))
	z.Close()
	f.Close()
	m, e := ReadManifest(path)
	if e != nil || m.ID != "provider" || m.APIVersion != "2.x" || m.Publishes["@community/service"].Version != "1.2.0" || len(m.CompiledAgainst) == 0 {
		t.Fatalf("%+v %v", m, e)
	}
	f, _ = os.Create(path)
	z = zip.NewWriter(f)
	w, _ = z.Create("../escape")
	w.Write([]byte("x"))
	z.Close()
	f.Close()
	if _, e = ReadManifest(path); e == nil {
		t.Fatal("unsafe metadata accepted")
	}
}

func TestPolicyRequiresSchemaAndPlugins(t *testing.T) {
	for _, s := range []string{`{}`, `{"schema_version":1}`, `{"plugins":{}}`} {
		path := filepath.Join(t.TempDir(), "policy.json")
		os.WriteFile(path, []byte(s), 0600)
		if _, e := ReadPolicy(path); e == nil {
			t.Fatalf("accepted missing structural field %s", s)
		}
	}
}
func TestManifestDuplicateKeysRejectedAndRawNullPreserved(t *testing.T) {
	if _, e := ParseManifest([]byte(`{"id":"first","id":"second","version":"1.2.3","apiVersion":"3.x"}`)); e == nil {
		t.Fatal("ambiguous identity accepted")
	}
	m, e := ParseManifest([]byte(`{"id":"hello","version":"1.2.3","apiVersion":"3.x","extension":null}`))
	if e != nil {
		t.Fatal(e)
	}
	p := Installed{Name: "hello", Version: "1.2.3", Source: "local", Filename: "hello.s2sp", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Verification: "unverified", LastCheckStatus: "unverified", Manifest: m.Raw}
	root := t.TempDir()
	writeFacts(t, root, []Installed{p})
	if _, e = ReadLock(root); e != nil {
		t.Fatalf("raw upstream extension broke lock: %v", e)
	}
}

func TestVersionAndAPISyntax(t *testing.T) {
	for _, v := range []string{"1.2.3", "1.2.3-alpha.1+build.01"} {
		if !ExactVersion(v) {
			t.Fatal("rejected pin", v)
		}
	}
	for _, v := range []string{"1.2", "01.2.3", "1.2.3-01", "1.2.3;touch x", "$(cmd)"} {
		if _, e := NormalizeVersion(v); e == nil {
			t.Fatal("accepted unsafe version", v)
		}
	}
	for _, v := range []string{"*", "^1.2.3", ">=1.2.3 <2.0.0", "~1.2.3 || >=2.0.0"} {
		if normalized, e := NormalizeVersion(v); e != nil || normalized != v {
			t.Fatal(v, normalized, e)
		}
	}
	if _, e := ParseManifest([]byte(`{"id":"hello","version":"1.2.3","apiVersion":"3;bad"}`)); e == nil {
		t.Fatal("accepted unsafe API version")
	}
}
func TestDiscoverRejectsMissingRoot(t *testing.T) {
	if _, e := Discover(filepath.Join(t.TempDir(), "missing")); e == nil {
		t.Fatal("invented inventory for absent server root")
	}
}
func TestUnsafeArchiveMembersAndSymlinks(t *testing.T) {
	for _, kind := range []string{"duplicate", "symlink", "file-parent", "path-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := pluginFile(t, root, "hello.s2sp", "hello", "1.2.3", true)
			f, _ := os.Create(path)
			z := zip.NewWriter(f)
			w, _ := z.Create("manifest.json")
			w.Write([]byte(`{"id":"hello","version":"1.2.3","apiVersion":"3.x"}`))
			w, _ = z.Create("plugin.js")
			w.Write([]byte("code"))
			switch kind {
			case "duplicate":
				z.Create("manifest.json")
			case "symlink":
				h := &zip.FileHeader{Name: "link"}
				h.SetMode(os.ModeSymlink | 0777)
				w, _ = z.CreateHeader(h)
				w.Write([]byte("target"))
			case "file-parent":
				z.Create("data")
				z.Create("data/child")
			}
			z.Close()
			f.Close()
			if kind == "path-symlink" {
				outside := filepath.Join(t.TempDir(), "outside.s2sp")
				os.Rename(path, outside)
				os.Symlink(outside, path)
			}
			if _, e := ReadManifest(path); e == nil {
				t.Fatal("unsafe archive/read accepted")
			}
		})
	}
}

func TestManifestRejectsCaseAliasedIdentityFields(t *testing.T) {
	for _, raw := range []string{`{"id":"hello","ID":"other","version":"1.2.3","apiVersion":"3.x"}`, `{"id":"hello","version":"1.2.3","apiVersion":"3.x","PluginDependencies":{"other":"*"}}`} {
		if _, e := ParseManifest([]byte(raw)); e == nil {
			t.Fatal("accepted case-aliased manifest field", raw)
		}
	}
}

func TestPolicyRejectsNoncanonicalStructFields(t *testing.T) {
	cases := map[string]string{
		"conflicting enablement": `{"schema_version":1,"plugins":{"hello":{"source":"registry","enabled":false,"Enabled":true}}}`,
		"root alias":             `{"schema_version":2,"SCHEMA_VERSION":1,"plugins":{}}`,
		"plugins alias":          `{"schema_version":1,"Plugins":{}}`,
		"entry alias only":       `{"schema_version":1,"plugins":{"hello":{"Source":"registry"}}}`,
		"mixed case entry":       `{"schema_version":1,"plugins":{"hello":{"source":"registry","eNaBlEd":false}}}`,
		"unknown root field":     `{"schema_version":1,"plugins":{},"Unknown":true}`,
		"unknown entry field":    `{"schema_version":1,"plugins":{"hello":{"source":"registry","Unknown":true}}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, PolicyFilename)
			if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := ReadPolicy(path); e == nil {
				t.Fatalf("accepted noncanonical policy fields: %s", raw)
			}
		})
	}
	root := t.TempDir()
	raw := cases["conflicting enablement"]
	os.WriteFile(filepath.Join(root, PolicyFilename), []byte(raw), 0600)
	if code, msg := cli(t, root, nil, "list"); code != 1 || !strings.Contains(msg, "Enabled") {
		t.Fatalf("CLI did not reject conflicting enabled alias: %d %s", code, msg)
	}
}
func TestPolicyPreservesCaseSensitiveIdentityKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), PolicyFilename)
	raw := `{"schema_version":1,"plugins":{"Enabled":{"source":"registry","enabled":false,"with":["Source","source"]},"enabled":{"source":"registry","enabled":true}}}`
	if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	p, e := ReadPolicy(path)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Plugins) != 2 || *p.Plugins["Enabled"].Enabled || !*p.Plugins["enabled"].Enabled || len(p.Plugins["Enabled"].With) != 2 {
		t.Fatalf("case-sensitive data keys were changed: %+v", p)
	}
}
