# S2Script CS2 Egg — approved design

Date: 2026-10-11 (America/Chicago)

Status: approved by the user on 2026-10-11, with the GPL license, lifecycle webhook, and plugin pin/disabled-state amendments incorporated below. Product implementation has not started. This document describes planned behavior, not a shipped or tested product.

## Purpose and requirements

Build a public, GPL-3.0-or-later CS2 egg for Pterodactyl and Pelican, focused on S2Script and its Metamod dependency. Provide the server and node-management capabilities documented by K4ryuu's CS2-Egg-Go, with S2Script-specific installation, updates, and plugin controls. The license matches upstream's “v3 or later” declaration and supersedes the original MIT request.

The user explicitly requested:

- S2Script as the only scripting framework, with Metamod installed alongside it.
- Automatic CS2 updates and shared VPK storage.
- Startup plugin updates and individual toggles for the bundled plugins.
- Broad feature parity with CS2-Egg-Go, including its optional node features.
- A design that can support a companion Discord bot later, with Discord server-up/server-down webhooks included in this project.
- Public installation and operating documentation, GPL-3.0-or-later licensing, and credits at the bottom of the README.

The user clarified the registry requirement: **maintain a list of installed plugins and check every plugin on that list at startup**. Do not install the entire registry. Entries can pin a version and can start disabled; a disabled plugin is placed in `addons/s2script/plugins/disabled/` and still receives updates unless pinned. Bundled plugins inherit S2Script's standard enablement unless the operator overrides it.

Approved scope: Linux x86-64 is the supported platform; both Pterodactyl and Pelican are targets; the node daemon is optional; native HUD support is an optional dependency feature; the interactive Discord bot itself is a later project, while webhook alerts are included now. These decisions do not reduce the requested operational feature scope.

## Verified upstream facts

Research was performed on 2026-10-11. Release versions and registry contents are observations, not versions to hard-code as “latest.”

| Finding | Consequence |
| --- | --- |
| CS2-Egg-Go's current README declares GPL v3 or later and its LICENSE contains GPL-3.0; its README identifies the older CS2-Egg as MIT. | Match GPL-3.0-or-later and retain upstream notices if code is reused. |
| At initial inspection the workspace was a new repository with no commits, configured for `s2script/s2script-cs2-egg`. | The first commit contains the design; there is no existing product implementation to extend. |
| S2Script runs on CS2 Linux x86-64 through Metamod with PLAPI 18 support. | Resolve a compatible Metamod release and check the installed runtime layout. |
| The runtime ZIP contains a native shim, native core, verified game package, gamedata, configurations, and bundled plugins. | Update the runtime as a matched release while the game is stopped; preserve operator configuration and data. |
| The latest stable release returned by GitHub was `v0.8.1`, with `s2script-cs2-linux-0.8.1.zip` and a SHA-256 asset digest. | Implement release discovery and digest verification rather than a fixed download URL. |
| The official registry client uses `/api/v1/plan`, `/api/v1/meta`, and `/api/v1/download/s2sp`. | Use the real protocol, including dependency plans, filenames, review state, warnings, and hashes. |
| Public registry reads do not require a deploy token. | Normal startup plugin updates need no publisher credentials. |
| The catalog distinguishes plugins shipped in releases; a live plan for `basecommands` had no matching version and metadata for `@s2script/basecommands` returned package not found. | Bundled plugins must have a release source, not an assumed registry source. Do not infer the availability of every other package from this sample. |
| S2Script's runtime only scans the top level of its plugin directory; opt-in plugins ship under `plugins/disabled/`. | Persist enable/disable choices across updates and keep disabled artifacts out of the scanned directory. |
| Custom Panorama HUD requires MultiAddonManager 1.6.1 or newer and S2Script's HUD workshop addon. | Offer an explicit HUD-support option and document it; this helper is not another scripting framework. |

S2Script source inspection used commit `86d1de3ba7366b08f7e8bb0db34ca2a1e84415d7`. Only documentation and license text were examined from CS2-Egg-Go; its product source has not been imported.

