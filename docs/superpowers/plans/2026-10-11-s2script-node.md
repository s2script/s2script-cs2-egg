# S2Script Shared Node Services Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add optional node coordination, safe shared CS2 generations, automatic updates/restarts, verified artifact caching, and workshop sharing without making standalone servers depend on the daemon.

**Architecture:** A host core binds authenticated local connections to administrator-configured server identities. Modules manage resources behind narrow interfaces and report health/events. The game-sharing module prepares immutable generations and changes a server's references only while its game process is stopped.

**Tech Stack:** Go 1.27.2, Linux Unix sockets, systemd, Wings HTTP API, SteamCMD, read-only Wings mounts, standard-library HTTP/filesystem test fixtures.

**Spec:** [Approved design](../specs/2026-10-11-s2script-cs2-egg-design.md); depends on Tasks 1–7 in [Server and plugins](2026-10-11-s2script-server.md).

## Global Constraints

- Linux x86-64 is the supported platform; both Pterodactyl and Pelican are targets; the node daemon is optional.
- App ID is 730.
- No Docker socket or Wings administrator credential is mounted into a game container.
- Restart policies are immediate, when empty with a maximum delay, or a configured maintenance window with a maximum delay.
- A shared tree is never passed to standalone SteamCMD.
- Switch each server to the new generation only while stopped; keep the previous generation while any running server still uses it.
- Source for distributed project binaries/images must be available under the project license; release workflows link each artifact to its source revision.

## Review Focus

- A process running under the common container UID must not claim another server identity or another volume: Task 8.
- Losing the node mid-session must not produce false confirmed-down events or duplicate webhook delivery ownership: Task 8.
- Restarting or uninstalling the node must not invalidate files still used by a running game: Task 9.
- Unknown player counts, midnight windows, or clock changes must not trigger an accidental empty-server restart: Task 10.
- Chunked workshop content and interrupted owner downloads must not publish partial items or mutate a live server: Task 11.

---

## File boundaries

`internal/node` owns registered server identities, service lifecycle, and module registration. `internal/protocol` owns transport schemas/version negotiation; `internal/nodeclient` integrates them into the entrypoint. `internal/shared` owns immutable game generations and reference tracking; `internal/wings` owns the restricted host-side API adapter; `internal/scheduler` owns restart decisions. `internal/cache` owns verified artifact caching and `internal/workshop` owns workshop metadata/generations. Module code must not read another module's private state.

### Task 8: Host core, bounded protocol, identity, and alert ownership

**Files:** Create `cmd/s2node/main.go`, `internal/config/node.go`, `internal/node/core.go`, `modules.go`, `identity.go`, `internal/protocol/message.go`, `server.go`, `client.go`, `internal/nodeclient/client.go`, matching tests, `deploy/s2node.service`, `examples/node.json`, and `docs/node.md`; modify `internal/server/prepare.go`, `process.go`, and `internal/alerts/queue.go`.

**Interfaces:** Produce `node.New(cfg config.Node, sink events.Sink) (*Core, error)` and `Core.Run(context.Context) error`. Produce `node.Module` with `Name() string`, `Start(context.Context, Host) error`, `Health() Health`, and `Close(context.Context) error`; `Health` holds healthy/degraded/failed status and an explanation. `node.Host` exposes immutable configured `node.Server` records (ID, absolute volume, socket path, game allocation), event publishing, current `server.Status`, and an authorized action dispatcher. Produce `protocol.Message{Version int, ID string, Type string, Payload json.RawMessage}` with a 1 MiB frame bound, request deadlines, capability negotiation, and messages for hello/status/events/game preparation/cache/workshop/restart scheduling. Bind server identity to the accepted socket, never the payload.

Expose server sockets at each registered volume's `egg/s2node.sock` and a separate administrator socket at `/run/s2node/admin.sock`. Expose `nodeclient.Connect(ctx context.Context, socketPath string) (*Client, error)`; `Client` provides typed methods for only negotiated capabilities. Add `server.GameProvider` with `Prepare(ctx context.Context, cfg config.Server) (handled bool, err error)` and `server.Options{GameProvider GameProvider}` to `server.Prepare`/`server.Run`; nil provider preserves standalone behavior. Update callers/tests together and carry this revised contract into later tasks.

