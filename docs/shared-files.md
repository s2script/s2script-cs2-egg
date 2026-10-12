# Immutable shared CS2 files

The optional node module installs Steam app **730** into a fresh staging directory and publishes a complete, immutable generation. It never updates published files or runs SteamCMD against a private tree containing shared links. The default host store is `/srv/s2node/shared`; the container mount target is `/mnt/s2node`.

Enable `shared.enabled` in the node configuration, set an absolute path to a dedicated, already bootstrapped host SteamCMD executable, and provision the store for the non-root daemon UID. No command installs users, changes mounts, enables services or changes kernel settings. Build as that daemon UID:

```sh
s2node shared-build --config /etc/s2node/node.json
s2node shared-prune --config /etc/s2node/node.json
```

Build is a separate administrative operation. `game.prepare` only reserves an already published generation and commits its reference; it does not run a long SteamCMD download inside the five-second transport deadline. Bootstrap the dedicated SteamCMD installation before the first build so its `linux64/steamclient.so` exists. SDK64 is required; SDK32 is seeded when available. Completeness validation requires an executable CS2 binary, gameinfo, SDK64, the installed app730/build manifest and at least one nonempty VPK. No live Steam download or CS2 acceptance is implied by the filesystem tests.

Each build uses a new tree, never a hardlinked clone. Before staging, free space must meet `shared.reserve_bytes` (default **64 GiB**); operators must increase it if their installation needs more. Copies preflight the selected file bytes plus16 MiB. Concurrent unrelated disk usage or growth during downloads can still exhaust space; failed builds never replace the current pointer. SHA256 file inventories, file fsync, directory fsync and a final publication pointer establish the generation boundary. Generation files are0444 or0555; published directories are0555. The trusted daemon remains their owner and must never modify published inodes.

## Delivery choices

| Mode | Startup | Update requirement |
| --- | --- | --- |
| `symlink` (recommended) | VPK links through the verified read-only `/mnt/s2node` mount; other files are private copies | Stopped local preparation under runtime/artifact locks |
| `copy`, with read-only mount | Distinct local files, verified against source manifest | Stopped local preparation |
| `copy`, without shared mount | Verify a complete, distinct local copy inventory installed by the host helper | Stop → host `shared-attach --mode copy` → start |
| `hardlink` | Verify the host-installed foreign-owned immutable inodes through the read-only mount; never create links in the container | Stop → host `shared-attach --mode hardlink` → start |

In `egg/server.json`, `shared` accepts `mount` (default `/mnt/s2node`), `mode` (`symlink`, `copy`, `hardlink`) and `overrides` (relative paths or subtrees to preserve). A missing/unwritable source mount never triggers standalone SteamCMD. No-mount copy startup needs an already complete host attachment matching the generation the node reserves. A newer generation, missing inventory, linked copy inode or damaged file fails with an instruction to perform stopped host Attach. This copy path does **not** require a container shared mount.

Only nonprivate VPKs are linked. Existing `game/csgo/gameinfo.gi`, cfg, addons, data, Steam bookkeeping at every depth (`steamapps`, `Steam`, `steamcmd`, `.steam`), their inodes/ownership/modes, and explicit overrides remain untouched. Missing private defaults are seeded as private copies. The separate `.s2shared.json` identifies the effective installed build without rewriting existing Steam bookkeeping. Modified managed files become persistent observed overrides. Explicit startup exclusions, helper exclusions and observed overrides are merged into the pending/completed local inventory as preserved operator policy, including no-mount copy startup. Helper Attach therefore honors startup exclusions without duplicated configuration. Removing a declaration from server/helper configuration does not revoke a recorded exclusion: while stopped, review and remove it from the local inventory (and any pending inventory) as well before allowing replacement.

## Administrator-configured Wings mounts

For Pterodactyl or Pelican, the administrator adds the shared host directory to Wings `allowed_mounts` in `/etc/pterodactyl/config.yml` or `/etc/pelican/config.yml`, creates the panel mount with source `/srv/s2node/shared`, target `/mnt/s2node`, and **Read Only**, permits the relevant nodes/eggs, and attaches it to each server. Follow the panel's normal restart steps. No Docker socket, Wings administrator credential or dynamic namespace/mount helper belongs inside a game container.

Startup pins the container-visible mount without traversing symlinks, checks the filesystem read-only flag and the selected generation's manifest digest. Host path existence is insufficient. Administrative Attach reports stopped filesystem completion; container mount readiness is checked on the following startup. A two-container live Wings mount test remains a separate acceptance item. See [Pelican mounts](https://pelican.dev/docs/guides/mounts/) and [Pterodactyl mounts](https://docs.pterodactyl.io/v2/guides/configuration/mounts).

## Private host helper

The daemon UID differs from the runtime UID. Supplementary group membership does not open0600 runtime/artifact locks or private plugin/config files. Administrative Attach therefore requires an explicitly provisioned **per-server helper**, using the same `s2node` binary, running as the actual non-root host volume UID. Its root/operator-owned config and executable are outside all container mounts. `helper_socket` in each registered node server selects its host-only Unix socket; `peer_uid` must also match the helper's actual host UID. Both sides authenticate Unix peer credentials. The helper's config fixes one volume, one store, daemon UID, runtime UID, common GID, socket and optional override paths. Requests contain bounded generation identifiers and typed phases, never arbitrary paths, shell commands or server selectors.

`examples/shared-helper.json` and the staged `s2node-helper@.service` show the contract. `s2node install --directory PATH` only stages binaries/units for review. The helper template deliberately uses an unconfigured service account until an operator supplies a root-owned instance drop-in:

```ini
[Service]
User=1000
Group=1000
SupplementaryGroups=1000
```