## Approaches considered

1. **Purpose-built Go implementation — selected.** Build a server entrypoint and an optional node daemon with explicit interfaces, focused on S2Script. The approved license amendment makes this GPL-3.0-or-later. Compatible upstream code may be reused with preserved attribution after inspection; do not assume it is safe or tested merely because it exists upstream.
2. **Adapt the older MIT shell egg.** Potentially quicker for some server-startup behavior, but carries a larger shell maintenance surface and does not provide the newer Go project's node capabilities automatically. Any reused MIT material would require preserving its original notices.
3. **Wholesale fork of CS2-Egg-Go.** Now license-compatible, but would carry framework-specific behavior and assumptions that need removal. Retain the approved focused architecture; evaluate individual upstream components during implementation where useful instead of assuming wholesale compatibility.

## Architecture

### Server entrypoint: `s2egg`

Runs as the container user and owns server startup, validated configuration, framework installation, installed-plugin inventory, plugin reconciliation, console handling, process supervision, and graceful shutdown. It runs without the node daemon and communicates with the daemon when configured.

Deliver it in an image containing the libraries and SteamCMD support needed by CS2. The launcher is a compiled Go binary. Extra server arguments are parsed into an argument vector; configuration is never evaluated as shell code. Debug tooling can be an opt-in image/setting, with its panel allocation documented.

### Optional host service: `s2node`

Runs through systemd on a Wings node. It owns shared game builds, workshop storage, addon download caching, scheduled updates and restarts, backups, crash bundles, persistent console logs, cleanup, metrics, alerts, and opt-in packet filtering.

Each module has independent enablement and health reporting. Failure of an optional module does not disable unrelated modules. A failure involving game-file consistency prevents an unsafe game launch instead of being treated as an optional warning.

### Interfaces and storage

- A versioned local protocol connects each container to the node through a dedicated Unix socket exposed only to that server.
- The daemon associates each connection with a configured server identity and volume. A container cannot select another server, arbitrary host paths, or unrestricted host commands.
- The host administrator explicitly configures node paths and Wings access. No Docker socket or Wings administrator credential is mounted into a game container.
- Node configuration, schemas, CLI JSON output, and event records have explicit versions.
- Separate policy decisions from SteamCMD, registry, filesystem, Wings, and firewall adapters so they can be tested without a live host.
- Provide read-only status and structured events as the first integration surface for a future Discord bot. Server actions use a narrow, authorized command layer with audit records; no public unauthenticated HTTP control service.

## Startup and update sequence

1. Validate panel variables and configuration before changing files. Create missing configuration defaults without replacing operator edits.
2. Acquire a per-server startup/update lock and recover or roll back an interrupted managed-file transaction.
3. Reconcile shared game files with the node if enabled, or install/update the standalone CS2 tree through SteamCMD. App ID is 730.
4. Install or update compatible Metamod and S2Script releases with the game stopped. Keep the S2Script native pair and shipped game package together. Restore the required Metamod SearchPath after a CS2 update without duplicating it.
5. Discover and reconcile the installed-plugin inventory, check every tracked plugin against its recorded source, and resolve registry dependency plans.
6. Download and verify the entire proposed update set into staging before changing the active managed files. Persist enough transaction state to recover from a process interruption.
7. Commit the prepared files, apply all plugin enable/disable choices, and preserve operator configs, databases, custom gamedata, and unmanaged files. Retain a known-good generation.
8. Reconcile workshop links while the game is stopped. Generate only settings owned by this project.
9. Launch CS2 with redacted startup logging, forward panel console input and shutdown signals, and report health/status/events.

A registry or release service outage permits startup with an already verified, internally consistent installation and a visible stale-update warning. A first installation with no valid artifacts fails clearly. Explicit configuration errors, conflicting dependency requests, unsafe downloads, or integrity failures are not silently ignored. A retained installation is only a fallback when it satisfies the requested configuration.

Startup performs plugin installation and updates. It does not replace plugin files while players are using the running server. S2Script's own hot-reload capability remains available to operators outside this updater.

