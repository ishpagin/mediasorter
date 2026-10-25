"""Work queue for remote GPU workers (ps-worker on a Windows PC): they analyze files, only this server writes the DB.

`scan` puts new/changed files into the `work_queue` table and analyzes them itself unless a worker is active
(see cmd_scan). This server hands out batches with a short lease, renewed by the worker's heartbeat; a lost
worker's batch returns to the queue. Results (metadata, faces, previews, SigLIP2 vectors) are validated and
written with photosorter.store_file, exactly as `scan` + `scenes.py embed` would write them.

All requests: POST JSON, header "Authorization: Bearer <token>" (PS_QUEUE_TOKEN or the file queue_token next to the DB).
    /lease     {worker, n, version}       -> {lease, ttl, params, files: [{path (Windows UNC), kind}], left}
    /heartbeat {lease}                    -> {n}  (files still held; 0 = lease lost)
    /result    {lease, path, meta, faces, previews, error}
    /release   {lease}                    -> unfinished files go back to the queue at once
For the web UI (mediasorter-web), the ML and the long jobs it does not run itself:
    /embed_text {text}                    -> {vector: [768 floats]}   SigLIP2 text vector for search
    /convert    {path}                    -> {jpeg: base64}           HEIC / RAW / TIFF for the viewer
    /job        {name?}                   -> {name, running, progress, message, error}  name starts one of
                                             cluster | stacks_build | stacks_score (one at a time); none = status
With an empty queue a /lease may start /app/nightly.sh (at most every PS_QUEUE_AUTOSCAN_MIN minutes, 0 = never).
"""
import argparse
import base64
import fcntl
import hashlib
import http.server
import json
import math
import os
import secrets
import subprocess
import threading
import time
from pathlib import Path

import photosorter as ps
import scenes
import stacks

MAX_BODY = 64 << 20
AUTOSCAN_MIN = float(os.environ.get("PS_QUEUE_AUTOSCAN_MIN", "120"))


def code_version():
    """Hash of the analysis code: a worker must run the same photosorter.py / scenes.py (line endings ignored)."""
    h = hashlib.sha256()
    for name in ("photosorter.py", "scenes.py"):
        h.update((ps.ROOT / name).read_bytes().replace(b"\r\n", b"\n"))
    return h.hexdigest()[:16]


def load_token(db):
    if os.environ.get("PS_QUEUE_TOKEN"):
        return os.environ["PS_QUEUE_TOKEN"]
    f = Path(db).parent / "queue_token"
    if not f.exists():
        f.write_text(secrets.token_urlsafe(24))
        f.chmod(0o600)
    return f.read_text().strip()


class Bad(Exception):
    """Client error -> HTTP status + message."""

    def __init__(self, status, msg):
        super().__init__(msg)
        self.status = status


def _num(v, name, none=False):
    if v is None and none:
        return None
    if isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v):
        raise Bad(400, f"bad {name}")
    return v


def _b64(v, name, size=None, none=False):
    if v is None and none:
        return None
    try:
        b = base64.b64decode(v, validate=True)
    except Exception:
        raise Bad(400, f"bad {name}")
    if size is not None and len(b) != size:
        raise Bad(400, f"bad {name} size")
    return b


def parse_result(r):
    """Worker JSON -> (meta, faces, previews, clips); everything checked: it is written to the DB."""
    meta = {}
    for k, v in (r.get("meta") or {}).items():
        if k not in ps.FILE_META:
            raise Bad(400, f"unknown meta {k}")
        if not (v is None or isinstance(v, str) and len(v) < 500 or not isinstance(v, bool) and isinstance(v, int)):
            v = _num(v, k)
        meta[k] = v
    faces = []
    for f in r.get("faces") or []:
        box = f.get("box")
        if not isinstance(box, list) or len(box) != 4:
            raise Bad(400, "bad box")
        faces.append({"t": _num(f.get("t"), "t", none=True), "box": [_num(x, "box") for x in box],
                      "det_score": _num(f.get("det_score"), "det_score"), "size": _num(f.get("size"), "size"),
                      "norm": _num(f.get("norm"), "norm"),
                      "emb": ps.np.frombuffer(_b64(f.get("emb"), "emb", 512 * 2), ps.np.float16),
                      "thumb": _b64(f.get("thumb"), "thumb", none=True)})
    previews, clips = [], []
    for p in r.get("previews") or []:
        previews.append((_num(p.get("t"), "t", none=True), _b64(p.get("jpeg"), "jpeg")))
        clips.append(_b64(p.get("clip"), "clip", scenes.DIM * 2))
    if len(faces) > 5000 or len(previews) > 20:
        raise Bad(400, "too many faces/previews")
    return meta, faces, previews, clips


