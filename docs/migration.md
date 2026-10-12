# Migration and rollback

This egg supports Metamod + S2Script only. There is no automatic conversion of
other framework plugins, permissions, configuration or data.

1. Stop the old server and take a consistent backup of the entire persistent
   root. Save its image digest, egg, allocation, GSLT location and framework/plugin
   versions. Keep a copy outside the new server's writable mount.
2. Create a separate server/volume using [installation](install.md). Reuse no
   active shared generation as a writable SteamCMD target. Install S2Script and
   verify native loading before copying application data.
3. Move only compatible configuration, maps and S2Script plugin data. Review
   plugin dependencies, package identity and API compatibility. Remove old
   framework launch settings from the new configuration.
4. Create/reconcile the [plugin inventory](plugins.md). Preserve exact pins and
   `enabled: false`; check the `disabled/` directory too. Keep panel toggles at
   `default` when the manifest should decide enablement.
5. Test with an alternate allocation, verify readiness and gameplay, then stop
   both instances before moving the production allocation. Preserve server
   identity only when intentionally continuing that same server's event history.

An image rollback alone does not restore downloaded CS2, frameworks, plugins,
SQLite data or settings. Stop the game and restore a matching consistent backup
before restarting with retained versions. [Backup restore](backups.md) previews
and validates changes, refuses an active runtime lease and keeps recovery
material. Test restores into a disposable volume first.

For node rollback, use the retained managed release as described in
[project updates](project-updates.md). State compatibility is required in both
directions; do not force an old binary to read a newer state ABI. Retain delivery
archives and action receipts so a restart cannot replay uncertain operations.
