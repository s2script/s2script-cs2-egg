# S2Script Operations and Public Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete operational parity, extend notifications for node events, and provide reproducible public distribution and honest acceptance evidence.

**Architecture:** Independent node modules consume the host core's configured identities, snapshots and event sink. Privileged effects and network actions sit behind testable adapters. CLI, Discord webhooks, metrics and a later bot consume shared structured state rather than separate control implementations.

**Tech Stack:** Go 1.27.2, Linux/systemd, configurable nftables adapter, archive/JSON/HTTP tooling, GitHub Actions, Docker build assets, Markdown operating guides.

**Spec:** [Approved design](../specs/2026-10-11-s2script-cs2-egg-design.md); depends on [Server and plugins](2026-10-11-s2script-server.md) and [Shared node services](2026-10-11-s2script-node.md).

## Global Constraints

- License project code under GPL-3.0-or-later, matching CS2-Egg-Go.
- Restore refuses a running server; live database snapshots must not be presented as application-consistent backups.
- Optional node nftables enforcement is scoped to configured game ports and a dedicated table.
- No webhook is contacted unless configured by the operator.
- No public unauthenticated HTTP control service.
- Source for distributed project binaries/images must be available under the project license; release workflows link each artifact to its source revision.
- Unit tests or a successful image build alone do not establish production readiness.

## Review Focus

- Secret tokens may appear fragmented across console chunks or error bodies; redact before disk/archive/webhook output: Task 12.
- Retention globs or a symlinked directory could cross into private data or live shared generations: Tasks 12–13.
- A successful archive write can still contain inconsistent live SQLite state; declare and enforce consistency mode: Task 13.
- An untrusted container event or Docker bridge address must not cause host-wide packet blocking: Task 14.
- A node self-update or release with mismatched protocol/source metadata must fail safely and retain the prior executable: Tasks 15–16.

---

## File boundaries

`internal/observability` handles redaction, bounded console history, log rotation, filtering, metrics, and snapshot presentation; split files by these responsibilities. `internal/crashes`, `internal/cleanup`, `internal/backup`, and `internal/guard` are node modules and expose pure planning functions where feasible. `internal/selfupdate` handles only project-binary releases, not CS2 or S2Script. Public guides document the actual integrated behavior after each task.

### Task 12: Logs, crash bundles, cleanup, metrics, and operational events

**Files:** Create `internal/observability/redact.go`, `logs.go`, `metrics.go`, `status.go`, `internal/crashes/module.go`, `bundle.go`, `internal/cleanup/plan.go`, `module.go`, matching tests, `docs/logging.md`, `docs/metrics.md`, `docs/cleanup.md`; update server console handling, node CLI, webhook event templates and parity docs.

**Interfaces:** Produce `observability.Redact(text string, secrets []string) string`, a stream redactor that buffers potential cross-chunk secret prefixes, and `observability.Snapshot(host node.Host) Snapshot` with per-server lifecycle/versions/player state and per-module health. Produce `cleanup.Plan(root string, rules []Rule, now time.Time) ([]Candidate, error)` and `cleanup.Apply(ctx context.Context, root string, candidates []Candidate) (Result, error)` with identity/revalidation before removal; `Rule` specifies contained relative pattern, age and maximum retained count; `Candidate` records relative path, inode/file identity and size. Produce `crashes.Capture(ctx context.Context, srv node.Server, status server.Status, tail []byte) (bundlePath string, err error)` with configurable byte/retention limits.

- [ ] Write `TestSecretSplitAcrossChunksRedacted`, `TestLogsRotateWithinBound`, `TestIntentionalStopNotCrash`, `TestCrashBundleHasVersionsWithoutSecrets`, `TestCleanupRejectsEscapingSymlinkAndChangedCandidate`, and `TestMetricsPrivateBindingAndBoundedLabels`. Assert config/data/live generations are never cleanup candidates and absent/unknown player counts are not emitted as zero.
- [ ] Run `go test ./internal/observability ./internal/crashes ./internal/cleanup` and confirm failures.
- [ ] Implement raw-event-first console parsing, configurable display filters, bounded redacted history/rotation, crash bundles, retention previews and safe application, and a private Prometheus endpoint with bounded metric labels. Provide `s2node status --json`, `top`, `doctor`, and log/crash/cleanup commands. Extend optional webhook events for these outcomes; do not upload dumps automatically or let event delivery block module work.
- [ ] Run focused tests, `go test -race ./...`, and an end-to-end synthetic crash fixture with token-bearing output; inspect the resulting bundle and webhook body for absence of secrets. Commit as `feat: add logs crash capture cleanup and node metrics`.

