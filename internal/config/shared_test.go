// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSharedStoreCannotOverlapPrivateVolumes(t *testing.T) {
	n := Node{SchemaVersion: 1, ID: "n", StateDir: "/var/lib/node", Shared: SharedNode{Enabled: true, Root: "/srv/one", SteamCMD: "/opt/steam/steamcmd.sh"}, Servers: []NodeServer{{ID: "one", Volume: "/srv/one", AccessGID: 1, Allocation: Allocation{IP: "127.0.0.1", Port: 27015}}}}
	if e := n.Validate(); e == nil {
		t.Fatal("shared store overlaps private volume")
	}
}
func TestConfiguredHostHardlinksCanBeVerifiedAtStartup(t *testing.T) {
	root := t.TempDir()
	if e := os.Mkdir(filepath.Join(root, "egg"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "egg/server.json"), []byte(`{"shared":{"mode":"hardlink"},"steam":{"allow_tokenless":true}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadServer(root, func(string) (string, bool) { return "", false }); e != nil {
		t.Fatal("operator-installed hardlinks cannot survive startup preparation", e)
	}
}
