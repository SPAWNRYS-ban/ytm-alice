#!/usr/bin/env python3
"""Exercise the native string-only sandbox without YouTube or a Station."""
import argparse
import json
from pathlib import Path
import struct
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('binary')
args = parser.parse_args()

def run(script, data='{}'):
    values = [script.encode(), data.encode()]
    raw = b''.join(struct.pack('<I', len(value))+value for value in values)
    return subprocess.run([args.binary], input=raw, capture_output=True, timeout=30)

result = run('JSON.stringify({std:typeof std,os:typeof os,require:typeof require,process:typeof process,input:JSON.parse(inputJSON)})', '{"value":"тест"}')
assert result.returncode == 0, result.returncode
assert json.loads(result.stdout) == {'std':'undefined','os':'undefined','require':'undefined','process':'undefined','input':{'value':'тест'}}
assert not result.stderr
assert run('throw new Error("signed-url-must-not-be-logged")').stdout == b''
assert run('throw new Error("signed-url-must-not-be-logged")').stderr == b''
assert subprocess.run([args.binary], input=struct.pack('<I', 0xffffffff), capture_output=True, timeout=5).returncode == 2

script = (Path(__file__).parents[1]/'resolver/solver.js').read_text()
result = run(script, json.dumps({'type':'player','player':'(function(){var x=1;}).call(this);','requests':[]}))
assert result.returncode == 0, result.returncode
assert json.loads(result.stdout) == {'type':'result','responses':[]}
print('Resolver: framed input, bundled EJS, bounded input and no host APIs or exception leaks OK')
