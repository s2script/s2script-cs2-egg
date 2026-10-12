// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cli(t *testing.T, root string, hook ReconcileFunc, args ...string) (int, string) {
	t.Helper()
	var out, errs bytes.Buffer
	args = append(args, "--root", root)
	code := RunCLI(context.Background(), args, func(k string) (string, bool) {
		if k == "ALLOW_TOKENLESS" {
			return "1", true
		}
		return "", false
	}, &out, &errs, hook)
	return code, out.String() + errs.String()
}
func own(t *testing.T, root string) Installed {
	t.Helper()
	items, e := Discover(root)
	if e != nil {
		t.Fatal(e)
	}
	p := items[0]
	p.Source = "registry"
	p.Managed = true
	p.Verification = "verified"
	p.LastCheckStatus = "current"
	writeFacts(t, root, []Installed{p})
	return p
}
func writeFacts(t *testing.T, root string, items []Installed) {
	t.Helper()
	c, e := StageLock(t.TempDir(), items)
	if e != nil {
		t.Fatal(e)
	}
	release, e := artifact.Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = artifact.Apply(context.Background(), root, []artifact.Change{c}); e != nil {
		t.Fatal(e)
	}
}
func TestCLIAddPersistsBeforeDeferredReconcile(t *testing.T) {
	root := t.TempDir()
	code, msg := cli(t, root, nil, "add", "hello-world", "--version", "1.2.3", "--disabled")
	if code != 1 || !strings.Contains(msg, "policy saved") || !strings.Contains(msg, "unavailable") {
		t.Fatalf("%d %s", code, msg)
	}
	p, e := ReadPolicy(filepath.Join(root, PolicyFilename))
	entry := p.Plugins["hello-world"]
	if e != nil || entry.Version != "1.2.3" || entry.Enabled == nil || *entry.Enabled {
		t.Fatalf("%+v %v", p, e)
	}
	items, e := Discover(root)
	if e != nil || len(items) != 0 {
		t.Fatal(items, e)
	}
	hook := func(ctx context.Context, r ReconcileRequest) error {
		if r.Policy.Plugins["hello-world"].Version != "1.2.3" {
			t.Fatal("hook missing saved policy")
		}
		if _, e := runtimelease.Acquire(root); e == nil {
			t.Fatal("hook ran without lifetime lease")
		}
		if _, e := artifact.Acquire(root); e == nil {
			t.Fatal("hook ran without artifact lock")
		}
		return errors.New("resolver test failure")
	}
	code, msg = cli(t, root, hook, "reconcile")
	if code != 1 || !strings.Contains(msg, "resolver test failure") {
		t.Fatalf("%d %s", code, msg)
	}
}
func TestCLIRoundTripAndLeaseGuard(t *testing.T) {
	root := t.TempDir()
	pluginFile(t, root, "hello.s2sp", "hello-world", "1.2.3", true)
	own(t, root)
	for _, args := range [][]string{{"pin", "hello-world", "--version", "1.2.3"}, {"disable", "hello-world"}, {"enable", "hello-world"}, {"unpin", "hello-world"}, {"inspect", "hello-world"}, {"list"}} {
		if code, msg := cli(t, root, nil, args...); code != 0 {
			t.Fatalf("%v: %d %s", args, code, msg)
		}
	}
	p, _ := ReadPolicy(filepath.Join(root, PolicyFilename))
	if p.Plugins["hello-world"].Version != "*" || !*p.Plugins["hello-world"].Enabled {
		t.Fatal(p)
	}
	release, e := runtimelease.Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if code, msg := cli(t, root, nil, "disable", "hello-world"); code != 1 || !strings.Contains(msg, "runtime") {
		t.Fatalf("%d %s", code, msg)
	}
	if code, msg := cli(t, root, nil, "pin", "hello-world", "--version", "1.2.3"); code != 0 {
		t.Fatalf("staged policy failed %d %s", code, msg)
	}
	p, _ = ReadPolicy(filepath.Join(root, PolicyFilename))
	if !*p.Plugins["hello-world"].Enabled {
		t.Fatal("blocked disable changed policy")
	}
}
func TestCLIRemovalPreservesLocalAndNeededPlugins(t *testing.T) {
	root := t.TempDir()
	path := pluginFile(t, root, "hello.s2sp", "hello-world", "1.2.3", true)
	if code, msg := cli(t, root, nil, "remove", "hello-world"); code != 1 || !strings.Contains(msg, "owned") {
		t.Fatalf("%d %s", code, msg)
	}
	p := own(t, root)
	p.Parents = []string{"consumer"}
	writeFacts(t, root, []Installed{p})
	if code, msg := cli(t, root, nil, "remove", "hello-world"); code != 1 || !strings.Contains(msg, "needed") {
		t.Fatalf("%d %s", code, msg)
	}
	p.Parents = nil
	writeFacts(t, root, []Installed{p})
	config := filepath.Join(root, "game/csgo/addons/s2script/configs/hello.json")
	os.MkdirAll(filepath.Dir(config), 0755)
	os.WriteFile(config, []byte("keep"), 0600)
	if code, msg := cli(t, root, nil, "remove", "hello-world"); code != 0 {
		t.Fatalf("%d %s", code, msg)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("artifact survived removal")
	}
	if b, e := os.ReadFile(config); e != nil || string(b) != "keep" {
		t.Fatal("config removed")
	}
}
func TestDiscoverOwnershipSurvivesPlacementOnly(t *testing.T) {
	root := t.TempDir()
	path := pluginFile(t, root, "hello.s2sp", "hello-world", "1.2.3", true)
	own(t, root)
	target, _ := Target("hello.s2sp", false)
	dest := filepath.Join(root, "game/csgo", target)
	os.MkdirAll(filepath.Dir(dest), 0755)
	os.Rename(path, dest)
	items, e := Discover(root)
	if e != nil || !items[0].Managed || items[0].Source != "registry" || items[0].Enabled {
		t.Fatalf("%+v %v", items, e)
	}
	os.Remove(dest)
	items, e = Discover(root)
	if e != nil || len(items) != 1 || items[0].LastCheckStatus != "missing" {
		t.Fatal(items, e)
	}
}
func TestCLIRejectsRecoveryWhileRuntimeHeld(t *testing.T) {
	root := t.TempDir()
	release, e := runtimelease.Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	os.Mkdir(filepath.Join(root, ".artifact-txn"), 0700)
	if code, msg := cli(t, root, nil, "add", "hello", "--version", "1.2.3"); code != 1 || !strings.Contains(msg, "recovery") {
		t.Fatalf("%d %s", code, msg)
	}
}

