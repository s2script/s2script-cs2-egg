# Connection tracing

Connection tracing records a short, redacted timeline for one source IP so you
can compare a first connection that returns to the menu with a retry that joins.
It records evidence; it does not diagnose that symptom by itself or assume Guard
caused it.

## Enable on a test server

Use the panel's admin startup-variable settings for a development server running
this egg. Both egg variants include these admin-only controls:

| Variable | Default | Meaning |
|---|---|---|
| `ENABLE_CONNECTION_TRACE` | `0` | Set to `1` for the reproduction |
| `CONNECTION_TRACE_IP` | empty | One client IP **as CS2 sees it**, without port or CIDR |
| `CONNECTION_TRACE_SECONDS` | `600` | Capture window, 60–1800 seconds |

Restart the test server to apply the variables. Keep `CONSOLE_LOG_LEVEL=INFO` or
`DEBUG` so records are visible. The window starts just before launching CS2 and
automatically stops. Every restart renews the window while enabled, so reset
`ENABLE_CONNECTION_TRACE=0` after testing. Invalid options disable only tracing
with a warning; they never prevent the server from starting.

IPv4 and bracket-free IPv6 targets are supported. The target is kept in memory
and sent over the existing volume socket for optional cached network evidence;
the tracer does not print it. Administrators and processes able to read the
container environment can still see it.

Leave existing Guard and network settings unchanged. Tracing does not enable
`ENABLE_GUARD`, start a service/sniffer, send game commands, change convars, or
alter firewall rules. Standalone eggs still collect engine evidence.

## Compare the two attempts

1. Note UTC time, join method (server browser, direct `connect`, Steam invite),
   server address/port, and whether the client has the workshop content.
2. Connect once and note the client's exact behavior and time. Wait a few seconds,
   then retry within the same capture window.
3. Look for `[connection-trace]` in the panel console. The existing **30-second
   heartbeat** flushes buffered events; clean shutdown also flushes. Each JSON
   record's `time` is the observation time, not the later print time.
4. If log persistence is already configured, `cs2node logs <server>` locates the
   daily log. Existing consolelog retention applies. Otherwise copy the panel
   records before scrollback disappears. Tracing creates no separate file.
5. Group by `session`, then `attempt`, and compare stages and `elapsed_ms`.

Illustrative timeline (the implementation emits JSON):

| Attempt | Elapsed | Stage | Detail |
|---|---:|---|---|
| 1 | 0 ms | `transport_accepted` | Steam transport accepted |
| 1 | 100 ms | `c2s_connect` | CS2 logged an incoming connect request |
| 1 | 110 ms | `client_connected` | CS2 assigned a userid |
| 1 | 130 ms | `signon` | `CONNECTED` |
| 1 | 20,000 ms | `dropped` | `timeout`, last signon `CONNECTED` |
| 2 | 0 ms | `transport_accepted` | New transport/endpoint |
| 2 | 150 ms | `signon` | `SPAWN` |
| 2 | 180 ms | `joined` | `FULL` |

`FULL` means CS2 logged completion of signon, not that the client rendered a frame
or a plugin finished authentication. Transport acceptance and C2S can appear in
either order. Session IDs are random per capture, not hashes of an address.

Attempt identity uses the endpoint, then engine userid/name and transport ID.
Separate source ports behind the same NAT remain separate. New transport IDs,
new endpoints, userid replacement, or a terminal event distinguish attempts.
Repeated C2S on an unfinished endpoint stays in the same attempt: without another
boundary, retransmission and a quick manual retry are indistinguishable.

Name-only drops/forced reconnects carry `correlation=name_only`, which is weaker
than an endpoint/userid match. Duplicate observed names, including other source
IPs, produce an unassigned `ambiguous_disconnect`. If the bounded identity table
fills, name-only attribution stops conservatively for the rest of the window.
Clients connected before capture may still introduce unseen collisions. Signon
lines without a matching observed connection are ignored rather than guessed.
Names whose previous connection was replaced, forced to reconnect, expired, or evicted also stay
ambiguous for the rest of the window: a delayed drop could belong to that old
connection. Up to 32 such names are retained; overflow disables name-only
attribution for the remainder of the capture.

