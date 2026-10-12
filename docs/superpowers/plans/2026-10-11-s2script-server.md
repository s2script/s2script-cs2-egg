# S2Script Server and Plugins Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the standalone server entrypoint, container/egg, safe S2Script startup updates, installed-plugin controls, and server lifecycle webhooks.

**Architecture:** Validated configuration feeds a stopped-server preparation pipeline, then process supervision owns the game lifecycle. Source adapters prepare verified artifacts; a shared transaction primitive commits them before launch. Plugin policy and installed facts remain separate.

**Tech Stack:** Go 1.27.2, Linux amd64, SteamCMD, Metamod, S2Script, Docker, standard-library test HTTP servers and temporary filesystems.

**Spec:** [Approved design](../specs/2026-10-11-s2script-cs2-egg-design.md); read the [master plan](2026-10-11-s2script-cs2-egg.md) for environment and execution constraints.

## Global Constraints

- License project code under GPL-3.0-or-later, matching CS2-Egg-Go.
- App ID is 730.
- No CounterStrikeSharp, Swiftly, or ModSharp installer or selector is included.
- Maintain a list of installed plugins and check every plugin on that list at startup.
- Bundled plugins inherit S2Script's standard enablement unless the operator overrides it.
- Deliver webhook notifications in the first runnable server milestone.
- No Docker socket or Wings administrator credential is mounted into a game container.

## Review Focus

- Pins and `enabled: false` must survive first installation, rediscovery, and upgrade, including disabled dependencies: Tasks 4–6.
- A stale S2Script release ZIP must not overwrite operator admin credentials, SQLite data, or custom gamedata: Tasks 2–3.
- A symlink already present in the server volume must not redirect an otherwise harmless archive entry outside the managed root: Task 2.
- Partial registry results, identity collisions, and modified manual plugins must not cause a partial or mistaken upgrade: Tasks 4–5.
- Readiness failures and retries must not send false/duplicate server-up notifications or hang shutdown: Tasks 6–7.

---

## File boundaries

`cmd/s2egg` owns CLI wiring; `internal/config` owns policy validation; `internal/panel` generates egg controls; `internal/artifact` owns safe downloads/extraction/transactions; `internal/framework` prepares Metamod/S2Script/HUD files; `internal/steam` invokes SteamCMD; `internal/plugins` owns inventory, reconciliation, and registry semantics; `internal/server` owns preparation and lifecycle; `internal/events` defines reusable events; `internal/alerts` delivers configured webhooks. Keep protocol wire types out of these internal implementations until Plan 2 introduces them.

Use module path `github.com/s2script/s2script-cs2-egg`. Each task names its public-to-other-packages contracts below; implementation-only helpers stay private. Tests live beside their owning code.

### Task 1: Validated server configuration, diagnostic CLI, and egg controls

**Files:** Create `go.mod`, `LICENSE`, `NOTICE`, `README.md`, `docs/parity.md`, `Makefile`, `cmd/s2egg/main.go`, `cmd/egggen/main.go`, `internal/config/server.go`, `internal/config/server_test.go`, `internal/plugins/catalog.go`, `internal/panel/egg.go`, `internal/panel/egg_test.go`, `pterodactyl/s2script-cs2-egg.json`.

**Interfaces:** Produce `config.LoadServer(root string, lookup func(string) (string, bool)) (Server, error)` and `config.Server` with validated root, identity, Steam, framework, plugin-policy, launch, readiness, and webhook settings. Produce `plugins.Bundled() []Builtin`, where `Builtin` has `Name string`, `DefaultEnabled bool`, and `Variable string`; the catalog contains the spec's 18 identities. Produce `panel.Generate(image string) ([]byte, error)` and `s2egg config check|show` / `s2egg doctor` (read-only diagnostics, secrets redacted).

- [ ] Obtain and verify the master plan's Go toolchain in a task-owned directory. Record any permitted reused source with original revision/path/notices; do not copy an upstream module without reading and testing it.
- [ ] Write `TestServerConfigRejectsInvalidPortAndBoolean`, `TestConfigRedactsSecrets`, and `TestEggMatchesCatalog`. Assert `appid == 730`, exactly 18 unique plugin controls, 14 effective enabled defaults, four disabled defaults, control values `default|0|1`, and no alternate-framework variables. Missing GSLT requires explicit tokenless mode; custom parameters containing shell syntax remain data.
- [ ] Run `go test ./internal/config ./internal/panel` and observe failures for the missing contracts before implementing them.
- [ ] Implement validated config/precedence, catalog and deterministic egg generation; wire read-only diagnostics. Include GPL-3.0 text with GPL-3.0-or-later SPDX notice, initial feature-status documentation, and README credits at the bottom. Describe prospective behavior as prospective.
- [ ] Run the focused tests and `go run ./cmd/egggen` in check mode against the saved JSON; require matching output and valid panel variable definitions. Commit as `feat: add S2Script server configuration and egg controls`.

