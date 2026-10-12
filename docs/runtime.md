# Runtime and image contract

`config.LoadServer` supplies a validated absolute-root configuration to `server.Prepare` or `server.Run`. The commands `s2egg prepare`, `s2egg run`, and `s2egg status` accept `--root PATH`. The process implementation is Linux-specific. The source-built runtime image and bounded Discord sink are supplied; subprocess tests do not establish actual CS2, native-framework, HUD-client, or panel acceptance.

## Preparation and lock ownership

`Run` acquires `runtimelease.Acquire(root)` before artifact recovery/preparation and retains it through game exit, process-group cleanup, and reader cleanup. `Prepare` takes that lease for the stopped preparation operation only. Both acquire the artifact lock second, recover interrupted transactions, prepare Steam/app 730, discover the installed list, resolve bundled exact pins, prepare the selected frameworks and plugins, then commit framework changes and plugin changes in **one** `artifact.Apply`. Plugin changes already include their inventory lock. Disabled placement is chosen before this commit; native bundled archive positions are never applied directly. Staging is cleaned on success and failure.

Only a typed temporary framework download outage permits one retry against `framework.Retained` with the same configuration and bundled pins. Retained status/warnings are visible in stderr and raw events. Integrity, policy, compatibility, malformed data, cancellation, and unavailable retained evidence fail closed.

`server.ReconcileLocked(ctx, cfg, request, log)` is the stopped CLI callback boundary. Its caller holds runtime lease then artifact lock and has already called `Recover`. It stages and commits the combined native/plugin update without running SteamCMD or reacquiring either lock. It requires an installed `game/csgo/gameinfo.gi`. Policy-only CLI edits can still save next-start choices while the runtime lease is held; artifact moves/removals cannot.

A future node provider must preserve this ordering. It must not recursively call `Prepare` while the same runtime lease is held. Introduce an explicitly owned preparation/provider integration at that point; this task does not provide node operations or a remote protocol.

## SteamCMD and empty installations

The image provides these inspected installer files as an immutable readable seed under `/opt/s2egg/steamcmd`:

- `steamcmd.sh`
- `linux32/steamcmd`
- `linux32/steamerrorreporter`
- `linux32/libstdc++.so.6`
- `linux32/crashhandler.so`

The official installer URL is `https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz`. The archive inspected on 2026-10-12 has SHA-256 `cebf0046bfd08cf45da6bc094ae47aa39ebf4155e5ede41373b579b8f1071e7c`. That is an observed integrity pin, not a publisher signature. The Dockerfile downloads, checks and extracts this archive without executing it during build. The image must supply `/usr/bin/env bash` and the loader/libraries for the 32-bit SteamCMD bootstrap as well as the selected amd64 CS2 runtime. No bootstrap/native binary was executed during development.

Runtime copies missing seed files safely into persistent `<root>/steamcmd`, preserving existing initialized files. It executes the absolute `<root>/steamcmd/steamcmd.sh`, with **HOME=<root>** and cwd `<root>/steamcmd`. If generated `linux64/steamclient.so` is missing, it first runs `+quit`. App updates use `+force_install_dir <absolute-root> +login anonymous +app_update 730 [validate] +quit` as separate arguments. SteamCMD output is bounded internally; credentials from server configuration are not sent to it. Standalone `s2egg prepare` converts SIGINT/SIGTERM into context cancellation and retains both locks until SteamCMD cleanup completes. The Steam adapter itself enables Linux child-subreaper mode before spawning, then kills and reaps only its owned group after waiting its direct child. Direct callers do not need to enable subreaping themselves; they must provide a cancellable context for their own shutdown lifecycle.

After initialization/update, SDK64 must exist and is copied to `<root>/.steam/sdk64/steamclient.so`. Generated SDK32 is copied to `<root>/.steam/sdk32/steamclient.so` when available; SDK32 absence is not treated as a demonstrated CS2 runtime failure. Missing SDK64 gives an actionable failure. An initialized update-disabled install reads its app manifest and copies SDK libraries without launching SteamCMD or networking. An incomplete install can still require initialization even when app updates are disabled. Bootstrap and SDK copies are confined to the server root and reject nonregular file targets. Persistent `steamcmd`, `.steam`, app files, plugin policy/inventory and `egg` state must survive container replacement.

## Launch, console and shutdown

