#!/usr/bin/env python3
"""Local Kuyase instances, with isolated /data and loopback-only ports."""
import argparse
import hashlib
import json
import socket
import struct
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request

REPO = Path(__file__).resolve().parents[2]
LAB = Path(os.environ.get('YTM_KUYASE_HOME', REPO/'.build/kuyase-lab')).resolve()
RUNTIME = LAB/'runtime'
MODELS = json.loads(Path(__file__).with_name('models.json').read_text())
VERSION = '1.4.0'
BASE = f'https://github.com/vypivshiy/kuyase/releases/download/{VERSION}'
APP = '/data/ytm-lab'
ENV = f'YTM_ALICE_DATA={APP}/data YAM_ROOT={APP} YTM_ALICE_CAST=0'


def run(*args, **kw):
    return subprocess.run(list(map(str, args)), check=True, **kw)


def capture(*args, **kw):
    return subprocess.check_output(list(map(str, args)), text=True, **kw).strip()


def unit(board):
    return f'ytm-kuyase-{board}.service'


def active(board):
    return subprocess.run(['systemctl', '--user', 'is-active', '--quiet', unit(board)]).returncode == 0


def adb_port(board):
    return 15555+MODELS[board]['offset']


def adb(board, *args, **kw):
    endpoint = f'127.0.0.1:{adb_port(board)}'
    state = subprocess.run(['adb', '-s', endpoint, 'get-state'], capture_output=True, text=True, timeout=10)
    if state.returncode != 0 or state.stdout.strip() != 'device':
        # The host ADB server may retain an offline transport after a VM reboot.
        subprocess.run(['adb', 'disconnect', endpoint], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
        run('adb', 'connect', endpoint, stdout=subprocess.DEVNULL, timeout=15)
        run('adb', '-s', endpoint, 'wait-for-device', timeout=20)
    return capture('adb', '-s', endpoint, *args, **kw)


def shell(board, command, **kw):
    # Old firmware adbd lacks shell_v2 and otherwise hides guest exit codes.
    wrapped = '(\n'+command+'\n); __ytm_rc=$?; printf \"\\n__YTM_LAB_EXIT=%d\\n\" \"$__ytm_rc\"'
    output = adb(board, 'shell', wrapped, **kw)
    clean, marker, code = output.rpartition('__YTM_LAB_EXIT=')
    if not marker or code.strip() != '0':
        raise RuntimeError(f'Guest command failed ({code.strip() if marker else "no exit marker"}): {clean.strip()}')
    return clean.strip()


def bootstrap():
    for name in ('qemu-system-aarch64', 'adb', 'mkfs.ext4', 'debugfs', 'e2fsck', 'systemd-run'):
        if not shutil.which(name):
            raise RuntimeError(f'Missing dependency: {name}')
    downloads = LAB/'downloads'
    downloads.mkdir(parents=True, exist_ok=True)
    for name in ('SHA256SUMS.txt', 'kuyase-linux64-portable.tar.gz', 'kuyase-kernel-arm64.img'):
        path = downloads/name
        if not path.exists():
            print('Download:', name, flush=True)
            urllib.request.urlretrieve(BASE+'/'+name, path.with_suffix(path.suffix+'.part'))
            path.with_suffix(path.suffix+'.part').replace(path)
    sums = dict((line.split()[1], line.split()[0]) for line in (downloads/'SHA256SUMS.txt').read_text().splitlines())
    for name in ('kuyase-linux64-portable.tar.gz', 'kuyase-kernel-arm64.img'):
        actual = hashlib.file_digest((downloads/name).open('rb'), 'sha256').hexdigest()
        if actual != sums[name]:
            raise RuntimeError(f'Checksum mismatch: {name}')
    RUNTIME.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=LAB) as staging:
        with tarfile.open(downloads/'kuyase-linux64-portable.tar.gz') as archive:
            archive.extractall(staging, filter='data')
        for source in Path(staging).rglob('*'):
            target = RUNTIME/source.relative_to(staging)
            if source.is_dir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                temp = target.with_name(target.name+'.new')
                shutil.copy2(source, temp)
                temp.replace(target)
    (RUNTIME/'work').mkdir(exist_ok=True)
    (LAB/'reports').mkdir(exist_ok=True)
    print('Kuyase', VERSION, 'ready:', RUNTIME)


