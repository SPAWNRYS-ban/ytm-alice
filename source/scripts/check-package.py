#!/usr/bin/env python3
"""Exercise real YAM install/upgrade/remove without services or a real device."""
import argparse
import os
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument('package', type=Path)
parser.add_argument('--yam', required=True)
parser.add_argument('--runner')
parser.add_argument('--platform', choices=('yandexmicro', 'yandexmini_2'), default='yandexmicro')
parser.add_argument('--profiles', required=True, type=Path)
args = parser.parse_args()
package = args.package.resolve()
version = subprocess.check_output(['python3', str(Path(__file__).with_name('check-version.py'))], text=True).strip()
with tempfile.TemporaryDirectory(prefix='ytm-package-check-') as directory:
    temporary = Path(directory)
    profiles = temporary/'profiles'
    profiles.mkdir()
    profile = (args.profiles/(args.platform+'.yaml')).read_text()
    profile = profile.replace('/data/yam', str(temporary/'default')).replace('/tmp/yam', str(temporary/'run'))
    (profiles/(args.platform+'.yaml')).write_text(profile)
    prefix = ([args.runner] if args.runner else [])+[args.yam, '--platform', args.platform, '--profiles', str(profiles), '--root', str(temporary/'root')]
    environment = {key:value for key,value in os.environ.items() if key not in ('YAM_ROOT','YAM_PLATFORM','YAM_PROFILES')}
    def invoke(*arguments):
        return subprocess.check_output(prefix+list(map(str, arguments)), env=environment, text=True)
    # This fixture checks package transactions only. Runtime compatibility is
    # covered by the Luna integration tests, not by a fake daemon here.
    dependency = temporary/'luna-dependency'
    dependency.mkdir()
    (dependency/'luna').write_text('#!/bin/sh\nexit 0\n')
    (dependency/'luna').chmod(0o755)
    (dependency/'yam.yaml').write_text('name: yam/luna\nversion: 0.2.7\nlicense: MIT\n'
        'description: Package transaction dependency fixture\ntargets: ['+args.platform+']\n'
        'files:\n  bin/luna: luna\n')
    host_package = temporary/'luna.yampkg'
    invoke('pack', dependency, '--output', host_package)
    invoke('install', '--yes', host_package)
    def installed():
        return [line.split() for line in invoke('list').splitlines()]
    invoke('install', '--yes', package)
    assert ['local/ytm-alice',version,args.platform] in installed()
    root = temporary/'root'
    for binary in ('ytm-alice','ytm-alice-player','alice-media','bt-proxy'):
        link = root/'bin'/binary
        assert link.is_symlink() and link.resolve().is_file() and os.access(link, os.X_OK), binary
    for name in ('LICENSE','THIRD_PARTY_NOTICES.md','media/UPSTREAM-LICENSE','media/upstream/src/main.rs'):
        assert (root/'share/local.ytm-alice'/name).is_file(), name
    plugin = root/'share/luna/plugins/local.ytm-alice'
    assert (plugin/'plugin.toml').is_file() and (plugin/'run.sh').is_file()
    forbidden = {'youtube-session.json','state.json','id_ed25519','cookies.sqlite','target','.git'}
    assert not any(path.name in forbidden for path in (root/'pkgs').rglob('*'))
    data = root/'var/local.ytm-alice'
    data.mkdir(parents=True, exist_ok=True)
    session = data/'youtube-session.json'
    session.write_text('{"fixture":"session-preservation"}')
    session.chmod(0o600)
    invoke('remove', 'local/ytm-alice')
    assert installed() == [['yam/luna','0.2.7',args.platform]]
    assert not (root/'bin/bt-proxy').exists()
    # Synthetic old packages exercise the real conflicts/replaces upgrade path.
    for name, binaries in [('ytm-alice', ('ytm-alice','ytm-alice-player')),
                            ('alice-media', ('alice-media','bt-proxy'))]:
        source = temporary/name
        source.mkdir()
        program = source/'program'
        program.write_text('#!/bin/sh\nexit 0\n')
        program.chmod(0o755)
        (source/'yam.yaml').write_text('name: local/'+name+'\nversion: 0.0.1\nlicense: MIT\n'
            'description: Upgrade fixture\ntargets: ['+args.platform+']\nfiles:\n'+
            ''.join('  bin/'+binary+': program\n' for binary in binaries))
        artifact = temporary/(name+'.yampkg')
        invoke('pack', source, '--output', artifact)
        invoke('install', '--yes', artifact)
    invoke('install', '--yes', package)
    assert ['local/ytm-alice',version,args.platform] in installed()
    assert not (root/'pkgs/local/alice-media').exists()
    assert session.read_text() == '{"fixture":"session-preservation"}'
    assert session.stat().st_mode & 0o777 == 0o600
    invoke('doctor')
    print(args.platform+': four executables, clean install, upgrade from two packages, removal, session preservation and doctor OK')