- [ ] Write `TestPeerCannotClaimOtherVolume`, `TestUnknownProtocolRejected`, `TestOversizeFrameClosed`, `TestModuleFailureIsIsolated`, `TestLostContactIsUnconfirmed`, and `TestOneWebhookOwnerPerRun`. Assert server A cannot act on B using a forged ID/path; one optional-module failure leaves other modules serving; disconnect reports lost contact, not confirmed game exit; acknowledged node-owned events are not independently delivered by the entrypoint.
- [ ] Run `go test ./internal/node ./internal/protocol ./internal/nodeclient` and verify failures before implementation.
- [ ] Implement bounded typed transport, restricted socket permissions/peer checks, safe volume resolution, idempotent event ingestion, readiness/status propagation, and the core/module lifecycle. Delivery ownership is negotiated per run and retained on transient disconnect; do not switch owners mid-run without an explicit handoff. Provide a host-owned durable event queue for acknowledged node delivery. Add install/config-check/status/doctor CLI and a least-privilege systemd unit; optional privileged modules request documented capabilities rather than granting every module unrestricted access.
- [ ] Run focused race/integration tests plus `go test -race ./...`; verify that absent daemon/capability keeps standalone server startup usable and a claimed-but-failed shared preparation cannot silently fall through. Commit as `feat: add optional node core and server protocol`.

### Task 9: Immutable shared game generations and safe per-server materialization

**Files:** Create `internal/shared/store.go`, `materialize.go`, `references.go`, `module.go`, matching tests, `docs/shared-files.md`, and `tests/integration/shared_test.go`; update node configuration and node-client game preparation.

**Interfaces:** Produce `shared.Store` with `Build(ctx context.Context) (Generation, error)`, `Attach(ctx context.Context, srv node.Server, generationID, mode string) error`, and `Prune(ctx context.Context) (removed []string, err error)`. `Generation` holds ID, Steam build ID, immutable host root, publication time, and file-manifest digest. Dependencies are the Task 3 Steam adapter, a live server-state reader, and Task 2 file/lock primitives. Persist generation references by server/run and reconcile them with live registered servers on daemon restart before pruning. Read-only container mount target is `/mnt/s2node`; host shared data defaults to `/srv/s2node/shared`.

- [ ] Write `TestBuildNeverMutatesPublishedGeneration`, `TestAttachRefusesRunningGame`, `TestPrivateFilesPreserved`, `TestPruneRetainsLiveGeneration`, `TestUnavailableMountNeverRunsSteamCMD`, and `TestHardlinkUpdateDoesNotMutateOldInode`. Assert the old VPK bytes stay unchanged while building the next version, and `gameinfo.gi`, cfg/addons/data, Steam bookkeeping, ownership of existing private files, and explicit operator overrides survive attachment.
- [ ] Run `go test ./internal/shared ./tests/integration` and observe new failures.
- [ ] Implement a serialized writable SteamCMD staging tree, complete-generation validation, publication, symlink/copy delivery, and same-filesystem immutable-only hardlink delivery. Preflight disk space before staging/copy fallback. Require explicit administrator-configured Wings read-only mounts and verify container-visible paths before acknowledging preparation. Preserve generation references during restart/crash recovery. Shared-to-standalone migration makes a distinct writable install; never run SteamCMD against a linked shared tree.
- [ ] Run focused tests including injected interrupted staging/publication/reference updates, missing mount, daemon restart, and cross-filesystem hardlink failure. A two-container live mount test is a separate acceptance item. Commit as `feat: share immutable CS2 game generations safely`.

### Task 10: CS2 polling, player-aware restart scheduling, and Wings actions

**Files:** Create `internal/wings/client.go`, `internal/scheduler/policy.go`, `jobs.go`, `clock.go`, `internal/shared/updates.go`, matching tests and `docs/updates.md`; modify `internal/nodeclient/client.go` and server event handling.