def import_image(board, path):
    if active(board):
        raise RuntimeError(f'Stop {board} before importing an image')
    destination = RUNTIME/'work'/f'{board}-system.ext4'
    if destination.exists():
        raise RuntimeError(f'Image already exists: {destination}')
    if path.suffix == '.ext4':
        shutil.copyfile(path, destination)
    else:
        # Published archives contain sparse-ready system images; do not extract
        # arbitrary firmware paths or device credentials into the host tree.
        with tarfile.open(path) as archive:
            members = [m for m in archive if m.isfile() and m.name.endswith('.ext4') and 'data' not in Path(m.name).name]
            if len(members) != 1:
                raise RuntimeError('Expected exactly one system .ext4. For raw dumps use kuyase prepare first.')
            member = members[0]
            temp = destination.with_suffix('.part')
            try:
                with archive.extractfile(member) as source, temp.open('wb') as target:
                    # Preserve zero runs as holes, avoiding multi-GB disk use.
                    while block := source.read(1024*1024):
                        if block.count(0) == len(block):
                            target.seek(len(block), 1)
                        else:
                            target.write(block)
                    target.truncate()
                temp.replace(destination)
            finally:
                temp.unlink(missing_ok=True)
    result = subprocess.run(['e2fsck', '-fn', str(destination)], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    if result.returncode != 0:
        destination.unlink()
        raise RuntimeError('Invalid ext4 image: '+result.stdout[-2000:])
    sha = hashlib.file_digest(path.open('rb'), 'sha256').hexdigest()
    manifest_path = LAB/'images.json'
    manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else {}
    manifest[board] = {'archive': str(path), 'sha256': sha, 'source': f'https://t.me/c/4457409211/{MODELS[board]["message"]}', 'image': str(destination)}
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2)+'\n')
    print(destination)