Runtime invokes Valve's installed `<root>/game/cs2.sh` directly with an argument vector and cwd `<root>/game`, HOME `<root>`. The wrapper owns CS2's library/runtime setup. There is no constructed shell command or guessed `LD_LIBRARY_PATH`. It receives dedicated/console/usercon, configured IP/port/player limit, game type/mode/map, literal custom arguments and the configured GSLT. GSLT and webhook environment variables are removed from the child environment. Inherited GAME_DEBUGGER/GDB_DEBUG_PORT are also removed. The validated default-off launch.debug_port (GDB_DEBUG_PORT) can set the fixed GAME_DEBUGGER wrapper only when gdbserver exists; readiness still waits for A2S and expires normally while waiting for debugger attachment. See [debug allocation/access guidance](install.md#opt-in-remote-debugging).

Case-insensitive custom `-ip`, `+ip`, `-port`, `+port`, `-hostport`, `+hostport`, `-sv_setsteamaccount`, and `+sv_setsteamaccount`, including `=value` forms, are rejected before preparation. Other custom arguments remain literal; this does not restrict legitimate `exec`/configuration use. Operator commands/configuration can still change engine behavior, so local-query association is evidence rather than protection against arbitrary operator changes.

`quit`/`exit` from stdin records `console_stop`. SIGINT/SIGTERM are handled during preparation and runtime; game signals reach the entire process group. Context cancellation has its own reason. A stop waits at most 10 seconds before SIGKILL; launcher exit also kills remaining group members. The supervisor enables Linux child-subreaper mode (a process-wide setting) and reaps only children in its owned PGID after the direct child's `cmd.Wait`. Children that deliberately leave the process group are outside this contract. As with any userspace supervisor, an uninterruptible kernel task cannot be forced to exit on a userspace deadline.

File stdin is independently reopened nonblocking through `/proc/self/fd`, without closing/changing the caller descriptor. Finite `strings.Reader`, `bytes.Reader`, and `bytes.Buffer` inputs are supported. Other `io.ReadCloser` inputs transfer close ownership to `Run` and must unblock `Read` on `Close`. Other arbitrary readers are rejected before preparation because a blocking `Read` cannot be canceled safely. Console lines are bounded at 64 KiB; oversized lines become an explicit diagnostic. Child output is redacted across write boundaries. Raw redacted console events are built before display processing; a child-printed reserved readiness marker is suppressed in display output.

## Readiness and live status

Before spawn, binding the configured UDP allocation must succeed and it is then released to the child. Probes target that allocation; unspecified bind addresses use the corresponding loopback address. A bounded A2S_INFO probe accepts direct responses or one four-byte challenge. It requires the supported protocol/layout, app 730, a nonempty loaded map and a Linux dedicated-server response. Optional fields are fully bounded and GameID must agree. Unsupported split packets, truncation, malformed or conflicting fields, unknown layouts, and failed/stale queries never report zero players as known.

The first successful probe emits exactly one `server.up` and the exact panel marker `S2EGG_READY`. Banner text does not establish readiness. Subsequent failures mark live readiness `unresponsive` and players unknown; successful recovery does not emit a second up. The query does **not** prove Metamod/S2Script/plugin loading or client HUD behavior. Actual `meta list`, plugin behavior and client evidence remain release acceptance requirements.

`server.Snapshot(ctx, root)` checks the live runtime lease and queries a mode-0600 Unix socket `.s2egg-status.sock`; it never trusts a PID file or saved status. The snapshot includes RunID, state, readiness, last map, explicitly known/unknown players, versions and intended stop reason. It fails if the owner cannot answer. `egg/server-id` persists a standalone random server identity when no `SERVER_ID` is supplied. RunID changes on every managed run; NodeID is empty until future node integration.

## Raw event and webhook sink contract

`events.Event` schema version 1 carries `ID`, `Type`, `Severity`, `ServerID`, `NodeID`, `RunID`, `CorrelationID`, UTC `At`, `Reason`, and string `Details`. CorrelationID is the RunID for lifecycle events. IDs are random per event. Secrets are redacted in console, status, event details and runtime errors; raw registry metadata is never forwarded.

`events.Sink.Publish(context.Context, Event) error` receives serial raw events from one asynchronous worker, with a two-second per-call context. A bounded 128-entry raw queue drops overflow without blocking process I/O; a separate 16-entry lifecycle queue reserves capacity for up/down/crash/start-failure events and is serviced first. Shutdown has one absolute two-second final-drain budget, then cancels the in-flight call and drops remaining events. Dropped/failed counts are reported without event content or credentials. There is no retry or goroutine per event in this layer; lifecycle handoff is at most once, and cancellation/failing sinks can still prevent delivery.

A sink **must honor context cancellation**; Go cannot force an arbitrary noncompliant `Publish` implementation to return. The Discord sink enqueues quickly, owns its separate bounded network retry/deduplication policy, and performs optional filtering after receiving raw events. The CLI wires the bounded dispatcher into `server.Run` and closes it on every return with a separate absolute two-second deadline. See [webhooks.md](webhooks.md) for selection, retry, deduplication and node ownership. The raw dispatch worker does not replace webhook delivery machinery.

| Event | Behavior |
| --- | --- |
| `server.up` | Once after first successful query; reason `ready` |
| `server.down` | Once after a previously ready process/group exits; includes exit code and intended/unexpected reason |
| `server.crash` | Additional diagnostic after an unexpected exit of a previously ready process |
| `server.start_failed` | Failure before first readiness, without an earlier up/down |
| `server.console` | Redacted raw line and stdout/stderr identity |
| `server.status` | Query result, with explicit `players_known` |
| `server.warning` | Retained generation and framework warnings |
| `server.plugin_check` | Bounded name/status/available-version outcome, without raw metadata |

The default webhook selection now uses `server.start_failed`. Legacy `startup.failure` remains accepted configuration; the dispatcher normalizes it to `server.start_failed` and deduplicates selection so one event is not delivered twice. Standalone delivery cannot be guaranteed after SIGKILL, host failure or power loss. The packaged source milestone includes webhook delivery and both runtime/debug build targets; actual image and native acceptance evidence remains separate.