## Installed-plugin inventory

Use `s2script-plugins.json` for desired plugin policy and a separate `s2script-plugins.lock.json` for installed facts. Document both schemas and provide commands to list, inspect, add, remove, pin, enable, disable, and reconcile plugins.

Plugin entries accept a `version` string (`*` for latest compatible, a supported version range, or an exact version pin) and an `enabled` boolean. New registry entries default to enabled when omitted. Bundled entries inherit the upstream release's active/disabled placement when omitted. `enabled: false` applies before the first game launch, not only on subsequent restarts, and remains false across updates. CLI equivalents include `plugins add <name> --version <version-or-range> --disabled`, `plugins pin <name> <version>`, and `plugins enable|disable <name>`.

Each inventory entry records:

- Canonical package name and source: `release`, `registry`, or `local`.
- Installed version, artifact hash, managed filename, and enabled state.
- Requested version policy: latest compatible, a version range, or an exact pin.
- Whether it was explicitly installed or is a dependency, plus reverse dependencies.
- Last check outcome and time, available version when known, and any actionable warning.

Scan both the active and disabled plugin directories. Read archive manifests without executing plugin code. Match plugins by canonical identity, not filename alone. Reject duplicate identities and managed filename collisions before applying changes.

For manually copied plugins, discover them into the list and perform a public registry lookup by manifest identity. A matching name alone is insufficient to overwrite a local artifact: verify that its installed version/hash corresponds to a published artifact, or require the operator to adopt that package as registry-managed. Unpublished or modified local plugins remain listed, preserved, and clearly marked as not automatically updatable. Malformed archives are reported without guessing their identity.

At every startup, inspect every installed entry, including disabled entries. Check release-sourced plugins through their matching framework release, registry plugins through registry metadata/plans, and local plugins for a verifiable registry match. Pins still receive an availability status but are not overwritten. Disabled plugins can receive an artifact update while remaining disabled.

Use the registry's dependency closure and optional bundle-member support. Reject incompatible versions across multiple roots, unsafe filenames, missing hashes, checksum mismatches, and incompatible declared host API requirements before writes. Show registry review warnings and expose an optional reviewed-only policy. Bound response size, download size, extraction size, retries, and timeouts. Restrict credential forwarding and redirects to the configured origin/trusted artifact hosts.

Do not turn a disabled plugin back on merely because another plugin requests it. An enabled plugin requiring an explicitly disabled dependency is a conflict: identify the required toggle before modification. A disabled plugin may keep its dependencies installed and updated without loading them; dependencies needed only by disabled roots inherit disabled placement unless explicitly enabled. A dependency also needed by an enabled root stays enabled unless an explicit disable causes a conflict. Retain local/manual files during reconciliation. Explicit removal only removes artifacts this project owns, and does not remove a plugin still needed by another installed plugin.

Bundled plugins follow the framework release by default. A bundled plugin's exact pin constrains the matching framework release; conflicting bundled pins or an explicit incompatible framework pin are rejected before modification. Do not independently upgrade `clientprefs` across a core generation that requires a matched cookie schema. The UI/CLI explains the coupled release selection rather than pretending that an incompatible individual version was installed. A future registry publication of a bundled name does not silently change its source.

## Default plugin controls

Expose an enable/disable panel control for each plugin, backed by a single catalog used to generate the egg and documentation. Variable names use `S2SCRIPT_PLUGIN_` plus the uppercase plugin name. Accept `default`, `1`, or `0`: `default` respects the installed-list override when present, otherwise S2Script's release default; `1` forces enabled and `0` forces disabled. This avoids an unchanged panel default silently defeating an explicit disabled entry in the plugin list.

