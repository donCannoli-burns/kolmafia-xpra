#!/usr/bin/env python3
"""Read-only dashboard browser over *selected* KoLmafia data subdirectories.

No external dependencies, no writes, no generic proxy, and no user-controlled commands.
Each directory component is opened with O_NOFOLLOW so symlinks cannot escape the mount.
"""
import json
import os
import stat
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

DATA_ROOT = os.environ.get('KOLMAFIA_DATA_ROOT', '/home/kolmafia/.kolmafia')
EXPOSE = ('scripts', 'data', 'relay', 'ccs', 'images')
DENY = frozenset(('settings', 'sessions', 'session', '.git', 'cookies',
                  'passwords', 'credentials', 'secrets', 'keys', 'tokens'))
SENSITIVE = ('password', 'passwd', 'secret', 'token', 'cookie', 'credential', 'private',
             'id_rsa', '_prefs', 'apikey', 'api_key', 'session')
TEXT_EXT = frozenset(('.ash', '.js', '.ts', '.json', '.txt', '.md', '.py', '.go',
                      '.html', '.css', '.xml', '.csv', '.yaml', '.yml', '.properties',
                      '.ini', '.ccs', '.java'))
MAX_FILE = 512 * 1024
MAX_ENTRIES = 250


class PublicError(Exception):
    def __init__(self, code, message):
        self.code, self.message = code, message
        super().__init__(message)


def parts_for(path):
    if not isinstance(path, str) or '\x00' in path or '\\' in path or path.startswith('/'):
        raise PublicError(400, 'Invalid path')
    if not path:
        return []
    pieces = path.split('/')
    if any(p in ('', '.', '..') or p.startswith('.') or p.casefold() in DENY or
           any(s in p.casefold() for s in SENSITIVE) for p in pieces):
        raise PublicError(403, 'Path is not exposed')
    if pieces[0] not in EXPOSE:
        raise PublicError(403, 'Root is not exposed')
    return pieces


def open_component_chain(root, pieces, final_directory=True):
    """Open each path segment relative to an already open directory FD (no symlinks)."""
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    try:
        fd = os.open(root, flags)
    except OSError:
        raise PublicError(503, 'Mounted data directory unavailable') from None
    try:
        for i, piece in enumerate(pieces):
            f = os.O_RDONLY | os.O_NOFOLLOW
            if i != len(pieces) - 1 or final_directory:
                f |= os.O_DIRECTORY
            try:
                next_fd = os.open(piece, f, dir_fd=fd)
            except OSError:
                raise PublicError(404, 'File or directory unavailable') from None
            os.close(fd)
            fd = next_fd
        return fd
    except BaseException:
        os.close(fd)
        raise


def safe_entry_name(name):
    value = name.casefold()
    return bool(name) and not name.startswith('.') and value not in DENY and not any(p in value for p in SENSITIVE)


def list_entries(root, path):
    pieces = parts_for(path)
    fd = open_component_chain(root, pieces)
    try:
        with os.scandir(fd) as it:
            entries = []
            for entry in it:
                if not safe_entry_name(entry.name):
                    continue
                if not pieces and entry.name not in EXPOSE:
                    continue
                # Both tests are no-follow; symlinks and special files are invisible.
                if entry.is_dir(follow_symlinks=False):
                    kind = 'directory'
                elif entry.is_file(follow_symlinks=False):
                    kind = 'file'
                else:
                    continue
                entries.append({'name': entry.name, 'type': kind,
                                'path': '/'.join((*pieces, entry.name))})
            entries.sort(key=lambda e: (e['type'] != 'directory', e['name'].casefold()))
            return entries[:MAX_ENTRIES], len(entries) > MAX_ENTRIES
    finally:
        os.close(fd)


def read_file(root, path):
    pieces = parts_for(path)
    if len(pieces) < 2:
        raise PublicError(400, 'Choose a file below a visible data folder')
    if os.path.splitext(pieces[-1])[1].lower() not in TEXT_EXT:
        raise PublicError(415, 'Only recognized text files can be previewed')
    parent = open_component_chain(root, pieces[:-1])
    try:
        try:
            fd = os.open(pieces[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        except OSError:
            raise PublicError(404, 'File unavailable') from None
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode):
                raise PublicError(415, 'Not a regular file')
            raw = os.read(fd, MAX_FILE + 1)
        finally:
            os.close(fd)
    finally:
        os.close(parent)
    if b'\x00' in raw:
        raise PublicError(415, 'Binary file preview disabled')
    return {'path': path, 'content': raw[:MAX_FILE].decode('utf-8', errors='replace'),
            'truncated': len(raw) > MAX_FILE}


def make_handler(root):
    class Handler(BaseHTTPRequestHandler):
        def send_json(self, code, payload):
            body = json.dumps(payload, ensure_ascii=False).encode('utf-8')
            self.send_response(code)
            self.send_header('Content-Type', 'application/json; charset=utf-8')
            self.send_header('Content-Length', str(len(body)))
            self.send_header('Cache-Control', 'no-store')
            self.send_header('X-Content-Type-Options', 'nosniff')
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            url = urlsplit(self.path)
            params = parse_qs(url.query, keep_blank_values=True)
            if url.path not in ('/tree', '/file', '/health'):
                return self.send_json(404, {'error': 'Unknown endpoint'})
            if url.path == '/health':
                return self.send_json(200, {'ok': True})
            value = params.get('path', [''])
            if len(value) != 1:
                return self.send_json(400, {'error': 'Specify one path'})
            try:
                if url.path == '/tree':
                    entries, truncated = list_entries(root, value[0])
                    self.send_json(200, {'entries': entries, 'truncated': truncated})
                else:
                    self.send_json(200, read_file(root, value[0]))
            except PublicError as e:
                self.send_json(e.code, {'error': e.message})
            except (OSError, ValueError):
                self.send_json(500, {'error': 'Data service failed'})

        def do_POST(self):
            self.send_json(405, {'error': 'Read-only API'})

        def log_message(self, fmt, *args):
            # Local network metadata only; do not log sensitive file contents.
            super().log_message(fmt, *args)

    return Handler


def make_server(root=DATA_ROOT, port=18081):
    return ThreadingHTTPServer(('127.0.0.1', port), make_handler(root))


if __name__ == '__main__':
    server = make_server()
    print(f'Dashboard data API listening on 127.0.0.1:{server.server_port}', flush=True)
    server.serve_forever()
