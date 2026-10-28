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

shutil.copyfile(root/'remote-cli.py', out/'ytm-alice-cli.py')
shutil.copyfile(root/'install-client.sh', out/'install-client.sh')
entries = [('ytm-alice-station/'+package.name, package)]
for name in ('install.py','THIRD_PARTY_NOTICES.md','media/UPSTREAM-LICENSE'):
    entries.append(('ytm-alice-station/'+name, root/name))
for name in ('README.md','LICENSE'):
    entries.append(('ytm-alice-station/'+name, repository/name))
tar_gz(out/'ytm-alice-station.tar.gz', entries)
tracked = subprocess.check_output(['git','ls-files','-z'], cwd=repository).decode().split('\0')
tar_gz(out/('ytm-alice-'+version+'-source.tar.gz'), [
    ('ytm-alice-'+version+'/'+name, repository/name) for name in tracked if name])
assets = [package, out/'ytm-alice-cli.py', out/'install-client.sh', out/'ytm-alice-station.tar.gz',
          out/('ytm-alice-'+version+'-source.tar.gz')]
(out/'SHA256SUMS').write_text(''.join(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.name+'\n'
                                    for path in sorted(assets)))
print('\n'.join(str(path) for path in assets))
