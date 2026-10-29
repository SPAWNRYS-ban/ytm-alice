#!/usr/bin/env python3
"""Collect Cargo dependency notices from the checksummed registry sources."""
import json
from pathlib import Path
import shutil
import subprocess

root = Path(__file__).resolve().parents[1]
metadata = json.loads(subprocess.check_output(['cargo','metadata','--locked','--format-version','1',
                                               '--filter-platform','armv7-unknown-linux-musleabihf',
                                               '--manifest-path',str(root/'media/upstream/Cargo.toml')]))
destination = root.parent/'licenses/rust'
if destination.exists():
    shutil.rmtree(destination)
destination.mkdir(parents=True, exist_ok=True)
lines = ['# Rust dependency licenses', '', 'Generated from the locked Cargo registry sources.', '']
for package in sorted(metadata['packages'], key=lambda item:(item['name'],item['version'])):
    if package['source'] is None:
        continue
    name = package['name']+'-'+package['version']
    source = Path(package['manifest_path']).parent
    files = sorted(path for path in source.rglob('*') if path.is_file()
                   and len(path.relative_to(source).parts) <= 2
                   and path.name.upper().startswith(('LICENSE','COPYING','NOTICE','UNLICENSE')))
    if package.get('license_file'):
        files.append(source/package['license_file'])
    if not files:
        raise SystemExit('Missing license text for '+name)
    folder = destination/name
    folder.mkdir(exist_ok=True)
    for path in set(files):
        (folder/path.name).write_bytes(path.read_bytes().replace(b'\r\n', b'\n'))
    lines.append('- '+name+' — '+str(package.get('license') or package.get('license_file')))
(destination/'README.md').write_text('\n'.join(lines)+'\n')
print('Collected notices for', len(lines)-4, 'Rust dependencies')