class Queue:
    def __init__(self, db):
        self.db = db
        self.con = scenes.open_db(db)  # also creates the clip table
        self.lock = threading.Lock()   # one connection, one request at a time (requests are short)
        self.last_autoscan = 0.0
        self.autoscan_proc = None

    def _seen(self):
        ps.set_setting(self.con, ps.QUEUE_WORKER_SEEN, str(time.time()))

    def lease(self, req):
        version = code_version()  # from disk, as the next `scan` will run it
        if req.get("version") != version:
            raise Bad(409, f"version: server {version}")
        n = max(1, min(int(_num(req.get("n", 32), "n")), 500))
        with self.lock:
            con, now = self.con, time.time()
            self._seen()
            lease = secrets.token_urlsafe(12)
            con.execute("""UPDATE work_queue SET lease = ?, lease_until = ? WHERE path IN (
                SELECT path FROM work_queue WHERE lease_until < ? AND remote_ok = 1 ORDER BY ord LIMIT ?)""",
                        (lease, now + ps.QUEUE_LEASE, now, n))
            rows = con.execute("SELECT path, kind FROM work_queue WHERE lease = ? ORDER BY ord", (lease,)).fetchall()
            files, local_only = [], []
            for p, kind in rows:
                w = ps.map_path(p, to_windows=True)
                (files.append({"path": w, "kind": kind}) if w.startswith("\\\\") else local_only.append(p))
            # outside PS_PATH_MAP the worker can't open the file: the server does it
            con.executemany("UPDATE work_queue SET lease = NULL, lease_until = 0, remote_ok = 0 WHERE path = ?",
                            [(p,) for p in local_only])
            con.commit()
            left = con.execute("SELECT COUNT(*) FROM work_queue").fetchone()[0]
            params = json.loads(ps.get_setting(con, ps.QUEUE_PARAMS, "{}"))
        out = {"lease": lease, "ttl": ps.QUEUE_LEASE, "params": params, "files": files, "left": left}
        if not files and not left:
            out["note"] = self.maybe_autoscan()
        return out

    def maybe_autoscan(self):
        """Empty queue and a worker waiting: look for new files now instead of at night."""
        if self.autoscan_proc and self.autoscan_proc.poll() is None:
            return "scan уже идёт"
        if AUTOSCAN_MIN <= 0 or time.time() - self.last_autoscan < AUTOSCAN_MIN * 60:
            return "очередь пуста"
        with open("/data/nightly.lock", "a") as f:  # same lock as nightly.sh
            try:
                fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except OSError:
                return "ночной scan уже идёт"
            fcntl.flock(f, fcntl.LOCK_UN)
        self.last_autoscan = time.time()
        log = open(Path(self.db).parent / "nightly-api.log", "a")
        self.autoscan_proc = subprocess.Popen(["/app/nightly.sh"], stdout=log, stderr=subprocess.STDOUT,
                                              start_new_session=True)
        print(f"{time.strftime('%F %T')} empty queue, worker waiting: started nightly.sh", flush=True)
        return "очередь пуста, запустил scan на сервере"

    def heartbeat(self, req):
        with self.lock:
            self._seen()
            n = self.con.execute("UPDATE work_queue SET lease_until = ? WHERE lease = ?",
                                 (time.time() + ps.QUEUE_LEASE, str(req.get("lease")))).rowcount
            self.con.commit()
        return {"n": n}

    def release(self, req):
        with self.lock:
            n = self.con.execute("UPDATE work_queue SET lease = NULL, lease_until = 0 WHERE lease = ?",
                                 (str(req.get("lease")),)).rowcount
            self.con.commit()
        return {"n": n}

    def result(self, req):
        path = ps.map_path(str(req.get("path")))
        lease = str(req.get("lease"))
        err = req.get("error")
        parsed = None if err else parse_result(req)  # decode outside the lock
        with self.lock:
            con = self.con
            row = con.execute("SELECT kind, size, mtime FROM work_queue WHERE path = ? AND lease = ?",
                              (path, lease)).fetchone()
            if not row:
                raise Bad(409, "not leased (lease expired or file re-queued)")
            if err:  # maybe a Windows-side problem (share, codec): let the server try it
                con.execute("UPDATE work_queue SET lease = NULL, lease_until = 0, remote_ok = 0 WHERE path = ?", (path,))
                print(f"{time.strftime('%F %T')} worker failed on {path}: {str(err)[:300]}", flush=True)
            else:
                meta, faces, previews, clips = parsed
                ps.store_file(con, path, row[0], row[1], row[2], meta, faces, previews, None, clips)
                con.execute("DELETE FROM work_queue WHERE path = ?", (path,))
                con.execute("UPDATE work_queue SET lease_until = ? WHERE lease = ?", (time.time() + ps.QUEUE_LEASE, lease))
            con.commit()
        return {"ok": True}


