#!/usr/bin/env python3
"""Build public binary, installation and source assets for a release."""
import argparse
import gzip
import hashlib
import io
from pathlib import Path
import shutil
import subprocess
import tarfile

root = Path(__file__).resolve().parents[1]
repository = root.parent
parser = argparse.ArgumentParser()
parser.add_argument('package', type=Path)
args = parser.parse_args()
package = args.package.resolve()
out = package.parent
version = subprocess.check_output(['python3', str(root/'scripts/check-version.py')], text=True).strip()

def tar_gz(path, entries):
    with path.open('wb') as destination, gzip.GzipFile(filename='', mode='wb', fileobj=destination, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w') as archive:
            for name, source in entries:
                content = source.read_bytes()
                info = tarfile.TarInfo(name)
                info.size = len(content)
                info.mode = 0o755 if source.stat().st_mode & 0o111 else 0o644
                archive.addfile(info, io.BytesIO(content))

# Architecture labels describe each executable, not a promise of model support.
binaries = [
    (root/'dist/ytm-alice', 'ytm-alice-'+version+'-linux-arm64'),
    (root/'dist/ytm-alice-player', 'ytm-alice-player-'+version+'-linux-armhf'),
    (root/'media/dist/bt-proxy', 'bt-proxy-'+version+'-linux-armv7-musl'),
]
# Fail before generating release bundles if a required build is missing.
for source, _ in binaries:
    if not source.is_file() or not source.stat().st_mode & 0o111:
        raise SystemExit('Missing executable build: '+str(source))
binary_assets = []
for source, name in binaries:
    destination = out/name
    shutil.copyfile(source, destination)
    destination.chmod(0o755)
    binary_assets.append(destination)

shutil.copyfile(root/'remote-cli.py', out/'ytm-alice-cli.py')
shutil.copyfile(root/'install-client.sh', out/'install-client.sh')
entries = [('ytm-alice-station/'+package.name, package)]
for name, path in [('install.py', root/'install.py'),
                   ('README.md', repository/'README.md'),
                   ('LICENSE', repository/'licenses/LICENSE'),
                   ('THIRD_PARTY_NOTICES.md', repository/'docs/THIRD_PARTY_NOTICES.md'),
                   ('media/UPSTREAM-LICENSE', repository/'licenses/media/UPSTREAM-LICENSE')]:
    entries.append(('ytm-alice-station/'+name, path))
tar_gz(out/'ytm-alice-station.tar.gz', entries)
tracked = subprocess.check_output(['git','ls-files','-z'], cwd=repository).decode().split('\0')
tar_gz(out/('ytm-alice-'+version+'-source.tar.gz'), [
    ('ytm-alice-'+version+'/'+name, repository/name) for name in tracked if name])
assets = binary_assets + [package, out/'ytm-alice-cli.py', out/'install-client.sh', out/'ytm-alice-station.tar.gz',
          out/('ytm-alice-'+version+'-source.tar.gz')]
(out/'SHA256SUMS').write_text(''.join(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.name+'\n'
                                    for path in sorted(assets)))
print('\n'.join(str(path) for path in assets))
