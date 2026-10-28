#!/usr/bin/env python3
"""Use the official YAM v2 packer; never constructs MANIFEST.yaml by hand."""
import argparse
import hashlib
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent
p = argparse.ArgumentParser()
p.add_argument('--yam', default='yam', help='YAM v2 executable')
p.add_argument('--runner', help='Optional emulator for an aarch64 YAM on x86_64')
p.add_argument('--profiles', type=Path, required=True)
p.add_argument('--output', type=Path, default=root/'release/ytm-alice-0.7.0-yandexmicro.yampkg')
a = p.parse_args()
a.output.parent.mkdir(parents=True, exist_ok=True)
cmd = ([a.runner] if a.runner else []) + [a.yam, '--platform', 'yandexmicro',
       '--profiles', str(a.profiles.resolve()), 'pack', str(root), '--output', str(a.output.resolve())]
subprocess.run(cmd, check=True)
digest = hashlib.sha256(a.output.read_bytes()).hexdigest()
a.output.with_suffix(a.output.suffix+'.sha256').write_text(f'{digest}  {a.output.name}\n')
print(f'{a.output.resolve()}\n{a.output.stat().st_size/1024/1024:.2f} MiB; SHA256 {digest}')