func TestEnableRequiredDependenciesDefersToResolver(t *testing.T) {
	root := t.TempDir()
	path := pluginFile(t, root, "consumer.s2sp", "consumer", "1.2.3", false)
	f, _ := os.Create(path)
	z := zip.NewWriter(f)
	w, _ := z.Create("manifest.json")
	w.Write([]byte(`{"id":"consumer","version":"1.2.3","apiVersion":"3.x","pluginDependencies":{"provider":"^1.0.0"}}`))
	w, _ = z.Create("plugin.js")
	w.Write([]byte("code"))
	z.Close()
	f.Close()
	own(t, root)
	if code, msg := cli(t, root, nil, "enable", "consumer"); code != 1 || !strings.Contains(msg, "dependency") {
		t.Fatalf("%d %s", code, msg)
	}
	if _, e := os.Stat(path); e != nil {
		t.Fatal("dependency conflict moved disabled plugin")
	}
}
func TestRequiredPublishedInterfaceBlocksRemoval(t *testing.T) {
	candidate := Installed{Name: "provider", Manifest: json.RawMessage(`{"id":"provider","version":"3.1.0","apiVersion":"3.x","publishes":{"@community/mapchooser":{"version":"1.2.0"}}}`)}
	consumer := Installed{Name: "consumer", Manifest: json.RawMessage(`{"id":"consumer","version":"1.0.0","apiVersion":"3.x","pluginDependencies":{"@community/mapchooser":"^1.2.0"}}`)}
	if e := checkRemoval(candidate, []Installed{candidate, consumer}); e == nil || !strings.Contains(e.Error(), "needed") {
		t.Fatal("ignored published interface consumer", e)
	}
	consumer.Manifest = json.RawMessage(`{"id":"consumer","version":"1.0.0","apiVersion":"3.x","pluginDependencies":{"unknown-interface":"^1.2.0"}}`)
	if e := checkRemoval(candidate, []Installed{candidate, consumer}); e == nil || !strings.Contains(e.Error(), "evidence unavailable") {
		t.Fatal("guessed unknown provider", e)
	}
}
func TestRediscoveredMissingArtifactIsPresent(t *testing.T) {
	root := t.TempDir()
	pluginFile(t, root, "hello.s2sp", "hello", "1.2.3", true)
	p := own(t, root)
	p.LastCheckStatus = "missing"
	writeFacts(t, root, []Installed{p})
	got, e := Discover(root)
	if e != nil || got[0].LastCheckStatus == "missing" {
		t.Fatal(got, e)
	}
}