### Task 13: Consistent backups, stopped-server restore, and retention

**Files:** Create `internal/backup/archive.go`, `restore.go`, `retention.go`, `module.go`, matching tests and `docs/backups.md`; update node CLI/config and backup webhook templates.

**Interfaces:** Produce `backup.Create(ctx context.Context, srv node.Server, opts Options) (Manifest, error)`, `backup.PreviewRestore(ctx context.Context, srv node.Server, archivePath string) (RestorePlan, error)`, and `backup.Restore(ctx context.Context, srv node.Server, plan RestorePlan) error`. `Options` defines output directory, include/exclude patterns, retention, minimum free bytes and `consistent|live-best-effort` mode. `Manifest` records server identity, creation time, consistency mode, per-file size/hash, versions and archive checksum. `RestorePlan` lists validated target files and the required recovery-copy location.

- [ ] Write `TestBackupIncludesPrivateStateNotSharedGame`, `TestConsistentBackupRequiresStoppedOrQuiescedServer`, `TestLowDiskLeavesPreviousBackupIntact`, `TestRestoreRefusesRunningGame`, `TestRestoreTraversalRejected`, and `TestRestoreKeepsUnrelatedFilesAndRecoveryCopy`. Assert plugin policies/lockfiles/configs/databases/custom overrides are included, shared VPKs are excluded, partial archives never become successful backups, and restore verifies its plan against current server state.
- [ ] Run `go test ./internal/backup` and confirm failures.
- [ ] Implement scheduled archives outside server volumes, checksums, consistency labels, bounded/disk-aware retention, previewed stopped-server restore, and a pre-restore recovery copy. Do not follow arbitrary volume symlinks or delete files absent from an archive. Provide list/now/preview/restore CLI with explicit destructive confirmation in the public operator flow; tests use fixture roots and never restore real user volumes.
- [ ] Run focused tests and a full fixture backup→modify→restore round trip, including forced interruption and live-file changes. Commit as `feat: back up and restore private CS2 server state`.

### Task 14: Console guard and isolated host packet enforcement

**Files:** Create `internal/guard/detect.go`, `policy.go`, `nft.go`, `module.go`, `state.go`, matching tests, `docs/guard.md`, and `tests/integration/guard_test.go`; modify raw server-console event ingestion and node guard commands.

**Interfaces:** Produce `guard.Observe(at time.Time, serverID, line string) []Finding` for documented/tested console signatures; `Finding` records rule, source evidence, validated client address/identity and time. Produce `guard.Decide(f Finding, policy Policy) Action`, where action is log/kick/block and includes scoped server allocation, reason and expiry. Produce `guard.Firewall` with `Apply(ctx context.Context, rules Ruleset) error`, `Block(ctx context.Context, allocation Allocation, address netip.Addr, until time.Time) error`, and `Unblock(...) error`; the dedicated table is `inet s2script_guard`. No shell command construction from messages.

- [ ] Write `TestGuardDefaultsToObservation`, `TestBridgePrivateAndAllowlistedAddressesNeverBlocked`, `TestReportsCannotSelectAnotherAllocation`, `TestRepeatedOffenceHasBoundedExpiry`, and `TestRulesTouchOnlyConfiguredGamePorts`. Assert kick/block only occurs with enabled policy and attributable evidence; spoofable query traffic is rate-limited rather than used alone for long-lived bans; unrelated SSH/Wings/panel ports remain untouched.
- [ ] Run `go test ./internal/guard` and confirm failures.
- [ ] Implement bounded windowed detectors with documented console fixtures, explicit kick/ban commands, allowlists, expiry/escalation state, explained action records, and the host nftables adapter. Inspect optional upstream guard code before any reuse and retain notices if incorporated. Provide blocks/why/block/unblock/rates CLI, rule previews, observation-first installation and reversible uninstall guidance. Support standalone console actions without assuming host enforcement is available.
- [ ] Run unit/race tests against a fake firewall. Run real nftables assertions only in a disposable namespace/VM with explicit available privileges; otherwise label enforcement unverified. Commit as `feat: add configurable server and node guard`.

### Task 15: Verified node self-update and bot integration contract