### Task 2: Verified artifacts and recoverable file transactions

**Files:** Create `internal/artifact/download.go`, `extract.go`, `transaction.go`, `lock.go`, and matching `_test.go` files.

**Interfaces:** Produce `artifact.Fetch(ctx context.Context, client *http.Client, url, sha256, dest string, limit int64) error`; `artifact.Extract(archive, stage string, limit int64) error` for ZIP/tar.gz; and `artifact.Apply(ctx context.Context, root string, changes []Change) error` / `artifact.Recover(root string) error`. `Change` has relative `Path`, staged `Source`, optional `Remove bool`, and `CreateOnly bool`. Produce `artifact.Acquire(root string) (release func() error, err error)` for the cross-process update lock. Changes cannot traverse symlink parents or escape the managed root.

- [ ] Write `TestFetchRejectsWrongHashAndOversize`, `TestExtractRejectsTraversalAndSymlinkEscape`, `TestApplyPreservesCreateOnlyFiles`, and `TestRecoverInterruptedCommit`. Assert the original complete generation survives download failure and an interrupted multi-file apply recovers before consumers run; `../escape`, absolute names, and preexisting escaping symlinks never write outside the root.
- [ ] Run `go test ./internal/artifact` and confirm the tests fail before implementation.
- [ ] Implement bounded HTTPS fetching, redirect/auth-origin restrictions, safe archive staging, a journaled recoverable transaction with retained previous files, and a real interprocess lock. Check every rename/write/fsync error. Never describe a sequence of per-file renames as an atomic multi-file update.
- [ ] Run `go test -race ./internal/artifact`; require all cases and concurrent-lock tests to pass. Commit as `feat: add verified recoverable artifact updates`.

### Task 3: CS2 and matched native framework preparation

**Files:** Create `internal/steam/runner.go`, `runner_test.go`, `internal/framework/releases.go`, `prepare.go`, `gameinfo.go`, `hud.go`, and matching tests; create `docs/frameworks.md`.

**Interfaces:** Produce `steam.Update(ctx context.Context, executable, root string, validate bool, log io.Writer) (buildID string, err error)` and `steam.LatestBuild(ctx context.Context, executable string) (string, error)`. Produce `framework.Resolve(ctx context.Context, cfg config.Server, bundledPins map[string]string) (Selection, error)` and `framework.Prepare(ctx context.Context, root string, selection Selection) (Prepared, error)`; `Selection` records component versions, asset URLs/digests, and release channel. `Prepared` contains `Changes []artifact.Change` plus bundled `[]BundledArtifact`; each bundled artifact records canonical name, version, SHA-256, staged path, filename and upstream enabled state. Produce `framework.PatchGameInfo(data []byte) ([]byte, error)`.

- [ ] Write `TestSteamUsesApp730AndSeparateArguments`, `TestGameInfoPatchIdempotent`, `TestNativePairRequired`, `TestBundledPinConstrainsRuntime`, `TestConflictingPinsFailBeforeDownload`, and `TestFrameworkPreservesOperatorState`. Assert `clientprefs` and native core stay in the selected release; two different exact bundled pins cannot be silently accepted; existing admin configs/data/custom gamedata retain identical bytes.
- [ ] Run `go test ./internal/steam ./internal/framework` and observe the missing-behavior failures.
- [ ] Implement SteamCMD install/update/build detection and official release discovery. Verify Metamod PLAPI compatibility using inspected upstream release/build metadata and live acceptance rather than filename assumptions. Inspect an actual S2Script release manifest/archive layout, select the complete native pair/game package, preserve notice files, and prepare managed changes without applying them. New config templates are create-only. Optional HUD support prepares compatible MultiAddonManager and merges addon `3790153369` into existing settings.
- [ ] Run focused tests with fixture releases, including missing digest and network failure. Require an explicit documented integrity policy when an upstream Metamod artifact lacks a publisher checksum; never call an unverified download hash-verified. Commit as `feat: prepare CS2 Metamod and matched S2Script releases`.