**Interfaces:** Produce `wings.Client.Restart(ctx context.Context, serverID string) error` and `wings.Client.State(ctx context.Context, serverID string) (state string, err error)` using an administrator-configured HTTPS endpoint and credentials, with verified TLS or explicit certificate pinning. Produce pure `scheduler.Decide(now time.Time, pendingSince time.Time, status server.Status, policy Policy) Decision`; `Policy` contains mode, maximum delay, IANA timezone, local-time window, and warning offsets; `Decision` contains wait/warn/restart and reason. Produce persisted idempotent restart jobs keyed by server and target generation, with the host action dispatcher as the sole executor.

- [ ] Write `TestUnknownPlayersNotEmpty`, `TestWindowSpanningMidnight`, `TestMaximumDelayAndWarnings`, `TestDuplicateUpdateCreatesOneRestartJob`, `TestFailedWingsRestartRetainsPendingJob`, and `TestStandaloneUpdateDoesNotExitWithoutRestartAdapter`. Assert immediate policy permits occupied servers with warning; empty policy waits for known zero players until its maximum deadline; timezone/DST transitions do not execute a job twice.
- [ ] Run `go test ./internal/scheduler ./internal/wings ./internal/shared` and confirm failures.
- [ ] Implement one node Steam build poll/update at a time, persisted generation-targeted jobs, readiness/player status freshness, configurable countdowns, and Wings-mediated restarts. Mark success only after the expected lifecycle transition, not just HTTP acceptance. An opt-in standalone poller may request a restart only when a supported lifecycle adapter is configured and validated; otherwise expose an update-pending status without stopping the game. Document this distinction from automatic startup updates.
- [ ] Run fake-clock/fake-Wings tests with timeouts, certificate failures, stale status, occupied servers, concurrent requests and daemon recovery; run `go test -race ./...`. Commit as `feat: coordinate CS2 updates and player-aware restarts`.

### Task 11: Verified addon cache and versioned workshop sharing

**Files:** Create `internal/cache/artifacts.go`, `module.go`, `internal/workshop/store.go`, `metadata.go`, `sync.go`, `module.go`, matching tests and `docs/workshop.md`; update protocol typed cache/workshop operations and framework/plugin adapters.

**Interfaces:** Produce `cache.Store.Get(ctx context.Context, url, sha256 string) (path string, err error)` using digest-addressed entries, in-flight deduplication, bounded downloads and Task 2 verification. Node policy decides allowed origins and fleet pins; a container cannot supply arbitrary host paths or turn the cache into a credential-bearing HTTP proxy. Produce `workshop.Store.Sync(ctx context.Context, srv node.Server) (Result, error)` and `Prune(ctx context.Context) (Result, error)`. `Result` records complete items imported/linked/released/skipped, bytes saved, and reasons. Item identities include Steam app ID 730, workshop item ID and published-file revision; preserve all chunked VPK files as one unit.

- [ ] Write `TestConcurrentArtifactRequestsDownloadOnce`, `TestBadDigestNeverCached`, `TestWorkshopChunkedItemPublishedTogether`, `TestWorkshopRunningServerRefused`, `TestLegacyUnversionedItemStaysLocal`, `TestInterruptedWorkshopOwnerNotPublished`, and `TestWorkshopMetadataPreservesOtherItems`. Assert two servers share identical immutable bytes with private writable bookkeeping; a newer revision uses another generation; referenced generations survive pruning.
- [ ] Run `go test ./internal/cache ./internal/workshop` and confirm failures.
- [ ] Implement source-allowlisted artifact cache and source/version pin resolution. Inspect actual Steam workshop ACF and API shapes before writing the parser. Import complete stopped-server downloads, elect one updater per pending revision, seed selected cached items, safely update per-server bookkeeping, and retain referenced old revisions. Treat symlinks as store references rather than follow arbitrary server-provided paths. Add cache/workshop status/sync/prune CLI and document MultiAddonManager redownload settings and local/unversioned fallback.
- [ ] Run focused tests, two-server fixture integration, `go vet ./...`, and `go test -race ./...`; record the real workshop-item acceptance check separately. Commit as `feat: cache framework artifacts and share workshop generations`.
