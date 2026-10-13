# Set up the node

The node wizard prepares shared CS2 files for Pelican or Pterodactyl. Run it in
SSH **on the Wings/game node**, not the separate web-panel machine. A partially
downloaded standalone server can switch; stop it first and keep its volume.

## 1. Before you run it

You need Ubuntu 22.04/24.04 or Debian 12/13, Linux amd64, `curl`, Python 3.10+,
`sudo`, healthy Docker/Wings, and a server created with this project's egg.
Standard volumes under `/var/lib`, numeric non-root container identities and
the usual Pelican/Pterodactyl Wings config paths are supported. For remapped
identities, custom paths or hardlinks, use the [manual guide](node-setup-manual.md).

Stop the servers you want to register in the panel. Back up their private
configs, addons and data. Allow disk space for the complete CS2 download and
another generation during updates; the wizard shows free space and asks for a
reserve, defaulting to 128 GiB.

Your dev servers sharing one host IP through different ports should use a
private Docker bridge. A macvlan container must have a distinct reserved IP,
never the host's IP. The wizard leaves Docker networking and allocations alone.

## 2. Run the wizard

Copy this one command into the node's SSH terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/scripts/setup-node.py | sudo python3 -
```

The wizard reads prompts from your terminal, so piping the download does not
consume your answers. It lists this egg's containers, lets you select servers,
and asks for their panel allocations and the matching release. It detects the
image's release label when available and verifies the downloaded release assets
against `SHA256SUMS` before installing them.

Read the summary, then type **INSTALL** to apply it. Closing input or cancelling
does not apply the plan. The wizard:

- Installs prerequisites, the matching s2node binary and a dedicated daemon account.
- Bootstraps host SteamCMD and grants directory/socket access without recursively
  changing ownership of game files.
- Merges node registrations and server settings, preserving optional modules,
  tokens, private configs and declared shared overrides.
- Writes a separate managed systemd drop-in and retains existing operator drop-ins.
- Backs up edited files as `.before-s2node-*`, allows the shared mount in Wings,
  and disables Wings' node-wide recursive boot chown to preserve daemon sockets.
- Restarts Wings/s2node and, if selected, starts the game download as a background
  systemd build. The build continues if SSH disconnects.

To inspect/download the script before running, or review the plan without edits:

```bash
curl -fL https://raw.githubusercontent.com/s2script/s2script-cs2-egg/main/scripts/setup-node.py -o setup-node.py
sudo python3 setup-node.py --dry-run
sudo python3 setup-node.py
```

`--release v0.1.1-alpha.2` selects an exact matching release;
`--wings-config /etc/pelican/config.yml` selects the config explicitly.
The existing `s2node install --directory ...` command still only stages files;
the setup script is the host provisioning wizard.

## 3. Attach the mount in the panel

This is the remaining panel step. The wizard does not need your panel API token.

In **Admin → Mounts**, create a mount with these values:

| Field | Value |
| --- | --- |
| Name | S2Script shared CS2 |
| Source | `/srv/s2node/shared` |
| Target | `/mnt/s2node` |
| Read Only | **Enabled** |
| User Mountable | Disabled |

Allow the relevant **node and egg**, then open each selected server's admin
**Mounts** page and attach it. See [Pelican's mount guide](https://pelican.dev/docs/guides/mounts/).
Stores are local to a node; development and production nodes need separate setup.

## 4. Wait for the build, then start one server

```bash
sudo systemctl status s2node-build --no-pager
sudo journalctl -u s2node-build -f
```

The host builder can be quiet during SteamCMD's download. Wait for successful
generation JSON containing `id` and `build_id`, and a successful service exit.
The wizard's final output also gives the generation/status checks. Do not start
a consumer before a complete generation is published.

Start one server in the panel. Check for **S2EGG_READY**, connect a client, verify
`meta list`/plugin behavior, then test Stop/Start. Its actual Docker mount must
be read-only and runtime status must report `shared_generation`; see the
[verification commands](node-setup-manual.md#10-verify-the-result-before-adding-more-servers).

## Reruns and errors

Rerun the same command to register more stopped servers. Keep all already
registered servers stopped during reconfiguration. Existing registrations and
optional modules are retained; custom layouts are sent to the manual guide.
Fix any reported error before starting games; provisioning is not an all-or-nothing
host transaction. Configuration backups are private and stay beside edited files.

If the build fails, inspect its journal, fix the network/disk/SteamCMD error, then:

```bash
sudo systemctl restart --no-block s2node-build
```

For detailed permissions, migration, rollback and recovery, use the
[manual setup](node-setup-manual.md), [node reference](node.md) and
[shared-file reference](shared-files.md). Automated installer fixtures cover
configuration and provisioning; a complete live Pelican/shared-CS2 acceptance
run remains outstanding.
