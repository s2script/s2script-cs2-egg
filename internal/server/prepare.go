// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/framework"
	"github.com/s2script/s2script-cs2-egg/internal/plugins"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/steam"
)

// GameProvider executes only inside the caller-owned runtime lease and artifact
// lock, after Recover. It may reserve a host generation, then materialize locally
// without recursively acquiring either lock. A claim or any error forbids Steam
// fallback. The provider must retain crash-safe generation references (Task9).
type GameProvider interface {
	Prepare(context.Context, config.Server) (handled bool, err error)
}
type WorkshopProvider interface {
	PrepareWorkshopLocked(context.Context, config.Server) error
}

type Options struct {
	ConsoleGuard     ConsoleGuard
	ArtifactFetcher  artifact.SelectedFetcher
	WorkshopProvider WorkshopProvider
	GameProvider     GameProvider
	ControlProvider  ControlProvider
}
type preparationRunKey struct{}

// PreparationRunID identifies the managed runtime; offline preparations use a separate run.
func PreparationRunID(ctx context.Context) string {
	id, _ := ctx.Value(preparationRunKey{}).(string)
	return id
}

func withProvider(standalone func(context.Context, config.Server) (string, error), provider GameProvider) func(context.Context, config.Server) (string, error) {
	return func(ctx context.Context, cfg config.Server) (string, error) {
		if provider != nil {
			handled, err := provider.Prepare(ctx, cfg)
			if err != nil {
				return "", err
			}
			if handled {
				return steam.InstalledBuild(cfg.Root)
			}
		}
		return standalone(ctx, cfg)
	}
}

type prepOptions struct {
	workshop WorkshopProvider
	retained func(context.Context, config.Server, map[string]string) (framework.Selection, error)
	steam    func(context.Context, config.Server) (string, error)
	resolve  func(context.Context, config.Server, map[string]string) (framework.Selection, error)
	native   func(context.Context, string, framework.Selection) (framework.Prepared, error)
	plugins  func(context.Context, config.Server, plugins.Policy, []plugins.Installed, framework.Selection, []framework.BundledArtifact) (plugins.Prepared, error)
}

func defaultPreparation() prepOptions {
	return prepOptions{steam: func(ctx context.Context, c config.Server) (string, error) { return prepareSteam(ctx, c, SteamSeed) }, resolve: framework.Resolve, retained: framework.Retained, native: framework.Prepare, plugins: plugins.Prepare}
}

// Prepare takes the runtime lease before artifact recovery and holds it through
// the complete stopped-server update. Run retains that lease through game exit.
func Prepare(ctx context.Context, cfg config.Server, options Options) error {
	release, e := runtimelease.Acquire(cfg.Root)
	if e != nil {
		return e
	}
	defer release()
	ctx = context.WithValue(ctx, preparationRunKey{}, randomID())
	_, e = prepareRuntimeOptions(ctx, cfg, func(kind, reason string, d map[string]string) {
		if kind == "server.warning" {
			fmt.Fprintln(os.Stderr, redactFor(cfg)("warning: "+reason+" "+d["message"]))
		}
	}, options)
	return e
}
func prepareRuntime(ctx context.Context, cfg config.Server, emit emitFunc) (map[string]string, error) {
	return prepareRuntimeOptions(ctx, cfg, emit, Options{})
}

func prepareRuntimeOptions(ctx context.Context, cfg config.Server, emit emitFunc, options Options) (map[string]string, error) {
	if cfg.Workshop.Enabled && options.WorkshopProvider == nil {
		return nil, errors.New("workshop sharing enabled but node unavailable")
	}
	preparation := defaultPreparation()
	if options.ArtifactFetcher != nil {
		preparation.native = func(ctx context.Context, root string, s framework.Selection) (framework.Prepared, error) {
			return framework.PrepareWithFetcher(ctx, root, s, options.ArtifactFetcher)
		}
		preparation.plugins = (plugins.Registry{Fetcher: options.ArtifactFetcher}).Prepare
	}
	preparation.workshop = options.WorkshopProvider
	preparation.steam = withProvider(preparation.steam, options.GameProvider)
	return prepareWith(ctx, cfg, emit, preparation)
}

