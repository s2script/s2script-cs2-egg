# Integrations and future Discord companion

Server-up/down, crash, startup-failure, update and optional operational notifications use the existing [Discord webhook integration](webhooks.md). The future companion bot can consume those events and the narrow private control contract described here. Account linking, slash commands, a Discord gateway and a remote authorization service are not implemented yet.

## Events and delivery

[events.schema.json](../schemas/events.schema.json) covers the actual runtime v1 and host v2 envelopes. Both carry event ID, type, severity, UTC timestamp and correlation identity. Runtime events include server/run identity. Host events use `origin: host`, a registered producer, `action_id`, node identity and optional related runtime; `run_id` stays empty and correlation equals the action ID. Node-wide `node.update` events have an empty server ID. Event schema versions are independent of wire protocol version 1.

A claimed container event identifies an authenticated volume socket, not a host-verified engine fact. The node replaces configured server/node identity and rejects forged host envelopes. Do not interpret names, console lines, reported player counts or source timestamps as remote authorization. Guard delegation makes that trust decision explicit.

The runtime negotiates one delivery owner for each run. The daemon durably records ownership before acknowledging it; loss of an acknowledgement never starts a second sender. Durable node admission is not a Discord delivery receipt. Network uncertainty can produce repeated remote messages with the same identity; consumers should deduplicate by event ID and retain their own processing receipts. Unacknowledged bounded runtime traffic is best effort. Selected-event queues, ownership records and archives are finite; capacity failures preserve existing ownership and require operator attention.

Operational modules save the exact original event input, timestamp and action/event keys before publication. Notification retries only republish that frozen outcome; they do not rerun backup, restore, guard or update actions. Display filters do not remove raw console evidence before observers. Webhook selection and secret redaction remain independent of presentation.

## Private control contract

Use the administrator's Unix socket, normally `/run/s2node/admin.sock`. Its filesystem restrictions and Linux peer credentials permit the daemon UID or root, followed by version/capability negotiation. Never mount it into game containers, expose it through an unauthenticated HTTP proxy or give a Discord bot the privileged helper socket.

Frames use a four-byte big-endian length followed by a UTF-8 JSON `protocol.Message`: `version`, request `id`, `type` and typed `payload`. Maximum frame size is 1 MiB. Begin with `hello` and a requested capability list; only returned capabilities can be invoked. Optional modules may be disabled or unavailable. Replies preserve the request ID; errors are typed responses. Idle/request deadlines are bounded. See [node transport and ownership](node.md).

[actions.schema.json](../schemas/actions.schema.json) documents an adapter subset, not an authorization grant. The implementation remains authoritative for cross-field constraints, bounds and operation-specific policy.

| Capability | Scope and behavior |
| --- | --- |
| `status` | Private node status, module health and registered runtime observations. |
| `update.admin` | List, cancel or explicitly retry an existing host-admitted game-update job. `allow_start` is an explicit operator decision for an offline job. |
| `operations.admin` | Scoped log/crash catalog reads, bounded tails, job receipts and archive status; sensitive exports and destructive cleanup require their separate documented procedures. |
| `backup.admin` | Scoped catalog/status, create, preview and stopped restore jobs with exact confirmation. See [backups](backups.md). |
| `guard.admin` | Inspect, preview, temporary block/unblock and explicit reconciliation within root-authorized policy. See [guard](guard.md). |
| `project.update` | Check, stage, inspect or request configured node activation. No caller URL, executable path, channel override or service command. |

`restart.schedule` belongs to a server-volume session. It reports an existing pending update admitted by the host poller and cannot create arbitrary host restart jobs or override host policy. It is not available on the administrator socket. `update.execute` is restricted to the internal host dispatcher. Container sockets cannot negotiate any administrator capability listed above.

An adapter must map its own authenticated user permissions to explicit server scope and allowed typed operations. No remote identity or permission model is supplied by this project yet. An accepted job/request is distinct from completion: save returned IDs, inspect their receipts and do not blindly repeat uncertain mutations. Where supported, repeat the identical stable request ID rather than creating a new one. A future bot must not turn user text into console commands, shell commands, filesystem paths or helper operations.

## Compatibility

Release manifests describe compiled protocol/capability support; enabled modules and caller grants still determine actual availability. Independently installed volume helpers expose read-only discovery without acquiring stopped leases. Helpers and the optional guard binary remain operator-owned and are upgraded separately from the daemon. See [project updates](project-updates.md) for source provenance, state compatibility and rollback.

No webhook is contacted unless configured. Nothing here creates bot accounts, sends direct messages, opens public ports or broadens the private socket's authority.
