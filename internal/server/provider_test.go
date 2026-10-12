// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
)

type providerFunc func(context.Context, config.Server) (bool, error)

func (f providerFunc) Prepare(ctx context.Context, c config.Server) (bool, error) { return f(ctx, c) }
func TestGameProviderCallerOwnsBothLocksAndFailureDoesNotFallback(t *testing.T) {
	root := t.TempDir()
	cfg := config.Server{Root: root}
	called := false
	failure := errors.New("claimed preparation failed")
	provider := providerFunc(func(ctx context.Context, c config.Server) (bool, error) {
		called = true
		if release, e := runtimelease.Acquire(root); !errors.Is(e, runtimelease.ErrHeld) {
			if e == nil {
				release()
			}
			t.Fatal("provider lacks lifetime lease")
		}
		if release, e := artifact.Acquire(root); e == nil {
			release()
			t.Fatal("provider lacks artifact lock")
		}
		return true, failure
	})
	if e := Prepare(context.Background(), cfg, Options{GameProvider: provider}); !errors.Is(e, failure) || !called {
		t.Fatalf("claimed error lost: %v", e)
	}
	if _, e := os.Stat(filepath.Join(root, "steamcmd")); !os.IsNotExist(e) {
		t.Fatal("claimed failure fell through to SteamCMD")
	}
	if release, e := runtimelease.Acquire(root); e != nil {
		t.Fatal(e)
	} else {
		release()
	}
}
func TestProviderUnclaimedErrorDoesNotFallThrough(t *testing.T) {
	sentinel := errors.New("uncertain provider response")
	o := defaultPreparation()
	steam := withProvider(o.steam, providerFunc(func(context.Context, config.Server) (bool, error) { return false, sentinel }))
	if _, e := steam(context.Background(), config.Server{Root: t.TempDir()}); !errors.Is(e, sentinel) {
		t.Fatal("provider uncertainty fell through")
	}
}
func TestNilAndDeclinedProviderPreserveStandalone(t *testing.T) {
	calls := 0
	steam := func(context.Context, config.Server) (string, error) { calls++; return "build", nil }
	for _, provider := range []GameProvider{nil, providerFunc(func(context.Context, config.Server) (bool, error) { return false, nil })} {
		got, e := withProvider(steam, provider)(context.Background(), config.Server{})
		if e != nil || got != "build" {
			t.Fatalf("fallback %s %v", got, e)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
