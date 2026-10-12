# Guard

Guard observes suspicious connection and pre-join behavior. It is disabled by default. Enable observation before choosing enforcement; the signatures are adapted from CS2-Egg-Go's IPv4 console grammar, not validated live CS2 captures. Native firewall and game acceptance remain required before production use.

## Runtime observation and kicks

Add `"guard": {"enabled": true, "action": "log"}` to `egg/server.json`. Set `action` to `kick` to permit typed `kickid <numeric userid>` commands for attributable pre-FULL clients. A successful write means the command was submitted, not that the engine confirmed removal. Runtime policy accepts `log` or `kick`; host blocks require the optional node helper.

Complete credential-redacted console lines reach guard before display filters. Connect and signon lines must establish a unique, fresh userid/address/name binding in the same run and reporting session. Reconnect or failed delta-decode warnings must exceed the configured threshold after the join grace period. Player names never become command operands. Chat, IP-only warnings, query traffic, keepalive messages, normal FULL signons, ambiguous bindings and reused IDs cannot authorize automatic enforcement.

Defaults: 30-second join grace, 120-second attribution lifetime, 10-second counting window, 5 reconnect warnings or 8 delta-decode warnings, 5-second action freshness and 30-second cooldown. `grace_seconds`, `ttl_seconds`, `window_seconds`, `reconnect_threshold`, `move_threshold`, `fresh_seconds`, `cooldown_seconds` and `pair_seconds` can be set in the guard object. Invalid values fail configuration validation. Queue exhaustion and observation drops refuse enforcement; drops discard client correlation.

## Optional host blocks

Enable the `guard` object in node configuration. `examples/node.json` starts disabled. `policies` maps registered server IDs to policies; omitted entries default to observation. `allocations` maps each enforcing server to its **external pre-DNAT IPv4 address, protocol and game port**, which may differ from the container bind allocation. Wildcard addresses are unsuitable for enforcement.

```json
{
  "enabled": true,
  "helper_socket": "/run/s2node-guard/helper.sock",
  "helper_uid": 2002,
  "policies": {
    "server-a": {"enabled": true, "action": "block", "delegated": false}
  },
  "allocations": {
    "server-a": {"ip": "203.0.113.10", "port": 27015, "protocol": "udp"}
  }
}
```

Replace the example address, server ID and UID. The helper independently authorizes allocations and policy through its root-owned configuration. Manual blocks require `block` policy on both sides. Automatic console-based blocks additionally require `delegated: true` on both sides. Delegation trusts that server's reported console evidence: socket authentication identifies its container, but a malicious plugin/container can fabricate correlated engine-shaped lines. Audit provenance remains `container-console-correlated`.

Private, loopback, link-local, reserved and explicit allowlisted sources are exempt. Add publicly numbered bridge networks and trusted networks to `allowlist` as canonical CIDRs on both node and helper policy. Allocation ports 22, 80, 443, 2022 and 8080 are always protected; add host-specific SSH, Wings and panel endpoints to helper `protected`. A protected endpoint with address `0.0.0.0` and empty protocol protects its port across addresses and protocols. Invalid endpoints fail validation.

Blocks are scoped to the exact destination tuple and source in `inet s2script_guard`, at the pre-DNAT prerouting hook. They expire in the kernel if the daemon stops. The default duration is 60 seconds; the default incident ceiling is 600 seconds, with a hard one-hour maximum. Repeated evidence cannot extend a live block. Escalation is bounded, and restart preserves only the remaining duration of currently authorized blocks. No global firewall flush is performed.

## Install the helper

The node's normal service has no network administration capability. Stage and review the optional `deploy/s2node-guard.service` separately; `s2node install` stages it without enabling it. Provision a distinct nonroot `s2guard` account, an `s2guard` group and the existing `s2node-control` group. The node account needs `s2node-control` access; game containers must not receive that group or this socket mount.

