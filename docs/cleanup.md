# Sealed cleanup and exact delivery archives

Ordinary cleanup is restricted to manager-created `logs`, `crashes` and `dumps` namespaces. It never walks arbitrary volume paths. Each sealed entry has a random managed ID and pinned device, inode, mode, size, mtime and ctime. Unsealed/active files, symlinks, hardlinks, changed entries and foreign-owned files cannot be deleted by path or age alone. Rules use flat relative filename patterns; nested/escaping patterns are refused. Configuration, data, control files, shared live generations, backup/catalog/recovery namespaces and delivery ownership archives are excluded. Shared generations remain under shared reference-aware pruning; the backup module owns its separate retention.

```sh
s2node cleanup-plan --config /etc/s2node/node.json --server SERVER_ID --namespace logs --retain 3 --age-seconds 86400
s2node cleanup-apply --config /etc/s2node/node.json --server SERVER_ID --namespace logs --id PLAN_ID --request cleanup-2026-10-12
s2node operations-job --config /etc/s2node/node.json --server SERVER_ID --id JOB_ID
```

Preview persists the exact identities for review. Application revalidates under the namespace manager lock, uses descriptor-safe operations, and returns removed/changed/skipped evidence. Repeating an applied plan returns its saved result. Both age and retained-count conditions apply. Private standalone artifacts or dumps require `--private` and the configured actual-UID helper; destructive application holds its real stopped-runtime lease and artifact lock. Node-owned sealed logs can be pruned while the game runs. A mere down/disconnect/status report never supplies stopped authority.

Plans are bounded to 128 directory entries (including the lock), catalog entries to 4096, jobs to the durable pool's 1024 records/128 queued, and persisted operational outcomes to 1024. Completed outcome/plan evidence is retained, not silently forgotten. These finite quotas require explicit operator recovery/maintenance at exhaustion; this version does not offer unsafe automatic historical deletion. Storage failures leave work unacknowledged/degraded. Interrupted unsealed files require inspection rather than age cleanup. Node text bounds, metadata/staging space and separate sensitive dump limits must all be included in filesystem capacity planning; `delivery-status` reports actual delivery disk usage including migration/orphan files. Binary retention counts all staged/unpublished dump files against its byte/file quotas.

## Archival is not deletion

```sh
s2node delivery-status --config /etc/s2node/node.json
s2node delivery-archive --config /etc/s2node/node.json
```

Runtime hot defaults remain 1024 pending, 8192 event/tombstone records and 4096 owners. Host operational records have independent bounds of 256 pending and 2048 hot records and never consume runtime owner quota. A common worker alternates runtime and host entries. Pending delivery stays hot. Explicit archival moves exact completed key/digest/outcome/request evidence and original owners/fences to immutable cold segments; it does not require finalization. New events for an archived unfinalized run retain the original owner and enter hot state.

Cold storage is finite: at most 64 segments, 256 MiB aggregate, 32 MiB per segment. Files are fsynced before atomic manifest publication/hot removal. Unpublished segments after a crash count toward quota and are reported; they are not automatically deleted. Every claim/admission validates referenced segments before treating an identity as unknown. Missing/corrupt evidence fails closed even for a hot lookup. Never delete state/archive files to regain quota or elect another sender. There is no TTL, cold compactor, or promise of unlimited historical reclamation. At cold exhaustion, existing evidence remains authoritative and new admissions eventually stop when hot capacity fills. Plan storage/maintenance accordingly.

## Explicit stopped finalization

```sh
s2node delivery-finalize --config /etc/s2node/node.json --server SERVER_ID --run EXACT_RUN_ID
```

This requires a configured operations helper and an actual stopped-runtime lease, held together with the artifact lock during commit. Core captures effective hot+cold owner/revision before asking the helper; intervening claims/events/status activity invalidate eligibility. A permanent fence retains original ownership and exact prior replay acknowledgements while rejecting novel events for that finalized run. Late activity is therefore handled explicitly, not guessed from terminal status or connection loss. Finalization is not required to archive; it is not automatic supervisor behavior.

## Queue compatibility

Runtime protocol/envelope schema 1 remains byte-compatible; host operational envelopes use schema 2. Durable queue format 2 is a separate version. For an existing format-1 state, stop the daemon and explicitly run:

```sh
s2node delivery-migrate --config /etc/s2node/node.json
```

The daemon lock is mandatory. Migration preserves old record keys/digests, owners and pending envelopes and writes an exact private fsynced `delivery-before-v2.json` before atomic format-2 replacement. A differing prior backup is refused. Read-only status/config inspection never migrates. Binaries supporting only delivery format 1 reject format 2. The migration copy is protected recovery material, not an ordinary cleanup target. The global release-state ABI begins at 1 encompassing these final formats; future update candidates must remain readable by the previous installed release and cannot perform automatic irreversible migration.

## Interrupted-operation recovery

A destructive operation reserves a durable outcome slot before execution and records an executing phase before acquiring its mutation authority. The reservation's `started_at` is action-start context, not completion time. Cleanup plans and helper dump receipts record their completion time; the module persists the complete terminal event, including its chosen `At`, before first publication. A failed terminal write is retried independently of generic job failure; unpublished in-memory results are never sent to a webhook first. After restart, recovery queries receipts only. Missing terminal evidence produces an explicit uncertain failure with the time uncertainty was established, without repeating deletion or reading a replacement dump source. `operations-job` reflects a recovered terminal outcome. Failure outcomes also enter this durable operational outbox.

Stopped finalization checks cancellation and helper readiness before commit entry. Once entered, caller cancellation cannot intentionally release the helper's lease; the node confirms the held session afterward. Unexpected helper loss after entry reports an actionable uncertain result and retains any committed fence and original owner. A socket protocol cannot make helper-process failure atomic with the node's filesystem commit. Never clear a fence or elect another sender to resolve uncertainty.
