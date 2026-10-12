# Plugin policy and inventory

`s2egg plugins` manages desired choices in `s2script-plugins.json` and installed facts in `s2script-plugins.lock.json`, both relative to the server root. `S2SCRIPT_PLUGIN_FILE` may select another policy file within that root. Discovery scans `game/csgo/addons/s2script/plugins/` and its `disabled/` directory. Disabled plugins remain in the installed inventory for future update checks.

The [bundled plugin table](bundled-plugins.md) is generated from the same catalog as the panel egg. This guide is maintained separately from generation.

## Desired policy

```json
{
  "schema_version": 1,
  "plugins": {
    "@s2script/basecommands": {"source": "release", "enabled": false},
    "hello-world": {"source": "registry", "version": "1.2.3", "enabled": false}
  }
}
```

Each key is the package's manifest `id`, which can differ from its filename. `source` is `release`, `registry`, or `local`. An omitted or empty `version` means `*`. Exact SemVer pins stay exact, including prerelease/build identifiers; discovery never writes resolved versions into this file. Supported policy ranges contain exact SemVer operands with `^`, `~`, `<`, `<=`, `>`, `>=`, or `=`, space-separated comparisons and `||` alternatives. Partial versions, tags and shell expressions are rejected. Registry reconciliation evaluates these ranges, including SemVer prerelease precedence; wildcard ranges exclude prereleases. Exact pins compare the full original version, including build metadata.

`enabled` has three states: `true`, `false`, or omitted. Null is invalid. Explicit panel `1`/`0` wins, followed by the saved policy pointer. `default` inherits policy. Reconciliation must preserve an existing chosen placement when neither explicitly overrides it; a newly bundled plugin falls back to its validated release placement. New registry entries default enabled. Optional `with` lists requested optional package identities. See the [policy schema](../schemas/s2script-plugins.schema.json) and [example](../examples/s2script-plugins.json).

## Commands

Run commands inside the game container or with `--root /path/to/server`. Configuration validation follows `s2egg config check`, so provide the normal Steam token setting or explicit `ALLOW_TOKENLESS=1` for an offline test tree.

```sh
s2egg plugins list --root /server
s2egg plugins inspect hello-world --root /server
s2egg plugins add hello-world --version 1.2.3 --disabled --root /server
s2egg plugins add another-plugin --with optional-addon --root /server
s2egg plugins pin hello-world --version 1.2.3 --root /server
s2egg plugins unpin hello-world --root /server
s2egg plugins disable hello-world --root /server
s2egg plugins enable hello-world --root /server
s2egg plugins remove hello-world --root /server
s2egg plugins reconcile --root /server
```

`list` and `inspect` emit JSON containing both desired policy and observed inventory. `add` saves version and enablement together before invoking registry reconciliation. The reconciliation library is implemented; the executable still has an unwired integration hook until the startup-orchestration task: `add` reports “policy saved” followed by an unavailable error, and `reconcile` reports unavailable. Neither command claims installation or contacts the registry until that hook is connected. A failed resolver leaves the desired policy available for the next attempt.

`pin` and `unpin` stage policy for a future reconciliation. `enable` and `disable` move existing owned artifacts while stopped and save the policy/lock in the same recoverable transaction. A panel override can change effective enablement at reconciliation even if a CLI command has saved a different desired value. Editing policy is also a way to stage enablement for next start, including for manual files; the direct CLI refuses to move an unowned artifact.

`remove` removes only matching artifacts owned by this project and their desired/installed records. It preserves plugin config/data and manual/local files. Required dependency parents and published interface consumers block removal or disabling. If the installed manifests cannot identify a required dependency provider, the command reports that registry dependency evidence is required. Enabling an existing owned plugin validates actual provider files, published contract versions, types hashes and enabled placement. A disabled required provider is reported as a conflict; stage its policy and reconcile the complete closure to enable it. Optional dependency declarations do not make a plugin required.

## Installed facts and safe changes

The [lock schema](../schemas/s2script-plugins.lock.schema.json) records manifest identity/version, canonical source, SHA-256, safe filename, observed enablement, ownership and verification, dependency parents, last-check status/time, raw manifest and available source/generation evidence. `source` describes origin; verification is a separate fact. Unknown files are `local` and `unverified`; the CLI never adopts them by guessing from an official-looking filename. Matching identity/version/digest/filename retains verified ownership across an active-to-disabled move. A changed identity or digest, or a rename, loses ownership. Missing tracked plugins remain visible with status `missing` so startup reconciliation can check the complete installed list.

Manifest readers inspect bounded ZIP/JSON without executing plugin content. They reject traversal, links/special members, duplicate members/JSON keys, excessive metadata/expanded sizes and ambiguous package identities. Additional safe ZIP members are allowed. Raw compatibility, required/optional interface dependencies, `publishes`, and `compiledAgainst` evidence survive inventory serialization. The host API is independent of the framework version. Reconciliation uses the explicitly selected staged framework's supported API evidence: host API 3 supports legacy API 2 and current API 3, and interface protocol 2 requires API 3. Only the source-proven API 2 phantom dependencies `@s2script/entity` and `@s2script/math` receive the host-3 builtin exemption; other missing providers fail visibly. Direct CLI operations needing that exemption use `framework.Retained` to validate live framework state, native artifacts and cached archives, without network access. Missing evidence prevents the operation.

