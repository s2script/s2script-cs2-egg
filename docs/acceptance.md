# Acceptance record

Status: **initial alpha; live acceptance incomplete**.

Automated tests exercise local processes, TLS/HTTP fixtures, filesystem
transactions, real Unix peer credentials, updater child execution, schema
producers and release packaging. GitHub CI builds runtime/debug images and checks
non-root HOME, SteamCMD seed and configuration. Its privileged fixture uses
separate UIDs only in a disposable runner.

These checks do not establish native CS2 compatibility, panel behavior,
workshop delivery, client HUD rendering or production firewall behavior.

| Scenario | Automated evidence | Live status |
| --- | --- | --- |
| Fresh install and startup failure | `internal/steam`, `internal/server`, `tests/integration` | Unrun: actual SteamCMD/CS2 install |
| Up → graceful down; unexpected down → crash | Server process and Discord HTTP fixtures | Unrun: actual game/panel stop; no real webhook posted |
| S2Script/Metamod native loading | Framework compatibility/archive fixtures | Unrun: `meta list`, plugin commands/gameplay |
| All 18 default plugin controls | Catalog, config and generated-egg tests | Unrun: actual panel controls and runtime default loading |
| Disabled plugin first install/update | Plugin registry/release transaction fixtures | Unrun: native scan verifies disabled files never loaded |
| Version pins and retained config/data | Exact pin/outage/ownership fixtures | Unrun: actual registry package upgrade and SQLite continuity |
| Registry outage | Verified installed/cache fallback fixtures | Unrun: external registry failure during live startup |
| Node outage | Peer protocol, ownership and lost-contact tests | Unrun: live daemon loss/reconnect under panel |
| Two-server VPK sharing and update | Generation/attachment tests and separate-UID CI | Unrun: real mounts, game updates and space accounting |
| Workshop/HUD | Metadata/content/chunk fixtures | Unrun: real content/client rendering |
| Backup, stopped restore and recovery | Hash/archive/lease/recovery tests and separate-UID CI | Unrun: actual game data restore/application consistency |
| Console guard and nftables | Parser ambiguity, scoped rules and helper fixtures | Unrun: disposable host kernel packet enforcement |
| Node update/rollback | Verified release fixtures and real child readiness test | Unrun: systemd upgrade and rollback on a disposable host |
| Public release/images | Offline package integrity/reproducibility tests | Requires actual Release and anonymous verification jobs |

The official HUD item remains **3790153369**. During research, its public
metadata request returned result 9; the cause was unknown. Another public item
returned the expected metadata shape. Neither result verifies HUD delivery.
Do not substitute an invented addon or claim the HUD works from fixture tests.

## Run and record live checks

Follow [tests/acceptance/README.md](../tests/acceptance/README.md) on disposable
infrastructure. Record the source revision, immutable image digests, date,
panel/Wings versions, CS2 build, Metamod/S2Script/plugin versions, exact commands,
expected versus actual outcome, and retained redacted evidence.

For each scenario mark PASS, FAIL or UNRUN with a reason. Do not replace this
record with a generic CI badge. Promote beyond alpha only after the required
environment-specific checks pass and observed failures are resolved.
