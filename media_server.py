"""Tiny local HTTP server that streams original photos/videos to the browser for full-screen viewing.

Listens on 127.0.0.1 only and requires a random per-process token, so other websites open in the browser
can't use it. On the server: PS_MEDIA_BIND=0.0.0.0:8502 and PS_MEDIA_URL=http://<server>:8502 (the address the
browser uses); the token still guards it. Browser-native images are sent as-is, HEIC/RAW/TIFF are converted to JPEG on the fly,
videos are streamed with HTTP Range support (seeking works).
"""
import http.server
import mimetypes
import os
import re
import secrets
import sqlite3
import threading
import urllib.parse
from pathlib import Path

import cv2

import photosorter as ps

BROWSER_IMAGE = {".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"}
VIDEO_TYPES = {".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm",
               ".mkv": "video/x-matroska", ".3gp": "video/3gpp", ".avi": "video/x-msvideo"}
MAX_CONVERTED = 3000  # px, long side of converted (HEIC/RAW) images


def start(db_path):
    """Start the server in a daemon thread; returns (base_url, token)."""
    token = secrets.token_urlsafe(24)

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            url = urllib.parse.urlparse(self.path)
            q = urllib.parse.parse_qs(url.query)
            if url.path != "/file" or not secrets.compare_digest(q.get("t", [""])[0], token):
                return self.send_error(403)
            try:
                fid = int(q["id"][0])
            except (KeyError, ValueError):
                return self.send_error(400)
            con = sqlite3.connect(db_path)
            row = con.execute("SELECT path FROM files WHERE id = ?", (fid,)).fetchone()
            con.close()
            if not row or not os.path.exists(row[0]):
                return self.send_error(404)
            path = row[0]
            ext = Path(path).suffix.lower()
            try:
                if ext in VIDEO_TYPES:
                    self._send_file(path, VIDEO_TYPES[ext])
                elif ext in BROWSER_IMAGE:
                    self._send_file(path, mimetypes.guess_type(path)[0] or "image/jpeg")
                else:  # HEIC, RAW, TIFF: decode and re-encode for the browser
                    img = ps.load_image(Path(path))
                    s = MAX_CONVERTED / max(img.shape[:2])
                    if s < 1:
                        img = cv2.resize(img, None, fx=s, fy=s, interpolation=cv2.INTER_AREA)
                    data = cv2.imencode(".jpg", img, [cv2.IMWRITE_JPEG_QUALITY, 90])[1].tobytes()
                    self._send_bytes(data, "image/jpeg")
            except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                pass  # the viewer moved on to another file

        def _headers(self, status, ctype, length, extra=()):
            self.send_response(status)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(length))
            self.send_header("Accept-Ranges", "bytes")
            self.send_header("Cache-Control", "private, max-age=3600")
            for k, v in extra:
                self.send_header(k, v)
            self.end_headers()

        def _send_bytes(self, data, ctype):
            self._headers(200, ctype, len(data))
            self.wfile.write(data)

        def _send_file(self, path, ctype):
            size = os.path.getsize(path)
            start, end, status, extra = 0, size - 1, 200, ()
            m = re.match(r"bytes=(\d*)-(\d*)$", self.headers.get("Range", ""))
            if m and (m[1] or m[2]):
                if m[1]:
                    start = int(m[1])
                    end = min(int(m[2]), size - 1) if m[2] else size - 1
                else:  # suffix range: last N bytes
                    start = max(0, size - int(m[2]))
                if start > end:
                    self.send_response(416)
                    self.send_header("Content-Range", f"bytes */{size}")
                    self.end_headers()
                    return
                status, extra = 206, (("Content-Range", f"bytes {start}-{end}/{size}"),)
            self._headers(status, ctype, end - start + 1, extra)
            with open(path, "rb") as f:
                f.seek(start)
                left = end - start + 1
                while left > 0:
                    chunk = f.read(min(left, 1 << 20))
                    if not chunk:
                        break
                    self.wfile.write(chunk)
                    left -= len(chunk)

    host, port = os.environ.get("PS_MEDIA_BIND", "127.0.0.1:0").rsplit(":", 1)
    server = http.server.ThreadingHTTPServer((host, int(port)), Handler)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return os.environ.get("PS_MEDIA_URL", f"http://127.0.0.1:{server.server_address[1]}"), token
