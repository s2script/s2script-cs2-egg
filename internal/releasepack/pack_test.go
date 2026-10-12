// SPDX-License-Identifier: GPL-3.0-or-later
package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
)

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = root
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                      "module github.com/s2script/s2script-cs2-egg\n\ngo 1.27.2\n",
		"internal/buildinfo/build.go": "package buildinfo\nvar Version=\"dev\";var Revision=\"unknown\"\n",
		"LICENSE":                     "fixture license\n", "NOTICE": "fixture notice\n", "third_party/Go-LICENSE": "fixture Go notice\n",
		"deploy/s2node.service": "fixture service\n", "deploy/s2node-launcher.conf": "fixture launcher\n",
		"examples/node.json": "{}\n", "schemas/events.schema.json": "{}\n",
	}
	main := "package main\nimport (\"fmt\";\"github.com/s2script/s2script-cs2-egg/internal/buildinfo\")\nfunc main(){fmt.Println(buildinfo.Version,buildinfo.Revision)}\n"
	files["cmd/s2egg/main.go"] = main
	files["cmd/s2node/main.go"] = main
	for p, s := range files {
		p = filepath.Join(root, p)
		if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(s), 0644); e != nil {
			t.Fatal(e)
		}
	}
	git(t, root, "init", "-q")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "fixture")
	git(t, root, "tag", "-a", "v1.2.3-alpha.1", "-m", "fixture")
	return root
}
func TestReleaseMetadataPointsToSourceRevision(t *testing.T) {
	root := fixture(t)
	revision := git(t, root, "rev-parse", "HEAD")
	parent := t.TempDir()
	opts := Options{Root: root, Tag: "v1.2.3-alpha.1", Output: filepath.Join(parent, "one"), Go: filepath.Join(runtime.GOROOT(), "bin/go")}
	m, e := Build(context.Background(), opts)
	if e != nil {
		t.Fatal(e)
	}
	if m.Source.Revision != revision || m.Tag != opts.Tag || m.Channel != "prerelease" {
		t.Fatal(m)
	}
	b, e := os.ReadFile(filepath.Join(opts.Output, releasecontract.ManifestName))
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := releasecontract.Decode(b)
	if e != nil || decoded.Source.Revision != revision {
		t.Fatal(e, decoded)
	}
	for _, d := range m.Descriptors() {
		if e := VerifyFile(filepath.Join(opts.Output, d.Name), releasecontract.FileDigest{SHA256: d.SHA256, Size: d.Size}); e != nil {
			t.Fatal(e)
		}
	}
	bin := filepath.Join(opts.Output, m.Node().Name)
	out, e := exec.Command(bin, "version").CombinedOutput()
	if e != nil || string(out) != "1.2.3-alpha.1 "+revision+"\n" {
		t.Fatal(string(out), e)
	}
	opts.Output = filepath.Join(parent, "two")
	if _, e = Build(context.Background(), opts); e != nil {
		t.Fatal(e)
	}
	a, _ := os.ReadDir(filepath.Join(parent, "one"))
	for _, f := range a {
		one, _ := os.ReadFile(filepath.Join(parent, "one", f.Name()))
		two, _ := os.ReadFile(filepath.Join(parent, "two", f.Name()))
		if !bytes.Equal(one, two) {
			t.Fatalf("non-reproducible %s", f.Name())
		}
	}
	source, _ := os.Open(filepath.Join(opts.Output, m.Source.Archive.Name))
	defer source.Close()
	gz, e := gzip.NewReader(source)
	if e != nil {
		t.Fatal(e)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h.Uid != 0 || h.Gid != 0 || h.ModTime.Unix() != 1767225600 {
			t.Fatal(h)
		}
		if strings.HasSuffix(h.Name, "cmd/s2node/main.go") {
			found = true
		}
	}
	if !found {
		t.Fatal("compiled source missing")
	}
	if _, e = Build(context.Background(), opts); e == nil {
		t.Fatal("overwrote release output")
	}
	os.WriteFile(filepath.Join(root, "untracked"), []byte("dirty"), 0644)
	opts.Output = filepath.Join(parent, "dirty")
	if _, e = Build(context.Background(), opts); e == nil {
		t.Fatal("accepted untracked source")
	}
	os.Remove(filepath.Join(root, "untracked"))
	os.WriteFile(filepath.Join(root, "NOTICE"), []byte("dirty"), 0644)
	if _, e = Build(context.Background(), opts); e == nil {
		t.Fatal("accepted changed source")
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "other")
	if _, e = Build(context.Background(), opts); e == nil {
		t.Fatal("accepted tag/HEAD mismatch")
	}
}
func TestPublisherAssetIntegrity(t *testing.T) {
	f := map[string]releasecontract.FileDigest{"asset": {SHA256: strings.Repeat("a", 64), Size: 12}}
	asset := PublisherAsset{Name: "asset", State: "uploaded", Size: 12, Digest: "sha256:" + strings.Repeat("a", 64)}
	if e := VerifyPublished(f, []PublisherAsset{asset}); e != nil {
		t.Fatal(e)
	}
	for _, mut := range []func(*PublisherAsset){func(a *PublisherAsset) { a.Size++ }, func(a *PublisherAsset) { a.Digest = "sha256:" + strings.Repeat("b", 64) }, func(a *PublisherAsset) { a.State = "starter" }} {
		bad := asset
		mut(&bad)
		if VerifyPublished(f, []PublisherAsset{bad}) == nil {
			t.Fatal("accepted publisher mismatch")
		}
	}
	if VerifyPublished(f, []PublisherAsset{asset, asset}) == nil || VerifyPublished(f, nil) == nil {
		t.Fatal("accepted absent/duplicate assets")
	}
	b, _ := json.Marshal(asset)
	if !bytes.Contains(b, []byte("sha256:")) {
		t.Fatal(string(b))
	}
}
func TestAliasCannotRegressOrChangeExistingIdentity(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	rev := strings.Repeat("a", 40)
	candidate := ImageIdentity{Version: "1.2.3", Revision: rev, Digest: digest}
	old := candidate
	old.Version = "1.2.2"
	if move, e := Promote("stable", &old, candidate); !move || e != nil {
		t.Fatal(move, e)
	}
	old.Version = "2.0.0"
	if move, e := Promote("prerelease", &old, candidate); move || e != nil {
		t.Fatal(move, e)
	}
	old = candidate
	old.Digest = "sha256:" + strings.Repeat("b", 64)
	if _, e := Promote("stable", &old, candidate); e == nil {
		t.Fatal("equal-version conflict")
	}
	candidate.Version = "1.3.0-alpha.1"
	if move, e := Promote("stable", nil, candidate); move || e != nil {
		t.Fatal(move, e)
	}
	old.Version = "garbage"
	if _, e := Promote("prerelease", &old, candidate); e == nil {
		t.Fatal("unknown alias identity")
	}
}
