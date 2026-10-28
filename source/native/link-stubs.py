#!/usr/bin/env python3
"""Generate link-only firmware ABI stubs; never ship or use them at runtime."""
from pathlib import Path
import re
import subprocess

symbols = subprocess.check_output(['arm-linux-gnueabihf-nm', '-u', 'player.o'], text=True)
libraries = {'libquasar_gstreamer': [], 'libglib': []}
for line in symbols.splitlines():
    symbol = line.split()[-1]
    if not re.fullmatch(r'[A-Za-z_][A-Za-z_0-9]*', symbol):
        raise SystemExit('Unexpected native symbol: '+symbol)
    if symbol.startswith(('gst_', '_gst_')):
        libraries['libquasar_gstreamer'].append(symbol)
    elif symbol.startswith(('g_', '_g_')):
        libraries['libglib'].append(symbol)
for name, names in libraries.items():
    if not names:
        raise SystemExit('No imports for '+name)
    source = Path(name+'.c')
    source.write_text('\n'.join('void '+symbol+'(void) {}' for symbol in sorted(names))+'\n')
    subprocess.run(['arm-linux-gnueabihf-gcc', '-shared', '-fPIC', str(source),
                    '-Wl,-soname,'+name+'.so', '-o', name+'.so'], check=True)
