# Uninstall without losing server data

Stop the affected servers through the panel and verify they are stopped before
removing runtime access or changing mounts. Save a consistent backup first.

For a standalone egg, switch to your replacement image/egg only after testing
its persistent layout. Removing a container or panel egg does not require
deleting the persistent volume. Deleting that volume destroys configuration,
plugin data, inventory, identity and installed game state.

Before removing s2node:

1. Disable new scheduled updates/backups and inspect pending or uncertain jobs.
2. Convert each shared server while stopped using the
   [standalone conversion procedure](shared-files.md). Ensure the independent
   writable install boots without any shared mount before detaching it.
3. Remove the node-owned webhook route through an orderly daemon shutdown and
   confirm standalone delivery behavior. Retain the private outbox/archives;
   do not erase unconfirmed delivery history.
4. Stop and disable only the specific s2node/helper units you installed. Remove
   reviewed unit drop-ins, sockets and binaries after no caller uses them.
5. If host guard was enabled, follow [guard removal](guard.md) and remove only
   its dedicated `inet s2script_guard` table. Never flush the host ruleset.
6. Retain shared generations until all registered references and live mounts
   are reconciled. Retain backup recovery catalogs until their explicit release
   conditions are met; they are not ordinary cleanup targets.

Remove service users/groups only after checking their remaining file ownership
and other consumers. Nothing in the project automatically deletes your server
volumes, makes a package private, or revokes external Discord/Steam credentials.
Rotate credentials separately if retiring them.