| Plugin | Initial setting |
| --- | --- |
| `basecommands` | enabled |
| `basechat` | enabled |
| `playercommands` | enabled |
| `antiflood` | enabled |
| `adminhelp` | enabled |
| `basecomm` | enabled |
| `basebans` | enabled |
| `reservedslots` | enabled |
| `basetriggers` | enabled |
| `funcommands` | enabled |
| `clientprefs` | enabled |
| `adminmenu` | enabled |
| `basevotes` | enabled |
| `zones` | enabled |
| `nominations` | disabled |
| `rockthevote` | disabled |
| `nextmap` | disabled |
| `funvotes` | disabled |

Explicit panel values override saved defaults consistently; `default` inherits them. Updates preserve operator choices and existing plugin configuration/data. Disablement moves the artifact out of the runtime's top-level scan rather than deleting plugin data. New upstream defaults are detected in release validation, surfaced in inventory, and added to the generated panel catalog deliberately; undocumented new plugins do not become silently enabled.

Native framework controls cover Metamod/S2Script update policy, release channel, and version pins. No CounterStrikeSharp, Swiftly, or ModSharp installer or selector is included. Optional HUD support installs a compatible MultiAddonManager and merges the documented HUD addon into its config without replacing existing addon selections.

## Game sharing and automatic updates

Shared mode keeps immutable game-build generations on the node. Containers mount the shared store read-only and link immutable assets into their own game tree. Configs, `gameinfo.gi`, addon state, Steam bookkeeping, and other writable content remain private to each server.

Build a new generation with one serialized SteamCMD update per node. Validate completion and record its build ID before publishing it. Switch each server to the new generation only while stopped; keep the previous generation while any running server still uses it. Garbage collection respects those references and retention settings.

Support symlink sharing through an explicitly configured read-only Wings mount as the recommended path. A copy mode supports hosts that cannot use the shared mount. Hardlink mode is limited to immutable generations on the same filesystem; updates never write through linked inodes.

Restart policies are immediate, when empty with a maximum delay, or a configured maintenance window with a maximum delay. Send a configurable player countdown before forced restarts. Obtain player counts through a tested status/query adapter; unknown counts are not assumed to mean empty. Restart through Wings and avoid duplicate restart jobs.

Standalone servers update through SteamCMD at startup. Optional running-update detection requests a controlled restart through the configured lifecycle adapter and verifies that the panel will restart it; it must not simply exit and leave the server offline. Shared servers skip container SteamCMD.

If the node becomes unavailable, a server with an intact pinned game generation can keep using it. A shared tree is never passed to standalone SteamCMD. Switching to standalone requires making a separate writable installation with a disk-space check; absence of a safe tree yields a clear failure.

## Operational feature parity

These are required milestones toward the requested parity target, not claims of existing implementation. The public README must label their actual implementation and validation status throughout development.

| Capability | Planned behavior |
| --- | --- |
| Panel compatibility | Importable Pterodactyl/Pelican egg, allocation and game settings, GSLT handling, workshop-map startup, extra arguments, console input, graceful stop, useful startup progress. |
| CS2 maintenance | Startup updates/validation controls, node polling, coordinated restarts, per-server policies, update progress and error reporting. |
| VPK/game sharing | Read-only shared generations, retained builds, standalone operation, safe copy fallback, protected server-specific files. |
| Framework/plugin cache | Deduplicate verified Metamod, S2Script, and registry downloads; honor server/node pins; prevent concurrent duplicate fetches. |
| Workshop cache | Cache complete versioned items including chunked VPKs; keep per-server writable bookkeeping; seed selected items; designate one updater; change links only while stopped; retain referenced versions; preserve unversioned items locally. |
| Console filter/logs | Configurable filtering, bounded buffers, rotating persistent logs, secret redaction, retention, and raw-event parsing before display filtering. |
| Guard | Optional console detection, observation mode, explicit kick/block policies, thresholds, allowlists, expirations, and explainable action records. Optional node nftables enforcement is scoped to configured game ports and a dedicated table. Protect bridge/private infrastructure addresses and validate server reports before host actions. |
| Backups/restores | Store private configuration, plugins, data, inventory, and relevant overrides outside server volumes; configurable retention and disk checks; stopped-server restore with preview and a recovery copy. Use stopped/quiesced backups for consistent plugin databases; label live snapshots as best effort. |
| Crash bundles | Record unexpected exits with redacted console tail, version inventory, and available dumps. Distinguish operator-requested shutdown/restart from an actual crash. Limit size and retention. |
| Cleanup | Scoped retention rules for demos/logs/dumps/cache, preview, path containment, and protection of active files and live game generations. |
| Metrics/status | Health checks, `status`, `doctor`, machine-readable CLI output, an interactive overview, and an optional private Prometheus endpoint. |
| Alerts | Discord webhook support for server up, server down, crash, startup failure, restart/update, backup, and guard events; configurable event selection, retries, rate limits, deduplication, and redaction. Supported without a companion bot and without the node daemon; no webhook is contacted unless configured by the operator. |
| Project updates | Versioned image tags plus stable/prerelease channels; opt-in verified node self-update, previous-binary rollback, configuration migrations, and protocol compatibility checks. |
| Diagnostics | Version reporting, installation checks, actionable error messages, log collection guidance, and explicitly enabled remote-debugging support. |