Two minutes without another matching engine event ends a pending observation as
`incomplete/observation_gap`. That is **not** proof of failure: quiet handshakes
and workshop downloads can take longer. Replacement, state eviction, shutdown,
and expiry likewise describe limits of observation. A crash can lose the current
unflushed batch. `events_lost` means the event buffer overflowed.

## Network and rejection evidence

If node Guard is already running, the heartbeat requests one entry from its
existing cached counters. The handler only reads memory: no new firewall handle,
netlink dump, rule, trusted-player entry or enforcement event. Older nodes ignore
the message. The initial `network_evidence_unavailable_until_sample` remains true
until a response arrives; after a node disconnect, previous samples are historical.

`network_sample` has no `attempt`: it spans **all protected game ports** for the
source IP, including queries and other clients behind that IP. `sampled_at` is
the node's sampling time; `time` is receipt at the egg. Counters are cumulative
within a kernel entry's lifetime, expire, and can reset. Compare changes only
across continuous fresh samples, and synchronize clocks across machines.

| Evidence | Interpretation |
|---|---|
| `counter_present` | UDP reached the node's accounting rule; does not prove CS2 acceptance or identify an attempt |
| `not_observed` | No entry in that sample; **not** proof no packet arrived |
| `unavailable`, or no response | Absent, stale, failed, unsupported or disconnected monitoring; no network conclusion |
| `host_block` | Existing target-IP block notice; does not prove this attempt hit the block |
| `host_would_block` | Dry-run notice; no actual drop implied |
| `c2s_connect` | CS2 itself logged receiving a connection request |
| `rejected` / `dropped` | Recognized engine rejection/disconnect text, with a redacted reason category |

The existing rules accept whitelisted/bridge sources and drop already blocked
sources **before** rate accounting. Counters are UDP-only. Short activity may fall
between samples; NAT, macvlan, proxies and upstream filters may bypass this view.
Quiet Guard rules and kernel log rate limiting can suppress notices. Missing
evidence does not establish a healthy path. Tracing changes none of these rules.

Compare timestamps with the existing read-only commands when available:

```bash
sudo cs2node blocks
sudo cs2node why <client-ip>
sudo cs2node rates 10
```

Those commands can expose raw IPs. Review their output before sharing it. Do not
enable Guard merely to obtain counters.

## Privacy, overhead and recognized logs

Only `[connection-trace]` records receive this feature's redaction. Existing raw
console output, crash tails and other logs retain their current behavior. Extract
trace records instead of sharing entire logs. Records omit IPs, ports, userids,
names, SteamIDs, transport IDs, tokens, raw console lines and free-text reasons.
Reasons become `timeout`, `client_disconnect`, `kicked`, `password`, `banned`,
`server_full`, or `other_redacted`: text categories, not verified root causes.

The anchored parser follows Guard's established connection, acceptance, C2S,
signon, drop and forced-reconnect shapes, with short engine channel tags and IPv6
endpoint support. It also recognizes `Rejecting connection from <endpoint>:
<reason>`. Other engine/plugin formats remain in normal console output but are
not inferred into trace events. The synthetic retry fixture uses established
repository log shapes and documentation addresses; it is not a captured incident.

Disabled tracing has a nil observer: no state, nonce, query, timer or goroutine.
Enabled tracing reuses `srv.Line`, the heartbeat, node socket and log sinks. Feed
buffers records without disk/socket writes. Bounds are 32 target attempts, 256
observed client identities for ambiguity detection, 128 queued events, and 8 KiB
per parsed line. Unrelated client identity context stays in memory only and is
cleared when the window ends. Overflow is explicit; the game continues.

## What the egg cannot observe alone

- Client console/UI state, whether a browser click sent a packet, local firewall,
  VPN/NAT behavior, and client workshop/download state.
- DNS/server-browser resolution, wrong-address attempts, and drops before the node.
- Exact packet timing, Steam authentication exchanges, return-path delivery,
  packet contents, and why a reply did not reach the client.
- Per-attempt packet history from aggregate sampled counters.

Collect client console output and UTC timestamps for both tries. With no server
evidence on the first try, compare direct `connect <address>:<port>` against the
browser flow. A separately authorized client/node packet capture may be needed;
this feature does not start one. Keep raw captures private.

[Docs index](../README.md)
