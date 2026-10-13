# Manual setup of shared CS2 files on a node

**Prefer the wizard? Use [Node setup](node-setup.md).** This page is the manual
alternative for reviewing individual provisioning steps and diagnosing custom setups.

Use this guide to give several Pelican servers on **one node** a shared CS2
installation. Start with one stopped development server. The recommended mode
is **symlink**: large game VPKs are shared; configs, plugins and other runtime
files stay in each server's own volume.

You can switch after a standalone download has started. You do not need to finish
it. The node builds a fresh complete installation, rather than resuming that
partial download. Keep the existing server volume and back up your configs,
addons and data before migration.

**Run terminal blocks in SSH on the Wings node, one block at a time.** Stop if a
block reports an error. Keep the same terminal open so the `S2_SETUP_` variables
remain available. Only the steps explicitly marked **In Pelican** use the panel.
No SSH access to the separate web-panel host is required.

This is a guided manual installation. Unlike CS2-Egg-Go's installer wizard,
`s2node install --directory ...` only stages files; it does not provision users,
configure Wings or enable services. The current alpha has automated fixture
coverage; a complete live Pelican/shared-game acceptance test remains outstanding.

## Before you begin

This walkthrough targets **Ubuntu 22.04/24.04 or Debian 12/13, Linux amd64**, a
standard Pelican installation at `/etc/pelican/config.yml`, numeric non-root
Wings container identities, and volumes under `/var/lib`. User-namespace
remapping, volumes under `/home`, existing s2node installations and hardlink mode
need the [node reference](node.md) instead of blindly repeating a fresh install.
For Pterodactyl, use its Wings config path and mount menus; the ownership and
read-only mount requirements are the same.

You need:

- SSH with `sudo` on the game node and permission to manage mounts in Pelican.
- A healthy Wings service and a stopped server using this project's egg.
- The release tag matching that server's image, such as `v0.1.1-alpha.2`.
- Space for a full CS2 download, the current shared generation and a replacement
  generation during updates, plus private server files and backups. The guide
  uses a **128 GiB free-space preflight**, not a guarantee of the game's size.
- Outbound access to GitHub, Valve's SteamCMD download and Steam services.

Check the node first:

```bash
uname -m
sudo systemctl is-active wings
df -h /srv /var/lib
```

Expect `x86_64` and `active`. Resolve Wings errors before continuing. Dev servers
sharing the host IP through different ports should use a private **bridge**
network. A macvlan container must have its own IP; giving it the node's IP causes
an address conflict. This storage setup does not change Docker networking.

For a layout with separate development and production nodes:

| Node | Game allocations | Docker network |
| --- | --- | --- |
| Development | One host IP, a different port for each server | Private bridge; Docker publishes the assigned host ports |
| Production | A dedicated IP for each server | Keep the working production design; macvlan needs distinct, reserved container IPs |

