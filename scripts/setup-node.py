#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
"""Interactive shared-CS2 provisioning for standard Debian/Ubuntu Wings nodes.

Install-time only: s2node and the server runtime remain Go binaries. No panel
credentials are collected. Run with --dry-run to discover and review without edits.
"""
import argparse
import copy
import datetime
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import pwd
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

REPOSITORY = 's2script/s2script-cs2-egg'
IMAGE = 'ghcr.io/' + REPOSITORY
BIN = Path('/usr/local/bin/s2node')
CONFIG = Path('/etc/s2node/node.json')
STATE = Path('/var/lib/s2node')
RUNTIME = Path('/run/s2node')
STORE = Path('/srv/s2node/shared')
STEAM = Path('/opt/s2node-steamcmd')
UNIT = Path('/etc/systemd/system/s2node.service')
DROPIN = Path('/etc/systemd/system/s2node.service.d/shared-setup.conf')
BUILD_UNIT = Path('/etc/systemd/system/s2node-build.service')
LICENSES = Path('/usr/local/share/licenses/s2node')
STEAM_HASH = 'cebf0046bfd08cf45da6bc094ae47aa39ebf4155e5ede41373b579b8f1071e7c'


def command(*args, capture=False, pass_fds=()):
    result = subprocess.run(list(map(str, args)), check=True, text=True,
                            stdout=subprocess.PIPE if capture else None, pass_fds=pass_fds)
    return result.stdout if capture else ''


def canonical(path):
    path = Path(path)
    if not path.is_absolute() or str(path.resolve()) != str(path):
        raise ValueError(f'Path must be absolute, clean and contain no symlinks: {path}')
    return path


def read_regular(path):
    canonical(path)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as f:
        if not stat.S_ISREG(os.fstat(f.fileno()).st_mode):
            raise ValueError(f'Expected a regular file: {path}')
        data = f.read((1 << 20) + 1)
    if len(data) > 1 << 20:
        raise ValueError(f'Configuration exceeds 1 MiB: {path}')
    return data


def atomic_write(path, data, mode, uid, gid):
    """Pin the parent, never follow a destination link, retain a private backup."""
    path = canonical(path)
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    temporary = '.s2node-' + secrets.token_hex(12)
    try:
        try:
            fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        except FileNotFoundError:
            fd = None
        if fd is not None:
            with os.fdopen(fd, 'rb') as f:
                if not stat.S_ISREG(os.fstat(f.fileno()).st_mode):
                    raise ValueError(f'Expected a regular file: {path}')
                old = f.read((128 << 20) + 1)
                if len(old) > 128 << 20:
                    raise ValueError(f'Existing file too large to back up: {path}')
            stamp = datetime.datetime.now().strftime('%Y%m%d-%H%M%S-%f')
            backup = path.name + '.before-s2node-' + stamp
            fd = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory)
            with os.fdopen(fd, 'wb') as f:
                f.write(old)
                os.fchown(f.fileno(), uid, gid)
                f.flush()
                os.fsync(f.fileno())
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory)
        with os.fdopen(fd, 'wb') as f:
            f.write(data)
            os.fchown(f.fileno(), uid, gid)
            os.fchmod(f.fileno(), mode)
            f.flush()
            os.fsync(f.fileno())
        os.rename(temporary, path.name, src_dir_fd=directory, dst_dir_fd=directory)
        os.fsync(directory)
    finally:
        try:
            os.unlink(temporary, dir_fd=directory)
        except FileNotFoundError:
            pass
        os.close(directory)


def server_details(container):
    identity = container['Name'].lstrip('/')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', identity):
        raise ValueError('Invalid server UUID/container name')
    if container['State']['Running']:
        raise ValueError(f'Stop {identity} in the panel before setup')
    host = container['HostConfig']
    if host.get('Privileged') or host.get('UsernsMode') not in ('', None):
        raise ValueError('Privileged/remapped containers require manual provisioning')
    mounts = [m for m in container['Mounts'] if m['Destination'] == '/home/container' and m['Type'] == 'bind']
    if len(mounts) != 1:
        raise ValueError('Expected exactly one bind mount at /home/container')
    volume = canonical(mounts[0]['Source'])
    info = volume.stat()
    if info.st_uid == 0 or container['Config']['User'] != f'{info.st_uid}:{info.st_gid}':
        raise ValueError('Container UID:GID must match the non-root host volume owner; review remapping')
    return {'id': identity, 'volume': str(volume), 'peer_uid': info.st_uid, 'access_gid': info.st_gid}


