# Private-state backups and restore

Backups are optional and disabled by default. They preserve private server configuration, S2Script plugins (including disabled plugins), plugin policy/lockfiles, databases, and custom overrides. Bulk shared CS2 files stay in the shared store. Archives contain private data; keep the catalog outside container mounts and protect any off-node copies accordingly. Nothing is uploaded automatically.

## Provision the helper

Follow [node installation](node.md) to provision the fixed-volume helper service. Use [backup-helper.json](../examples/backup-helper.json), substituting the real server ID, daemon UID, host volume UID and access group. You can add its `backup` object to an existing shared/operations helper configuration. It also works without shared storage.

The operator must create the backup root as the actual **non-root server volume UID**, mode **0700**, outside **every** game-container mount, private volume, shared store and operations root. Keep its root ID stable and unique. The catalog binds that ID, server ID, and volume device/inode; moving/replacing the volume requires deliberate operator migration. Root IDs are not secrets or authentication tokens.

Keep the helper binary/config and configuration ancestors root-owned and not writable by the game or daemon. Its private socket uses the configured access group; the helper authenticates the daemon UID and the daemon checks the helper UID. Add the backup root to the helper's filesystem sandbox if using `ReadWritePaths`. Preserve the target files' supplementary GIDs in the service configuration. No root, CHOWN, FOWNER or DAC capability is needed. Do not mount this socket or catalog into the game container.

Set each registered server's `helper_socket` in node configuration. Enable the module:

```json
"backup": {
  "enabled": true,
  "schedule": false,
  "interval_seconds": 86400,
  "mode": "consistent"
}
```

Start the configured helper and node services using the installation guide. These examples do not start services automatically. `schedule: true` requests an interval backup for registered servers with helpers; 15 minutes–30 days are supported. The saved schedule prevents duplicate admission after node restart. There is no automatic stop/start.

## Create and inspect

```sh
s2node backup-now --server SERVER_ID --request nightly-2026-10-12
s2node backup-job --server SERVER_ID --id JOB_ID
s2node backup-list --server SERVER_ID
```

Mutations return a durable job immediately. Reuse `--request` when an admission response is lost; conflicting reuse is rejected. The CLI prints a generated request ID when omitted. `backup-job` shows pending/running/complete/failed/uncertain and the helper's catalog result. `backup-status --server SERVER_ID --id JOB_ID` asks for the helper's saved receipt, useful after a transport failure. An error or timeout is not proof that no work occurred.

The default `consistent` mode requires the managed runtime lease to be free. Stop the server through your panel first. Unknown status, inaccessible private files, or a disconnected runtime never authorize a backup or restore. Games launched outside the managed supervisor are unsupported because they do not hold this lease.

`--mode live-best-effort` permits backup without stopping. Selected files are checked for changes, and pending artifact/shared transactions are refused. Even if every file appears stable, this **does not provide an application-consistent database snapshot**. Use stopped backups for recoverable SQLite/plugin state.

Default selection includes `egg/`, root `s2script-plugins.json` and `s2script-plugins.lock.json`, `game/csgo/gameinfo.gi`, `cfg/`, `addons/`, `data/`, `custom/`, and persisted shared overrides. Helper `include` and `exclude` arrays select additional relative file/directory prefixes or Go filepath patterns. Explicitly include custom private VPKs outside these locations. Control/lease/socket/transaction/cache/log/dump/backup namespaces and dot-prefixed components are always excluded. Selected symlinks, hardlinks and special files fail instead of being followed. Unselected shared VPK links are skipped.

The compressed archive, external manifest and catalog publication are fsynced before the completed archive is listed. SHA256 detects changed bytes; it is not proof of provenance. Framework/shared version facts are recorded separately. A private-state restore does not roll back a shared game generation or guarantee framework compatibility.

## Preview and restore

Stop the managed server, choose an ID from `backup-list`, then:

```sh
s2node backup-preview --server SERVER_ID --id BACKUP_ID --request preview-change-1
s2node backup-job --server SERVER_ID --id PREVIEW_JOB_ID
s2node backup-document --server SERVER_ID --id PLAN_ID --document plan
s2node backup-restore --server SERVER_ID --id PLAN_ID --confirm PLAN_DIGEST --request restore-change-1
s2node backup-job --server SERVER_ID --id RESTORE_JOB_ID
```