1. Install the verified `s2node` binary at `/usr/local/bin/s2node`, owned by root and not group/world writable. Its parent directories and the configured `nft` executable must also be root-owned and nonwritable by service accounts.
2. Create `/etc/s2node/guard`, root-owned and mode `0750`, group `s2guard`. Copy [the helper example](../examples/guard-helper.json) to `policy.json`, root:`s2guard`, mode `0640`. Replace `node_uid`, `runtime_uid`, `access_gid`, all `container_uids`, server IDs and external allocations with actual values. Do not omit deployed container UIDs from the reviewed policy.
3. Keep policy `enabled: false` initially. Confirm `/usr/sbin/nft` matches the installed executable. The service creates `/run/s2node-guard` and private `/var/lib/s2node-guard`; never place these in game volumes.
4. Preview as an operator with access to the policy: `s2node guard-helper-preview --config /etc/s2node/guard/policy.json`. This renders rules without executing nft. Review mappings and exemptions; changing root policy takes effect on helper restart.
5. In a disposable host or VM first, install the reviewed unit, run `systemctl daemon-reload`, then `systemctl enable --now s2node-guard`. This is an explicit operator action. Starting even an observation-only helper initializes its dedicated table. Enable the required helper and node policies only after inspection.

`CAP_NET_ADMIN` is broad kernel authority. The helper's fixed table restriction is enforced by this implementation and operator-owned policy, not a table-specific kernel capability. Its Unix socket checks the exact configured node UID; it accepts typed operations, never arbitrary commands, paths, allocations or nft expressions. It uses fixed nft arguments, bounded input/output and two-second operation deadlines. The installed privileged helper must remain operator-owned even if ordinary node self-updates are enabled.

## Administration

All node commands use the private administrator socket and print JSON:

```sh
s2node guard-info --config /etc/s2node/node.json
s2node guard-preview --config /etc/s2node/node.json
s2node guard-blocks --server server-a
s2node guard-block --server server-a --address 8.8.8.8 --seconds 60 --request operator-incident-1
s2node guard-why --server server-a --id <returned-action-ID>
s2node guard-unblock --server server-a --address 8.8.8.8 --request operator-release-1
s2node guard-rates
s2node guard-refresh
```

The addresses above illustrate syntax; only block a source you intend to restrict. Retain request IDs after uncertain responses. Reusing the same request returns its original record; conflicting reuse is rejected. Actions are queued, so inspect `guard-why` for their outcome. `guard-blocks` lists saved authorized expiries, not proof of kernel enforcement; uncertain kernel state is explicitly labeled. Use `--offset` with its returned `next` value for further pages.

`guard-preview` renders node mappings only. The root-owned helper preview is authoritative. `guard-refresh` explicitly reconciles the dedicated table with the helper's current in-memory authorized policy and unexpired desired blocks. It may apply a previously uncertain desired block for its remaining duration; inspect the list first. A full reconciliation clears uncertainty; unrelated successful actions do not. New root policy requires restarting the helper.

Host outcomes can use the existing configured webhook pipeline's `guard` event. Notifications retry persisted outcomes; they never replay enforcement. No webhook is sent without operator configuration. Audit records are finite: 4,096 records per node, eligible for pruning after 24 hours only when published and inactive. Capacity exhaustion requires resolving delivery/retention, not deleting live state. Active blocks are limited to 256/server and 2,048/helper.

## Optional packet rates

Helper `rates` defaults to empty. A rate entry names an already authorized allocation, `per_second`, `burst` and optional `allowlist`. UDP rules match the IPv4 A2S query prefix and query bytes; TCP rules match new SYN packets. Each meter keys by source address, with 1,024 entries per allocation, a 30-second idle timeout and at most 16 rate allocations. Suggested initial values are UDP 20/second with burst 40, or TCP 10/second with burst 20; measure legitimate traffic before enabling.

Unsupported nft syntax/capabilities fail startup or refresh. There is no silent aggregate-rate fallback. `guard-rates` returns bounded timestamped kernel drop counters, and reports unavailable counters as unknown rather than zero. Single block/unblock changes preserve meter state; a full refresh recreates it.

## Removal and acceptance

First stop and disable the optional helper. Review `nft list table inet s2script_guard`, then remove only that table with `nft delete table inet s2script_guard`. Remove its reviewed unit, socket/config/state directories and dedicated account only when no longer needed; retain audit data as required. Disable node guard configuration. Do not flush the host ruleset or alter Wings, SSH or panel services.

Before production: verify real console signatures and numeric kicks on a disposable CS2 server; inspect generated rules with the installed nft version; test two distinct allocations, exemptions, bounded rate behavior, kernel expiry without the daemon, restart with remaining expiry, helper permission rejection and loss of contact. The repository tests use fake firewall execution and source-derived console fixtures; they do not establish live host acceptance.