// prepareWith requires the lifetime lease from its caller; only this function
// takes the artifact lock. The CLI callback enters reconcileManaged directly.
func prepareWith(ctx context.Context, cfg config.Server, emit emitFunc, o prepOptions) (map[string]string, error) {
	release, e := artifact.Acquire(cfg.Root)
	if e != nil {
		return nil, e
	}
	defer release()
	if e = artifact.Recover(cfg.Root); e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	build, e := o.steam(ctx, cfg)
	if e != nil {
		return nil, e
	}
	policy, e := plugins.ReadPolicy(cfg.Plugins.File)
	if e != nil {
		return nil, e
	}
	installed, e := plugins.Discover(cfg.Root)
	if e != nil {
		return nil, e
	}
	versions, e := reconcileManaged(ctx, cfg, policy, installed, emit, o)
	if e != nil {
		return nil, e
	}
	if o.workshop != nil {
		if e = o.workshop.PrepareWorkshopLocked(ctx, cfg); e != nil {
			return nil, e
		}
	}
	versions["steam_build"] = build
	if f, err := os.Open(filepath.Join(cfg.Root, ".s2shared.json")); err == nil {
		var local struct {
			Generation struct {
				ID string `json:"id"`
			} `json:"generation"`
		}
		err = json.NewDecoder(io.LimitReader(f, 64<<20)).Decode(&local)
		f.Close()
		if err != nil {
			return nil, errors.New("invalid shared generation status")
		}
		versions["shared_generation"] = local.Generation.ID
	}
	return versions, nil
}

// ReconcileLocked is a CLI/node integration boundary, NOT a locking entrypoint.
// Caller MUST hold runtimelease then artifact lock and have completed Recover.
// It does not run SteamCMD, release locks, or recursively call Prepare.
func ReconcileLocked(ctx context.Context, cfg config.Server, req plugins.ReconcileRequest, log io.Writer) error {
	if req.Root != cfg.Root || req.PolicyPath != cfg.Plugins.File {
		return errors.New("reconciliation configuration does not match locked root")
	}
	if info, e := os.Stat(filepath.Join(cfg.Root, "game/csgo/gameinfo.gi")); e != nil || !info.Mode().IsRegular() {
		return errors.New("installed game unavailable; run s2egg prepare before plugin reconciliation")
	}
	cfg.Plugins.Overrides = req.Overrides
	emit := func(kind, reason string, d map[string]string) {
		if kind == "server.warning" && log != nil {
			fmt.Fprintln(log, redactFor(cfg)("warning: "+reason+" "+d["message"]))
		}
	}
	_, e := reconcileManaged(ctx, cfg, req.Policy, req.Installed, emit, defaultPreparation())
	if e != nil {
		return errors.New(redactFor(cfg)(e.Error()))
	}
	return nil
}
func reconcileManaged(ctx context.Context, cfg config.Server, policy plugins.Policy, installed []plugins.Installed, emit emitFunc, o prepOptions) (map[string]string, error) {
	pins := map[string]string{}
	for name, p := range policy.Plugins {
		if p.Source == "release" && plugins.ExactVersion(p.Version) {
			pins[name] = p.Version
		}
	}
	selection, e := o.resolve(ctx, cfg, pins)
	if e != nil {
		return nil, e
	}
	prepared, e := o.native(ctx, cfg.Root, selection)
	if e != nil {
		prepared.Cleanup()
	}
	var unavailable *framework.UnavailableError
	if errors.As(e, &unavailable) && ctx.Err() == nil {
		selection, e = o.retained(ctx, cfg, pins)
		if e != nil {
			return nil, e
		}
		emit("server.warning", "retained_framework", map[string]string{"message": "official download unavailable; retrying verified retained generation"})
		prepared, e = o.native(ctx, cfg.Root, selection)
		if e != nil {
			prepared.Cleanup()
		}
	}
	if e != nil {
		return nil, e
	}
	defer prepared.Cleanup()
	selection = prepared.Selection
	if selection.Stale {
		emit("server.warning", "retained_framework", map[string]string{"message": "using verified retained framework generation"})
	}
	for _, warning := range selection.Warnings {
		emit("server.warning", "framework_warning", map[string]string{"message": warning})
	}
	p, e := o.plugins(ctx, cfg, policy, installed, selection, prepared.Bundled)
	if e != nil {
		return nil, e
	}
	defer p.Cleanup()
	// Plugins.Changes already contains its inventory lock. The combined commit is
	// the only point at which any native/plugin generation becomes live.
	changes := append(append([]artifact.Change{}, prepared.Changes...), p.Changes...)
	if e = artifact.Apply(ctx, cfg.Root, changes); e != nil {
		return nil, e
	}
	for _, check := range p.Checks {
		emit("server.plugin_check", check.Status, map[string]string{"name": check.Name, "available_version": check.AvailableVersion})
	}
	versions := map[string]string{"s2script": selection.S2Script.Version, "metamod": selection.Metamod.Version}
	if selection.HUD != nil {
		versions["hud"] = selection.HUD.Version
	}
	return versions, nil
}
