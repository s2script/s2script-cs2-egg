# SPDX-License-Identifier: GPL-3.0-or-later
import hashlib
import importlib.util
import io
import json
import os
import subprocess
from pathlib import Path
import tempfile
import tarfile
import types
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('setup_node', Path(__file__).resolve().parents[2] / 'scripts/setup-node.py')
setup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(setup)


class SetupTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='s2-setup-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.volume = self.root / 'volume'
        self.volume.mkdir()
        self.uid, self.gid = os.getuid(), os.getgid()

    def container(self):
        return {'Name': '/first-server', 'Id': 'a' * 64, 'Image': 'sha256:fixture',
                'Config': {'Image': 'ghcr.io/s2script/s2script-cs2-egg:prerelease',
                           'User': f'{self.uid}:{self.gid}', 'Env': ['SERVER_IP=192.0.2.10', 'SERVER_PORT=27015']},
                'State': {'Running': False}, 'HostConfig': {'Privileged': False, 'UsernsMode': ''},
                'Mounts': [{'Type': 'bind', 'Source': str(self.volume), 'Destination': '/home/container'}]}

    def test_discovery_rejects_running_remapped_or_symlinked_volume(self):
        server = setup.server_details(self.container())
        self.assertEqual((server['id'], server['peer_uid'], server['access_gid']), ('first-server', self.uid, self.gid))
        for change in ['running', 'user', 'privileged', 'symlink']:
            with self.subTest(change=change):
                container = self.container()
                if change == 'running':
                    container['State']['Running'] = True
                elif change == 'user':
                    container['Config']['User'] = '12345:12345'
                elif change == 'privileged':
                    container['HostConfig']['Privileged'] = True
                else:
                    link = self.root / 'linked'
                    link.symlink_to(self.volume, target_is_directory=True)
                    container['Mounts'][0]['Source'] = str(link)
                with self.assertRaises(ValueError):
                    setup.server_details(container)

    def test_merge_preserves_modules_servers_and_prevents_duplicate_allocations(self):
        old = {'schema_version': 1, 'id': 'dev', 'servers': [
            {'id': 'old', 'volume': '/var/lib/pelican/old', 'peer_uid': 1000, 'access_gid': 1000,
             'allocation': {'ip': '192.0.2.10', 'port': 27016}}],
            'observability': {'enabled': True}, 'shared': {'enabled': True, 'root': '/srv/s2node/shared'}}
        server = setup.server_details(self.container())
        server['allocation'] = {'ip': '192.0.2.10', 'port': 27015}
        result = setup.node_config(old, [server], 128)
        self.assertEqual(result['observability'], {'enabled': True})
        self.assertEqual([s['id'] for s in result['servers']], ['old', 'first-server'])
        self.assertEqual(result['shared']['reserve_bytes'], 137438953472)
        self.assertEqual(len(setup.node_config(result, [server], 128)['servers']), 2)
        with self.assertRaises(ValueError):
            setup.node_config(result, [dict(server, id='second-server')], 128)

    def test_atomic_edit_keeps_private_backup_and_rejects_symlinks(self):
        path = self.volume / 'server.json'
        path.write_text('{"private":"original"}\n')
        setup.atomic_write(path, b'{"private":"updated"}\n', 0o600, self.uid, self.gid)
        self.assertEqual(json.loads(path.read_text()), {'private': 'updated'})

        backups = list(self.volume.glob('server.json.before-s2node-*'))
        self.assertEqual(len(backups), 1)
        self.assertEqual(json.loads(backups[0].read_text()), {'private': 'original'})
        self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)
        link = self.volume / 'link.json'
        link.symlink_to(path)
        with self.assertRaises(ValueError):
            setup.atomic_write(link, b'bad', 0o600, self.uid, self.gid)
        self.assertEqual(json.loads(path.read_text()), {'private': 'updated'})

    def test_acl_is_applied_to_pinned_directory_and_refuses_symlink(self):
        setup.set_acl(self.volume, f'u:{self.uid}:r-x')
        acl = subprocess.check_output(['getfacl', '-cpn', str(self.volume)], text=True)
        self.assertIn(f'user:{self.uid}:r-x', acl)
        link = self.root / 'acl-link'
        link.symlink_to(self.volume, target_is_directory=True)
        with self.assertRaises(ValueError):
            setup.set_acl(link, f'u:{self.uid}:rwx')
        self.assertEqual(subprocess.check_output(['getfacl', '-cpn', str(self.volume)], text=True), acl)

    def test_wings_and_server_edits_preserve_values(self):
        wings = self.root / 'config.yml'
        wings.write_text('token: fixture\nsystem:\n  check_permissions_on_boot: true\ndocker:\n  network:\n    driver: bridge\nallowed_mounts:\n- /other\n')
        setup.configure_wings(wings)
        import yaml
        updated = yaml.safe_load(wings.read_text())
        self.assertEqual(updated['token'], 'fixture')
        self.assertEqual(updated['docker'], {'network': {'driver': 'bridge'}})
        self.assertFalse(updated['system']['check_permissions_on_boot'])
        self.assertEqual(updated['allowed_mounts'], ['/other', '/srv/s2node/shared'])
        setup.configure_wings(wings)
        self.assertEqual(yaml.safe_load(wings.read_text()), updated)
        egg = self.volume / 'egg'
        egg.mkdir()
        config = egg / 'server.json'
        config.write_text('{"schema_version":1,"steam":{"gslt":"fixture"},"shared":{"overrides":["game/custom"]}}')
        setup.configure_server(setup.server_details(self.container()))
        result = json.loads(config.read_text())
        self.assertEqual(result['steam'], {'gslt': 'fixture'})
        self.assertEqual(result['shared'], {'overrides': ['game/custom'], 'mode': 'symlink', 'mount': '/mnt/s2node'})
        self.assertEqual(config.stat().st_mode & 0o777, 0o600)

    def test_checksum_failure_and_missing_asset_are_rejected(self):
        asset = self.root / 's2node-v1.2.3-linux-amd64'
        asset.write_bytes(b'fixture')
        digest = hashlib.sha256(b'fixture').hexdigest()
        setup.verify_asset(asset, f'{digest}  {asset.name}\n')
        for checksums in ['0' * 64 + f'  {asset.name}\n', f'{digest}  another-file\n']:
            with self.assertRaises(ValueError):
                setup.verify_asset(asset, checksums)

    def test_eof_declined_confirmation_and_dry_run_never_apply(self):
        with self.assertRaises(EOFError):
            setup.prompt(io.StringIO(''), io.StringIO(), 'Continue?', 'yes')
        for answer, dry_run in [('no', False), ('INSTALL', True)]:
            called = []
            with patch.object(setup, 'apply', side_effect=lambda *args: called.append(args)):
                setup.confirm_and_apply({'servers': [], 'release': 'v1.2.3'},
                                        lambda *args: answer, io.StringIO(), dry_run)
            self.assertEqual(called, [])

    def test_malformed_config_is_rejected_before_mutation(self):
        for existing in [[], {'schema_version': 1, 'servers': 'bad'},
                         {'schema_version': 1, 'servers': [], 'shared': []}]:
            with self.subTest(existing=existing), self.assertRaises(ValueError):
                setup.node_config(existing, [], 128)

    def test_discovery_uses_image_release_and_dry_run_has_no_host_edits(self):
        wings = self.root / 'wings.yml'
        wings.write_text('system: {}\n')
        server = {'id': 'first-server', 'volume': '/var/lib/pelican/first-server',
                  'peer_uid': self.uid, 'access_gid': self.gid}

        def command(*args, capture=False, **kwargs):
            if args[:3] == ('docker', 'ps', '-aq'):
                return 'container-id\n'
            if args[:2] == ('docker', 'inspect'):
                return json.dumps([self.container()])
            if args[:3] == ('docker', 'image', 'inspect'):
                return json.dumps([{'Config': {'Labels': {'org.opencontainers.image.version': '1.2.3'}}}])
            if args == ('systemctl', 'is-active', '--quiet', 'wings'):
                return ''
            raise AssertionError(f'Discovery attempted a host mutation: {args}')

        answers = iter(['1', '192.0.2.10', '27015', 'v1.2.3', '128', 'y'])
        with patch.object(setup, 'CONFIG', self.root / 'node.json'), \
                patch.object(setup, 'command', side_effect=command), \
                patch.object(setup, 'server_details', return_value=server), \
                patch.object(setup.shutil, 'disk_usage', return_value=types.SimpleNamespace(free=256 << 30)):
            plan = setup.collect(lambda *args: next(answers), io.StringIO(), wings, None)
            setup.confirm_and_apply(plan, lambda *args: 'INSTALL', io.StringIO(), True)
        self.assertEqual(plan['release'], 'v1.2.3')
        self.assertEqual(plan['config']['servers'][0]['allocation'], {'ip': '192.0.2.10', 'port': 27015})
        self.assertFalse((self.root / 'node.json').exists())
        self.assertEqual(wings.read_text(), 'system: {}\n')

    def test_provisioning_writes_services_and_preserves_private_settings(self):
        paths = {name: self.root / relative for name, relative in {
            'BIN': 'bin/s2node', 'CONFIG': 'etc/node.json', 'STATE': 'state', 'RUNTIME': 'run',
            'STORE': 'store/shared', 'STEAM': 'steamcmd', 'UNIT': 'units/s2node.service',
            'DROPIN': 'units/s2node.service.d/shared-setup.conf', 'BUILD_UNIT': 'units/s2node-build.service',
            'LICENSES': 'licenses'}.items()}
        for p in paths.values():
            p.parent.mkdir(parents=True, exist_ok=True)
        operator = paths['DROPIN'].parent / 'sharing.conf'
        operator.write_text('[Service]\nReadWritePaths=/operator-store\n')
        (paths['STEAM'] / 'linux64').mkdir(parents=True)
        (paths['STEAM'] / 'linux64/steamclient.so').write_text('fixture')
        (self.volume / 'egg').mkdir()
        (self.volume / 'egg/server.json').write_text('{"schema_version":1,"steam":{"gslt":"fixture"}}')
        wings = self.root / 'wings.yml'
        wings.write_text('token: fixture\nsystem: {}\n')
        server = dict(setup.server_details(self.container()), allocation={'ip': '192.0.2.10', 'port': 27015})
        release = 'v1.2.3'
        archive = self.root / f's2script-cs2-egg-{release}-deployment.tar.gz'
        with tarfile.open(archive, 'w:gz') as tar:
            for name in ['deploy/s2node.service', 'LICENSE', 'NOTICE', 'third_party/Go-LICENSE']:
                member = tarfile.TarInfo(f's2script-cs2-egg-{release}/{name}')
                data = b'[Service]\nUser=s2node\n' if name.endswith('.service') else b'fixture license'
                member.size = len(data)
                tar.addfile(member, io.BytesIO(data))
        assets = {f's2node-{release}-linux-amd64': b'fixture executable', archive.name: archive.read_bytes()}
        checksums = ''.join(hashlib.sha256(data).hexdigest() + '  ' + name + '\n' for name, data in assets.items())

        def download(url, path, *args):
            path.write_bytes(checksums.encode() if path.name == 'SHA256SUMS' else assets[path.name])

        def command(*args, capture=False, **kwargs):
            if args[:2] == ('docker', 'inspect'):
                return json.dumps([self.container()])
            return ''

        account = types.SimpleNamespace(pw_uid=self.uid + 1, pw_gid=self.gid, pw_dir=str(paths['STATE']))
        # External host services/accounts are mocked; all file generation and merge operations are real.
        with patch.multiple(setup, **paths), patch.object(setup, 'command', side_effect=command), \
                patch.object(setup, 'download', side_effect=download), \
                patch.object(setup.pwd, 'getpwnam', return_value=account), \
                patch.object(setup.os, 'fchown'):
            cfg = setup.node_config({}, [server], 128)
            cfg['updates'] = {'enabled': True}
            setup.apply({'servers': [server], 'config': cfg, 'release': release, 'wings': wings, 'build': True}, io.StringIO())
            setup.apply({'servers': [server], 'config': cfg, 'release': release, 'wings': wings, 'build': False}, io.StringIO())
        self.assertEqual(paths['BIN'].read_bytes(), b'fixture executable')
        self.assertEqual(paths['BIN'].stat().st_mode & 0o777, 0o755)
        self.assertEqual(json.loads(paths['CONFIG'].read_text())['servers'][0]['id'], 'first-server')
        self.assertIn(f'ReadWritePaths={paths["STORE"]} {self.volume}/egg', paths['DROPIN'].read_text())
        self.assertIn(str(paths['STEAM']), paths['DROPIN'].read_text())
        self.assertEqual(operator.read_text(), '[Service]\nReadWritePaths=/operator-store\n')
        self.assertEqual(len(json.loads(paths['CONFIG'].read_text())['servers']), 1)
        build = paths['BUILD_UNIT'].read_text()
        self.assertIn('User=s2node\n', build)
        self.assertIn('Type=oneshot\n', build)
        self.assertIn(f'ExecStart={paths["BIN"]} shared-build --config {paths["CONFIG"]}', build)
        self.assertEqual(json.loads((self.volume / 'egg/server.json').read_text())['steam']['gslt'], 'fixture')
        self.assertTrue(list(self.root.glob('wings.yml.before-s2node-*')))


if __name__ == '__main__':
    unittest.main()