def node_config(existing, servers, reserve):
    if not isinstance(existing, dict):
        raise ValueError('Node configuration must be a JSON object')
    cfg = copy.deepcopy(existing) if existing else {
        'schema_version': 1, 'id': platform.node().split('.')[0],
        'state_dir': str(STATE), 'admin_socket': str(RUNTIME / 'admin.sock'), 'servers': []}
    if cfg.get('schema_version') != 1:
        raise ValueError('Expected node schema_version 1')
    if cfg.get('state_dir', str(STATE)) != str(STATE) or cfg.get('admin_socket', str(RUNTIME / 'admin.sock')) != str(RUNTIME / 'admin.sock'):
        raise ValueError('Custom node-private paths require the manual guide')
    shared = cfg.setdefault('shared', {})
    if not isinstance(shared, dict) or not isinstance(cfg.get('servers', []), list) or not all(isinstance(s, dict) for s in cfg.get('servers', [])):
        raise ValueError('Invalid shared settings or server registration list')
    if shared.get('root', str(STORE)) != str(STORE) or shared.get('steamcmd', str(STEAM / 'steamcmd.sh')) != str(STEAM / 'steamcmd.sh'):
        raise ValueError('Existing custom shared-store/SteamCMD paths require the manual guide')
    shared.update(enabled=True, root=str(STORE), steamcmd=str(STEAM / 'steamcmd.sh'), reserve_bytes=reserve << 30)
    registered = {s['id']: s for s in cfg.setdefault('servers', [])}
    if len(registered) != len(cfg['servers']):
        raise ValueError('Duplicate registered server IDs')
    for server in servers:
        previous = registered.get(server['id'], {})
        if previous and any(previous[k] != server[k] for k in ('volume', 'peer_uid', 'access_gid')):
            raise ValueError('Existing server identity differs; review before reconfiguration')
        registered[server['id']] = dict(previous, **server)
    cfg['servers'] = list(registered.values())
    allocations, volumes = set(), []
    for server in cfg['servers']:
        allocation = server['allocation']
        ip = str(ipaddress.ip_address(allocation['ip']))
        port = int(allocation['port'])
        if not 1 <= port <= 65535 or (ip, port) in allocations:
            raise ValueError('Invalid or duplicate game allocation')
        allocations.add((ip, port))
        volume = canonical(server['volume']) if Path(server['volume']).exists() else Path(server['volume'])
        if any(volume == other or volume in other.parents or other in volume.parents for other in volumes):
            raise ValueError('Registered server volumes overlap')
        volumes.append(volume)
    return cfg


def configure_wings(path):
    import yaml  # Installed from the node's distribution, only during provisioning.
    before = read_regular(path)
    cfg = yaml.safe_load(before)
    if not isinstance(cfg, dict) or not isinstance(cfg.get('system', {}), dict):
        raise ValueError('Invalid Wings YAML; repair it before setup')
    mounts = cfg.setdefault('allowed_mounts', [])
    if not isinstance(mounts, list) or not all(isinstance(v, str) for v in mounts):
        raise ValueError('Wings allowed_mounts must be a list of paths')
    if str(STORE) not in mounts:
        mounts.append(str(STORE))
    cfg.setdefault('system', {})['check_permissions_on_boot'] = False
    rendered = yaml.safe_dump(cfg, sort_keys=False).encode()
    if yaml.safe_load(rendered) != cfg:
        raise ValueError('Wings YAML did not round-trip safely')
    if rendered != before:
        info = path.stat()
        atomic_write(path, rendered, stat.S_IMODE(info.st_mode), info.st_uid, info.st_gid)


def configure_server(server):
    path = Path(server['volume']) / 'egg/server.json'
    cfg = json.loads(read_regular(path)) if path.exists() else {'schema_version': 1}
    if not isinstance(cfg, dict) or cfg.get('schema_version', 1) != 1 or not isinstance(cfg.get('shared', {}), dict):
        raise ValueError('Invalid server JSON; repair it before setup')
    cfg.setdefault('shared', {}).update(mode='symlink', mount='/mnt/s2node')
    atomic_write(path, (json.dumps(cfg, indent=2) + '\n').encode(), 0o600, server['peer_uid'], server['access_gid'])


