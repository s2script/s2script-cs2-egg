# Optional node core

The Linux x86-64 `s2node` daemon is optional. The ordinary Pterodactyl or Pelican egg starts without it. The core provides socket-bound identity, typed capability negotiation, runtime status reports, optional-module lifecycle, and durable webhook ownership. Shared game generations, workshop management, restart execution and later administrative modules are separate milestones; this core advertises those capabilities only when an explicitly registered implementation provides them. No Docker socket or Wings administrator credential enters the game container.

## Build, stage and configure

Run `make build` to build `bin/s2egg` and `bin/s2node` from the same licensed source revision. `s2node install --directory /absolute/staging/path` creates a reviewable binary and the included service unit. It does not create users, change a host service, enable systemd, mount volumes, or change a firewall. Review the staged files before copying the binary to `/usr/local/bin/s2node` and unit to the host systemd unit directory. Keep the source and license available alongside distributed binaries.

Copy [examples/node.json](../examples/node.json) to a private `/etc/s2node/node.json`, replacing the node ID, panel server IDs, absolute volume paths, actual host-visible container UIDs, access GIDs and allocated IP/ports. The default administrator socket is `/run/s2node/admin.sock`; tests and custom installations may set another absolute private path. Configuration rejects unknown fields, overlapping server volumes, overlapping node-private paths, duplicate IDs/allocations, symlinked volume/egg directories and invalid app-independent allocation data. Node/game protocol preparation is fixed to CS2 app 730. Volume and `egg` directories must already exist.

Provision a dedicated unprivileged `s2node` account. Give that account only traversal access to each configured volume and write access to its `egg` directory, using a dedicated group or a narrow directory ACL. Put both the daemon and relevant container process in the configured `access_gid` group. A group-writable, setgid `egg` directory is useful, but its parent traversal permissions must also permit the daemon. The daemon must itself belong to every configured socket group; it cannot assign arbitrary groups. Avoid broad writable access to all server files.

Review [deploy/s2node.service](../deploy/s2node.service). It runs as `s2node`, with no Linux capabilities or privilege escalation. Add an operator drop-in containing the required `SupplementaryGroups=` and `ReadWritePaths=` entries for the registered **egg directories only**. `ProtectHome=yes` intentionally blocks volumes under home directories; relocating volumes or narrowly reviewing a deployment-specific sandbox exception is an operator decision. Systemd creates private `/run/s2node` and `/var/lib/s2node` directories. No privileged optional module is bundled. Future privileged helpers require their own documented minimal capabilities; in-process modules are trusted code, not a security sandbox.

Before enabling the reviewed unit, run as its service identity:

```
s2node config-check --config /etc/s2node/node.json
s2node doctor --config /etc/s2node/node.json
s2node run --config /etc/s2node/node.json
```

`config-check` validates configuration, path resolution and group membership without creating sockets or state. `doctor` explains required path permissions and reports whether existing private runtime files are accessible to its own process. Neither proves the eventual service sandbox permits writes, assesses a game's stopped state, modifies permissions or acquires runtime locks. `s2node status --config ...` reads the private administrator socket, reporting server snapshots and module health. Only the daemon UID and root can use that socket (0600, private parent directory).

## Identity and access contract

Each registered volume exposes `egg/s2node.sock`, owned by the daemon with mode 0660 and configured access GID. Directory descriptors are pinned without following symlinks; no container-provided path selects a host volume. The server record associated with the accepted listener is authoritative. Linux SO_PEERCRED checks the expected host-visible peer UID as an additional gate. Several containers may have the same UID: UID alone never selects their identity.

This relies on Wings/container mount confinement: a container must only see its own volume socket. A same-UID host process that can traverse every volume is not isolated by peer credentials. Do not expose the administrator socket or another server's socket to a container. User-namespace remapping must use the actual host-visible UID in configuration.

Hello returns the authoritative configured server ID. `s2egg run` uses it for that active run, overriding a differing `SERVER_ID` or saved standalone `egg/server-id`; the saved file is unchanged. Returning to standalone restores that identity. Node ingestion also binds ServerID/NodeID to its listener/configuration and preserves Event.ID, RunID, CorrelationID and timestamps. Payload IDs cannot claim another server. Event IDs reused with changed content are rejected. Status/action requests accept no server selector or arbitrary path on server sockets. Administrator status is read-only in this milestone.

The existing `.runtime-lease`, `.artifact-lock` and `.s2egg-status.sock` remain private (0600). Supplementary groups do **not** bypass that restriction for a separate daemon UID. Task8 Host.Status uses runtime protocol reports and events, not those private files. Its initial state is `unknown`; a lost connection gives `lost_contact`, clears readiness and player knowledge, and never fabricates a confirmed game exit. These reports are observations, not a stopped-state proof for filesystem mutations. An inaccessible lease/status socket must remain unavailable/unknown. Future administrative attach/backup/restore needs cooperative caller-owned execution or a separately reviewed narrow access/ACL contract. Never infer stopped from EACCES or missing PID files.

## Preparation and downstream interfaces

`server.Prepare(ctx, cfg, server.Options{GameProvider: provider})` and `server.Run(ctx, cfg, sink, stdin, stdout, stderr, options)` own the runtime lease before artifact lock/recovery. They call `GameProvider.Prepare(ctx, cfg) (handled bool, err error)` only at the stopped Steam preparation step. The provider does not recursively call Prepare or reacquire either lock. Nil or an explicit `(false, nil)` preserves standalone behavior. Any error aborts, including an uncertain response with handled false. A true claim never falls through to standalone SteamCMD; successful materialization must supply a valid local installed app manifest. Framework/plugin reconciliation retains the existing ordering and transaction.