## Discord integration boundary

Build a versioned event model now: server lifecycle, game/framework/plugin updates, restart scheduling, backup results, and guard decisions. Events include stable node/server IDs, UTC timestamps, correlation IDs, severity, and redacted structured details.

Deliver webhook notifications in the first runnable server milestone. `server.up` means the game has passed its readiness check, not merely that a process was spawned. `server.down` means the previously ready game stopped; include the cause (operator stop, scheduled restart, unexpected exit, or lost contact) and emit at most one transition for a given run. Startup failures get a distinct event and must not announce a successful server-up. A node-observed loss of contact is labelled unconfirmed until reconciled, not presented as a confirmed game exit. The optional node can observe abrupt container termination; a standalone process cannot promise delivery after SIGKILL or host/power failure. Both limitations are documented. Configured webhook delivery must never block startup or shutdown indefinitely. When the node owns delivery, the entrypoint does not independently send duplicate notifications; event IDs support retry deduplication.

The future bot consumes these events and calls the same narrow actions used by the CLI. The first project includes local integration documentation and optional webhook delivery, not Discord application provisioning, slash commands, account linking, or a public control gateway. A separate bot project can add role-to-server authorization and remote transport without embedding bot credentials in game containers.

## Public distribution and documentation

Deliver source, reproducible build instructions, tagged release binaries/checksums, a container Dockerfile and publishing workflow, generated egg JSON, systemd service assets, example configs, and CI checks. The intended repository/image namespace is `s2script/s2script-cs2-egg`; document local build/import instructions until published releases actually exist. Do not present unbuilt image tags or nonexistent release URLs as usable downloads.

Required guides:

1. Quick start for a standalone server: prerequisites, build/release image, egg import, allocations, GSLT, initial startup, and `meta list`/S2Script verification.
2. Node installation: tested OS versions, directories and ownership, service account/privileges, Wings credentials, read-only mounts, modules, and first two-server shared setup.
3. Plugin management: installed list, source types, all default toggles, manual-plugin adoption, dependencies, pins, update failures, rollback, and HUD setup.
4. All panel variables and config fields, their defaults, precedence, validation, and secret handling.
5. Updates, restart windows/timezones, backups and restore, workshop cache, guard tuning, troubleshooting, and complete uninstall/migration procedures.
6. Development: architecture, build/test commands, release procedure, contribution guidance, security reporting, and the future bot's local integration contract.

License project code under GPL-3.0-or-later, matching CS2-Egg-Go. Include the GPL-3.0 license text, an SPDX declaration, a record of reused upstream revisions/files, and preserved third-party notices. Source for distributed project binaries/images must be available under the project license; release workflows link each artifact to its source revision. Keep game/framework downloads and their upstream notices distinct from this project's license declaration. End the README with credits linking K4ryuu's CS2-Egg-Go and CS2-Egg, describing actual reuse when applicable, and acknowledging S2Script, Metamod:Source, and any included native helper.

