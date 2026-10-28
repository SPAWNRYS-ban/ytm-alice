"""HTTP transport, credential boundaries and user-local installation."""
import contextlib
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('remote_cli', ROOT/'remote-cli.py')
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


@contextlib.contextmanager
def server(responder):
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            # Drain POST data before closing, otherwise macOS may reset the
            # socket and discard an error response with an unread request body.
            self.payload = self.rfile.read(int(self.headers.get('Content-Length', 0)))
            responder(self)
        def log_message(self, *_):
            pass
    http = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    worker = threading.Thread(target=http.serve_forever, daemon=True)
    worker.start()
    try:
        yield 'http://127.0.0.1:'+str(http.server_port)
    finally:
        http.shutdown()
        http.server_close()
        worker.join()


class HTTPTests(unittest.TestCase):
    def test_commands_and_auth_cross_the_json_body_boundary(self):
        received = []
        def reply(handler):
            received.append((handler.path, dict(handler.headers), json.loads(handler.payload)))
            body = json.dumps({'state': {'status': 'paused'}}).encode()
            handler.send_response(200)
            handler.end_headers()
            handler.wfile.write(body)
        with server(reply) as url:
            client = cli.Client(base_url=url)
            self.addCleanup(client.close)
            self.assertEqual(client.request('auth-import', auth={'cookie':'SAPISID=test-only'})['state']['status'], 'paused')
            self.assertIsNone(client.process)
        path, headers, body = received[0]
        self.assertEqual(path, '/api/command')
        self.assertEqual(headers['Origin'], url)
        self.assertEqual(headers['X-Ytm-Client'], 'web')
        self.assertNotIn('test-only', str(headers))
        self.assertEqual(body['auth']['cookie'], 'SAPISID=test-only')

    def test_errors_keep_provider_message_and_redirects_never_forward_credentials(self):
        received = []
        def destination(handler):
            received.append(True)
            handler.send_response(200)
            handler.end_headers()
            handler.wfile.write(b'{}')
        with server(destination) as target:
            def redirect(handler):
                handler.send_response(307)
                handler.send_header('Location', target+'/stolen')
                handler.end_headers()
                handler.wfile.write(b'{"error":"redirect denied"}')
            with server(redirect) as url:
                client = cli.Client(base_url=url)
                self.addCleanup(client.close)
                with self.assertRaisesRegex(cli.ClientError, 'redirect denied'):
                    client.request('auth-import', auth={'cookie':'test-only'})
        self.assertEqual(received, [])

    def test_malformed_server_response_is_reported_without_echoing_it(self):
        def reply(handler):
            handler.send_response(502)
            handler.end_headers()
            handler.wfile.write(b'not-json secret-placeholder')
        with server(reply) as url:
            client = cli.Client(base_url=url)
            self.addCleanup(client.close)
            with self.assertRaises(cli.ClientError) as error:
                client.request()
            self.assertNotIn('secret-placeholder', str(error.exception))

    def test_config_roundtrip_and_invalid_urls(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/'nested/config.json'
            with patch.dict(os.environ, {'YTM_ALICE_CONFIG':str(path), 'YTM_ALICE_URL':''}):
                self.assertEqual(cli.connection_url(), 'http://alice.lan')
                cli.save_connection('http://station.local:8080/')
                self.assertEqual(cli.connection_url(), 'http://station.local:8080')
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                path.write_text('{"url":42}')
                with self.assertRaises(cli.ClientError):cli.connection_url()
        for value in (None, 7, 'https://alice.lan', 'http://user:pass@alice.lan',
                      'http://alice.lan:0', 'http://alice.lan:65536',
                      'http://alice.lan/path', 'http://alice.lan?x=1', 'http://alice.lan\n'):
            with self.subTest(value=value), self.assertRaises(cli.ClientError):
                cli.normalize_url(value)


class InstallerTests(unittest.TestCase):
    def install(self, directory, valid):
        release = directory/'release'
        release.mkdir(exist_ok=True)
        client = ROOT.joinpath('remote-cli.py').read_bytes()
        (release/'ytm-alice-cli.py').write_bytes(client)
        digest = hashlib.sha256(client).hexdigest() if valid else '0'*64
        (release/'SHA256SUMS').write_text(digest+'  ytm-alice-cli.py\n')
        destination = directory/'bin'
        destination.mkdir(exist_ok=True)
        installed = destination/'ytm-alice'
        installed.write_text('old-client')
        environment = dict(os.environ, YTM_ALICE_RELEASE_URL=release.as_uri(),
                           YTM_ALICE_BIN_DIR=str(destination))
        result = subprocess.run(['sh', str(ROOT/'install-client.sh')], env=environment,
                                text=True, capture_output=True)
        return result, installed

    def test_checksum_failure_preserves_existing_installation(self):
        with tempfile.TemporaryDirectory() as directory:
            result, installed = self.install(Path(directory), False)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(installed.read_text(), 'old-client')
            self.assertEqual(list(installed.parent.iterdir()), [installed])

    def test_verified_client_is_installed_executable(self):
        with tempfile.TemporaryDirectory() as directory:
            result, installed = self.install(Path(directory), True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(installed.read_bytes(), ROOT.joinpath('remote-cli.py').read_bytes())
            version = subprocess.check_output([str(installed), '--version'], text=True)
            self.assertEqual(version.strip(), 'ytm-alice '+cli.VERSION)


if __name__ == '__main__':
    unittest.main()