def verify_asset(path, checksums):
    matches = [line.split()[0] for line in checksums.splitlines()
               if len(line.split()) == 2 and line.split()[1].lstrip('*') == path.name]
    if len(matches) != 1 or not re.fullmatch(r'[a-f0-9]{64}', matches[0]):
        raise ValueError(f'Missing/ambiguous release checksum: {path.name}')
    if hashlib.sha256(path.read_bytes()).hexdigest() != matches[0]:
        raise ValueError(f'Checksum mismatch: {path.name}; no asset will be installed')


def download(url, path, limit=128 << 20):
    class HTTPSOnly(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, request, fp, code, msg, headers, newurl):
            if not newurl.startswith('https://'):
                raise ValueError('Refusing non-HTTPS download redirect')
            return super().redirect_request(request, fp, code, msg, headers, newurl)
    if not url.startswith('https://'):
        raise ValueError('Downloads require HTTPS')
    with urllib.request.build_opener(HTTPSOnly()).open(url, timeout=120) as response, path.open('xb') as f:
        total = 0
        while chunk := response.read(1 << 20):
            total += len(chunk)
            if total > limit:
                raise ValueError('Download exceeded size limit')
            f.write(chunk)


def prompt(reader, out, title, default=''):
    print(f'\n{title}' + (f' [{default}]' if default else '') + ': ', end='', file=out, flush=True)
    line = reader.readline()
    if not line:
        raise EOFError('Input closed; setup cancelled')
    return line.strip() or default


def collect(ask, out, wings, release):
    read_regular(wings)
    command('systemctl', 'is-active', '--quiet', 'wings')
    containers = command('docker', 'ps', '-aq', capture=True).split()
    if not containers:
        raise ValueError('No containers found. Create a server with this egg in the panel first')
    inspected = json.loads(command('docker', 'inspect', *containers, capture=True))
    candidates = [c for c in inspected if c['Config']['Image'].startswith((IMAGE + ':', IMAGE + '@'))]
    if not candidates:
        raise ValueError('No S2Script CS2 containers found. Create/start once, stop, then rerun')
    print('\nS2Script CS2 servers on this node:', file=out)
    for index, container in enumerate(candidates, 1):
        print(f"  {index}. {container['Name'].lstrip('/')} ({'RUNNING — stop first' if container['State']['Running'] else 'stopped'})", file=out)
    answer = ask('Servers to share files with (numbers separated by commas)', '1')
    indexes = [int(v.strip()) - 1 for v in answer.split(',')]
    if not indexes or len(indexes) != len(set(indexes)) or any(i < 0 or i >= len(candidates) for i in indexes):
        raise ValueError('Select valid, distinct server numbers')
    selected, versions = [], set()
    for index in indexes:
        container = candidates[index]
        server = server_details(container)
        volume = Path(server['volume'])
        if not volume.is_relative_to('/var/lib') or not re.fullmatch(r'/var/lib/[A-Za-z0-9_./-]+', str(volume)) or len(str(volume / 'egg/s2node.sock')) > 107:
            raise ValueError('Use the manual guide for custom/long volume paths')
        canonical(volume / 'egg')
        if (volume / 'egg/server.json').exists():
            config = json.loads(read_regular(volume / 'egg/server.json'))
            if not isinstance(config, dict) or config.get('schema_version', 1) != 1 or not isinstance(config.get('shared', {}), dict):
                raise ValueError('Invalid existing server JSON')
        env = dict(v.split('=', 1) for v in container['Config'].get('Env', []) if '=' in v)
        ip = str(ipaddress.ip_address(ask(f"{server['id']}: panel allocation IP", env.get('SERVER_IP', ''))))
        if ipaddress.ip_address(ip).is_unspecified:
            raise ValueError('Enter the public panel allocation, not 0.0.0.0 or ::')
        port = int(ask(f"{server['id']}: game allocation port", env.get('SERVER_PORT', '27015')))
        server['allocation'] = {'ip': ip, 'port': port}
        selected.append(server)
        image = json.loads(command('docker', 'image', 'inspect', container['Image'], capture=True))[0]
        version = image.get('Config', {}).get('Labels', {}).get('org.opencontainers.image.version', '')
        if version and version != 'dev':
            versions.add('v' + version.removeprefix('v'))
    if len(versions) > 1:
        raise ValueError('Selected images use different releases; align them first')
    release = release or ask('Matching s2node release tag', next(iter(versions), ''))
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', release) or versions and release not in versions:
        raise ValueError('Use the release tag matching the selected server images')
    disk_path = STORE
    while not disk_path.exists():
        disk_path = disk_path.parent
    free = shutil.disk_usage(disk_path).free
    print(f'\nAvailable on the shared-store filesystem: {free / (1 << 30):.1f} GiB.\n'
          'Allow for a complete game download plus old generations during updates.', file=out)
    reserve = int(ask('Minimum free space before a CS2 build, GiB', '128'))
    if not 1 <= reserve <= 1048576:
        raise ValueError('Free-space reserve must be a positive whole number of GiB')
    existing = json.loads(read_regular(CONFIG)) if CONFIG.exists() else {}
    cfg = node_config(existing, selected, reserve)
    # Restarting the daemon requires keeping all registered consumers stopped.
    by_name = {c['Name'].lstrip('/'): c for c in inspected}
    for server in cfg['servers']:
        if server['id'] in by_name and by_name[server['id']]['State']['Running']:
            raise ValueError(f"Stop registered server {server['id']} before rerunning setup")
        volume = canonical(server['volume'])
        if not re.fullmatch(r'/var/lib/[A-Za-z0-9_./-]+', str(volume)) or len(str(volume / 'egg/s2node.sock')) > 107:
            raise ValueError('Existing custom/long volume paths require the manual guide')
        canonical(volume / 'egg')
    answer = ask('Start a background CS2 build after setup? y/n', 'n' if (STORE / 'current.json').exists() else 'y').lower()
    if answer not in ('y', 'yes', 'n', 'no'):
        raise ValueError('Answer y or n for the background build')
    build = answer in ('y', 'yes')
    if build and free < reserve << 30:
        raise ValueError('Not enough free space for the selected reserve. Free space or review the reserve before rerunning')
    return {'servers': selected, 'config': cfg, 'release': release, 'wings': wings, 'build': build}


