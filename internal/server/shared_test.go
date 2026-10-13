// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedMarkerForbidsStandaloneSteam(t *testing.T) {
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, ".s2shared.json"), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := prepareSteam(context.Background(), config.Server{Root: root}, t.TempDir(), nil); e == nil {
		t.Fatal("shared tree entered standalone preparation")
	}
	if _, e := os.Stat(filepath.Join(root, "steamcmd")); !os.IsNotExist(e) {
		t.Fatal("SteamCMD initialization ran before shared guard")
	}
}