class WebUI:
    """Calls of the Go web UI: the text model, image conversion, long jobs."""
    MAX_CONVERTED = 3000  # px, long side, as in media_server

    def __init__(self, db):
        self.db = db
        self.lock = threading.Lock()
        self.clip = None
        self.job = {"name": None, "running": False, "progress": 0.0, "message": "", "error": ""}

    def embed_text(self, req):
        text = req.get("text")
        if not isinstance(text, str) or not text.strip():
            raise Bad(400, "text?")
        with self.lock:  # the model loads once (a few seconds), then stays
            if self.clip is None:
                self.clip = scenes.Clip(image=False)
            vec = self.clip.text([text.strip()])[0]
        return {"vector": [round(float(v), 6) for v in vec]}

    def convert(self, req):
        path = req.get("path")
        con = ps.open_db(self.db)
        try:  # only files the DB knows
            if not isinstance(path, str) or not con.execute("SELECT 1 FROM files WHERE path = ?", (path,)).fetchone():
                raise Bad(404, "unknown file")
        finally:
            con.close()
        import cv2
        img = ps.load_image(Path(path))
        if img is None:
            raise Bad(422, "cannot decode")
        s = self.MAX_CONVERTED / max(img.shape[:2])
        if s < 1:
            img = cv2.resize(img, None, fx=s, fy=s, interpolation=cv2.INTER_AREA)
        return {"jpeg": base64.b64encode(cv2.imencode(".jpg", img, [cv2.IMWRITE_JPEG_QUALITY, 90])[1].tobytes()).decode()}

    def run_job(self, req):
        name = req.get("name")
        if name is None:
            return dict(self.job)
        jobs = {"cluster": self._cluster, "stacks_build": self._stacks_build, "stacks_score": self._stacks_score}
        if name not in jobs:
            raise Bad(400, "unknown job")
        if self.job["running"]:
            raise Bad(409, f"already running: {self.job['name']}")
        self.job = {"name": name, "running": True, "progress": 0.0, "message": "", "error": ""}

        def work():
            try:
                self.job["message"] = jobs[name]() or ""
            except Exception as e:
                self.job["error"] = repr(e)
                print(f"{time.strftime('%F %T')} job {name}: {e!r}", flush=True)
            finally:
                self.job["running"] = False
                self.job["progress"] = 1.0

        threading.Thread(target=work, daemon=True).start()
        return dict(self.job)

    def _progress(self, f):
        self.job["progress"] = max(0.0, min(1.0, float(f)))

    def _cluster(self):
        ps.cmd_cluster(ps.build_parser().parse_args(["--db", self.db, "cluster"]))
        return "Кластеры пересчитаны"

    def _stacks_build(self):
        con = stacks.open_db(self.db)
        try:
            k, n = stacks.build(con, progress=self._progress)
        finally:
            con.close()
        return f"Стопок: {k}, фото в них: {n}"

    def _stacks_score(self):
        con = stacks.open_db(self.db)
        try:
            stacks.score(con, workers=12, progress=self._progress)
        finally:
            con.close()
        return "Качество оценено"


def serve(db, bind):
    q = Queue(db)
    web = WebUI(db)
    token = load_token(db)
    routes = {"/lease": q.lease, "/heartbeat": q.heartbeat, "/result": q.result, "/release": q.release,
              "/embed_text": web.embed_text, "/convert": web.convert, "/job": web.run_job}

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def _send(self, status, obj):
            body = json.dumps(obj, ensure_ascii=False).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            try:
                if not secrets.compare_digest(self.headers.get("Authorization", ""), f"Bearer {token}"):
                    raise Bad(403, "bad token")
                if self.path not in routes:
                    raise Bad(404, "no such call")
                size = int(self.headers.get("Content-Length") or 0)
                if not 0 < size <= MAX_BODY:
                    raise Bad(413, "bad body size")
                req = json.loads(self.rfile.read(size))
                if not isinstance(req, dict):
                    raise Bad(400, "expected an object")
                self._send(200, routes[self.path](req))
            except Bad as e:
                self._send(e.status, {"error": str(e)})
            except Exception as e:
                print(f"{time.strftime('%F %T')} {self.path}: {e!r}", flush=True)
                self._send(500, {"error": repr(e)})

    host, port = bind.rsplit(":", 1)
    srv = http.server.ThreadingHTTPServer((host, int(port)), Handler)
    print(f"{time.strftime('%F %T')} queue API on {bind}, db {db}, code {code_version()}", flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--db", default=ps.DEFAULT_DB)
    ap.add_argument("--bind", default=os.environ.get("PS_QUEUE_BIND", "0.0.0.0:8503"))
    a = ap.parse_args()
    serve(a.db, a.bind)
