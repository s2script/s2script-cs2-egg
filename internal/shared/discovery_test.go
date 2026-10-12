// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestHelperDiscoveryNeverAcquiresStoppedLease(t *testing.T) {
	volume := t.TempDir()
	unlock, err := runtimelease.Acquire(volume)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	sock := filepath.Join(t.TempDir(), "helper.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeHelper(ctx, l, HelperConfig{Volume: volume, SharedRoot: "/configured", NodeUID: uint32(os.Geteuid())})
	}()
	defer func() { cancel(); l.Close(); <-done }()
	h, err := volumehelper.Discover(ctx, sock, uint32(os.Geteuid()))
	if err != nil || h.DiscoveryVersion != 1 || len(h.Provides) != 1 || h.Provides[0] != "shared.attach.v1" {
		t.Fatal(h, err)
	}
	if _, err := os.Stat(filepath.Join(volume, ".artifact-lock")); !os.IsNotExist(err) {
		t.Fatal("discovery touched stopped artifact path", err)
	}
}
