# Private node metrics

Enable `observability.enabled` and set `observability.privateMetrics` to an explicit literal loopback address, for example `127.0.0.1:9100` or `[::1]:9100`. Empty means disabled. Public/wildcard addresses and DNS names are rejected. There is no public HTTP control service.

Only GET `/metrics` is served. The listener caps accepted connections at 32 and concurrent handlers at eight, with 8 KiB maximum headers, two-second header timeout, three-second read/write timeouts and five-second idle timeout. Configuration does not modify firewall rules or start any host service.

| Metric | Labels and meaning |
| --- | --- |
| `s2node_server_state` | configured `server`, fixed `state` enum; current observation |
| `s2node_players_known` | configured `server`; 1 only for a ready, fresh, nonnegative known count |
| `s2node_players` | configured `server`; omitted when unknown/stale/unready |
| `s2node_module_health` | registered `module`, fixed healthy/degraded/failed `state` |

Run IDs, event IDs, maps, version strings and error messages are never metric labels. Unknown player count is not emitted as zero. Runtime liveness observations do not authorize filesystem mutations. Snapshots include lifecycle, versions, map/player knowledge and module health through `observability.Snapshot(host) Report`. `s2node status --json` and `top` use the private admin socket; `top --once` emits one JSON snapshot and plain `top` repeats every two seconds. `doctor` remains read-only and lists effective text limits, binding and helper requirements. See [logging](logging.md) for bounded histories and [cleanup](cleanup.md) for disk quotas.
