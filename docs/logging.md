# Logs and crash artifacts

Set `observability.enabled` in the node configuration to enable node-owned logs and text crash bundles beneath `state_dir/observability/SERVER_ID`. Standalone `s2egg` accepts the same `observability` object in server JSON and writes beneath `egg/observability/SERVER_ID` as the runtime UID. Neither is enabled by default. See [node example](../examples/node.json).

Known GSLT and configured webhook secrets are redacted by one canonical redactor before runtime display/events and persisted text. It handles secrets split across writes and overlapping secret prefixes. Configuration permits at most 128 known secrets, 16 KiB each and 64 KiB combined; invalid redaction settings withhold content. This is known-secret replacement, not a guarantee that arbitrary unknown credentials are recognized. Binary dumps are never described as redacted.

Console lines are bounded at 64 KiB; oversized lines are discarded in full. Runtime parsing and redacted raw events precede display filtering. `observability.display_exclude` accepts up to 32 literal substring filters, each 256 bytes, affecting displayed console only. It does not suppress observation, logging or lifecycle facts. A bounded serial event queue keeps disk/network work out of process I/O.

Defaults per server are 16 MiB per log, six files including the active log, a 256 KiB in-memory tail, and 20 text crash bundles of at most 2 MiB each. This is at most 136 MiB of retained text per server, plus catalogs, staging during rotation/capture, cleanup plans and operational job/outcome state. Configurable ceilings are 64 MiB × 24 logs, 1 MiB tail and 8 MiB × 80 bundles. Files are private (0600) within private directories (0700). Active logs are never cleanup candidates; a restarted active log is sealed before another is opened. Lowering bounds below an existing active file fails recovery rather than silently truncating it.

Only `server.crash` with `unexpected_exit` captures a crash bundle. Intentional stop and startup failure do not. Bundles contain an allowlist of component versions, run/server identity, capture time and bounded redacted tail. A terminal event may overtake queued console facts; both local and node tails are explicitly labeled **bounded best effort**, not complete crash transcripts. Dropped observations degrade module health.

All node commands use the private administrator socket:

```sh
s2node status --config /etc/s2node/node.json --json
s2node top --config /etc/s2node/node.json --once
s2node logs --config /etc/s2node/node.json --server SERVER_ID
s2node logs-list --config /etc/s2node/node.json --server SERVER_ID
s2node logs-read --config /etc/s2node/node.json --server SERVER_ID --id ARTIFACT_ID --offset 0
s2node crashes --config /etc/s2node/node.json --server SERVER_ID
s2node crash-read --config /etc/s2node/node.json --server SERVER_ID --id ARTIFACT_ID --offset 0
```

Lists are paged (default/max 128). CLI read commands decode and write the requested text bytes in chunks up to 64 KiB; increment `--offset` for the next chunk. The private wire reply encodes its byte field as JSON/base64 and also carries size and ID. `logs` returns the in-memory tail. Node text reads do not need access to private runtime files and work while games run. `--private` selects the configured actual-UID helper for standalone sealed logs/crashes; that helper needs the fixed standalone `server_id`. Missing helper authority fails closed.

## Sensitive binary dumps

Dump retention is off by default and is separate from text crash capture. The operator may enable `operations.dumps` in a root-owned helper configuration. [The operations-only example](../examples/operations-helper.json) uses a pre-provisioned 0700 runtime-UID-owned root outside the game volume and a fixed contained source directory. No sharing store is required. Existing helper peer credentials, root-owned configuration/ancestor checks and actual runtime→artifact locking remain enforced.

Defaults: 512 MiB per file, 2 GiB total and four retained files; validated maxima are 2 GiB per file, 8 GiB total and 16 files. Retention requires a confirmed stopped runtime, regular owned single-link source and unchanged content/identity. Oversized files are skipped with metadata, never truncated into purported valid dumps. Interrupted copies and unpublished files count toward quota; they require inspected recovery and are not age-deleted. Repeating retention of an already sealed content digest returns its original artifact ID, including at quota.

```sh
s2node dump-retain --config /etc/s2node/node.json --server SERVER_ID --id core.123 --request capture-123
s2node operations-job --config /etc/s2node/node.json --server SERVER_ID --id JOB_ID
s2node dumps --config /etc/s2node/node.json --server SERVER_ID
s2node dump-export --config /etc/s2node/node.json --server SERVER_ID --id ARTIFACT_ID --output /private/local/core.dmp
```

Job acceptance is not completion. Source names are single filenames inside the configured dump source, never arbitrary paths. Export creates an exclusive local 0600 file, checks artifact identity/size across bounded chunks and verifies the complete SHA-256 digest. It never prints binary bytes, attaches them to a routine bundle, or uploads them remotely. The source dump remains in place. Treat exports as sensitive. Filesystem cleanup uses [sealed plans](cleanup.md).

## Observation and operational events

Modules subscribe through `Host.Subscribe(capacity)` (1–1024 items, max 16 subscribers). `Observation` contains an immutable copied registered server descriptor, redacted event, host receive time, source, session ID and sequence. `container_report` means an authenticated container claim; it is not engine attestation. `local_runtime` and `host_module` are chosen by their ingress adapters. Event details cannot supply authority or allocation. Delivery is nonblocking with an explicit dropped counter.

Selected runtime events use exact durable replay evidence. Unselected events use a 2048-entry process-local window: `first_seen_window` means only absent from that window, and `window_duplicate` is not permanent deduplication. Restart creates a fresh session. Consumers must enforce freshness and session policy. Observation precedes webhook selection and capacity checks; malformed or conflicting identities are rejected. Completed host events can return exact duplicate receipts without reconstructing an observation payload.

Host modules with the internal operational-event grant publish persisted `events.OperationalEvent` inputs through their bound `Host.PublishOperational`. Producer comes from registration; only configured server IDs are valid, with node scope reserved for `selfupdate`/`node.update`. Containers and runtime `Core.Publish` reject host fields/schema 2. Persist original terminal input and `At` before publishing; retries must reuse them. Same action/event identity plus identical canonical input returns the original receipt; conflicting scope/time/payload fails. Receipt `acknowledged` means durable admission, `duplicate` an earlier durable admission, and `filtered` no delivery selected. None means remote receipt or action execution.

Dump requests now require a stable `request_id` all the way to the helper. A private helper journal (at most 1024 directory entries including its lock) binds the request's canonical input, original source inode/device/size/timestamps, prepared digest and original result. An interrupted capture can recover its exact sealed digest receipt; a repeated request cannot substitute a replacement source. Journal evidence is protected from ordinary cleanup and is not age-evicted. New request IDs are required for deliberate new captures. The closed `dump-result` and `cleanup-result` helper operations query recovery evidence without rerunning mutation. Operations helpers must be upgraded with the node for this additive request/confirmation protocol; unsupported old helpers fail closed.

Known secrets in externally exposed runtime/host identifiers are refused before admission or persistence. Exact accepted internal replay keys remain unchanged. Historic status identifiers are sanitized for presentation, and secret-bearing admin job identifiers are refused before job/outcome persistence. This supplements text-field redaction without weakening replay identity.
