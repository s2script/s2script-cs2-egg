# Verified artifact cache and workshop sharing

Both modules are optional. Standalone framework/registry resolution, verification,
installation and plugin reconciliation remain usable without a node. Workshop
sharing is explicitly selected in both node and server configuration. App ID is 730.

## Artifact cache

Example node configuration additions:

```json
{
  "cache": {
    "enabled": true,
    "root": "/srv/s2node/cache",
    "max_bytes": 536870912,
    "origins": ["github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "www.s2script.com"],
    "pins": {"s2script": "0.8.1", "metamod": "2.0.0-git1476", "mam": "1.6.2"}
  }
}
```

Registry pin keys use `registry/<published-package-name>`. An explicit local
selection that conflicts with a fleet pin fails, showing both versions. The node
independently resolves an exact official release or registry plan and matches the
selected digest/source commit. The caller retains framework compatibility,
reviewed-only policy, plugin dependencies, extraction and transaction ownership.
Policy also applies when existing installed/local cached bytes can be reused.

All three framework sources require the existing GitHub publisher SHA-256 policy.
Missing publisher digests fail. An observed content hash is never substituted for
publisher evidence. Source commits identify source; they do not authenticate binary
bytes. Metamod discovery stays on the compatible 2.0 feed; MAM is the supported
v1.6.2 steamrt3 asset. Registry archives retain their verified plan digest.

Cache bytes are addressed by SHA-256 and rechecked on retrieval. Downloads are
bounded (512MiB default, configurable up to2GiB), HTTPS-only, origin-allowlisted,
credential-free, and deduplicated across callers/processes. Redirects obey the same
allowlist. Configure public origins only. Corruption fails closed. Two module
workers process jobs, with serialized cache publication/download access. Jobs have
a 30-minute outer bound; HTTP downloads have a five-minute bound.

The five-second protocol carries submit/validate/status/read/release operations.
It never carries a host path or arbitrary download URL. A server-scoped handle
permits 256 KiB reads. The container writes its private stage and verifies the whole
digest before existing extraction. A canceled request cannot publish partial
bytes; stable request IDs allow ambiguous transport retries. Persisted pending
jobs resume on module startup. At most 1024 durable jobs / 128 queued jobs are admitted.
Completed handles are released by clients; ambiguous abandoned handles remain
conservative retention evidence. Cache pruning refuses while handles remain.

```
s2node cache-status --config /etc/s2node/node.json
s2node cache-sync --config /etc/s2node/node.json --source s2script --version 0.8.1
s2node cache-sync --config /etc/s2node/node.json --source registry --name hello-world --version 0.1.0
s2node cache-prune --config /etc/s2node/node.json
```

## Workshop policy and trust

```json
{
  "workshop": {
    "enabled": true,
    "root": "/srv/s2node/workshop",
    "trusted_importers": ["map-updater"],
    "items": ["3070244462"],
    "mode": "copy",
    "mount": "/mnt/s2workshop"
  }
}
```

`trusted_importers` defaults to empty and contains registered server IDs. Other
servers only consume published generations. Steam's public details API confirms
item identity, consumer app 730 and current published revision/size. It does **not**
cryptographically authenticate the bytes uploaded by a game volume. Cross-server
publication explicitly trusts the administrator-approved importer's volume.
Local SHA-256 values identify and protect stored content; they are not Steam
publisher signatures. Import authorization is checked before staging, on resume,
and at publication, including administrative sync.

Removing importer permission prevents further publication, including outstanding
uploads. Restart the daemon/helper with reviewed configuration changes. Previously
approved immutable generations remain usable. Replacing them requires stopped
attachment and reference reconciliation. A former updater cannot be replaced on a
timeout, disconnect, status report or absent PID. Stopped sync of the former owner's
actual helper revokes its unchanged outstanding ownership after role removal.

A generation consists of the complete item directory, keyed by 730/item/revision.
The ACF parser preserves other items/fields and rejects duplicate/ambiguous keys.
Installed/details revisions must agree, with no pending different latest manifest.
Legacy/unversioned items remain local. Unknown symlinks and special files are refused.
A revision with conflicting bytes is never overwritten. Bounds: 100 selected items,
512 regular files and 32 GiB per item, bounded ACF/tree parsing, 4096 elections and 8192
references. Unsupported/oversized items fail actionably rather than publish partly.

