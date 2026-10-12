<!-- SPDX-License-Identifier: GPL-3.0-or-later -->
# CS2 updates and coordinated restarts

Startup preparation and live update polling are separate options. Startup continues to use `steam.update` / inverse `SRCDS_STOP_UPDATE` and the negotiated shared provider. Polling is opt-in. App 730 is the only supported application. Node credentials and helpers stay on the host; never mount a Docker socket, Wings bearer token or helper socket into a game container.

## Node setup

Enable the shared store as described in [shared-files.md](shared-files.md), then merge these fields into the host node configuration:

```json
{
  "updates": {
    "enabled": true,
    "poll_seconds": 300,
    "wings": {
      "endpoint": "https://wings.example.net:8080",
      "token": "HOST_ONLY_WINGS_BEARER_TOKEN"
    }
  },
  "servers": [{
    "id": "configured-server-uuid",
    "updates": {
      "enabled": true,
      "attach_mode": "",
      "policy": {
        "mode": "when_empty",
        "max_delay_seconds": 3600,
        "freshness_seconds": 15,
        "warning_seconds": [60, 30, 10, 0]
      }
    }
  }]
}
```

This is a merge fragment: retain the server's existing volume, peer UID, access GID, allocation and helper settings. Server IDs must equal Wings IDs. The HTTPS origin must have no path, query, userinfo or fragment. Standard certificate verification is the default. For an explicitly managed self-signed certificate, configure `certificate_sha256` as the 64 hexadecimal digits of its leaf DER certificate SHA256. A pin replaces PKI verification and must be rotated deliberately. Redirects are rejected. Config formatting and transport errors redact tokens.

Use empty `attach_mode` for symlink or mounted-copy startup. Set it to `hardlink` for hardlinks or `copy` for copy installations **without the shared readonly container mount**. Those modes also require the existing per-server `helper_socket` provisioning. Selecting the wrong attachment mode can leave startup pending or failed; the scheduler never falls back to standalone SteamCMD.

The node polls Steam metadata and builds immutable generations under the same build lock used by `shared-build`. Lock contention defers the next poll. A download cannot overlap another node metadata/build operation. Existing jobs continue to run while a new download occurs. The installed published build, rather than the earlier metadata response, identifies the target. Per-server target selections and separate durable scheduler pins keep preparation on its scheduled generation even if the global current generation advances. Scheduler pins cannot be retired by an unrelated stopped attachment; completion/cancel clears only the selection and pin matching its own generation. Repeated cancellation of a completed or canceled job is inert. Source references stay conservative; only Task9's stopped helper reconciliation can retire them.

## Policies and warnings

- `immediate` starts its countdown when a fresh running server is eligible, including occupied servers.
- `when_empty` requires a fresh, known zero-player A2S sample. A failed, old, future-dated or absent sample is not empty.
- `maintenance_window` requires `timezone` (IANA name), `window_start` and `window_end` (`HH:MM`), for example `America/Chicago`, `23:00`, `02:00`. Windows can span midnight; the ending minute is exclusive. Zone data is embedded in the binaries.

Maximum delay is 1–604800 seconds. Poll intervals are 10–86400 seconds. Freshness is 1–300 seconds (default 15); warnings contain at most 16 offsets, each0–3600 seconds. Defaults use `when_empty`, a 3600-second deadline and a zero-second announcement. A zero-second announcement is still a best-effort warning. Configure longer offsets for a visible countdown.

An absolute UTC deadline is fixed at admission. Empty/window eligibility starts a persisted countdown and is rechecked before action, including after stop arming. Losing eligibility resets the countdown, but warning offsets already attempted are not replayed. The deadline truncates a countdown and can override player occupancy or a window; it never authorizes an offline start, stale/lost runtime contact, or an operator veto. A current failed A2S probe is unknown-player evidence; it can reach the maximum-delay path while an old zero-player sample cannot remain fresh.

Warnings use a fixed numeric `say` command through Wings. HTTP204 means dispatch acceptance; Wings may itself fail an individual command. Jobs record attempted/accepted/failed-or-ambiguous warning outcomes, never claim player delivery, and do not duplicate the same offset after recovery. Persistent job keys/phases, not local clock labels, prevent a repeated DST hour from executing twice.

## Lifecycle and recovery