The node `game.prepare` operation may publish/reserve an immutable generation; it must not mutate a server volume or treat a held runtime lease as evidence the server is stopped. Task9 supplies a compatible local materializer under the caller's locks and durable reservation/reference commit/recovery. A reservation survives caller failure until explicit safe release; old generations remain while any runtime references them. Administrative Attach is a separate stopped operation that acquires its own lease using the reviewed access contract. A shared tree is never sent to standalone SteamCMD. The Task8 client fails closed if an external preparation module advertises a capability for which no local materializer exists.

`node.Module` exposes Name, Start(context, Host), Health and Close(context). Start/Close/handlers must honor context cancellation; Health and capability enumeration must return promptly. Start errors or recovered panics mark only that module failed; successful modules and core sockets remain available. `Core.AddModule(module, grants)` registers explicit capabilities before Run. Module Host returns copied Server records and status, publishes run-associated events only with an events grant, and dispatches only granted actions. Server records contain ID, absolute volume, socket and game allocation. In-process modules remain trusted and cannot be forcibly stopped if they violate cancellation.

Task9: implement `game.prepare` reservation plus local GameProvider materialization/reference lifecycle. Task10: implement typed `cache` and `workshop` handlers with bounded content identifiers, never client paths. Task11: implement `restart.schedule` with immediate, when_empty, or maintenance_window policy and a required maximum delay; unknown player counts must not count as empty. Module effects and scheduling are not implemented by the transport itself.

## Wire protocol and limits

Protocol v1 is a 4-byte big-endian length followed by a JSON Message containing Version, ID, Type and Payload. Frames are limited to 1 MiB before allocation. Unknown versions and invalid lengths close the connection; typed payloads reject unknown fields. Hello negotiates capabilities before other requests. Each socket allows at most 32 simultaneous sessions; config allows at most 256 servers. Reads, handlers and writes have bounded 5-second request/idle windows. Module callbacks must honor the context. Idle sessions reconnect; the client retries only idempotent claim/event/status operations once with the same run/event identity. Mutating optional-module actions are never blindly retried.

The typed client exposes Status, ReportStatus, ClaimRun, PublishEvent, PrepareGame, Cache, Workshop and ScheduleRestart only when negotiated. A session has no generic arbitrary host-command method. Readiness, map, versions, intended stop and known/unknown players propagate in periodic runtime-status events. The first version check and peer gate occur before any optional handler can act.

## Durable ownership and delivery

One route belongs to one runtime RunID. An initially absent daemon or missing events capability selects standalone for that run. Otherwise the daemon durably records `node` or `standalone` before returning the claim. A configured acknowledgement-capable host transport can own delivery; a generic events.Sink.Publish cannot. A lost claim response never activates a fallback sender. Existing node ownership survives reconnect/restart, even if its delivery implementation later becomes unavailable. There is no automatic mid-run handoff. Standalone-owned events can still be observed by the node without a second webhook sender.

The host's configured webhook is independent of the container webhook; no webhook URL/token is sent through protocol events. CLI uses the standalone dispatcher only when that run's negotiated owner is standalone. The explicit transport capability includes `Enabled()`; a disabled endpoint cannot claim delivery. The node uses `alerts.AcknowledgedDelivery.Accepts` to filter unselected traffic before persistent admission, and `Deliver` to obtain explicit `delivered`, `filtered` or failed/uncertain results. `Dispatcher.Publish` remains a nonblocking in-memory queue, never a delivery receipt. Dispatcher formatting redacts its configuration for %v, %+v and %#v.

Selected events and ownership are written to a private atomic snapshot, with file and containing-directory fsync before ACK. An exclusive host-state lock prevents multiple delivery workers using the same state. Remote success/explicit filtering replaces the event payload with a digest tombstone. Failure retains it for a later attempt. Delivery retries use the same event identity. Discord may have accepted a message whose response was lost, or the daemon may crash after remote success before recording its receipt: delivery is at least once under those uncertainties, not exactly once. Unacknowledged runtime raw events remain best effort and can be dropped by the entrypoint's bounded queue or final drain.

| Resource | Maximum/default limit |
| --- | --- |
| One persisted event | 16 KiB |
| Pending selected events | 1024 |
| Event records, including receipt tombstones | 8192 |
| Per-server/run ownership records | 4096 total per node |
| Durable snapshot | 32 MiB, plus at most one 32 MiB replacement file |
| Delivery attempt context | 30 seconds; serial worker |
| Failed-delivery scan | Once per second, also on admission |

Lower pending/record/run quotas can be configured; maxima cannot be raised. Unselected console/status traffic creates no event tombstones and cannot consume selected-event capacity. At quota or storage failure, admission fails without ACK; current ownership is retained and never evicted. A storage commit uncertainty requires daemon restart before further admissions. Tombstones and owners are not automatically deleted. Safe retention/archival is a Task12 interface: operators must establish no running client/retry references archived state before any offline migration. Deleting delivery state while clients still run can destroy ownership guarantees. A filled queue needs operator attention; the core does not silently discard acknowledged records.

## Verification scope

Tests use temporary Unix sockets, local files, fake game launchers and an in-process HTTP fixture. They cover forged identity/path attempts, expected peer UID, socket modes, unknown protocol, oversize frames, deadlines, module failure, lost contact, ownership ambiguity/reconnect, durable restart/dedup/quotas, filtered floods, real CLI identity migration, provider lock ownership/no-fallback and explicit transport receipts. They do not establish live Wings/Pelican, CS2/native/HUD, actual Discord, host systemd or firewall acceptance. Do not deploy by changing this development host's services or permissions to make tests pass.