VPK validation checks the little-endian v1/v2 directory structure, preload lengths,
archive references and every byte extent. Missing chunks fail even when another
VPK exists. Directory archives need not have an `_dir.vpk` suffix. Newer CS2 v2
signature size 20 can describe a header plus further signature payload; extra
signature payload is allowed. Extent validation is not publisher signature or CRC
verification. Format facts were independently implemented after inspecting
[ValvePak's primary parser](https://github.com/ValveResourceFormat/ValvePak/blob/cfe3fe90b0ea69818b70845feebaea35a8ecac70/ValvePak/ValvePak/Package.Read.cs);
no upstream parser code or noncommercial Valve SDK code was copied.

## Startup and stopped sync

Add to each participating server's saved configuration:

```json
{"workshop":{"enabled":true,"items":["3070244462"],"mode":"copy","wait_seconds":30,"mount":"/mnt/s2workshop"}}
```

Startup already owns runtime lease→artifact lock and recovery. Workshop preparation
uses those locks without reacquiring them or calling a host helper. It resumes its
private journal, uploads a trusted complete download through bounded frames, pins
selected generations, stages content and privately updates ACF, then commits a
receipt. Boot receipts only add/renew pins; they never retire old references.
A restart after successful seal safely replays only exact chunks against the immutable
published generation; changed chunks or revoked importers are rejected.
Completed workshop jobs are retired after local completion. Do not manually erase
pending journals or references to bypass a failed preparation.

Only one trusted server owns a missing/new revision. Before it runs its normal game
downloader, shared bytes are copied to private writable files. SteamCMD is never
pointed at a shared workshop tree. The complete download is imported at that server's
next startup or an explicit stopped host sync. This module does not stop/start games
or secretly schedule another restart.

A newly starting selected follower waits at most its configured selection budget
(default 30 s, maximum 300 s), then reports pending item/revision/owner and remediation.
No eligible importer yields `no_trusted_importer`. It does not silently download a
second copy. Existing running servers keep their old generation references. Local,
unversioned, unrelated and sharing-disabled items do not participate in the gate.
Steam result!=1, including an unavailable/private item, is actionable failure;
no replacement workshop ID is guessed.

Disable MultiAddonManager's forced-redownload-on-mount setting:
`mm_addon_mount_download 0`. Otherwise a running addon manager may attempt to write
immutable shared bytes. Actual game/addon-manager update and mounting behavior still
requires live acceptance. If sharing is unsuitable, keep the item unselected/local.

```
s2node workshop-status --config /etc/s2node/node.json
s2node workshop-sync --config /etc/s2node/node.json --server map-updater
s2node workshop-prune --config /etc/s2node/node.json
```

Sync requires the existing host-only, fixed-volume helper, running as the actual
non-root volume UID. Root-owned configuration/ancestors fix its authority; both
Unix peers authenticate UID. Extend its reviewed configuration with:

```json
{"workshop_root":"/srv/s2node/workshop","workshop_items":["3070244462"],"workshop_mode":"copy","workshop_mount":"/mnt/s2workshop"}
```

Node and helper workshop delivery modes must match; the host request cannot silently change the helper policy.
A helper can have only a workshop root; game sharing need not be enabled. Preserve
the existing volume/socket/node_uid/runtime_uid/access_gid fields. The daemon never
opens 0600 private ACF or lock files through supplementary-group assumptions.
The helper takes actual runtime→artifact locks and recovery for the entire session.
Inaccessible locks never mean stopped. Host sync retains a pre-session reference
snapshot and retires only unchanged records after a durable helper receipt;
new/renewed records survive delayed receipts. Missing inventory does not mean empty:
the helper scans actual content under its stopped locks before allowing pin retirement.
Untracked links, foreign-owned or multiply linked files, malformed or oversized trees
leave inventory unknown and retain existing pins. A new boot pin cannot clear that
unknown state. A later stopped sync can establish known inventory only after every
shared reference is accounted for or content is verified private. Unknown inventories
block pruning.

Copy mode works without a mount. Symlink mode requires the workshop root mounted
read-only at the configured container path, with readable generation directories.
Hardlinks require a common filesystem/mount layout, distinct source/runtime UIDs,
and provisioned common GID: daemon-owned 0444 source files are linked by the daemon
into the helper's private 0770 stage, verified by inode, and renamed by the helper.
The helper never chmods/chowns shared file inodes. EXDEV or permissions fail; choose
copy or symlink explicitly. New hardlinks require stopped host sync; startup only
verifies an already attached hardlink generation. Verification requires the exact
local item/revision and private ACF identity, whole content hashes, and authenticated
`link-info` metadata for the pinned node generation. Local device/inode/readonly mode
and source UID must match, with source UID distinct from the runtime UID. Equal-byte
copies or different revisions require stopped sync; they do not count as shared.
`link-info` contains only bounded typed source identity (maximum 256KiB); no host paths
or extra readonly mount are required. Private ACF remains a separate,
writable file with its prior mode/ownership.

Provision these paths in the existing service sandbox yourself: daemon read/write
cache/workshop roots and permitted helper stage access; helper private volume
read/write plus workshop-store read-only; host-only sockets outside container
mounts. This code neither changes host service policy nor mounts anything.

Fixture tests cover complete chunks, private bookkeeping, generation/reference
retention, framed transfer, actual Unix helper locking, and interrupted recovery.
The existing explicit ephemeral-CI cross-UID fixture now additionally exercises
private workshop upload and daemon-owned hardlinks; it is skipped in ordinary local
runs. Real Steam download/ACF, CS2/MAM loading, panel mounts, distinct-UID CI and
operational updater acceptance are reported separately from those fixtures.
