#!/usr/bin/env python3
"""Build the pinned, patched Luna dependency with the same checks as CI."""
import argparse
import hashlib
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import urllib.request

UPSTREAM = 'https://github.com/nyakokitsu/luna.git'
COMMIT = '4f769b5f184c3f83e50d7c22b16f90d6a392b602'
TOOLCHAIN = 'https://github.com/musl-cc/musl.cc/releases/download/v0.0.1/aarch64-linux-musl-cross.tgz'
TOOLCHAIN_SHA256 = 'c909817856d6ceda86aa510894fa3527eac7989f0ef6e87b5721c58737a06c38'
repository = Path(__file__).resolve().parents[2]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--yam', type=Path, required=True)
parser.add_argument('--profiles', type=Path, required=True)
parser.add_argument('--work-dir', type=Path, default=repository/'.build/luna-integration')
parser.add_argument('--output', type=Path, default=repository/'release')
parser.add_argument('--toolchain-dir', type=Path)
args = parser.parse_args()
if platform.system() != 'Linux' or platform.machine() != 'x86_64':
    parser.error('This cross-build requires Linux x86_64')
work = args.work_dir.resolve()
source = work/'source'
if source.exists():
    parser.error('Use an empty --work-dir; refusing to overwrite an existing checkout')
work.mkdir(parents=True, exist_ok=True)
output = args.output.resolve()
output.mkdir(parents=True, exist_ok=True)

def run(*command, cwd=source, env=None):
    subprocess.run(list(map(str, command)), cwd=cwd, env=env, check=True)

run('git', 'clone', '--no-checkout', UPSTREAM, source, cwd=work)
run('git', 'checkout', '--detach', COMMIT)
patch = repository/'source/luna-host/ytm-provider.patch'
run('git', 'apply', '--check', patch)
run('git', 'apply', '--index', patch)
run('cargo', 'test', '--workspace', '--locked')
run('cargo', 'build', '--locked', '--bin', 'luna')
for check in ('yam_binary_http.py', 'media_provider_http.py', 'yam_service.py'):
    run('python3', 'tests/'+check, 'target/debug/luna')

if args.toolchain_dir:
    toolchain = args.toolchain_dir.resolve()
else:
    archive = work/'aarch64-linux-musl-cross.tgz'
    with urllib.request.urlopen(TOOLCHAIN, timeout=30) as remote, archive.open('wb') as destination:
        shutil.copyfileobj(remote, destination)
    if hashlib.sha256(archive.read_bytes()).hexdigest() != TOOLCHAIN_SHA256:
        raise SystemExit('musl toolchain SHA-256 mismatch')
    def archive_filter(member, destination):
        # The published loader symlink points at /lib/libc.so. Relocate it
        # inside the cross sysroot instead of referencing the build host.
        if member.issym() and member.linkname == '/lib/libc.so':
            member.linkname = 'libc.so'
        return tarfile.data_filter(member, destination)
    with tarfile.open(archive) as compressed:
        compressed.extractall(work, filter=archive_filter)
    toolchain = work/'aarch64-linux-musl-cross'
compiler = toolchain/'bin/aarch64-linux-musl-gcc'
if not compiler.is_file():
    raise SystemExit('Cross compiler not found: '+str(compiler))
run('rustup', 'target', 'add', 'aarch64-unknown-linux-musl')
environment = dict(os.environ, PATH=str(toolchain/'bin')+os.pathsep+os.environ['PATH'],
                   CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=str(compiler),
                   YAM_BIN=str(args.yam.resolve()), YAM_PROFILES=str(args.profiles.resolve()))
for model in ('yandexmicro', 'yandexmini_2'):
    run('cargo', 'build', '--release', '--locked', '--target', 'aarch64-unknown-linux-musl',
        '--manifest-path', f'stations/{model}/Cargo.toml', '--bin', 'luna-'+model, env=environment)
    run('sh', 'tools/package-station.sh', model,
        output/f'luna-0.2.7-ytm-{model}.yampkg', env=environment)
print('Patched Luna packages ready:', output)