Read **every** preview page before confirming. The document command writes JSON text and prints the next byte offset on stderr; repeat with `--offset N` and join pages for large documents. `backup-document --document manifest --id BACKUP_ID` reads a manifest. Lists return up to 64 summaries and documents up to 64 KiB per page. Archives remain on helper disk.

The preview identifies selected files, current/absent targets, parent identities, volume identity, archive digest and the external recovery-copy location. It expires after 24 hours. Execution requires its exact digest and revalidates the archive, targets and parents under stopped runtime exclusion. Changed input requires another preview. Client edits to a downloaded plan cannot alter the helper's saved authority. Arbitrary archive imports and cross-server restores are unsupported.

Before replacing anything, restore seals a recovery archive of overwritten files and an absent-target ledger. Existing permitted UID/GID/mode are retained; foreign owners, unavailable supplementary groups and privileged mode bits are rejected. New files are 0600 and new directories 0700. Files unrelated to the archive are retained. Per-file replacements use the artifact transaction journal; this is recoverable work, not an atomic multi-file change.

If interrupted, managed startup recovers the artifact journal before launching the game. A restore request is never blindly replayed. Inspect its receipt and retained recovery copy; uncertainty requires a fresh preview. Do not remove journals or treat an uncertain response as a successful rollback. Recovery copies remain pinned, including after a failed attempt. Their absent-target list describes newly created files; the normal restore path remains replace-only and does not delete these automatically.

## Limits and retention

| Resource | Default |
| --- | --- |
| Selected data / compressed archive / individual file | 32 GiB / 16 GiB / 8 GiB |
| Files / manifest / saved preview | 32,768 / 16 MiB / 32 MiB |
| Minimum free space per affected filesystem | 4 GiB (minimum configurable 64 MiB) |
| Completed backups | 7, 30 days, 64 GiB |
| Total catalog, including staging and recovery | 160 GiB |
| Pinned recovery copies | 5, 64 GiB total |
| Outstanding jobs | 16 per server; 1 active per server, 2 per node |
| Node job history | 128 per server; active/uncertain/unpublished entries protected |
| Saved previews | 16; 64 MiB aggregate; 24 hours |

Configurable numeric policy bounds have finite limits of at most four times their defaults. Free-space checks budget archive/recovery/staging and transaction copies, combining reservations on the same filesystem. Streams enforce archive and disk bounds; independent disk consumers can still cause a clean failure. An archive larger than the retention byte budget is rejected before publication, preserving all previous backups. Retention runs only after publishing a new backup, protects that backup regardless of timestamp order, and respects preview pins. A failed creation never prunes prior backups to make space. If protected backups prevent meeting the budget, creation returns an explicit retention error and keeps the published backup.

Recovery is never removed by age or generic cleanup. While the managed server is stopped, explicitly release and reclaim a completed recovery copy:

```sh
s2node backup-recovery-release --server SERVER_ID --id RECOVERY_ID --confirm ARCHIVE_DIGEST --request release-recovery-1
```

Active/uncertain recovery and recovery referenced by a saved preview cannot be released. The helper's finite operation-receipt catalog allows 512 entries, including restore receipts/lock metadata. At capacity it refuses new work; preserve the catalog and inspect it with the operator before any offline migration. It never silently forgets an attempted restore to regain space.

Recognized, scoped orphan stages can be reclaimed by the backup manager under exclusion. Unknown files or incomplete ownership evidence require operator inspection. Generic cleanup does not touch this catalog.

## Notifications and validation

Add `backup` and `restore` to the configured node webhook event list to receive bounded operation outcomes with the affected server ID. No archive contents or database/config values are included. Delivery retries publish the saved outcome; they never repeat the backup or restore. An empty webhook URL contacts nothing.

Fixture tests cover archive selection, private permissions, changing inputs, stopped leases, preview revalidation, recovery retention, rollback, interrupted jobs and private admin routing. The isolated CI fixture also exercises separate daemon/runtime UIDs. These checks do not establish live panel/CS2 acceptance; test a backup and restore on a disposable server before operational use.
