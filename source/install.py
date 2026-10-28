#!/usr/bin/env python3
"""One-command install of the YAM v2 artifact through existing root SSH access."""
import argparse
import json
from pathlib import Path
import shlex
import subprocess
import uuid

p = argparse.ArgumentParser(description='Установить ytm-alice .yampkg и автозапуск через существующий root SSH.')
p.add_argument('package', type=Path)
p.add_argument('--host', default='alice', help='SSH alias with existing root access')
p.add_argument('--ssh-json', type=Path, help='Optional existing SSH argument array')
a = p.parse_args()
if not a.package.is_file(): p.error('Архив пакета не найден')
existing = Path.home()/'.config/alice-cli/ssh.json'
if a.ssh_json is None and a.host == 'alice' and existing.is_file(): a.ssh_json = existing
ssh = json.loads(a.ssh_json.read_text()) if a.ssh_json else ['ssh', '-o', 'BatchMode=yes', a.host]
remote = '/tmp/ytm-alice-'+uuid.uuid4().hex+'.yampkg'
def run(command, **kw): return subprocess.run(ssh+[command], check=True, **kw)
with a.package.open('rb') as source:
    run('umask 077; cat > '+shlex.quote(remote), stdin=source)
try:
    # Stop the previous processes before YAM replaces their package payloads.
    # Persistent queue/session and media rollback markers are outside payloads.
    media_was_running = subprocess.run(ssh+[
        '[ -x /data/yam/bin/alice-media ] && /data/yam/bin/alice-media status >/dev/null 2>&1'
    ]).returncode == 0
    player_was_running = subprocess.run(ssh+[
        '[ -x /data/yam/bin/ytm-alice ] && /data/yam/bin/ytm-alice --json status >/dev/null 2>&1'
    ]).returncode == 0
    run('if [ -x /data/yam/bin/ytm-alice ]; then /data/yam/bin/ytm-alice daemon stop; fi')
    if media_was_running:
        run('/data/yam/bin/alice-media stop')
    try:
        run('yam install --yes '+shlex.quote(remote))
    except subprocess.CalledProcessError:
        if media_was_running:
            subprocess.run(ssh+['/data/yam/bin/alice-media start'])
        if player_was_running:
            subprocess.run(ssh+['/data/yam/bin/ytm-alice daemon start'])
        raise
    run('/data/yam/bin/ytm-alice daemon install')
    run('yam doctor && /data/yam/bin/ytm-alice status')
    run('/data/yam/bin/ytm-alice web status')
except subprocess.CalledProcessError:
    raise SystemExit('Установка или настройка не завершена; причина указана выше. '
                     'Очередь и сессия не удаляются. Проверьте yam list и yam doctor; '
                     'после успешной установки повторите ytm-alice daemon install.')
finally:
    subprocess.run(ssh+['rm -f '+shlex.quote(remote)])
print('Установлен единый пакет YAM v2: ytm-alice и встроенный медиаслой; автозапуск настроен.')