Artifact moves, removals, reconciliation and recovery require a stopped managed runtime. The shared `.runtime-lease` lock is held from before managed startup updates until the actual game process exits. Offline writers take that lease before `.artifact-lock`, which serializes discovery, staging and transaction application. Policy-only edits can proceed while the lifetime lease is busy, under the artifact lock, if no recovery is pending. Pending recovery requires stopping the server even for a policy-only edit. Read commands also refuse to inspect a pending transaction. Lock files are stable lock identities and must never be deleted or replaced to bypass a busy owner. This guard covers the managed entrypoint; it cannot detect arbitrary external game processes.

## Integration contract

`plugins.Discover(serverRoot)` and `plugins.ReadLock(serverRoot)` use the server root. `plugins.Target(filename, enabled)` returns a path relative to `game/csgo`; prepend `game/csgo/` exactly once for `artifact.Change.Path`. `plugins.StageLock(stageDir, installed)` produces a staged change for the root lock file. The caller owns runtime lifetime exclusion, `artifact.Acquire`, `Recover`, the combined `Apply`, and stage cleanup. Keep all framework, bundled/registry plugin placement and lock changes in a single transaction at startup.

`plugins.RunCLI` accepts a `ReconcileFunc` receiving `ReconcileRequest{Root, PolicyPath, Policy, Overrides, Installed}`. The callback runs after recovery with both locks held; it must not reacquire them. Use `PanelOverride(request.Overrides, name)` and `ResolveEnabled` to preserve default inheritance; configuration's effective bundled defaults are not explicit overrides. The resolver must check every installed entry, including disabled and missing ones, preserve exact pins and existing chosen placement, and populate source/dependency/compatibility evidence rather than infer it from filenames.


## Registry checks and retained operation

`plugins.Prepare(ctx, cfg, policy, installed, selection, bundled)` checks every installed identity, including disabled, pinned, missing, and local entries. Registry roots use the public `/api/v1/meta` and `/api/v1/plan` endpoints without publisher credentials. Metadata is bounded raw JSON because the inspected SDK does not publish its response schema. Availability is taken from the verified wildcard Plan root; exact pins use a separate exact Plan and preserve their existing bytes. New dependency artifacts also get a verification check result. Local files receive metadata checks but are preserved; explicit registry adoption requires exact matching identity, version and publisher digest. A filename alone never proves ownership.

`plugins.reviewed_only` in `egg/server.json` defaults to `false`; `S2SCRIPT_REVIEWED_ONLY=0|1` overrides it. When enabled, every selected registry artifact must have exactly `reviewState: "reviewed"`. The website label "approved" is not accepted as equivalent. The setting is rechecked against retained facts during an outage. Registry responses, artifacts and redirects must stay on the configured HTTPS origin. An embedding application may supply `Registry{BaseURL, Client}` for a trusted mirror or TLS fixture; the default origin is `https://www.s2script.com`. Cross-origin storage redirects currently fail closed until an upstream source policy is verified.

Plans from all roots must agree on artifact identity, version, digest, source URL and filename. The entire closure is downloaded and hash-checked before changes are returned. Required dependencies resolve through manifest `publishes` identities and their contract versions; provider package versions do not stand in for published versions. Cycles, missing providers, type-hash drift and explicit disabled-provider conflicts abort the whole preparation. Exclusive implicit dependencies of a disabled root stay disabled. Shared required providers are enabled for enabled consumers unless explicitly disabled. Optional bundle members are requested through Plan's `with` query and recorded with the root.

The lock's `registry_roots` records the resolved root version, requested range/optional members, registry origin and complete member identities/versions/digests. A bundle root is resolution evidence, not a fabricated installed artifact. HTTP 429/5xx and recognized transient connection failures permit a fresh verification of this retained closure against the current policy and actual complete artifacts. Missing/stale evidence, changed pins or optional selections, source conflicts, digest errors, malformed responses, TLS/redirect rejection and HTTP 404 do not permit fallback. A partial response is conservatively terminal. Outage checks report `retained-unavailable`, without inventing a current available version.

`Prepared.Changes` includes the staged lock update; apply it with all plugin/framework changes in one `artifact.Apply`. `Prepared.Installed`, `Prepared.Checks`, and `Prepared.StageDir` expose the outcome and stage lifecycle; call `Prepared.Cleanup` after apply or any later failure. A failed preparation removes its stage and returns no changes. `StageLockWithRoots` stages complete resolution evidence; the older `StageLock` deliberately invalidates it on direct artifact moves/removals. Policy-only pin/unpin edits preserve evidence, which fallback independently checks against the changed policy. After a direct artifact mutation, a successful online reconciliation is needed before registry-outage startup can reuse a closure. Neither Prepare variant acquires locks, recovers transactions, or applies changes itself.

These guarantees are covered by local TLS/ZIP/transaction fixtures. A successful live registry metadata schema and native CS2 loading have not been established; startup/CLI orchestration is still the next milestone.
