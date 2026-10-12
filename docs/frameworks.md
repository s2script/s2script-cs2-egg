# Framework preparation

The `internal/steam` and `internal/framework` libraries prepare the CS2 stack. CLI installation/startup wiring and live native-loading acceptance are later work. `s2egg run` is not available in this milestone. Tests use a fake SteamCMD executable and TLS fixture servers; no native game/framework binary is executed on the development host.

SteamCMD receives an argument vector with `+force_install_dir ROOT +login anonymous +app_update 730 [validate] +quit`. Shell punctuation and spaces in ROOT stay in one argument. Update returns the installed app-730 manifest's build ID. LatestBuild reads only the public branch reported by `+app_info_print 730`. Output is bounded; cancellation kills the subprocess group, including wrapper descendants. The caller keeps game consumers stopped and holds the artifact lock during preparation.

## Official release selection and pins

S2Script selection uses [official GitHub releases](https://github.com/s2script/s2script/releases), choosing stable latest, or the first published release when the prerelease channel permits previews. Exact S2Script pins use `0.8.1` or `v0.8.1`. Every bundled plugin belongs to the selected native/runtime release: a pin such as `@s2script/clientprefs=0.8.1` constrains the whole S2Script generation. Different exact bundled versions, unknown bundled identities, or a conflicting explicit runtime pin fail before release discovery.

Metamod latest comes from the [official 2.0 Linux build feed](https://mms.alliedmods.net/mmsdrop/2.0/mmsource-latest-linux), followed by the exact release's GitHub metadata. The operator pin is the asset version `2.0.0-git1476`; resolution deliberately translates this to GitHub tag `2.0.0.1476`. The four-component tag is not accepted as a configuration pin. GitHub's generic latest release can be an older 1.12 build and is not the compatibility selector.

For every component, resolution follows the selected Git tag to its immutable commit, including bounded annotated-tag dereferencing. `target_commitish` alone can be a moving branch and is not source evidence. Selection records version, asset URL, publisher SHA-256, source commit, integrity provenance, channel, and API compatibility evidence.

Every downloaded archive requires the GitHub asset's publisher-provided `sha256:` digest. Missing digests fail closed, including for Metamod. There is no unverified-download fallback and a locally computed digest is not called publisher verification. Downloads require HTTPS and bounded bytes; official redirects are limited to the public GitHub API/raw/release CDN and the official Metamod feed. Errors omit artifact URLs. Resolver overrides exist for embedded callers and controlled TLS fixtures; normal callers use the zero-value official resolver.

## Compatibility and archive contents

At inspected S2Script v0.8.1 source commit `86d1de3ba7366b08f7e8bb0db34ca2a1e84415d7`, [loader.rs](https://github.com/s2script/s2script/blob/86d1de3ba7366b08f7e8bb0db34ca2a1e84415d7/core/src/loader.rs) declares host API 3 and explicitly accepts legacy API 2. Protocol-2 metadata requires API 3 separately. Resolution validates both host-3 and the explicit legacy-2 branch from the selected release's immutable loader source. The supported family mapping is `[2,3]`; it is not derived from framework version `0.8.1` and is not simple major equality. Same-family new releases can resolve automatically. New API families or changed source/schema contracts require a deliberate compatibility update and fail explicitly meanwhile. The plugin reader/reconciler must enforce protocol-2's additional API-3 rule.

The whole selected release is checked before publishing changes:

- Both `addons/s2script/bin/linuxsteamrt64/s2script.so` and colocated `libs2script_core.so` are required. The core's `$ORIGIN` dependency stays intact.
- `game-packages.json` schema 2 must identify exactly one `@s2script/cs2` package matching source2/csgo, with owner cs2. Bootstrap, gamedata, and trusted-functions references must stay in its package and match their declared SHA-256 values.
- The archive VERSION and bundled manifests must agree with the selected version. Bundled archives use the real `id`, `version`, and `apiVersion` fields, with additional archive members permitted. Raw manifest bytes retain `pluginDependencies`, `optionalPluginDependencies`, `publishes`, `compiledAgainst`, and protocol metadata for downstream reconciliation. Dependency names can be published interfaces rather than package IDs.
- Metamod includes the CS2 `metamod.2.cs2.so` and `libserver.so` pair. Other games' binaries are excluded. Its exact source [ISmmPluginExt.h](https://github.com/alliedmodders/metamod-source/blob/c47debe129df84c0e54d7393347d2a6afa3b9ee5/core/ISmmPluginExt.h) must declare PLAPI 18. A source/build association and verified archive establish preparation evidence; actual Metamod acceptance of S2Script still requires a live server test.

Shipped notice/license files are preserved. S2Script configs, data templates, custom gamedata, plugin README files, and Metamod's metaplugins.ini are create-only. Existing admin settings, SQLite data, custom gamedata, and plugin enablement remain operator-owned. Release-owned native/game-package/gamedata/translation files are replaceable managed files. Files absent from a later release are not broadly deleted; no recursive replacement removes an operator tree.

Prepared bundled artifacts carry canonical name, version, SHA-256, filename, staged path, upstream enabled state and raw manifest. Framework changes never place `.s2sp` files. The plugin reconciler combines those artifacts with saved/panel policy and supplies the final active/disabled placement changes. Clientprefs follows the same matched-generation rule as the native core. Upstream v0.8.1 has 18 bundled plugins, with 14 enabled and 4 disabled; explicit operator overrides belong to reconciliation.

Gameinfo changes insert one unconditional `Game csgo/addons/metamod` search path before the existing paths, preserving unrelated text/comments/conditions. Patching is idempotent. Malformed, deeply nested or ambiguous SearchPaths files, and conditional Metamod entries, fail rather than produce a launch claim.

## Retained state, verified cache and fallback

Preparation stages schema-1 `egg/framework-state.json` as part of the same transaction. It contains the selection and SHA-256 facts for managed native/game/helper files. Config/data/custom files and bundled plugin placements are excluded from immutable-file facts; the plugin layer owns its installed inventory. Disabling S2Script or Metamod updates requires valid retained state and unchanged native/game files. A pin conflicting with the retained version fails before network access. A first install with updates disabled requires an existing valid managed installation; missing/corrupt state is an error.

Selected verified archives are staged as create-only changes at `egg/cache/frameworks/<sha256>.archive`. This is a local immutable archive cache, not a node/shared-game cache. Preparation reads cached archives through pinned descriptors, rechecks their complete SHA-256 and revalidates extraction/layout. Corrupt cache entries fail instead of being quietly replaced. New cache entries become persistent only when the caller applies the combined transaction; failed preparation does not write the live tree. `Prepared.Cleanup()` removes disposable staging, not the cache. Older cache entries are retained; automatic pruning is not implemented yet.

`*framework.UnavailableError` means a transport failure or HTTP 429/5xx. Context cancellation, publisher-digest mismatch, source trust failure, missing exact release (404), incompatible API/schema, invalid cache/state, and pin conflicts never qualify as a transient outage. Resolve automatically selects a retained generation on an eligible outage only after verifying native/game facts, official source URLs/commits, complete cached archives, the same requested channel, and all exact/bundled pins. Returned `Selection.Stale` and `Warnings` visibly report this choice. Startup must log/notify that warning.

If online Resolve succeeds but Prepare returns `UnavailableError`, the caller may explicitly call `framework.Retained(ctx, cfg, bundledPins)` and prepare that verified selection. This helper repeats policy, source, state and cache checks. Cancellation always stops the sequence. An offline fallback does not permit skipping framework/plugin checks or launching a partial generation.

Required caller sequence:

1. Stop consumers, acquire `artifact.Acquire(root)` once, and successfully `artifact.Recover(root)` before consuming installation files.
2. Resolve installed plugin policy/pins, call framework Resolve, log any stale warning, and call Prepare. On a typed temporary Prepare outage only, use Retained and retry Prepare once.
3. Reconcile every installed plugin and all returned bundled artifacts. Combine plugin changes and `Prepared.Changes`, including state/cache/gameinfo, in one artifact Apply call.
4. Clean up staging on success or failure and release the caller-held lock. Launch only after all preparation/reconciliation/application succeeds. Apply is recoverable per-file replacement, not an atomic multi-file rename.

## Optional HUD

HUD preparation requires installed app-730 build **24934554 or newer**, checked before downloads. It selects [MultiAddonManager v1.6.2](https://github.com/Source2ZE/MultiAddonManager/releases/tag/v1.6.2), specifically `MultiAddonManager-v1.6.2-steamrt3.tar.gz`, using its publisher digest and immutable tag commit. The inspected archive's native API reports PLAPI 18. This exact supported helper release is pinned deliberately; a new helper/runtime build requires compatibility review. Its archive contains no license member; source repository/commit provenance is retained, and the [GPL-3.0 source license](https://github.com/Source2ZE/MultiAddonManager/blob/main/LICENSE) remains available upstream.

Preparation installs the helper's native/VDF members and merges addon `3790153369` into `game/csgo/cfg/multiaddonmanager/multiaddonmanager.cfg`, retaining other addon IDs, settings and comments. Multiple/unsupported assignments fail clearly. [Official HUD requirements](https://www.s2script.com/docs/concepts/hud) remain subject to live server/client acceptance; this milestone does not claim the HUD renders or the helper loads.
