"""Exercise the real desktop installer through an isolated SSH transport."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


FAKE_SSH = r'''
import json, os, sys
from pathlib import Path
path = Path(os.environ['YTM_INSTALL_TEST_STATE'])
state = json.loads(path.read_text())
command = sys.argv[-1]
state['commands'].append(command)
code = 0
if command.startswith('umask 077; cat > '):
    state['uploaded'] = bool(sys.stdin.buffer.read())
elif 'alice-media status' in command:
    code = 0 if state['media'] else 1
elif '--json status' in command:
    code = 0 if state['player'] else 1
elif 'daemon stop' in command:
    state['player'] = False
elif command == '/data/yam/bin/alice-media stop':
    state['media'] = False
elif command.startswith('yam install --yes '):
    code = 1 if state['reject_package'] else 0
    if not code:
        state['installed'] = True
elif command.endswith('ytm-alice daemon install'):
    state['media'] = True
    state['player'] = True
elif command.endswith('alice-media start'):
    state['media'] = True
elif command.endswith('ytm-alice daemon start'):
    state['player'] = True
elif command.startswith('rm -f '):
    state['cleaned'] = True
path.write_text(json.dumps(state))
sys.exit(code)
'''


class InstallTests(unittest.TestCase):
    def run_install(self, media=False, player=False, reject=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state_path = root/'state.json'
            state_path.write_text(json.dumps({
                'media': media, 'player': player, 'reject_package': reject,
                'commands': [], 'installed': False,
            }))
            transport = root/'ssh.py'
            transport.write_text(FAKE_SSH)
            ssh_json = root/'ssh.json'
            ssh_json.write_text(json.dumps([sys.executable, str(transport)]))
            package = root/'candidate.yampkg'
            package.write_bytes(b'opaque-package-uploaded-over-ssh')
            environment = dict(os.environ, YTM_INSTALL_TEST_STATE=str(state_path))
            result = subprocess.run([
                sys.executable, str(Path(__file__).with_name('install.py')),
                str(package), '--ssh-json', str(ssh_json),
            ], env=environment, capture_output=True, text=True)
            return result, json.loads(state_path.read_text())

    def test_clean_install_sets_up_both_components(self):
        result, state = self.run_install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(state['uploaded'] and state['installed'] and state['cleaned'])
        self.assertTrue(state['media'] and state['player'])

    def test_upgrade_stops_old_media_before_replacing_payload(self):
        result, state = self.run_install(media=True, player=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        commands = state['commands']
        replace = next(i for i, command in enumerate(commands) if command.startswith('yam install '))
        setup = commands.index('/data/yam/bin/ytm-alice daemon install')
        self.assertLess(commands.index('/data/yam/bin/alice-media stop'), replace)
        self.assertLess(replace, setup)

    def test_rejected_package_restores_running_services(self):
        result, state = self.run_install(media=True, player=True, reject=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(state['installed'])
        self.assertTrue(state['media'] and state['player'] and state['cleaned'])

    def test_rejected_package_keeps_previously_idle_services_idle(self):
        result, state = self.run_install(reject=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(state['media'] or state['player'] or state['installed'])
        self.assertTrue(state['cleaned'])


if __name__ == '__main__':
    unittest.main()
