# Implementation status

This file records the configuration, reconciliation and server-supervision milestone. The approved design remains prospective wherever a behavior is marked planned. Unit tests and generated JSON checks do not establish live-server or panel acceptance.

| Capability | Status / evidence |
| --- | --- |
| Typed configuration, defaults, precedence, validation | Implemented; config tests cover invalid overrides, tokenless policy, schema/path validation, pins and quoted argument data |
| Secret redaction and read-only config check/show | Implemented; JSON/formatting and CLI tests include GSLT/webhook redaction and no writes |
| Read-only doctor | Implemented; checks local prerequisites only; does not assess game readiness |
| Canonical bundled catalog and inherited controls | Implemented; 18 identities, 14 release-enabled / 4 disabled; generated documentation and egg controls share the catalog |
| PTDL_v2 development egg JSON | Generated and structurally tested; live Pterodactyl/Pelican import not yet exercised |
| Game startup, console, graceful stop | Implemented with process-group fixtures, signal/stdin forwarding and local A2S readiness; native CS2 acceptance and workshop management remain pending |
| SteamCMD install/update/validation | Startup wired; image seed/writable HOME/SDK contract tested with subprocess fixtures; Task7 supplies image seed; live install untested |
| Metamod/S2Script preparation, version/channel pins, compatibility | Implemented as staged changes; official publisher digests, immutable source/API evidence, native pair/game-package checks and retained state/cache fallback; startup/CLI wiring implemented; live loading untested |
| Verified artifact download/extraction and recoverable application | Implemented; bounded HTTPS/hash/archive handling, descriptor-safe paths and caller-held lock; consumers must Recover before launch |
| Installed-plugin inventory and startup checks of every entry | Inventory/CLI implemented; reconciliation library checks every installed identity, including disabled/pinned/local/missing entries; startup invocation implemented |
| Registry/release/local plugin discovery, dependency resolution, pins | Implemented as staged changes with published-interface contracts, verified bundle root/member evidence and policy-checked outage fallback; executable resolver wired; successful live registry acceptance not established |
| Disabled plugin updates and persistent placement/config/data | Implemented in preparation and direct CLI moves; TLS/ZIP/transaction tests cover disabled updates, shared/exclusive dependencies, immutable pins, local preservation and config continuity |
| HUD support and MultiAddonManager setup | Preparation library enforces minimum installed CS2 build, stages compatible helper and merges addon settings; live server/client rendering not tested |
| Lifecycle webhook events and delivery | Canonical raw lifecycle/console/status events implemented; Task7 adds filtered webhook delivery for the first packaged runnable milestone; no webhook contacted |
| Optional node daemon and narrow local protocol | Planned; no Docker socket or Wings admin credential in the container |
| Shared game generations, cache, coordinated update/restart | Planned |
| Backups/restores, workshop management, guard, scheduling | Planned |
| Container build/publishing, release binaries/checksums, CI | Planned; local Go builds and tests work |
| Live CS2/Wings smoke/acceptance tests | Not run; runtime code is tested with fixtures; image/infrastructure and native acceptance are pending |
| Companion Discord bot | Later separate project; outside this milestone |

The sole supported framework stack is Metamod plus S2Script. Operational feature parity uses CS2-Egg-Go as a reference, with initial credits and license provenance in the repository's README and NOTICE. No upstream product module has been copied.

The exact startup, status, event, SteamCMD, and future node contracts are documented in [runtime.md](runtime.md).