## Delivery sequence and evidence

Deliver in increments under this shared architecture:

1. Server entrypoint, container, generated egg, CS2/Metamod/S2Script startup, installed-plugin inventory/updates/toggles/pins, lifecycle webhooks, and standalone documentation.
2. Node service, server identity/protocol, shared build generations, coordinated CS2 updates/restarts, addon cache, and workshop cache.
3. Backup/restore, cleanup, logs, crash capture, metrics, guard, extended operational alerts, and node release management.
4. Public release hardening: fresh installation, migration/rollback/uninstall verification, full parity matrix, and bot integration documentation.

The whole task is not complete merely because the first increment runs. Track the operational parity table until every included capability is implemented and validated, or an explicit user-approved scope change is recorded.

Use fixture HTTP servers and temporary filesystems for repeatable tests of release discovery, registry plans, version conflicts, hash failures, malicious paths/symlinks, download interruption, transaction recovery, plugin discovery, toggles, config preservation, and concurrent startup. Test process/console/signal behavior with a fake game executable and SteamCMD adapter. Use fake Wings and query adapters to test restart scheduling and unknown-player-count handling.

For node features, test immutable generation retention, mount/path mapping, shared/standalone transitions, workshop multi-file items, backup restoration, retention boundaries, secret redaction, protocol authorization, event backpressure, and optional-module isolation. Run firewall tests in a disposable network namespace/VM, never against the development machine's active rules.

Release acceptance requires a real Linux CS2 container smoke test and a disposable Wings setup: verify `meta list`, an enabled and disabled bundled plugin, registry update behavior, two-server file sharing, a coordinated game update, a failed-download fallback, and backup restoration. Also verify the intended HUD path with an actual client if HUD support is shipped. Unit tests or a successful image build alone do not establish production readiness. When infrastructure or credentials are unavailable, report the unrun acceptance checks explicitly and keep the release status provisional.

## Approval record

The user approved the design with three amendments: match upstream GPL-3.0, include server lifecycle webhooks, and explicitly support version pins and starting plugins disabled while retaining updates for disabled plugins. These amendments are incorporated. Proceed to implementation planning; no second design approval is required. The configured workflow requires review of the written implementation plan and selection of the execution method before product implementation.

## Research references

- [CS2-Egg-Go README and license statement](https://github.com/K4ryuu/CS2-Egg-Go#license)
- [CS2-Egg-Go LICENSE](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/LICENSE)
- [Older CS2-Egg](https://github.com/K4ryuu/CS2-Egg)
- [CS2-Egg-Go documentation index](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/README.md)
- [Upstream panel-variable reference](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/reference/panel-variables.md)
- [Upstream shared game updates](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/modules/vpk-sync.md)
- [Upstream workshop behavior](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/modules/workshop.md)
- [Upstream guard behavior](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/modules/guard.md)
- [Upstream backups](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/modules/backups.md)
- [Upstream alerts](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/modules/alerts.md)
- [Upstream release/update behavior](https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/upgrading.md)
- [S2Script installation](https://www.s2script.com/docs/getting-started)
- [S2Script publishing and operator installs](https://www.s2script.com/docs/publishing)
- [S2Script plugin catalog](https://www.s2script.com/plugins)
- [S2Script HUD requirements](https://www.s2script.com/docs/concepts/hud)
- [Official registry client at inspected revision](https://github.com/s2script/s2script/blob/86d1de3ba7366b08f7e8bb0db34ca2a1e84415d7/packages/sdk/src/registry/client.ts)
- [Official plugin installer at inspected revision](https://github.com/s2script/s2script/blob/86d1de3ba7366b08f7e8bb0db34ca2a1e84415d7/packages/sdk/src/registry/install.ts)
- [Release packaging at inspected revision](https://github.com/s2script/s2script/blob/86d1de3ba7366b08f7e8bb0db34ca2a1e84415d7/scripts/package-release.sh)
- [Observed S2Script release v0.8.1](https://github.com/s2script/s2script/releases/tag/v0.8.1)
