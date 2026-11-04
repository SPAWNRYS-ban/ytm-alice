#!/usr/bin/env python3
"""Fetch the pinned upstream amalgamation, verifying it before extraction."""
import hashlib
from pathlib import Path
import urllib.request
import zipfile
import io

URL = 'https://github.com/quickjs-ng/quickjs/releases/download/v0.18.0/quickjs-amalgam.zip'
SHA256 = '6c05066a6b62b483e2ba0f83d7a16688c6958182b214bfd693529434cef84a9e'
destination = Path(__file__).resolve().parent/'.build'
destination.mkdir(exist_ok=True)
with urllib.request.urlopen(URL, timeout=60) as response:
    data = response.read(16*1024*1024)
if hashlib.sha256(data).hexdigest() != SHA256:
    raise SystemExit('QuickJS checksum mismatch')
with zipfile.ZipFile(io.BytesIO(data)) as archive:
    for name in ('quickjs-amalgam.c', 'quickjs.h'):
        (destination/name).write_bytes(archive.read(name))