def owned_directory(path, uid, gid, mode):
    path = canonical(path)
    path.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fchown(fd, uid, gid)
        os.fchmod(fd, mode)
    finally:
        os.close(fd)


def set_acl(path, acl):
    fd = os.open(canonical(path), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        command('setfacl', '-m', acl, f'/proc/self/fd/{fd}', pass_fds=(fd,))
    finally:
        os.close(fd)


def check_stopped(plan):
    latest = json.loads(command('docker', 'inspect', *(s['id'] for s in plan['config']['servers']), capture=True))
    found = {s['id']: s for s in (server_details(c) for c in latest)}
    if any(found[s['id']] != {k: s[k] for k in ('id', 'volume', 'peer_uid', 'access_gid')} for s in plan['config']['servers']):
        raise ValueError('Selected server identity changed; rerun discovery')


def apply(plan, out):
    check_stopped(plan)
    for path in (BIN, CONFIG, STATE, RUNTIME, STORE, STEAM, UNIT, DROPIN, BUILD_UNIT):
        canonical(path)
    with tempfile.TemporaryDirectory(prefix='s2node-setup-') as temporary:
        temporary = Path(temporary)
        base = f"https://github.com/{REPOSITORY}/releases/download/{plan['release']}"
        sums = temporary / 'SHA256SUMS'
        print('\nDownloading and verifying the matching release…', file=out, flush=True)
        download(base + '/SHA256SUMS', sums, 1 << 20)
        binary = temporary / f"s2node-{plan['release']}-linux-amd64"
        archive = temporary / f"s2script-cs2-egg-{plan['release']}-deployment.tar.gz"
        for asset in (binary, archive):
            download(base + '/' + asset.name, asset)
            verify_asset(asset, sums.read_text())
        with tarfile.open(archive) as tar:
            prefix = f"s2script-cs2-egg-{plan['release']}/"
            packaged = {}
            for name in ('deploy/s2node.service', 'LICENSE', 'NOTICE', 'third_party/Go-LICENSE'):
                member = tar.getmember(prefix + name)
                if not member.isfile() or member.size > 1 << 20:
                    raise ValueError('Invalid deployment archive member')
                packaged[name] = tar.extractfile(member).read()
        print('Installing prerequisites and provisioning the unprivileged daemon…', file=out, flush=True)
        command('apt-get', 'update')
        command('apt-get', 'install', '-y', 'ca-certificates', 'tar', 'acl', 'python3-yaml', 'lib32gcc-s1', 'lib32stdc++6')
        check_stopped(plan)
        # Parse Wings before changing accounts, binaries or service configuration.
        import yaml
        wings = yaml.safe_load(read_regular(plan['wings']))
        if not isinstance(wings, dict) or not isinstance(wings.get('system', {}), dict):
            raise ValueError('Repair the Wings YAML before provisioning')
        try:
            account = pwd.getpwnam('s2node')
        except KeyError:
            command('useradd', '--system', '--user-group', '--home-dir', str(STATE), '--shell', '/usr/sbin/nologin', 's2node')
            account = pwd.getpwnam('s2node')
        if account.pw_uid == 0 or account.pw_dir != str(STATE) or any(s['peer_uid'] == account.pw_uid for s in plan['config']['servers']):
            raise ValueError('The s2node account must differ from every runtime UID')
        groups = sorted({s['access_gid'] for s in plan['config']['servers']})
        command('usermod', '-a', '-G', ','.join(map(str, groups)), 's2node')
        for path, uid, gid, mode in (
            (STATE, account.pw_uid, account.pw_gid, 0o700), (RUNTIME, account.pw_uid, account.pw_gid, 0o700),
            (CONFIG.parent, 0, account.pw_gid, 0o750), (STORE.parent, 0, 0, 0o755),
            (STORE, account.pw_uid, account.pw_gid, 0o755), (STEAM, account.pw_uid, account.pw_gid, 0o750),
            (DROPIN.parent, 0, 0, 0o755)):
            owned_directory(path, uid, gid, mode)
        for server in plan['config']['servers']:
            volume = Path(server['volume'])
            owned_directory(volume / 'egg', server['peer_uid'], server['access_gid'], 0o700)
            for directory in [*reversed(volume.parents), volume]:
                if directory != Path('/'):
                    set_acl(directory, 'u:s2node:r-x')
            set_acl(volume / 'egg', 'u:s2node:rwx')
        if not (STEAM / 'linux64/steamclient.so').is_file():
            print('Bootstrapping the dedicated host SteamCMD…', file=out, flush=True)
            steam = temporary / 'steamcmd_linux.tar.gz'
            download('https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz', steam)
            verify_asset(steam, f'{STEAM_HASH}  {steam.name}\n')
            command('tar', '--no-same-owner', '-xzf', steam, '-C', STEAM)
            command('chown', '-R', 's2node:s2node', STEAM)
            command('runuser', '-u', 's2node', '--', 'env', f'--chdir={STEAM}', f'HOME={STATE}', STEAM / 'steamcmd.sh', '+quit')
            if not (STEAM / 'linux64/steamclient.so').is_file():
                raise ValueError('SteamCMD SDK64 missing after bootstrap')
        atomic_write(BIN, binary.read_bytes(), 0o755, 0, 0)
        atomic_write(UNIT, packaged['deploy/s2node.service'], 0o644, 0, 0)
        owned_directory(LICENSES, 0, 0, 0o755)
        for name in ('LICENSE', 'NOTICE', 'third_party/Go-LICENSE'):
            atomic_write(LICENSES / Path(name).name, packaged[name], 0o644, 0, 0)
        cfg = plan['config']
        atomic_write(CONFIG, (json.dumps(cfg, indent=2) + '\n').encode(), 0o640, 0, account.pw_gid)
        group_text = ' '.join(map(str, groups))
        writable_paths = [str(STORE), *(str(Path(s['volume']) / 'egg') for s in cfg['servers'])]
        if cfg.get('updates', {}).get('enabled'):
            writable_paths.append(str(STEAM))
        writable = ' '.join(writable_paths)
        atomic_write(DROPIN, f'# Managed by setup-node.py; put custom overrides in a separate drop-in.\n[Service]\nSupplementaryGroups={group_text}\nReadWritePaths={writable}\n'.encode(), 0o644, 0, 0)
        build_unit = f'''[Unit]
Description=Build a shared S2Script CS2 generation
After=network-online.target
[Service]
Type=oneshot
User=s2node
Group=s2node
SupplementaryGroups={group_text}
Environment=HOME={STATE}
ExecStart={BIN} shared-build --config {CONFIG}
TimeoutStartSec=infinity
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths={STORE} {STEAM} {STATE}
'''
        atomic_write(BUILD_UNIT, build_unit.encode(), 0o644, 0, 0)
        check_stopped(plan)
        for server in plan['servers']:
            configure_server(server)
        command('runuser', '-u', 's2node', '--', BIN, 'config-check', '--config', CONFIG)
        configure_wings(plan['wings'])
        command('systemctl', 'restart', 'wings')
        command('systemctl', 'is-active', '--quiet', 'wings')
        command('systemctl', 'daemon-reload')
        command('systemctl', 'enable', 's2node')
        command('systemctl', 'restart', 's2node')
        command('systemctl', 'is-active', '--quiet', 's2node')
        if plan['build']:
            command('systemctl', 'start', '--no-block', 's2node-build')
    print('\nNode service installed. Finish in Pelican/Pterodactyl:', file=out)
    print(f'  Admin → Mounts: source {STORE}, target /mnt/s2node, Read Only ON.\n'
          '  Allow this node and egg; attach the mount to each selected server.\n'
          '  Keep servers stopped until the build completes. Then start one and verify S2EGG_READY.\n'
          '  Build status: sudo systemctl status s2node-build --no-pager\n'
          '  Build logs:   sudo journalctl -u s2node-build -f\n'
          f'  Generation:   sudo test -s {STORE}/current.json\n'
          '  Node status:  sudo -u s2node s2node status\n'
          '  Retry build:  sudo systemctl restart --no-block s2node-build', file=out)


def confirm_and_apply(plan, ask, out, dry_run):
    print(f"\nSetup summary — release {plan['release']}", file=out)
    for server in plan['servers']:
        allocation = server['allocation']
        print(f"  {server['id']} → {server['volume']} (symlink mode)\n"
              f"    UID:GID {server['peer_uid']}:{server['access_gid']}; allocation {allocation['ip']}:{allocation['port']}", file=out)
    if 'config' in plan:
        print(f"  Node configuration: {CONFIG}; shared store: {STORE}\n"
              f"  Wings configuration: {plan['wings']}\n"
              f"  Free-space reserve: {plan['config']['shared']['reserve_bytes'] / (1 << 30):g} GiB\n"
              f"  Start background game build: {'yes' if plan['build'] else 'no'}", file=out)
    print('  Install prerequisites, s2node account, binary, service and host SteamCMD.\n'
          '  Back up/merge configuration; allow a read-only shared mount.\n'
          '  Disable Wings ownership repair on this node to preserve daemon sockets.\n'
          '  Restart Wings/s2node. Other optional modules keep their existing settings.\n'
          '  Docker networking and panel allocations are managed separately.', file=out)
    if dry_run:
        print('Dry run: no downloads, file edits, account changes or service changes.', file=out)
        return
    if ask('Type INSTALL to apply, or anything else to cancel', '') != 'INSTALL':
        print('Cancelled; nothing changed.', file=out)
        return
    apply(plan, out)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', help='Exact release tag; must match selected images')
    parser.add_argument('--wings-config', type=Path, help='Wings YAML; auto-detect Pelican/Pterodactyl')
    parser.add_argument('--dry-run', action='store_true', help='Discover and review without changing the node')
    args = parser.parse_args()
    if platform.system() != 'Linux' or platform.machine() not in ('x86_64', 'amd64'):
        raise ValueError('Supported platform: Linux amd64')
    if os.geteuid() != 0:
        raise ValueError('Run on the Wings node with sudo python3')
    if not Path('/usr/bin/apt-get').exists():
        raise ValueError('This wizard supports Debian/Ubuntu; use the manual guide for other systems')
    paths = [p for p in (Path('/etc/pelican/config.yml'), Path('/etc/pterodactyl/config.yml')) if p.exists()]
    if not args.wings_config and len(paths) != 1:
        raise ValueError('Specify --wings-config when neither/both panel configurations exist')
    wings = args.wings_config or paths[0]
    print('S2Script node setup — shared CS2 files\nRun on the Wings host. Stop the selected servers in the panel first.')
    with open('/dev/tty') as terminal:
        ask = lambda title, default='': prompt(terminal, sys.stdout, title, default)
        plan = collect(ask, sys.stdout, wings, args.release)
        confirm_and_apply(plan, ask, sys.stdout, args.dry_run)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, EOFError, subprocess.CalledProcessError, tarfile.TarError) as error:
        print(f'\nSetup stopped: {error}\nKeep servers stopped. Configuration backups use .before-s2node-*; fix the error and rerun.', file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print('\nSetup interrupted. Keep servers stopped; rerun to resume provisioning.', file=sys.stderr)
        sys.exit(130)
