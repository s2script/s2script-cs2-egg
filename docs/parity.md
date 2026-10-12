# Implementation status

This file records the configuration, plugin inventory and reconciliation-library milestone. The approved design remains prospective wherever a behavior is marked planned. Unit tests and generated JSON checks do not establish live-server or panel acceptance.

| Capability | Status / evidence |
| --- | --- |
| Typed configuration, defaults, precedence, validation | Implemented; config tests cover invalid overrides, tokenless policy, schema/path validation, pins and quoted argument data |
| Secret redaction and read-only config check/show | Implemented; JSON/formatting and CLI tests include GSLT/webhook redaction and no writes |
| Read-only doctor | Implemented; checks local prerequisites only; does not assess game readiness |
| Canonical bundled catalog and inherited controls | Implemented; 18 identities, 14 release-enabled / 4 disabled; generated documentation and egg controls share the catalog |
| PTDL_v2 development egg JSON | Generated and structurally tested; live Pterodactyl/Pelican import not yet exercised |
| Game startup, console, graceful stop, workshop-map launch | Planned; `s2egg run` is not implemented |
| SteamCMD install/update/validation | Library implemented; fake-process arguments/build/cancellation tests; CLI wiring/live game installation later |
| Metamod/S2Script preparation, version/channel pins, compatibility | Implemented as staged changes; official publisher digests, immutable source/API evidence, native pair/game-package checks and retained state/cache fallback; live loading and CLI integration later |
| Verified artifact download/extraction and recoverable application | Implemented; bounded HTTPS/hash/archive handling, descriptor-safe paths and caller-held lock; consumers must Recover before launch |
| Installed-plugin inventory and startup checks of every entry | Inventory/CLI implemented; reconciliation library checks every installed identity, including disabled/pinned/local/missing entries; startup invocation awaits Task6 |
| Registry/release/local plugin discovery, dependency resolution, pins | Implemented as staged changes with published-interface contracts, verified bundle root/member evidence and policy-checked outage fallback; executable resolver wiring awaits Task6; successful live registry acceptance not established |
| Disabled plugin updates and persistent placement/config/data | Implemented in preparation and direct CLI moves; TLS/ZIP/transaction tests cover disabled updates, shared/exclusive dependencies, immutable pins, local preservation and config continuity |
| HUD support and MultiAddonManager setup | Preparation library enforces minimum installed CS2 build, stages compatible helper and merges addon settings; live server/client rendering not tested |
| Lifecycle webhook events and delivery | Planned for the first runnable server milestone; URL/event/timing validation only; no webhook is contacted now |
| Optional node daemon and narrow local protocol | Planned; no Docker socket or Wings admin credential in the container |
| Shared game generations, cache, coordinated update/restart | Planned |
| Backups/restores, workshop management, guard, scheduling | Planned |
| Container build/publishing, release binaries/checksums, CI | Planned; local Go builds and tests work |
| Live CS2/Wings smoke/acceptance tests | Not run; runtime and required infrastructure are not delivered in this milestone |
| Companion Discord bot | Later separate project; outside this milestone |

The sole supported framework stack is Metamod plus S2Script. Operational feature parity uses CS2-Egg-Go as a reference, with initial credits and license provenance in the repository's README and NOTICE. No upstream product module has been copied.
