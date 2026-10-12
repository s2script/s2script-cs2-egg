# Disposable live acceptance

This suite is an operator-run procedure, not a passing production certification.
Use a disposable Linux amd64 Docker/panel host with sufficient disk for two
servers, shared generations and backups. Do not alter a production firewall.
The compose profile downloads real game/framework content and contains no
webhook configuration.

Set private `S2_ACCEPTANCE_GSLT_A` and `S2_ACCEPTANCE_GSLT_B` locally.
From the repository root:

```sh
docker compose -f tests/acceptance/compose.yml --profile live build server-a
docker compose -f tests/acceptance/compose.yml --profile live up -d
docker compose -f tests/acceptance/compose.yml --profile live logs -f
```

1. **First install:** verify persistent UID ownership, app 730, Metamod/S2Script,
   one readiness marker per run, native `meta list` and client gameplay. Force
   a controlled startup failure in a copy; it must produce start_failed, no up.
2. **Defaults/disabled/pins:** compare all 18 controls with the catalog. Install
   an explicitly selected registry plugin disabled and record its hash/version.
   Restart with a newer available version: bytes/version update under
   `plugins/disabled/`, without ever becoming loadable. Pin an exact version,
   restart again and verify retained bytes, config and SQLite/plugin data.
   Test panel `default`, `0` and `1` precedence.
3. **Registry outage:** block only the disposable host's registry route or use
   an isolated test proxy. Verified retained compatible artifacts may work;
   missing/unverifiable required artifacts must fail without partial changes.
4. **Two-server sharing:** provision the optional node/helper and read-only mounts
   exactly as [shared-files](../../docs/shared-files.md) describes. Verify VPK
   sharing and distinct mutable files. Keep A running while B updates; A's
   generation must remain unchanged. Test the selected symlink/copy/hardlink
   mode, stopped attachment, pruning and standalone conversion.
5. **Node outage:** stop the disposable node while a game runs. Verify game
   continuity, conservative retained references and lost-contact labeling.
   Reconnect and reconcile ownership; never infer a confirmed down from timeout.
6. **Lifecycle:** use a local HTTP sink or an explicitly authorized test webhook.
   Verify up → down for graceful stop, up → down → crash for unexpected exit,
   and startup failure without up. Repeat/retry stable event IDs and inspect
   delivery evidence. Host power loss cannot promise standalone notification.
7. **Updates/backups:** run an update with players present, stale/unknown counts
   and a maintenance window. Verify bounded countdown/cancellation and correct
   target generation. Stop, back up, modify test data, preview/restore and verify
   hashes/config/data. Restore must refuse a running game and changed preview.
8. **HUD/workshop:** use the official documented addon, compatible build and
   MultiAddonManager. Verify chunk completeness and client rendering. A metadata
   result alone is insufficient; record failures honestly.
9. **Host guard:** only in a disposable network namespace/host, inspect preview,
   exact external allocation and allowlists. Verify log/kick/block separately,
   timeout expiry and unrelated management traffic. Never flush a host ruleset.
10. **Managed node update:** install a release baseline and root-owned pinned
    launcher on a disposable systemd host. Verify candidate readiness, one
    rollback on failure, previous release retention and operator stop precedence.
11. **Panels:** repeat install/start/stop/restart and all allocation/plugin
    controls on the intended Pterodactyl/Pelican and Wings versions.

Use the evidence fields in [acceptance.md](../../docs/acceptance.md). Record
PASS/FAIL/UNRUN individually. Remove test containers with compose `down`;
volumes remain unless explicitly deleted after preserving needed evidence.