def start(board, clean=False):
    if active(board):
        print(board, 'already running')
        return
    if not (RUNTIME/'work'/f'{board}-system.ext4').exists():
        raise RuntimeError(f'Import image first: {board}')
    subprocess.run(['systemctl', '--user', 'reset-failed', unit(board)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    command = [str(RUNTIME/'kuyase'), 'run', '--headless', '--board', board, '--mode', 'stub', '--snapshot', '--keep-alive', '--port-offset', str(MODELS[board]['offset']), '--no-persist-data' if clean else '--persist-data']
    run('systemd-run', '--user', '--collect', '--unit', unit(board), '--property', f'WorkingDirectory={RUNTIME}', '--property', 'KillMode=control-group', '--property', 'TimeoutStopSec=15', '--setenv', 'KUYASE_SMP=2', '--setenv', 'KUYASE_MEM=512M', '--setenv', 'KUYASE_BIND_ALL=0', '--setenv', 'KUYASE_LAN_TAP=', *command)
    invocation = capture('systemctl', '--user', 'show', unit(board), '-p', 'InvocationID', '--value')
    deadline = time.monotonic()+200
    while time.monotonic() < deadline:
        if not active(board):
            raise RuntimeError(capture('journalctl', '--user', '-u', unit(board), '-n', '30', '--no-pager'))
        logs = capture('journalctl', '--user', f'_SYSTEMD_INVOCATION_ID={invocation}', '-n', '8', '--no-pager', '-o', 'cat')
        if f"Headless VM '{board}' READY" in logs:
            # Firmware rootfs is a snapshot: /data hooks are persistent, but
            # stock init does not discover them. Start the lab payload here.
            shell(board, 'test ! -x /data/ytm-lab-start.sh || sh /data/ytm-lab-start.sh', timeout=45)
            print(board, 'ready')
            return
        time.sleep(1)
    raise RuntimeError('Boot timeout: '+board)


def stop(board):
    if active(board):
        # QEMU termination cannot flush the guest kernel's dirty page cache.
        # Explicitly commit /data before stopping the service's process group.
        shell(board, 'sync', timeout=30)
        run('systemctl', '--user', 'stop', unit(board))


def deploy(board, package):
    if not active(board):
        raise RuntimeError('Start model first: '+board)
    payload = LAB/'payload'
    payload.mkdir(parents=True, exist_ok=True)
    # Package format is zstd tar. Extract only its application binaries;
    # package target compatibility and YAM lifecycle are separate tests.
    names = ['ytm-alice', 'ytm-alice-player', 'ytm-alice-resolver']
    members = set(capture('tar', '--zstd', '-tf', package).splitlines())
    native = ['ytm-alice-player-armhf', 'ytm-alice-player-arm64']
    available = [name for name in native if 'files/bin/'+name in members]
    if available and len(available) != len(native):
        raise RuntimeError('Incomplete dual-ABI player payload')
    names.extend(available)
    run('tar', '--zstd', '-xf', package, '-C', payload, *('files/bin/'+name for name in names))
    shell(board, f'mkdir -p {APP}/bin {APP}/data; {ENV} {APP}/bin/ytm-alice daemon stop 2>/dev/null || true')
    for name in names:
        path = payload/'files/bin'/name
        adb(board, 'push', path, f'{APP}/bin/{name}.new', timeout=90)
        actual = shell(board, f'sha256sum {APP}/bin/{name}.new').split()[0]
        if actual != hashlib.file_digest(path.open('rb'), 'sha256').hexdigest():
            raise RuntimeError('Guest checksum mismatch: '+name)
    shell(board, ' && '.join(f'chmod 755 {APP}/bin/{name}.new && mv {APP}/bin/{name}.new {APP}/bin/{name}' for name in names))
    settings = json.dumps({'web_addr': ':8090', 'radio': False, 'announce_text': '', 'announce_errors': False, 'playback_led': False}, separators=(',', ':'))
    print(shell(board, f'chmod 755 {APP}/bin/*; test -f {APP}/data/settings.json || printf %s {shlex.quote(settings)} > {APP}/data/settings.json; {ENV} {APP}/bin/ytm-alice daemon start', timeout=45))
    # Launch app from persistent /data after each guest boot; the base image stays clean.
    init = f'#!/bin/sh\n{ENV} {APP}/bin/ytm-alice daemon start >>/tmp/ytm-lab-boot.log 2>&1\n'
    shell(board, f'printf %s {shlex.quote(init)} > /data/ytm-lab-start.sh; chmod 755 /data/ytm-lab-start.sh; sync')
    print(shell(board, f'{ENV} {APP}/bin/ytm-alice --version'))
    print('Panel:', f'http://127.0.0.1:{18090+MODELS[board]["offset"]}')


def bridge_state(board):
    def receive(stream, count):
        data = bytearray()
        while len(data) < count:
            chunk = stream.recv(count-len(data))
            if not chunk:
                raise RuntimeError('Hardware bridge disconnected')
            data.extend(chunk)
        return data
    with socket.create_connection(('127.0.0.1', 18091+MODELS[board]['offset']), timeout=10) as stream:
        kind, length = struct.unpack('<BI', receive(stream, 5))
        if kind != 1 or length > 16*1024*1024:
            raise RuntimeError('Invalid bridge StateSnapshot')
        return json.loads(receive(stream, length))


def native_audio(board):
    fixture = LAB/'tone.m4a'
    if not fixture.exists():
        run('ffmpeg', '-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'sine=frequency=440:duration=4', '-c:a', 'aac', '-b:a', '48k', '-movflags', '+faststart', '-y', fixture)
    # Some stock images lack inetd/httpd. Use the same static helper everywhere
    # so fixture delivery is independent of firmware applets and libc versions.
    helper = LAB/'downloads/busybox-armv7l'
    helper_sha = 'cd04052b8b6885f75f50b2a280bfcbf849d8710c8e61d369c533acf307eda064'
    if not helper.exists():
        temporary = helper.with_suffix('.part')
        urllib.request.urlretrieve('https://busybox.net/downloads/binaries/1.31.0-defconfig-multiarch-musl/busybox-armv7l', temporary)
        if hashlib.file_digest(temporary.open('rb'), 'sha256').hexdigest() != helper_sha:
            temporary.unlink()
            raise RuntimeError('Fixture HTTP helper checksum mismatch')
        temporary.replace(helper)
    if hashlib.file_digest(helper.open('rb'), 'sha256').hexdigest() != helper_sha:
        raise RuntimeError('Fixture HTTP helper checksum mismatch')
    shell(board, 'mkdir -p /tmp/ytm-fixture/www', timeout=15)
    adb(board, 'push', fixture, '/tmp/ytm-fixture/www/tone.m4a', timeout=20)
    adb(board, 'push', helper, '/tmp/ytm-fixture/busybox', timeout=20)
    shell(board, 'chmod 755 /tmp/ytm-fixture/busybox; setsid /tmp/ytm-fixture/busybox httpd -f -p 127.0.0.1:18884 -h /tmp/ytm-fixture/www </dev/null >/tmp/ytm-fixture/http.log 2>&1 & echo $! > /tmp/ytm-fixture/http.pid; sleep 1; kill -0 "$(cat /tmp/ytm-fixture/http.pid)"', timeout=15)
    try:
        command = f'(sleep 8; echo stop) | LD_LIBRARY_PATH=/system/vendor/quasar:/system/vendor/quasar/libs {APP}/bin/ytm-alice-player http://127.0.0.1:18884/tone.m4a'
        output = shell(board, command, timeout=30)
        events = [json.loads(line) for line in output.splitlines() if line.startswith('{')]
        if not any(e.get('event') == 'playing' for e in events) or not any(e.get('event') == 'position' and e.get('position_ms', 0) > 0 for e in events):
            raise RuntimeError('Native player did not play the AAC fixture: '+output)
        return events
    finally:
        shell(board, 'test ! -f /tmp/ytm-fixture/http.pid || kill \"$(cat /tmp/ytm-fixture/http.pid)\"; rm -f /tmp/ytm-fixture/http.pid', timeout=10)


def smoke(board):
    report = {'board': board, 'time': time.strftime('%Y-%m-%dT%H:%M:%S%z'), 'checks': {}}
    def check(name, fn):
        try:
            report['checks'][name] = {'ok': True, 'output': fn()}
        except Exception as error:
            report['checks'][name] = {'ok': False, 'error': str(error)}
    check('firmware', lambda: shell(board, 'uname -m; echo busybox_ELF_class; od -An -tu1 -j4 -N1 /bin/busybox; echo maind_ELF_class; od -An -tu1 -j4 -N1 /system/vendor/quasar/maind; cat /proc/device-tree/model; cat /proc/asound/cards', timeout=20))
    check('version', lambda: shell(board, f'{ENV} {APP}/bin/ytm-alice --version', timeout=20))
    check('status', lambda: shell(board, f'{ENV} {APP}/bin/ytm-alice status', timeout=20))
    for endpoint in ('info', 'state'):
        def http_get(endpoint=endpoint):
            with urllib.request.urlopen(f'http://127.0.0.1:{18090+MODELS[board]["offset"]}/api/{endpoint}', timeout=20) as response:
                return json.load(response)
        check('web_'+endpoint, http_get)
    def bridge():
        state = bridge_state(board)
        if state.get('board_slug') != board or state.get('ipc_error_count') != 0 or state.get('led_trigger_conflict_count') != 0:
            raise RuntimeError(json.dumps(state))
        return state
    check('native_aac_alsa', lambda: native_audio(board))
    check('tts_ipc', lambda: shell(board, f'{ENV} {APP}/bin/ytm-alice say Тест', timeout=25))
    check('hardware_bridge', bridge)
    report['ok'] = all(c['ok'] for c in report['checks'].values())
    (LAB/'reports').mkdir(exist_ok=True)
    (LAB/'reports'/f'{board}.json').write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
    for name, value in report['checks'].items():
        print(board, name, 'PASS' if value['ok'] else 'FAIL', value.get('error', ''))
    return report['ok']


def report_matrix():
    reports = LAB/'reports'
    reports.mkdir(parents=True, exist_ok=True)
    lifecycle_path = reports/'lifecycle.json'
    lifecycle = json.loads(lifecycle_path.read_text()) if lifecycle_path.exists() else {}
    expansion_path = reports/'expansion.json'
    expansion = json.loads(expansion_path.read_text()) if expansion_path.exists() else {}
    legacy_logs = {'yandexmicro': 'micro', 'yandexmini2': 'mini2'}
    summary = {}
    rows = ['# Матрица стенда kuyase', '', f'Сформировано: {time.strftime("%Y-%m-%d %H:%M:%S %z")}. Kuyase {VERSION}.', '',
            'Результаты относятся к последнему прогону каждой модели. Колонка «ВМ» отражает текущее состояние службы.', '',
            '| Модель | Профиль | ВМ | CLI / HTTP | AAC → ALSA | TTS | Перезапуск | Самотест kuyase | Панель |',
            '| --- | --- | --- | --- | --- | --- | --- | --- | --- |']
    def label(value):
        return 'OK' if value is True else 'ошибка' if value is False else '—'
    for board, model in MODELS.items():
        path = reports/f'{board}.json'
        result = json.loads(path.read_text()) if path.exists() else {}
        checks = result.get('checks', {})
        app_checks = [checks.get(k, {}).get('ok') for k in ('version', 'status', 'web_info', 'web_state')]
        app_ok = all(app_checks) if all(v is not None for v in app_checks) else None
        lifecycle_ok = expansion.get(board, {}).get('lifecycle_ok', lifecycle.get(board, {}).get('ok'))
        test_log = LAB/f'test-{legacy_logs.get(board, board)}.log'
        text = test_log.read_text() if test_log.exists() else ''
        emulator_ok = True if 'ALL KUYASE STANDALONE EMULATOR TESTS PASSED' in text else False if 'FAILED' in text else None
        running = active(board)
        url = f'http://127.0.0.1:{18090+model["offset"]}'
        summary[board] = {'name': model['name'], 'running': running, 'url': url, 'check_time': result.get('time'),
                          'app_ok': app_ok, 'native_audio_ok': checks.get('native_aac_alsa', {}).get('ok'),
                          'tts_ok': checks.get('tts_ipc', {}).get('ok'), 'lifecycle_ok': lifecycle_ok, 'emulator_test_ok': emulator_ok,
                          'checks': {k: v['ok'] for k, v in checks.items()}}
        row = summary[board]
        rows.append(f'| {model["name"]} | `{board}` | {"работает" if running else "остановлена"} | {label(app_ok)} | {label(row["native_audio_ok"])} | {label(row["tts_ok"])} | {label(lifecycle_ok)} | {label(emulator_ok)} | [открыть]({url}) |')
    rows.extend(['', 'Подробности ошибок находятся в JSON-отчётах моделей и test-*.log рядом с каталогом reports.', ''])
    (reports/'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2)+'\n')
    (reports/'matrix.md').write_text('\n'.join(rows))
    print(reports/'matrix.md')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['bootstrap', 'list', 'import', 'start', 'stop', 'status', 'shell', 'deploy', 'smoke', 'test', 'logs', 'report'])
    parser.add_argument('board', nargs='?', choices=[*MODELS, 'all'])
    parser.add_argument('arguments', nargs='*')
    parser.add_argument('--clean', action='store_true', help='Temporary /data; writes disappear when VM stops')
    args = parser.parse_args()
    if args.action == 'bootstrap':
        bootstrap()
        return
    if args.action == 'report':
        report_matrix()
        return
    if args.action in ('list', 'status'):
        for board, model in MODELS.items():
            ready = (RUNTIME/'work'/f'{board}-system.ext4').exists()
            if ready or args.action == 'list':
                print(f'{board:16} {"running" if active(board) else "stopped":8} {"image" if ready else "missing":7} adb=127.0.0.1:{adb_port(board)} web=http://127.0.0.1:{18090+model["offset"]}  {model["name"]}')
        return
    if args.board is None:
        parser.error('board required')
    boards = [b for b in MODELS if (RUNTIME/'work'/f'{b}-system.ext4').exists()] if args.board == 'all' else [args.board]
    if not boards:
        raise RuntimeError('No imported images')
    success = True
    for board in boards:
        if args.action == 'start':
            start(board, args.clean)
            if not args.clean:
                print(shell(board, 'test ! -x /data/ytm-lab-start.sh || /data/ytm-lab-start.sh', timeout=45))
        elif args.action == 'stop':
            stop(board)
        elif args.action == 'import':
            if args.board == 'all' or len(args.arguments) != 1:
                parser.error('import requires one board and one archive path')
            import_image(board, Path(args.arguments[0]).resolve())
        elif args.action == 'shell':
            if len(args.arguments) != 1:
                parser.error('shell requires one quoted command')
            print(shell(board, args.arguments[0], timeout=300))
        elif args.action == 'deploy':
            if len(args.arguments) != 1:
                parser.error('deploy requires a .yampkg path')
            deploy(board, Path(args.arguments[0]).resolve())
        elif args.action == 'smoke':
            success = smoke(board) and success
        elif args.action == 'test':
            if active(board):
                raise RuntimeError('Stop model before the emulator self-test: '+board)
            run(RUNTIME/'kuyase', 'test', '--board', board, '--snapshot', '--port-offset', str(MODELS[board]['offset']), cwd=RUNTIME)
        elif args.action == 'logs':
            run('journalctl', '--user', '-u', unit(board), '-n', '60', '--no-pager')
    if not success:
        raise SystemExit(1)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, subprocess.SubprocessError) as error:
        sys.exit(str(error))
