"""Exercise real line transport and the native-to-terminal state boundary."""
import importlib.util
import argparse
import sqlite3
import sys
import tempfile
import time
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('remote_cli', Path(__file__).with_name('remote-cli.py'))
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


class ClientTests(unittest.TestCase):
    def test_one_connection_handles_errors_without_losing_next_response(self):
        peer = '''import json,sys
for number,line in enumerate(sys.stdin):
    request=json.loads(line)
    response={'error':'нет такого трека'} if request['action']=='jump' else {'state':{'status':'paused'},'sequence':number}
    print(json.dumps(response),flush=True)
'''
        client = cli.Client([sys.executable, '-u', '-c', peer])
        self.addCleanup(client.close)
        self.assertEqual(client.request()['sequence'], 0)
        process = client.process
        with self.assertRaisesRegex(cli.ClientError, 'нет такого трека'):
            client.request('jump', index=999)
        self.assertEqual(client.request()['sequence'], 2)
        self.assertIs(client.process, process)
        client.close()
        self.assertIsNotNone(process.poll())

    def test_closed_connection_is_reported(self):
        client = cli.Client([sys.executable, '-c', 'pass'])
        self.addCleanup(client.close)
        with self.assertRaises(cli.ClientError):
            client.request()
        self.assertIsNone(client.process)
        # The UI can reconnect on its next refresh, without replaying a command.
        client.transport = [sys.executable, '-u', '-c',
                            "import json,sys; sys.stdin.readline(); print(json.dumps({'state':{'status':'paused'}}))"]
        self.assertEqual(client.request()['state']['status'], 'paused')

    def test_native_json_is_preserved_and_current_track_is_safe(self):
        native = {'state': {'status': 'playing', 'index': 1, 'position_ms': 1200,
                           'queue': [{'id': 'a'}, {'id': 'b'}], 'error': 'media unavailable'}}
        view = cli.panel_state(native)
        self.assertEqual(view['current']['id'], 'b')
        self.assertEqual(view['position'], 1)
        self.assertEqual(view['state'], 'playing')
        self.assertEqual(view['error'], 'media unavailable')
        self.assertNotIn('current', native['state'])
        native['state']['index'] = 100
        self.assertIsNone(cli.panel_state(native)['current'])
        self.assertEqual(cli.panel_state({'state': {'queue': None}})['queue'], [])


class LoginTests(unittest.TestCase):
    def test_firefox_filters_domains_containers_and_expired_cookies_even_when_locked(self):
        with tempfile.TemporaryDirectory() as folder:
            database = Path(folder)/'cookies.sqlite'
            db = sqlite3.connect(database)
            self.addCleanup(db.close)
            db.execute('CREATE TABLE moz_cookies(name TEXT,value TEXT,host TEXT,originAttributes TEXT,expiry INTEGER,lastAccessed INTEGER)')
            later = int(time.time())+10000
            db.executemany('INSERT INTO moz_cookies VALUES(?,?,?,?,?,?)', [
                ('SAPISID','youtube-test-session','.youtube.com','',later,1),
                ('SECRET','google-only','.google.com','',later,1),
                ('PRIVATE','another-container','.youtube.com','^userContextId=1',later,1),
                ('EXPIRED','old-session','.youtube.com','',1,1),
            ])
            db.commit()
            db.execute('BEGIN EXCLUSIVE')
            try:cookie = cli.firefox_cookie(Path(folder))
            finally:db.rollback()
            self.assertEqual(cookie, 'SAPISID=youtube-test-session')

    def test_header_import_regenerates_signature_instead_of_saving_old_authorization(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)/'browser.json'
            path.write_text('{"Cookie":"SAPISID=test-session", "Authorization":"old-signature", "X-Goog-AuthUser":"2"}')
            args = argparse.Namespace(cookies=None,headers=str(path),profile=None,account_index=None)
            auth = cli.login_credentials(args)
            self.assertEqual(auth, {'cookie':'SAPISID=test-session','auth_user':'2'})
            self.assertNotIn('old-signature', str(auth))

    def test_auth_is_sent_only_on_stdin_and_not_echoed_in_responses(self):
        peer = '''import json,sys
request=json.loads(sys.stdin.readline())
assert request['action']=='auth-import' and 'cookie' in request['auth']
print(json.dumps({'auth':{'configured':True,'checked':True,'valid':True}}),flush=True)
'''
        client = cli.Client([sys.executable, '-u', '-c', peer])
        self.addCleanup(client.close)
        result = client.request('auth-import',auth={'cookie':'SAPISID=test-only','auth_user':'0'})
        self.assertTrue(result['auth']['valid'])
        self.assertNotIn('test-only', str(result))
        self.assertFalse(any('test-only' in arg for arg in client.process.args))


if __name__ == '__main__':
    unittest.main()
