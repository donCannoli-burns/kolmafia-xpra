import importlib.util
import json
import os
from pathlib import Path
import tempfile
import threading
import unittest
from urllib.request import urlopen, Request
from urllib.error import HTTPError
from urllib.parse import quote

HERE = Path(__file__).resolve().parents[1] / 'dashboard' / 'api.py'
spec = importlib.util.spec_from_file_location('dashboard_api', HERE)
api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(api)


class DataBrowserTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / 'scripts').mkdir()
        (self.root / 'settings').mkdir()
        (self.root / 'scripts' / 'hello.ash').write_text('print("hello");\n')
        (self.root / 'settings' / 'GLOBAL_prefs.txt').write_text('private-key')
        (self.root / 'scripts' / 'secret-token.txt').write_text('secret-key')
        (self.root / 'scripts' / 'binary.png').write_bytes(b'\x00hello')
        (self.root / 'scripts' / 'escape.ash').symlink_to(self.root / 'settings' / 'GLOBAL_prefs.txt')
        self.server = api.make_server(str(self.root), 0)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown(); self.server.server_close(); self.thread.join()
        self.tmp.cleanup()

    def get(self, path, method='GET'):
        req = Request(f'http://127.0.0.1:{self.server.server_port}{path}', method=method)
        try:
            with urlopen(req, timeout=3) as response:
                return response.status, json.load(response)
        except HTTPError as response:
            return response.code, json.load(response)

    def test_roots_are_allowlisted(self):
        status, result = self.get('/tree?path=')
        self.assertEqual(status, 200)
        self.assertEqual([x['name'] for x in result['entries']], ['scripts'])

    def test_file_preview(self):
        status, result = self.get('/file?path=scripts%2Fhello.ash')
        self.assertEqual(status, 200)
        self.assertEqual(result['content'], 'print("hello");\n')

    def test_hides_sensitive_and_symlinks(self):
        status, result = self.get('/tree?path=scripts')
        self.assertEqual(status, 200)
        self.assertEqual([x['name'] for x in result['entries']], ['binary.png', 'hello.ash'])
        for path in ('scripts/escape.ash','scripts/secret-token.txt','settings/GLOBAL_prefs.txt','scripts/../settings/GLOBAL_prefs.txt'):
            status, _ = self.get('/file?path=' + quote(path))
            self.assertNotEqual(status, 200, path)

    def test_disallows_other_methods_and_binary(self):
        self.assertEqual(self.get('/tree', 'POST')[0], 405)
        self.assertEqual(self.get('/file?path=scripts%2Fbinary.png')[0], 415)

    def test_no_absolute_paths(self):
        for path in ('/etc/passwd', '../etc/passwd', 'scripts//hello.ash', 'scripts/./hello.ash', 'scripts\\hello.ash'):
            status, _ = self.get('/tree?path=' + quote(path))
            self.assertNotEqual(status, 200, path)


if __name__ == '__main__':
    unittest.main()
