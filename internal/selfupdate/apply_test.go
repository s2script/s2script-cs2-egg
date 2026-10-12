// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureRelease(t *testing.T, version, body string) Release {
	t.Helper()
	files := map[string]releasecontract.FileDigest{}
	for _, key := range []string{"s2node", "s2egg", "source", "egg", "deployment"} {
		files[key] = releasecontract.FileDigest{SHA256: storeio.Hash([]byte(body)), Size: int64(len(body))}
	}
	m, err := releasecontract.New(version, strings.Repeat("a", 40), files)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	return Release{Manifest: m, ManifestSHA256: storeio.Hash(b), Version: version, SourceRevision: m.Source.Revision, AssetURL: m.Node().URL, SHA256: m.Node().SHA256, Protocol: m.Compatibility.NodeContainer.Protocol, verified: true}
}
func fixtureManager(t *testing.T, body string) *Manager {
	t.Helper()
	root := filepath.Join(t.TempDir(), "node-releases")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return &Manager{Root: root, Client: Client{Enabled: true, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	})}}}
}
func TestSelfUpdateKeepsPreviousBinary(t *testing.T) {
	ctx := context.Background()
	m := fixtureManager(t, "binary")
	a := fixtureRelease(t, "1.0.0", "binary")
	b := fixtureRelease(t, "1.1.0", "binary")
	if err := m.Stage(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := m.Initialize(ctx, a.Version); err != nil {
		t.Fatal(err)
	}
	if err := m.Stage(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate(ctx, b.Version); err != nil {
		t.Fatal(err)
	}
	j, err := m.Status()
	if err != nil || j.Current != "1.0.0" || j.Candidate != "1.1.0" || j.Phase != "pending" {
		t.Fatal(j, err)
	}
	for _, v := range []string{a.Version, b.Version} {
		if _, err := os.Stat(m.binary(v)); err != nil {
			t.Fatal("previous binary lost", err)
		}
	}
}
func TestSameVersionDifferentDigestRefused(t *testing.T) {
	m := fixtureManager(t, "one")
	if err := m.Stage(context.Background(), fixtureRelease(t, "1.0.0", "one")); err != nil {
		t.Fatal(err)
	}
	if err := m.Stage(context.Background(), fixtureRelease(t, "1.0.0", "two")); err == nil {
		t.Fatal("rewrote immutable release")
	}
}
func TestPartialOrSymlinkReleaseCannotActivate(t *testing.T) {
	m := fixtureManager(t, "binary")
	a := fixtureRelease(t, "1.0.0", "binary")
	if err := m.Stage(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.binary(a.Version)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", m.binary(a.Version)); err != nil {
		t.Fatal(err)
	}
	if err := m.Initialize(context.Background(), a.Version); err == nil {
		t.Fatal("selected symlink executable")
	}
}
func TestReadOnlyDeploymentRefused(t *testing.T) {
	m := fixtureManager(t, "binary")
	if err := os.Chmod(m.Root, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(m.Root, 0700)
	if err := m.Stage(context.Background(), fixtureRelease(t, "1.0.0", "binary")); err == nil {
		t.Fatal("read-only managed root accepted")
	}
}