The IP entered below is the server's **panel allocation**, not a private bridge
address. The Wings hostname points to the host's reachable IP. A Docker macvlan
pool address is not automatically the node's IP; check the actual interface and
DNS rather than substituting one for the other. See Docker's
[bridge](https://docs.docker.com/engine/network/drivers/bridge/) and
[macvlan](https://docs.docker.com/engine/network/drivers/macvlan/) references.

## 1. Install the host prerequisites

These packages provide download tools, JSON/YAML generation, directory ACLs and
the 32-bit libraries used by host SteamCMD. No Go compiler is required.

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl tar python3 python3-yaml acl \
  lib32gcc-s1 lib32stdc++6 tmux
```

An existing s2node configuration means this is an upgrade or an additional-server
operation. Do not overwrite it with the fresh configuration below:

```bash
sudo test ! -e /etc/s2node/node.json
```

Expected: no output and exit status 0. If the test fails, follow
[Add another server](#add-another-server) or [node updates](project-updates.md).

## 2. Enter the first server's details

Find its **UUID/container name** in Pelican, or list containers:

```bash
sudo docker ps -a --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
```

Stop that server in Pelican. Enter the UUID, matching release tag, and the server's
public allocation when prompted. These are input prompts, not literal example
values to paste into configuration.

```bash
read -r -p 'Panel server UUID/container name: ' S2_SETUP_SERVER
read -r -p 'Matching release tag, including v: ' S2_SETUP_RELEASE
read -r -p 'Game allocation IP: ' S2_SETUP_IP
read -r -p 'Game allocation port: ' S2_SETUP_PORT

S2_SETUP_VOLUME=$(sudo docker inspect --format \
  '{{range .Mounts}}{{if and (eq .Destination "/home/container") (eq .Type "bind")}}{{.Source}}{{end}}{{end}}' \
  "$S2_SETUP_SERVER")
S2_SETUP_UID=$(sudo stat -c '%u' "$S2_SETUP_VOLUME")
S2_SETUP_GID=$(sudo stat -c '%g' "$S2_SETUP_VOLUME")
S2_SETUP_USER=$(sudo docker inspect --format '{{.Config.User}}' "$S2_SETUP_SERVER")
S2_SETUP_RUNNING=$(sudo docker inspect --format '{{.State.Running}}' "$S2_SETUP_SERVER")

python3 - "$S2_SETUP_SERVER" "$S2_SETUP_RELEASE" "$S2_SETUP_IP" "$S2_SETUP_PORT" \
  "$S2_SETUP_VOLUME" "$S2_SETUP_UID" "$S2_SETUP_GID" "$S2_SETUP_USER" "$S2_SETUP_RUNNING" <<'PY'
import ipaddress, pathlib, re, sys
server, release, ip, port, volume, uid, gid, user, running = sys.argv[1:]
assert re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', server), 'Invalid server ID'
assert re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', release), 'Use a release tag such as v0.1.1-alpha.2'
ipaddress.ip_address(ip)
assert 1 <= int(port) <= 65535, 'Invalid game port'
assert running == 'false', 'Stop the server in Pelican first'
assert re.fullmatch(r'/var/lib/[A-Za-z0-9_./-]+', volume), 'This walkthrough requires a simple /var/lib volume path'
p = pathlib.Path(volume)
assert p.is_dir() and str(p.resolve()) == volume, 'Volume must exist without symlink components'
assert int(uid) > 0 and user == f'{uid}:{gid}', 'Container identity differs from volume ownership; review remapping/permissions before continuing'
print(f'Stopped server: {server}\nVolume: {volume}\nRuntime UID:GID: {uid}:{gid}')
PY
```

Expected: the stopped server, its actual host volume and numeric owner. If Docker
reports **No such object**, start/recreate the container through healthy Wings,
stop it, then repeat this step. Do not guess a volume path or change its owner.

## 3. Install the matching node release

Use a published release whose public-verification job passed. The checksum check
happens before the downloaded binary is installed. Parentheses keep a failed
download from closing your SSH shell.

```bash
S2_SETUP_DOWNLOADS=$(mktemp -d)
(
  set -eu
  cd "$S2_SETUP_DOWNLOADS"
  base="https://github.com/s2script/s2script-cs2-egg/releases/download/$S2_SETUP_RELEASE"
  curl -fL "$base/SHA256SUMS" -o SHA256SUMS
  curl -fL "$base/s2node-$S2_SETUP_RELEASE-linux-amd64" -o "s2node-$S2_SETUP_RELEASE-linux-amd64"
  curl -fL "$base/s2script-cs2-egg-$S2_SETUP_RELEASE-deployment.tar.gz" \
    -o "s2script-cs2-egg-$S2_SETUP_RELEASE-deployment.tar.gz"
  python3 - "$S2_SETUP_RELEASE" <<'PY'
import pathlib, sys
release = sys.argv[1]
names = {f's2node-{release}-linux-amd64', f's2script-cs2-egg-{release}-deployment.tar.gz'}
lines = [line for line in pathlib.Path('SHA256SUMS').read_text().splitlines()
         if len(line.split()) == 2 and line.split()[1].lstrip('*') in names]
assert len(lines) == 2 and {line.split()[1].lstrip('*') for line in lines} == names, 'Both assets need release checksums'
pathlib.Path('selected-checksums.txt').write_text('\n'.join(lines) + '\n')
PY
  sha256sum --check selected-checksums.txt
  mkdir deployment
  tar -xzf "s2script-cs2-egg-$S2_SETUP_RELEASE-deployment.tar.gz" \
    --strip-components=1 -C deployment
  sudo install -m 0755 "s2node-$S2_SETUP_RELEASE-linux-amd64" /usr/local/bin/s2node
  sudo install -m 0644 deployment/deploy/s2node.service /etc/systemd/system/s2node.service
  sudo install -d -m 0755 /usr/local/share/licenses/s2node
  sudo install -m 0644 deployment/LICENSE deployment/NOTICE deployment/third_party/Go-LICENSE \
    /usr/local/share/licenses/s2node/
)
/usr/local/bin/s2node version
```

Expected: checksum `OK` lines for **both downloaded assets**, followed by the
matching s2node version/revision. Keep the release/source link with your node's
installation record. See [release downloads](releases.md#installing-downloads).

## 4. Create the daemon account and prepare access

The daemon uses a different UID from the game. It can read directory entries and
create its server socket; it does not gain read access to private mode-0600 game
files. These ACL changes are not recursive.

```bash
id s2node >/dev/null 2>&1 || sudo useradd --system --user-group \
  --home-dir /var/lib/s2node --shell /usr/sbin/nologin s2node
sudo usermod -a -G "$S2_SETUP_GID" s2node
sudo install -d -o s2node -g s2node -m 0700 /var/lib/s2node /run/s2node
sudo install -d -o root -g s2node -m 0750 /etc/s2node
sudo install -d -o root -g root -m 0755 /srv/s2node
sudo install -d -o s2node -g s2node -m 0755 /srv/s2node/shared
sudo install -d -o s2node -g s2node -m 0750 /opt/s2node-steamcmd
sudo install -d -o "$S2_SETUP_UID" -g "$S2_SETUP_GID" -m 0700 "$S2_SETUP_VOLUME/egg"

sudo python3 - "$S2_SETUP_VOLUME" "$S2_SETUP_UID" <<'PY'
import pathlib, pwd, subprocess, sys
p = pathlib.Path(sys.argv[1])
assert pwd.getpwnam('s2node').pw_uid != int(sys.argv[2]), 'Daemon and runtime UIDs must differ'
assert p.is_dir() and str(p.resolve()) == str(p), 'Refusing symlinked volume path'
for directory in [*reversed(p.parents), p]:
    if directory != pathlib.Path('/'):
        subprocess.run(['setfacl', '-m', 'u:s2node:r-x', str(directory)], check=True)
subprocess.run(['setfacl', '-m', 'u:s2node:rwx', str(p / 'egg')], check=True)
PY
```

The current implementation opens each directory with `O_RDONLY`, so **search-only
(`--x`) ACLs are insufficient**. Do not recursively chown the server to s2node or
make its private files group/world-readable.

## 5. Bootstrap a dedicated host SteamCMD

This SteamCMD belongs to s2node, outside all server volumes. The archive hash is
the same observed integrity pin used by the runtime image; a mismatch requires
checking the upstream archive and this project's reviewed pin.

```bash
(
  set -eu
  archive="$S2_SETUP_DOWNLOADS/steamcmd_linux.tar.gz"
  curl -fL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz -o "$archive"
  printf '%s  %s\n' cebf0046bfd08cf45da6bc094ae47aa39ebf4155e5ede41373b579b8f1071e7c "$archive" \
    | sha256sum --check -
  sudo tar --no-same-owner -xzf "$archive" -C /opt/s2node-steamcmd
  sudo chown -R s2node:s2node /opt/s2node-steamcmd
  sudo -u s2node sh -c 'cd /opt/s2node-steamcmd && env HOME=/var/lib/s2node ./steamcmd.sh +quit'
  sudo test -s /opt/s2node-steamcmd/linux64/steamclient.so
)
```

Expected: SteamCMD initializes and the final file check succeeds. This step does
not download CS2. A missing 32-bit loader/library or SDK64 must be fixed first.

## 6. Generate the node configuration and service override

The following command writes JSON and the systemd override for you. It refuses
to overwrite an existing node configuration. Other optional modules stay off.

```bash
sudo python3 - "$S2_SETUP_SERVER" "$S2_SETUP_VOLUME" "$S2_SETUP_UID" "$S2_SETUP_GID" \
  "$S2_SETUP_IP" "$S2_SETUP_PORT" <<'PY'
import grp, ipaddress, json, os, pathlib, socket, sys
server, volume, uid, gid, ip, port = sys.argv[1:]
ipaddress.ip_address(ip)
assert 1 <= int(port) <= 65535
assert pathlib.Path(volume).is_dir() and str(pathlib.Path(volume).resolve()) == volume
config = {
    'schema_version': 1, 'id': socket.gethostname().split('.')[0],
    'state_dir': '/var/lib/s2node', 'admin_socket': '/run/s2node/admin.sock',
    'servers': [{'id': server, 'volume': volume, 'peer_uid': int(uid),
                 'access_gid': int(gid), 'allocation': {'ip': ip, 'port': int(port)}}],
    'shared': {'enabled': True, 'root': '/srv/s2node/shared',
               'steamcmd': '/opt/s2node-steamcmd/steamcmd.sh', 'reserve_bytes': 128 << 30}
}
p = pathlib.Path('/etc/s2node/node.json')
with p.open('x') as f:
    json.dump(config, f, indent=2)
    f.write('\n')
os.chown(p, 0, grp.getgrnam('s2node').gr_gid)
os.chmod(p, 0o640)
directory = pathlib.Path('/etc/systemd/system/s2node.service.d')
directory.mkdir(mode=0o755, parents=True, exist_ok=True)
override = directory / 'sharing.conf'
with override.open('x') as f:
    f.write(f'[Service]\nSupplementaryGroups={gid}\nReadWritePaths=/srv/s2node/shared {volume}/egg\n')
os.chmod(override, 0o644)
print('Created /etc/s2node/node.json and sharing.conf')
PY

sudo -u s2node /usr/local/bin/s2node config-check --config /etc/s2node/node.json
sudo -u s2node /usr/local/bin/s2node doctor --config /etc/s2node/node.json
```

Expected: `node configuration valid`. Doctor may report private runtime/artifact
files as unavailable to its different UID; that is expected and does not mean a
game is stopped. The service needs write access to the **shared store** for its
reservation metadata, as well as the selected server's **egg directory** for the
socket. The service's base unit already permits its private state/runtime paths.

## 7. Build the first shared game generation

This is the long CS2 download. Use a persistent terminal session so an SSH
disconnect does not terminate it:

```bash
tmux new -s s2node-build
```

Inside the new tmux terminal, run:

```bash
sudo -u s2node /usr/local/bin/s2node shared-build --config /etc/s2node/node.json
```

The current host builder may be quiet during SteamCMD's download. It prints a
generation JSON object only after success. For Steam's progress logs, use another
SSH terminal to locate the current build's content log:

```bash
sudo find /srv/s2node/shared/.build -path '*/Steam/logs/content_log.txt' -print
```

Use `sudo tail -f` on the printed path. To detach tmux without stopping the build,
press **Ctrl+B**, then **D**; return with `tmux attach -t s2node-build`.

Wait for successful generation JSON containing `id` and `build_id`. Failed builds
are not published. A second build starts a new staging tree; do not repeatedly
cancel and relaunch a healthy download. Leave the build terminal with `exit` once
complete, returning to the original terminal with your setup variables.

## 8. Allow the read-only mount and preserve socket ownership

The daemon's socket belongs to s2node. Wings' recursive ownership repair on each
boot would change that owner, so this node uses explicit ownership provisioning
instead. The command below adds the allowed source and sets
`system.check_permissions_on_boot` to `false`. This setting applies to the whole
Wings node; provision file ownership explicitly for its servers afterward.

This is a terminal command. It edits the YAML and makes a private timestamped
backup; **do not paste Python or a Docker template into `config.yml`**. Existing
configuration values are retained, while YAML formatting/comments are rewritten.

```bash
sudo python3 - <<'PY'
import datetime, os, pathlib, shutil, stat, tempfile, yaml
p = pathlib.Path('/etc/pelican/config.yml')
assert not p.is_symlink(), 'Review the actual Wings config path before editing'
info = p.stat()
config = yaml.safe_load(p.read_text())
assert isinstance(config, dict)
allowed = config.setdefault('allowed_mounts', [])
assert isinstance(allowed, list) and all(isinstance(v, str) for v in allowed)
if '/srv/s2node/shared' not in allowed:
    allowed.append('/srv/s2node/shared')
config.setdefault('system', {})['check_permissions_on_boot'] = False
rendered = yaml.safe_dump(config, sort_keys=False)
assert yaml.safe_load(rendered) == config
backup = p.with_name(p.name + '.before-s2node-' + datetime.datetime.now().strftime('%Y%m%d-%H%M%S-%f'))
shutil.copyfile(p, backup)
os.chmod(backup, 0o600)
fd, temporary = tempfile.mkstemp(prefix='.s2node-', dir=p.parent)
try:
    with os.fdopen(fd, 'w') as f:
        f.write(rendered)
        f.flush()
        os.fsync(f.fileno())
        os.fchmod(f.fileno(), stat.S_IMODE(info.st_mode))
        os.fchown(f.fileno(), info.st_uid, info.st_gid)
    os.replace(temporary, p)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
print(f'Updated Wings configuration; backup: {backup}')
PY

sudo systemctl restart wings
sudo systemctl is-active wings
```

Expected: `active`. If Wings fails, inspect `sudo journalctl -u wings -n 60
--no-pager` before starting a game.

**In Pelican → Admin → Mounts**, create:

| Field | Value |
| --- | --- |
| Name | S2Script shared CS2 |
| Source | `/srv/s2node/shared` |
| Target | `/mnt/s2node` |
| Read Only | **Enabled** |
| User Mountable | Disabled unless you want users to attach it themselves |

Allow the relevant **node and egg**, then attach the mount on the test server's
mounts page. Restart/recreate the server's container through the panel as needed
for a changed mount to take effect. Host path existence alone does not prove the
container received a read-only mount. See [Pelican's mount guide](https://pelican.dev/docs/guides/mounts/).

## 9. Select symlink mode and start the daemon

The server defaults already select symlink mode at `/mnt/s2node`. This command
makes the selection explicit, merging it into the existing `egg/server.json`
instead of replacing your other settings:

```bash
sudo python3 - "$S2_SETUP_VOLUME" "$S2_SETUP_UID" "$S2_SETUP_GID" <<'PY'
import json, os, pathlib, shutil, sys, tempfile
volume, uid, gid = sys.argv[1:]
p = pathlib.Path(volume) / 'egg/server.json'
assert not p.is_symlink(), 'Review symlinked configuration before editing'
config = json.loads(p.read_text()) if p.exists() else {'schema_version': 1}
assert isinstance(config, dict) and config.get('schema_version', 1) == 1
if p.exists():
    backup = p.with_name('server.json.before-sharing')
    assert not backup.exists(), 'Existing migration backup: review it before repeating this step'
    shutil.copyfile(p, backup)
    os.chown(backup, int(uid), int(gid))
    os.chmod(backup, 0o600)
config.setdefault('shared', {}).update({'mode': 'symlink', 'mount': '/mnt/s2node'})
fd, temporary = tempfile.mkstemp(prefix='.server-sharing-', dir=p.parent)
try:
    with os.fdopen(fd, 'w') as f:
        f.write(json.dumps(config, indent=2) + '\n')
        f.flush()
        os.fsync(f.fileno())
        os.fchown(f.fileno(), int(uid), int(gid))
        os.fchmod(f.fileno(), 0o600)
    os.replace(temporary, p)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY

sudo systemctl daemon-reload
sudo systemctl enable --now s2node
sudo systemctl is-active s2node
sudo -u s2node /usr/local/bin/s2node status --config /etc/s2node/node.json
```

Expected: `active`, the registered server, and a healthy `shared` module. A stopped
server's game state can be `unknown`; it has not connected yet. Verify its socket:

```bash
sudo stat -c '%U %G %a %n' "$S2_SETUP_VOLUME/egg/s2node.sock"
```

Expected: owner `s2node`, the configured access group and mode `660`.

**In Pelican**, start the existing test server. Its startup connects to
`egg/s2node.sock` and materializes the published generation before launching CS2.
Mounted symlink startup does this itself; a per-server host helper is **not**
required. `shared-attach`, hardlinks and copy-without-mount are separate advanced
paths in the [shared-files reference](shared-files.md).

## 10. Verify the result before adding more servers

```bash
sudo docker inspect --format \
  '{{range .Mounts}}{{if eq .Destination "/mnt/s2node"}}{{println .Source "RW=" .RW}}{{end}}{{end}}' \
  "$S2_SETUP_SERVER"
sudo docker exec "$S2_SETUP_SERVER" /usr/local/bin/s2egg status --root /home/container
sudo -u s2node /usr/local/bin/s2node status --config /etc/s2node/node.json
```

Check all of these:

- The mount is `/srv/s2node/shared` with **`RW= false`**.
- Runtime status reports a **`shared_generation`** and the expected Steam build.
- The game reaches **`S2EGG_READY`**, a client connects, and `meta list` plus actual
  plugin behavior confirm the native stack.
- Panel Stop works and the next Start also uses the shared generation.
- SSH and Wings remain responsive throughout startup.

If the console runs a standalone `app_update 730`, the initial node connection or
shared capability was unavailable. Check the registration, UID/GID, ACLs, socket,
service and mount before continuing. A known shared install never silently falls
back to standalone SteamCMD when its shared mount/generation is unavailable.

## Switching a partial standalone download

Steps 2–10 are also the migration path. You do **not** move the partial install
into `/srv/s2node/shared` or point SteamCMD at another server's volume. First shared
startup replaces managed game files with generation files/links while preserving
private configs, addons, data, `gameinfo.gi` and Steam bookkeeping. Declare any
intentional changes to managed game files in `shared.overrides` before migration.

Old partial-download/cache files can remain because Steam state is preserved.
After successful verification, inspect the stopped volume before deciding what
cache data to remove. This guide does not delete game directories or Steam state.
Keep the existing server entry, GSLT, allocation and private-volume backup.

## Add another server

Keep it stopped and use the same published store. Repeat detail discovery and
directory/socket access provisioning for its own UUID, volume and UID/GID. Add a
new entry to `servers` in the **existing** `/etc/s2node/node.json`; do not replace
the first server or rerun the fresh configuration generator. Add its access GID
to the daemon's supplementary groups and its `egg` directory to the service's
`ReadWritePaths`. Attach the same read-only panel mount and select symlink mode.

Run `config-check` as s2node, reload systemd, restart s2node, then start/verify that
one server. Dev and production on different nodes need independently provisioned
stores; Pelican mounts do not synchronize nodes. Repeat the [full node reference](node.md)
when identities or filesystem layouts differ.

## Updates, logs and returning to standalone

- **Logs:** `sudo journalctl -u s2node -n 80 --no-pager`. Use Wings logs for panel
  connectivity and container creation; use the game console for runtime errors.
- **Game updates:** run another `shared-build` as s2node to publish a new complete
  generation. Test a stopped/start cycle on one server before rolling out. Old
  generations stay while referenced. Automatic scheduling is a separate opt-in
  module; see [updates](updates.md).
- **Pruning:** do not delete generation directories by hand. Use the documented
  reference-aware [retention procedure](shared-files.md#recovery-and-retention).
- **Return to standalone:** stop the game, safely detach its shared links with
  `s2egg shared-standalone` using the matching runtime and mounted sources, then
  disable that server's shared preparation and remove its panel mount. Keep the
  referenced generations until detachment is complete. See
  [leaving shared mode](shared-files.md#recovery-and-retention).

## When a check fails

| Symptom | Next check |
| --- | --- |
| Wings fails with `yaml: line ...` | Inspect that part of its config; do not paste command templates into YAML. Restore the private backup if necessary, keep shared servers stopped, then retry the mount step. |
| `directory unavailable or contains symlink` | Actual volume path, symlink components and **read/search** permission on every ancestor; this path does not traverse symlinks. |
| `daemon must belong to configured socket access groups` | `id s2node`, the configured GID and the systemd `SupplementaryGroups` override. |
| Read-only filesystem during node reserve/build | Daemon ownership and its shared-store `ReadWritePaths`; the **container** mount should be read-only, the host daemon's store must be writable. |
| `shared read-only mount unavailable` | Panel mount attachment and Docker's actual `RW=false`; a host directory alone is insufficient. |
| Socket ownership changes after a game Start | Wings `system.check_permissions_on_boot`, reinstalls or another recursive ownership tool; keep mixed daemon/runtime ownership intact. |
| Shared module has no published generation | Finish a successful `shared-build` before starting the consumer. |
| Host build loses Steam connection | Node routing, DNS, network load and Steam availability; an incomplete build is not published. |