Replace numeric identities with the actual provisioned volume UID and common access GID; they are examples. Configure the daemon's supplementary group separately. Systemd's per-instance runtime directory holds the socket outside the volume; the helper may assign its newly created socket/stage to a group it already belongs to. Existing locks and private files stay0600 and retain their owner. The helper has no root, DAC, CHOWN or FOWNER capability. Inaccessible locks produce an explicit provisioning/stopped-state error. It acquires **runtime lease → artifact lock → recovery**, normally holds them through materialization and node reference reconciliation, and releases on session completion/disconnect/timeout. A delayed receipt is not proof that those locks remain held; reference retirement uses the transaction fence described below. A held runtime lease is an error for administrative Attach; stale protocol status or EACCES never proves stopped.

```sh
# After the administrator stops the managed game and provisions the helper:
s2node shared-attach --config /etc/s2node/node.json \
  --server registered-id --generation GENERATION_ID --mode symlink
```

The helper also supports the opt-in [backup/restore contract](backups.md). A runtime started outside the managed supervisor does not participate in the lease contract.

## Hardlink ownership and mount contract

For hardlinks the stopped helper creates one random0770 stage under its volume's `egg` directory using the configured common GID. The source-owning daemon links manifest-selected VPKs into the pinned stage; the runtime-UID helper verifies the actual source/staged inode identity and renames entries into place. Neither side chmods/chowns linked inodes. Source UID must equal the daemon and differ from the runtime; no container may receive CAP_FOWNER. Files must have no write bits. The runtime can remove its name but cannot chmod/write the foreign-owned shared inode. Giving the container the source-owner UID would invalidate this protection and is rejected.

The actual Linux link operation determines support. Separate mounts, including separate binds of one underlying filesystem, can return **EXDEV** despite matching `st_dev`. `protected_hardlinks` is not disabled; source ownership permits the daemon's operation. Any link failure is actionable and requires explicitly selecting copy or symlink mode; there is no silent fallback.

The default node unit's `ProtectSystem=strict` plus separate `ReadWritePaths` binds can split the source and stage into different mounts. For opt-in hardlinks an administrator can provision a dedicated common parent containing only this store and its registered volumes, then expose **that one parent** through one reviewed `ReadWritePaths` entry in both relevant service namespaces. Keep private0600 file protection and source ownership enforced by Unix permissions; do not add a separate read-only bind for the source in the host helper/daemon namespace. A possible layout is `/srv/s2node/shared` and `/srv/s2node/volumes/SERVER/egg`; node state remains `/var/lib/s2node`. The container still receives its read-only shared mount. Existing layouts spanning mounts should use copy/symlink. This is an operator-reviewed deployment change, never something this application applies automatically.

## Recovery and retention

Reservations are persisted by server/run/preparation identity before ACK, retried idempotently, and retained across disconnect/restart. Local pending metadata precedes file replacements; interruption resumes that generation under the caller's existing locks before consumers can start. Each rename is durable, but a multi-file attachment is not atomic. Local inventory commits precede the idempotent node reference commit. Old/current/pending references remain even after ambiguous responses. Module startup loads/validates persisted references and binds the registered-server reader. Prune refuses to proceed for newly registered servers without durable shared history; prepare or safely attach them first. Observed live build IDs add retention only; no status value authorizes mutation or reference release.

Before requesting helper attachment, the daemon snapshots eligible reference identities and contents. After a completed materialization receipt, it can retire only unchanged records from that snapshot. New reservations and renewed/retried pins carry durable revision nonces and remain referenced, even if the helper dies/releases its lease before a buffered receipt is consumed. An absent/failed receipt cannot trigger reconciliation. This also protects reused administrative pins at quota and avoids treating the receipt as current stopped-state proof. Preserved operator symlinks and known immutable hardlink dependencies retain their previous generations during reconciliation. Normal local startup conservatively retains prior generations until this explicit stopped reconciliation. Removed registrations and uncertain runs also retain references; no automatic deletion policy guesses their lifecycle. The bounded snapshot allows8192 reference records,100000 files/generation and64 MiB metadata. Quota exhaustion fails without ACK and requires operator reconciliation; a stopped Attach to an already referenced generation reuses its durable pin so that reconciliation can still proceed at quota. Abandoned staging directories may be removed only while no build is active; they are never published or used by consumers.

To leave shared mode, stop the managed game and run `s2egg shared-standalone --root PATH`. It takes both locks, recovers, and examines links throughout the volume, including binaries, private files and explicit overrides. Hardlinked regular files become distinct writable inodes; symlinks are copied only when they name exact regular files in known generation manifests through pinned paths. Executable bits are preserved. Unknown/external/directory symlinks, linked control files, unavailable sources or any failed/interrupted copy produce an actionable error and retain the shared marker; arbitrary symlink targets are never read. Repair those entries while stopped and retry. Only a final scan confirming no links remain permits marker removal and standalone SteamCMD. Disable shared preparation for that server before its next standalone launch; merely disconnecting the node is insufficient until migration finishes. Host references remain conservative until separately reconciled.

Coordinated update scheduling uses persisted stop → helper Attach → start phases for hardlink mode and no-mount copy mode. A single Wings restart cannot install their next generation and will fail preparation. Symlink/mounted-copy generations can materialize during normal stopped startup. See [updates and recovery](updates.md) for durable scheduler phases.

Filesystem/unit tests include real EXDEV using separate temporary filesystems. The separately gated CI fixture uses two numeric subprocess UIDs within a freshly allocated isolated temporary root; it verifies0600 access and actual immutable hardlinks. Local unprivileged runs skip that fixture and do not claim cross-UID or live-container acceptance.