### Task 4: Plugin policy, discovery, pins, and disabled placement

**Files:** Create `internal/plugins/policy.go`, `inventory.go`, `manifest.go`, `cli.go`, matching tests, `schemas/s2script-plugins.schema.json`, `examples/s2script-plugins.json`, and `docs/plugins.md`; modify `cmd/s2egg/main.go`.

**Interfaces:** Produce `plugins.ReadPolicy(path string) (Policy, error)`, `plugins.Discover(root string) ([]Installed, error)`, `plugins.ResolveEnabled(name string, desired *bool, upstream bool, panel string) (bool, error)`, and `plugins.Target(filename string, enabled bool) (string, error)`. `Policy` has schema version and a name-keyed map of `Entry{Source string, Version string, Enabled *bool, With []string}`. `Installed` has name, version, source, SHA-256, filename, enabled state, dependency parents, and last-check status/time. Empty version normalizes to `*`; exact semver pins remain exact. Persist installed facts in `s2script-plugins.lock.json`, never rewrite user policy with resolved versions.

- [ ] Write `TestDiscoverActiveAndDisabled`, `TestFirstInstallDisabled`, `TestPolicyPinsSurviveRediscovery`, `TestPanelDefaultInheritsPolicy`, and `TestDuplicateIdentityRejected`. Assert `enabled:false` targets only `addons/s2script/plugins/disabled/`; `default` + explicit false stays false, `1` enables, `0` disables; missing bundled state uses release placement; pinned `1.2.3` remains `1.2.3` after discovery.
- [ ] Run `go test ./internal/plugins` and confirm failure before implementing the contracts.
- [ ] Inspect the real `.s2sp` manifest format before implementing bounded manifest reads. Implement the desired/installed schemas, active and disabled scans, source verification states, safe version parsing, and CLI list/add/remove/pin/unpin/enable/disable/inspect/reconcile commands. `plugins add <name> --version 1.2.3 --disabled` persists both choices before installation; mutations that move artifacts require the game to be stopped. Do not execute plugin content or assume a renamed file changes package identity.
- [ ] Run focused tests and CLI round-trip checks against a temporary server tree. Require duplicate identities, unsafe archive metadata, and incompatible schema versions to fail visibly. Commit as `feat: track installed plugins pins and disabled state`.

### Task 5: Registry reconciliation for every installed plugin

**Files:** Create `internal/plugins/registry.go`, `reconcile.go`, `lockfile.go`, and matching tests; update `docs/plugins.md`.

**Interfaces:** Produce `plugins.Registry` with `Plan(ctx context.Context, name, versionRange string, with []string) (RegistryPlan, error)`, `Meta(ctx context.Context, name string) (Metadata, error)`, and verified artifact fetching through Task 2. Mirror inspected official API fields rather than inventing response shapes. Produce `plugins.Prepare(ctx context.Context, cfg config.Server, policy Policy, installed []Installed, bundled []framework.BundledArtifact) (Prepared, error)`. `Prepared` has `Changes []artifact.Change`, `Installed []Installed`, and check results for every inventoried identity. Framework code must not import `plugins` (avoid a package cycle).

- [ ] Write `TestDisabledRegistryPluginUpdatesInDisabled`, `TestPinnedPluginIsCheckedButNotReplaced`, `TestConflictingClosuresRejectWholePlan`, `TestLocalModifiedPluginNotAdoptedByName`, `TestMissingHashAndFilenameCollisionAbort`, `TestDisabledRequiredDependencyFails`, and `TestDisabledRootDependencyPlacement`. Fixture: installed disabled v1.0.0, available v1.1.0 → staged v1.1.0 only under `disabled/`; pinned v1.0.0 → availability reports v1.1.0 but installed bytes/version remain v1.0.0. An enabled root requiring an explicitly disabled dependency fails; a disabled root keeps exclusive implicit dependencies disabled, while a dependency also needed by an enabled root stays enabled absent an explicit disable.
- [ ] Run `go test ./internal/plugins` and confirm new regression cases fail.
- [ ] Implement metadata checks for all entries, public API dependency plans, bundle optional members, source/hash-based adoption, version/host compatibility checks, safe filenames and source conflicts. Resolve/download/verify the full set before returning changes. Preserve immutable pins, local files, disabled placement, and user configs. Verify registry-unavailable fallback only when the retained lock/artifacts satisfy requested policy. Persist the new lockfile in the same recoverable apply operation as plugin changes.
- [ ] Run `go test -race ./internal/plugins` with 404/429/5xx, malformed JSON, dependency-cycle/conflict, partial-download, and cross-origin redirect fixtures. Commit as `feat: reconcile registry updates across the installed plugin list`.

