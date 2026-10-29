#!/usr/bin/env python3
"""Reject a release with disagreeing source, package or tag versions."""
from pathlib import Path
import os
import re

root = Path(__file__).resolve().parents[1]
version = re.search(r'const version = "([0-9]+\.[0-9]+\.[0-9]+)"', (root/'main.go').read_text())[1]
assert re.search(r"VERSION = '([^']+)'", (root/'remote-cli.py').read_text())[1] == version
assert re.search(r'^version: (.+)$', (root/'yam.yaml').read_text(), re.M)[1] == version
if os.environ.get('GITHUB_REF_TYPE') == 'tag':
    assert os.environ['GITHUB_REF_NAME'] == 'v'+version, 'Tag must match the source version'
print(version)
