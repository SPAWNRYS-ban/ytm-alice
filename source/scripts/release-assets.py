#!/usr/bin/env python3
"""Build public installation/source bundles from the tracked release source."""
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
parser.add_argument('packages', nargs='+', type=Path)
args = parser.parse_args()
packages = sorted(path.resolve() for path in args.packages)
out = packages[0].parent
version = subprocess.check_output(['python3', str(root/'scripts/check-version.py')], text=True).strip()
expected = {f'ytm-alice-{version}-{platform}.yampkg' for platform in ('yandexmicro','yandexmini_2')}
if (len(packages) != len(set(packages)) or any(path.parent != out or path.name not in expected
                                            or not path.is_file() for path in packages)):
    parser.error('Expected current-version Station packages in the same output directory')

def tar_gz(path, entries):
    with path.open('wb') as destination, gzip.GzipFile(filename='', mode='wb', fileobj=destination, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w') as archive:
            for name, source in entries:
                content = source.read_bytes()
                info = tarfile.TarInfo(name)
                info.size = len(content)
                info.mode = 0o755 if source.stat().st_mode & 0o111 else 0o644
                archive.addfile(info, io.BytesIO(content))

shutil.copyfile(root/'remote-cli.py', out/'ytm-alice-cli.py')
shutil.copyfile(root/'install-client.sh', out/'install-client.sh')
entries = [('ytm-alice-station/'+package.name, package) for package in packages]
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
assets = packages+[out/'ytm-alice-cli.py', out/'install-client.sh', out/'ytm-alice-station.tar.gz',
          out/('ytm-alice-'+version+'-source.tar.gz')]
assets += [path.with_suffix('.yampkg.sha256') for path in packages if path.with_suffix('.yampkg.sha256').is_file()]
(out/'SHA256SUMS').write_text(''.join(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.name+'\n'
                                    for path in sorted(assets)))
print('\n'.join(str(path) for path in assets))