A job is keyed by registered server and immutable target generation; completed keys remain for deduplication. There is one active job per server. The bounded private snapshot retains up to 4096 jobs; reaching the limit is an actionable error rather than deleting idempotence history. Jobs retain original run/source, target pin, deadline, warning receipts, phase and action identity. Inspect jobs through the administrator socket:

```sh
s2node updates-list --config /etc/s2node/node.json
s2node updates-retry --config /etc/s2node/node.json --job JOB_ID
s2node updates-cancel --config /etc/s2node/node.json --job JOB_ID
# Explicitly authorizes starting an offline server; use only after inspection:
s2node updates-retry --config /etc/s2node/node.json --job JOB_ID --allow-start
```

These are host-administrator operations. Commands serialize with an in-flight action and cannot revoke a power request already accepted by Wings. The container protocol can report status and fetch its own bounded control/schedule status; it cannot select arbitrary target generations, power actions, job IDs to execute, server paths or host commands. Only the host action dispatcher executes persisted jobs.

Ordinary symlink/mounted-copy updates request Wings restart. Hardlink/no-mount-copy updates use a host-generated action ID, bound to the current run and expiring after 30 seconds. The runtime acknowledges the intent, then Wings sends the reserved `s2egg_scheduled_stop TOKEN` console opcode. The supervisor consumes it only if the exact unexpired token was acknowledged. Invalid reserved commands are discarded, never forwarded to CS2. A regular quit/exit, signal or canceled context vetoes automatic continuation. The scheduler requires matching `scheduled_update` terminal evidence before requesting final Wings stop/confirming offline, invoking stopped helper Attach, and requesting start. The helper independently acquires the real lifetime/artifact leases; a status report is never a lease.

HTTP202 is only acceptance. Restart, stop and start intent is saved before the request. Timeouts, refused requests, missed terminal evidence, daemon recovery during actions and unexplained offline state remain pending for inspection instead of replaying power requests. A new ready run must report the exact target generation **and** Steam build with fresh evidence before completion. Servers originally offline, deliberately stopped while waiting, or observed under a different run are held. Administrator offline retry explicitly grants a new start; cancel preserves conservative generation references.

The scheduler refreshes Wings state, runtime evidence, and elapsed time after blocking preparation and again immediately before lifecycle dispatch. Wings has no atomic observe-and-act API or actor-tagged power-operation history, so an operator stop after the final observation can still race a restart; a manual operation after a completed scheduler stop also cannot be attributed reliably. **Pause/cancel active update jobs before manually stopping or starting a server.** A concurrent operator action after the last observation cannot be perfectly attributed. Recovery never treats an old arm acknowledgement, accepted HTTP request or stale helper receipt as current stop authority. No live Wings/CS2 acceptance is implied by the local fake-service tests.

## Standalone polling

In `egg/server.json`, polling without an adapter reports `update_pending` and keeps the game running:

```json
{
  "updates": {
    "enabled": true,
    "poll_seconds": 300,
    "adapter": "",
    "policy": {
      "mode": "when_empty",
      "max_delay_seconds": 3600,
      "freshness_seconds": 15,
      "warning_seconds": [60, 10, 0]
    }
  }
}
```

Set `adapter` to `supervisor` to opt into the built-in lifecycle adapter. It is validated only when this supervisor owns the lifetime lease, signal/input handling and a nonshared installation with no node game provider. Unknown or unusable adapters remain pending-only. Shared installations use host coordination and never invoke this standalone poller. There is no external command adapter and no assumption that exiting the container causes a panel restart.

The persistent supervisor reads console input once, owns the lease across child runs, and restarts only after a private update request. It stops/reaps the old process group, runs ordinary preparation while stopped, launches a new child and waits for readiness. Metadata checks serialize with preparation. The stopped preparation manifest determines the actual target if Steam advances after polling; pending state clears only after that installed build is ready. Operator intent is latched permanently, including during preparation or between children; ordinary crashes/exits do not loop. Old child console queues are discarded. Input queues are bounded and can drop excess non-stop lines. Caller file descriptors stay open; owned nonfile readers close when the supervisor exits. A failed preparation stops the supervisor with an error; it never launches partly prepared game files.

Standalone pending/countdown state is scoped to the supervisor lifetime. Restarting the supervisor performs normal startup preparation; durable cross-daemon generation jobs are a node feature. No standalone poll installs into a shared tree.