**Files:** Create `internal/selfupdate/releases.go`, `apply.go`, matching tests, `docs/integrations.md`, `docs/project-updates.md`, and `schemas/events.schema.json`; update node CLI/config, systemd integration, alerts and protocol documentation.

**Interfaces:** Produce `selfupdate.Check(ctx context.Context, channel, current string) (Release, error)` and `selfupdate.Apply(ctx context.Context, release Release, executable string) error`. `Release` holds version, source revision, protocol range, trusted asset URL and SHA-256. A host administrator configures permitted self-update policy; container protocol cannot request arbitrary executable URLs or restart host services. Publish the Task 6 event schema and Task 8 authorized action schemas for future consumers.

- [ ] Write `TestSelfUpdateRejectsBadHashAndForeignRedirect`, `TestSelfUpdateKeepsPreviousBinary`, `TestProtocolMismatchPreventsActivation`, `TestFailedActivationCanRollback`, and `TestBotInterfaceHasNoArbitraryShellAction`. Assert exact-version, stable/prerelease choices are honored and disabled auto-update makes no update request. Validate emitted lifecycle events against the public schema.
- [ ] Run `go test ./internal/selfupdate ./internal/protocol` and confirm failures.
- [ ] Implement opted-in scheduled/manual verified updates with previous-binary retention, controlled systemd activation, protocol compatibility and rollback. Document event IDs, timestamps, acknowledgement/ownership semantics, bounded delivery, limitations on exactly-once remote delivery, status and narrow audited actions. Bot account linking, Discord slash commands and remote authorization gateway remain later work; server-up/down webhooks already function from Task 7.
- [ ] Run fixture tests for interruption, read-only binary path and failed service activation. No test replaces a real system binary or restarts this machine's services. Commit as `feat: add verified node updates and bot integration contract`.

### Task 16: Public documentation, release automation, and end-to-end acceptance

**Files:** Create `.github/workflows/release.yml`, `.github/CONTRIBUTING.md`, `.github/SECURITY.md`, `docs/configuration.md`, `docs/migration.md`, `docs/uninstall.md`, `docs/troubleshooting.md`, `docs/acceptance.md`, `scripts/check-generated.go`, `tests/acceptance/README.md`, and `tests/acceptance/compose.yml`; update `README.md`, `NOTICE`, `Makefile`, CI, Dockerfiles, egg, systemd assets and all parity entries.

**Interfaces:** Produce reproducible `make test`, `make build`, `make egg-check`, and `make release-check` targets. Release output includes Linux `s2egg`/`s2node`, checksums, generated egg, service/config assets, immutable source-revision metadata and corresponding source archive/link. Publish versioned image tags and controlled stable/prerelease aliases only from explicit release workflows; this implementation task prepares the workflow, not an unsolicited public release.

- [ ] Write `TestGeneratedEggAndSchemasCurrent`, `TestReleaseMetadataPointsToSourceRevision`, `TestDocumentedExampleConfigsValidate`, and acceptance scenarios for first install, two-server sharing/update, disabled-plugin first install/update, version pins, node outage, registry outage, graceful stop/crash webhooks and backup restore. Assertions include unchanged disabled placement, retained pinned bytes, preserved config/data, and the correct lifecycle-event sequence.
- [ ] Run `go test ./tests/...` and generated-output checks, observing the expected failures for incomplete fixtures/assets before implementing them.
- [ ] Complete install instructions for Pterodactyl and Pelican, required ports/GSLT, resource/disk requirements, mounts/permissions, node module setup, plugin commands and all 18 toggles, pins, HUD dependency, webhooks, migration/rollback/uninstall and troubleshooting. Explain panel settings versus manifest precedence and bundled-pin/runtime coupling with examples. Verify all local instructions against actual CLI help. Preserve upstream notices and end the README with credits. Cross-reference every spec capability to its implementation and evidence in `docs/parity.md`.
- [ ] Run the complete automated gates once: `go vet ./...`, `go test -race ./...`, both Linux builds, `make egg-check`, and `make release-check`. Exercise CI/image builds when available. Perform live CS2/Wings/HUD/firewall acceptance in the documented disposable environment or record each blocked/unrun case and leave release status provisional. Do not create a real webhook post unless the user explicitly authorizes it.
- [ ] Review the whole branch using the selected execution workflow, resolve actionable findings, and rerun affected checks. Commit as `docs: complete public installation and release verification`. Present exact implemented scope, test results, live-test limitations and concrete artifact paths without claiming untested production readiness.