### Task 6: Server startup and process lifecycle

**Files:** Create `internal/events/event.go`, `internal/server/prepare.go`, `process.go`, `readiness.go`, `console.go`, and tests; modify `cmd/s2egg/main.go`.

**Interfaces:** Produce `events.Event{Version int, ID string, Type string, ServerID string, NodeID string, RunID string, At time.Time, Reason string, Details map[string]string}` and `events.Sink` with `Publish(context.Context, Event) error`. Produce `server.Prepare(ctx context.Context, cfg config.Server) error`, `server.Run(ctx context.Context, cfg config.Server, sink events.Sink, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)`, and `server.Status` with run ID, readiness state, map, players with explicit known/unknown state, versions, and intended stop reason. Expose a local status snapshot for CLI mutation checks and Plan 2; use a live lock/socket as the authority, not an unvalidated PID file.

- [ ] Write `TestPrepareBeforeProcessSpawn`, `TestDisabledNeverAppearsActiveDuringPrepare`, `TestConsoleInputForwarding`, `TestShutdownReapsProcessGroup`, `TestReadinessGatesUpEvent`, and `TestFailedStartupEmitsNoUp`. Assert one `server.up` after a successful readiness probe, one `server.down` after a ready process exits, and `server.start_failed` without an earlier up when readiness fails. Stop commands and unexpected exits carry distinct reasons.
- [ ] Run `go test ./internal/server` against a tiny subprocess fixture and confirm new behaviors fail.
- [ ] Implement startup locking/recovery, Steam/framework/plugin preparation, one committed stopped-server managed update, toggle application, and launch argument construction. Supervise the process group, reap children, forward signals/stdin, and enforce a bounded shutdown timeout. Parse readiness from a tested local game query or verified engine signal, not a generic banner. Build raw console/status events before optional filtering and redact GSLT/webhook credentials everywhere.
- [ ] Run focused subprocess/integration tests and `go test -race ./...`; require no leaks, no early up, and no partial launch after preparation failure. Commit as `feat: supervise S2Script server startup and lifecycle`.

### Task 7: Standalone lifecycle webhooks and runnable distribution

**Files:** Create `internal/alerts/discord.go`, `queue.go`, tests, `docker/Dockerfile`, `docker/Dockerfile.debug`, `.dockerignore`, `.github/workflows/ci.yml`, `docs/install.md`, `docs/webhooks.md`, `docs/testing.md`, and `tests/integration/server_test.go`; update config, egg, README and parity documentation.

**Interfaces:** Produce `alerts.NewDiscord(cfg Config, client *http.Client) (*Dispatcher, error)`; `Dispatcher` implements `events.Sink` and `Close(ctx context.Context) error`. `Config` has URL, enabled event types, bounded queue capacity, per-attempt timeout and retry limits. Secret URL storage must not appear in generated public examples. Default event selection when configured: `server.up`, `server.down`, `server.start_failed`, `server.crash`; restart/update events are selectable.

- [ ] Write `TestWebhookUpDownAndCrashPayloads`, `TestRetryHonors429AndDeduplicatesEventID`, `TestWebhookTimeoutDoesNotBlockServerStop`, and `TestNoWebhookWhenUnconfigured`. Assert failed readiness sends startup-failure only; a crash sends distinct crash/down transitions with one down per run; an ignored duplicate event ID sends no duplicate request. Bound queue growth and report dropped/failed deliveries without exposing the URL.
- [ ] Run `go test ./internal/alerts ./tests/integration` and observe failures before implementation.
- [ ] Implement bounded delivery, Discord retry-after handling, redaction, event selection, and shutdown flushing with a deadline. Wire standalone delivery now and an explicit ownership boundary for node delivery in Plan 2. Build non-root runtime/debug images, generated importable egg, and an install/verify/rollback guide using real local image-build commands until public artifacts exist.
- [ ] Run focused tests, `go vet ./...`, `go test -race ./...`, and Linux `s2egg` build. Build the image and run the real CS2 smoke checklist only if Docker access is available; otherwise record the unrun checks. Commit as `feat: ship standalone egg with Discord lifecycle webhooks`.
