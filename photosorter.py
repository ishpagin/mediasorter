"""Group all photos/videos by the people in them (face clustering), stored in SQLite.

    scan     : find every face in --src, store embeddings, thumbnails, previews and metadata
               (date, GPS + place, camera, duration, picture hash) in the DB (incremental, read-only)
    cluster  : group faces into people (no reference photos needed)
    gallery  : write people.html with sample faces of every person
    people   : list people in the terminal
    name     : give a person a name                 (name 12 "Masha")
    merge    : join several people into one         (merge 12 40 41)
    hide     : hide a person (strangers) from gallery/people
    export   : copy all files with a person to a review folder
    apply    : move originals of files still left in the review folder to the Recycle Bin

Models: InsightFace buffalo_l (SCRFD det_10g + ArcFace w600k_r50), run with ONNX Runtime.
"""
import argparse
import base64
import csv
import html
import io
import os
import pickle
import re
import shutil
import sqlite3
import sys
import time
from collections import Counter
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime
from pathlib import Path

import cv2
import numpy as np
import onnxruntime as ort
from tqdm import tqdm

ROOT = Path(__file__).resolve().parent
MODELS = ROOT / "models"
DEFAULT_DB = os.environ.get("PS_DB", str(ROOT / "faces.db"))
# Windows share prefix = mount point here, ';'-separated; Windows side with / or \ (docker compose eats "\\"):
# "//nas/home/=/mnt/nas-homes/user/;//nas/homes/=/mnt/nas-homes/"
PATH_MAP = [(w.replace("\\", "/"), l) for w, l in
            (m.split("=", 1) for m in os.environ.get("PS_PATH_MAP", "").split(";") if "=" in m)]


def map_path(p, to_windows=False):
    r"""\\nas\home\Photos\a.jpg <-> /mnt/nas-homes/user/Photos/a.jpg by PS_PATH_MAP (longest prefix wins).
    Paths outside the map are returned unchanged."""
    for win, local in sorted(PATH_MAP, key=lambda m: -len(m[1] if to_windows else m[0])):
        if to_windows and p.startswith(local):
            return (win + p[len(local):]).replace("/", "\\")
        if not to_windows and p.replace("\\", "/").startswith(win):
            return local + p.replace("\\", "/")[len(win):]
    return p

RAW_EXT = {".cr3", ".cr2", ".nef", ".arw", ".dng", ".raf", ".orf", ".rw2"}
IMAGE_EXT = {".jpg", ".jpeg", ".png", ".bmp", ".webp", ".tif", ".tiff", ".heic", ".heif", ".gif"} | RAW_EXT
VIDEO_MIN_FACE = 40   # px; smaller faces in videos are skipped
VIDEO_MAX_FACES = 100  # per video, the largest/sharpest are kept
VIDEO_EXT = {".mp4", ".mov", ".m4v", ".avi", ".mkv", ".3gp", ".mts", ".m2ts", ".wmv", ".webm"}

# ArcFace 5-point alignment template for 112x112 crops
ARCFACE_DST = np.array(
    [[38.2946, 51.6963], [73.5318, 51.5014], [56.0252, 71.7366], [41.5493, 92.3655], [70.7299, 92.2041]],
    dtype=np.float32,
)


# ================================================================ models

def make_session(path):
    ort.set_default_logger_severity(3)
    try:
        ort.preload_dlls()
    except Exception:
        pass
    # the GPU is shared (other services): grow the CUDA arena only as needed, optional cap per session (PS_GPU_MEM_MB)
    cuda = {"arena_extend_strategy": "kSameAsRequested"}
    if os.environ.get("PS_GPU_MEM_MB"):
        cuda["gpu_mem_limit"] = int(os.environ["PS_GPU_MEM_MB"]) << 20
    providers = [("CUDAExecutionProvider", cuda) if p == "CUDAExecutionProvider" else p
                 for p in ("CUDAExecutionProvider", "CPUExecutionProvider") if p in ort.get_available_providers()]
    return ort.InferenceSession(str(path), providers=providers)


class Detector:
    """SCRFD face detector (det_10g.onnx)."""

    strides = (8, 16, 32)
    num_anchors = 2

    def __init__(self, size, score_thr=0.5, nms_thr=0.4):
        self.sess = make_session(MODELS / "det_10g.onnx")
        self.input_name = self.sess.get_inputs()[0].name
        self.size = size
        self.score_thr = score_thr
        self.nms_thr = nms_thr
        self._centers = {}

    def _anchor_centers(self, h, w, stride):
        key = (h, w, stride)
        if key not in self._centers:
            ys, xs = np.mgrid[:h, :w]
            c = (np.stack([xs, ys], axis=-1).astype(np.float32) * stride).reshape(-1, 2)
            self._centers[key] = np.repeat(c, self.num_anchors, axis=0)
        return self._centers[key]

    def detect(self, img):
        """Returns (boxes Nx5 [x1,y1,x2,y2,score], kps Nx5x2) in original image coordinates.

        Runs at two scales: the full size finds small faces, the half size finds faces that fill
        the frame (selfies) — at the full size those exceed the model's largest anchors and are missed.
        """
        found = [self._detect_at(img, s) for s in (self.size, self.size // 2)]
        boxes = np.concatenate([b for b, _ in found])
        kpss = np.concatenate([k for _, k in found])
        if len(boxes) < 2:
            return boxes, kpss
        idx = cv2.dnn.NMSBoxes(
            [[float(b[0]), float(b[1]), float(b[2] - b[0]), float(b[3] - b[1])] for b in boxes],
            boxes[:, 4].tolist(), self.score_thr, self.nms_thr,
        )
        idx = np.array(idx).reshape(-1)
        return boxes[idx], kpss[idx]

    def _detect_at(self, img, size):
        h, w = img.shape[:2]
        scale = min(1.0, size / max(h, w))  # never upscale: big faces get missed
        nh, nw = int(round(h * scale)), int(round(w * scale))
        ch, cw = -(-nh // 32) * 32, -(-nw // 32) * 32  # canvas padded to a multiple of the largest stride
        canvas = np.zeros((ch, cw, 3), dtype=np.uint8)
        canvas[:nh, :nw] = img if scale == 1.0 else cv2.resize(img, (nw, nh), interpolation=cv2.INTER_AREA)
        blob = cv2.dnn.blobFromImage(canvas, 1.0 / 128, (cw, ch), (127.5, 127.5, 127.5), swapRB=True)
        outs = self.sess.run(None, {self.input_name: blob})

        boxes, scores, kpss = [], [], []
        for i, stride in enumerate(self.strides):
            sc = outs[i].reshape(-1)
            bb = outs[i + 3].reshape(-1, 4) * stride
            kp = outs[i + 6].reshape(-1, 10) * stride
            centers = self._anchor_centers(ch // stride, cw // stride, stride)
            keep = np.where(sc >= self.score_thr)[0]
            if not len(keep):
                continue
            c = centers[keep]
            d = bb[keep]
            boxes.append(np.stack([c[:, 0] - d[:, 0], c[:, 1] - d[:, 1], c[:, 0] + d[:, 2], c[:, 1] + d[:, 3]], axis=1))
            scores.append(sc[keep])
            kpss.append(kp[keep].reshape(-1, 5, 2) + c[:, None, :])
        if not boxes:
            return np.zeros((0, 5), np.float32), np.zeros((0, 5, 2), np.float32)

        boxes = np.concatenate(boxes) / scale
        scores = np.concatenate(scores)
        kpss = np.concatenate(kpss) / scale
        idx = cv2.dnn.NMSBoxes(
            [[float(b[0]), float(b[1]), float(b[2] - b[0]), float(b[3] - b[1])] for b in boxes],
            scores.tolist(), self.score_thr, self.nms_thr,
        )
        idx = np.array(idx).reshape(-1)
        return np.hstack([boxes[idx], scores[idx, None]]).astype(np.float32), kpss[idx].astype(np.float32)


class Embedder:
    """ArcFace recognition model (w600k_r50.onnx) -> 512-d vectors."""

    def __init__(self):
        self.sess = make_session(MODELS / "w600k_r50.onnx")
        self.input_name = self.sess.get_inputs()[0].name

    @staticmethod
    def align(img, kps):
        m, _ = cv2.estimateAffinePartial2D(kps, ARCFACE_DST, method=cv2.LMEDS)
        return cv2.warpAffine(img, m, (112, 112), borderValue=0.0)

    def embed(self, crops):
        """Returns (L2-normalized embeddings, raw norms). Low raw norm = low-quality face."""
        if not crops:
            return np.zeros((0, 512), np.float32), np.zeros(0, np.float32)
        blob = cv2.dnn.blobFromImages(crops, 1.0 / 127.5, (112, 112), (127.5, 127.5, 127.5), swapRB=True)
        emb = self.sess.run(None, {self.input_name: blob})[0]
        norms = np.linalg.norm(emb, axis=1)
        return emb / norms[:, None], norms


class FaceEngine:
    def __init__(self, det_size, min_face):
        self.det = Detector(det_size)
        self.emb = Embedder()
        self.min_face = min_face

    def faces(self, img):
        """Returns list of dicts: box, det_score, size, emb, norm, thumb (JPEG bytes)."""
        boxes, kpss = self.det.detect(img)
        if not len(boxes):
            return []
        sizes = np.minimum(boxes[:, 2] - boxes[:, 0], boxes[:, 3] - boxes[:, 1])
        keep = sizes >= self.min_face
        boxes, kpss, sizes = boxes[keep], kpss[keep], sizes[keep]
        embs, norms = self.emb.embed([Embedder.align(img, k) for k in kpss])
        return [
            {"box": b[:4], "det_score": float(b[4]), "size": float(s), "emb": e, "norm": float(n),
             "thumb": make_thumb(img, b)}
            for b, s, e, n in zip(boxes, sizes, embs, norms)
        ]


def make_thumb(img, box, out=128, margin=0.35):
    x1, y1, x2, y2 = box[:4]
    w, h = x2 - x1, y2 - y1
    side = max(w, h) * (1 + 2 * margin)
    cx, cy = (x1 + x2) / 2, (y1 + y2) / 2
    H, W = img.shape[:2]
    a, b = int(max(0, cx - side / 2)), int(max(0, cy - side / 2))
    c, d = int(min(W, cx + side / 2)), int(min(H, cy + side / 2))
    crop = img[b:d, a:c]
    if crop.size == 0:
        return None
    s = out / max(crop.shape[:2])
    crop = cv2.resize(crop, None, fx=s, fy=s, interpolation=cv2.INTER_AREA if s < 1 else cv2.INTER_LINEAR)
    return cv2.imencode(".jpg", crop, [cv2.IMWRITE_JPEG_QUALITY, 80])[1].tobytes()


# ================================================================ media loading

_heif_registered = False


def _pil_open(src):
    """PIL Image from a path or bytes, with HEIC support."""
    global _heif_registered
    from PIL import Image
    if not _heif_registered:
        import pillow_heif
        pillow_heif.register_heif_opener()
        Image.MAX_IMAGE_PIXELS = None  # our own files, not untrusted uploads: allow 200 MP panoramas
        _heif_registered = True
    return Image.open(io.BytesIO(src) if isinstance(src, (bytes, bytearray, memoryview)) else src)


def _load_with_pil(src):
    from PIL import ImageOps
    with _pil_open(src) as im:
        im = ImageOps.exif_transpose(im).convert("RGB")
        return cv2.cvtColor(np.asarray(im), cv2.COLOR_RGB2BGR)


def read_image(path):
    """Returns (BGR image, source for EXIF reading: bytes, path or None)."""
    ext = path.suffix.lower()
    if ext in RAW_EXT:
        import rawpy
        with rawpy.imread(str(path)) as r:
            th = r.extract_thumb()  # embedded full-size JPEG: fast, no RAW development
        if th.format == rawpy.ThumbFormat.JPEG:
            data = bytes(th.data)
            return cv2.imdecode(np.frombuffer(data, np.uint8), cv2.IMREAD_COLOR), data
        return cv2.cvtColor(th.data, cv2.COLOR_RGB2BGR), None
    if ext in (".heic", ".heif"):
        return _load_with_pil(path), path
    data = np.fromfile(str(path), dtype=np.uint8)  # handles non-ASCII Windows paths
    try:
        img = cv2.imdecode(data, cv2.IMREAD_COLOR)  # applies EXIF orientation
    except cv2.error:  # some GIFs make OpenCV throw instead of returning None
        img = None
    if img is None:
        img = _load_with_pil(data.tobytes())
    return img, data.tobytes()


def load_image(path):
    return read_image(path)[0]


# ---------------------------------------------------------------- metadata

def _exif_dt(value, offset=None):
    """'2018:08:13 13:50:40' (+ '+03:00') -> ISO 8601, or None."""
    try:
        dt = datetime.strptime(str(value).strip()[:19], "%Y:%m:%d %H:%M:%S")
    except ValueError:
        return None
    s = dt.isoformat()
    return s + offset.strip() if offset and re.fullmatch(r"[+-]\d\d:\d\d", str(offset).strip()) else s


def _dms(v, ref):
    try:
        d = float(v[0]) + float(v[1]) / 60 + float(v[2]) / 3600
    except (TypeError, IndexError, ValueError, ZeroDivisionError):
        return None
    return -d if ref in ("S", "W") else d


def _text(v):
    v = str(v).strip("\x00 ").strip() if v is not None else ""
    return v or None


def image_meta(src):
    """Date taken, GPS and camera from EXIF."""
    meta = {}
    if src is None:
        return meta
    try:
        with _pil_open(src) as im:
            exif = im.getexif()
    except Exception:
        return meta
    ifd = exif.get_ifd(0x8769)  # Exif sub-IFD
    dt = ifd.get(0x9003) or ifd.get(0x9004) or exif.get(0x0132)  # DateTimeOriginal / Digitized / DateTime
    if dt:
        meta["taken_at"] = _exif_dt(dt, ifd.get(0x9011) or ifd.get(0x9010))
        if meta["taken_at"]:
            meta["date_source"] = "exif"
    meta["camera_make"] = _text(exif.get(0x010F))
    meta["camera_model"] = _text(exif.get(0x0110))
    gps = exif.get_ifd(0x8825)
    if gps:
        lat, lon = _dms(gps.get(2), gps.get(1)), _dms(gps.get(4), gps.get(3))
        if lat is not None and lon is not None and (abs(lat) > 1e-6 or abs(lon) > 1e-6):
            meta["lat"], meta["lon"] = lat, lon
            try:
                meta["alt"] = float(gps.get(6))
            except (TypeError, ValueError, ZeroDivisionError):
                pass
    return meta


def video_meta(container, stream):
    md = {**container.metadata, **stream.metadata}
    meta = {}
    # Apple: local time with offset; others: UTC creation_time
    for key in ("com.apple.quicktime.creationdate", "creation_time"):
        if md.get(key):
            try:
                dt = datetime.fromisoformat(md[key].replace("Z", "+00:00"))
                if dt.year > 1971:
                    meta["taken_at"], meta["date_source"] = dt.isoformat(timespec="seconds"), "video"
                    break
            except ValueError:
                pass
    loc = md.get("com.apple.quicktime.location.ISO6709") or md.get("location") or md.get("location-eng")
    m = re.match(r"([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)?", loc or "")
    if m:
        meta["lat"], meta["lon"] = float(m[1]), float(m[2])
        if m[3]:
            meta["alt"] = float(m[3])
    meta["camera_make"] = _text(md.get("com.apple.quicktime.make") or md.get("make"))
    meta["camera_model"] = _text(md.get("com.apple.quicktime.model") or md.get("model"))
    meta["codec"] = stream.codec_context.name
    if stream.average_rate:
        meta["fps"] = float(stream.average_rate)
    return meta


def date_from_name(name):
    """IMG_20180813_135040.jpg, VID-20190101-WA0001.mp4, 2019-01-01 12.30.45.jpg ..."""
    m = re.search(r"(?<!\d)((?:19|20)\d\d)[-_.]?(0[1-9]|1[0-2])[-_.]?([0-2]\d|3[01])"
                  r"(?:[-_. T]?([01]\d|2[0-3])[-_.]?([0-5]\d)[-_.]?([0-5]\d))?(?!\d)", name)
    if not m:
        return None
    try:
        return datetime(*(int(x) for x in m.groups() if x is not None)).isoformat()
    except ValueError:
        return None


def dhash(img):
    """64-bit difference hash (signed, fits SQLite INTEGER): near-identical images have close hashes."""
    g = cv2.resize(cv2.cvtColor(img, cv2.COLOR_BGR2GRAY), (9, 8), interpolation=cv2.INTER_AREA)
    v = int(np.packbits(g[:, 1:] > g[:, :-1]).view(">u8")[0])
    return v - (1 << 64) if v >= 1 << 63 else v


def make_preview(img, size=320):
    s = size / max(img.shape[:2])
    if s < 1:
        img = cv2.resize(img, None, fx=s, fy=s, interpolation=cv2.INTER_AREA)
    return cv2.imencode(".jpg", img, [cv2.IMWRITE_JPEG_QUALITY, 75])[1].tobytes()


# ---------------------------------------------------------------- analysis

def _upright(frame):
    """BGR array with the stream's display rotation applied (phone videos are often stored sideways)."""
    arr = frame.to_ndarray(format="bgr24")
    k = int(round((getattr(frame, "rotation", 0) or 0) / 90)) % 4
    return np.ascontiguousarray(np.rot90(arr, k)) if k else arr


def iter_video_frames(container, stream, step, duration):
    """Yields (time_sec, upright BGR frame), roughly one keyframe per `step` seconds."""
    stream.codec_context.skip_frame = "NONKEY"  # decode keyframes only
    if duration and duration > 2 * step:
        # seek to each sample point: reads only a small part of the file (matters on a NAS)
        start = stream.start_time or 0
        t, last = 0.0, None
        while t < duration:
            container.seek(start + int(t / stream.time_base), stream=stream)  # keyframe at or before t
            frame = next(container.decode(stream), None)
            if frame is None:
                break
            ft = float(frame.time if frame.time is not None else t)
            if ft != last:
                yield ft, _upright(frame)
            last = ft
            t += step
        return
    last = -1e9
    for frame in container.decode(stream):
        t = float(frame.time or 0.0)
        if t - last >= step:
            last = t
            yield t, _upright(frame)


def analyze(engine, path, kind, video_step, n_video_previews=6):
    """Returns (meta dict, faces, previews [(t_sec, jpeg)]). Faces in videos are de-duplicated per file."""
    if kind == "image":
        img, src = read_image(path)
        meta = image_meta(src)
        meta.update(width=img.shape[1], height=img.shape[0], phash=dhash(img))
        faces = engine.faces(img)
        for f in faces:
            f["t"] = None
        return meta, faces, [(None, make_preview(img))]

    import av
    with av.open(str(path)) as container:
        stream = container.streams.video[0]
        meta = video_meta(container, stream)
        duration = float(stream.duration * stream.time_base) if stream.duration else (container.duration or 0) / 1e6
        meta["duration"] = duration or None
        faces, kept, previews = [], [], []
        next_preview = 0.0
        for t, frame in iter_video_frames(container, stream, video_step, duration):
            if "width" not in meta:
                meta.update(width=frame.shape[1], height=frame.shape[0], phash=dhash(frame))
            if t >= next_preview and len(previews) < n_video_previews:
                previews.append((t, make_preview(frame)))
                next_preview = t + (duration or 0) / n_video_previews
            for f in engine.faces(frame):
                # crowds: tiny faces in videos are strangers far away, not worth keeping
                if f["size"] < VIDEO_MIN_FACE:
                    continue
                # skip a face if a near-identical one was already kept from this video
                if kept and max(float(f["emb"] @ k) for k in kept) >= 0.6:
                    continue
                f["t"] = t
                kept.append(f["emb"])
                faces.append(f)
    faces.sort(key=lambda f: -f["size"] * f["det_score"])
    return meta, faces[:VIDEO_MAX_FACES], previews


# ================================================================ database

SCHEMA = """
CREATE TABLE IF NOT EXISTS files (
    id         INTEGER PRIMARY KEY,
    path       TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL,            -- image | video
    size         INTEGER,
    mtime        INTEGER,
    width        INTEGER,                  -- as displayed (rotation applied)
    height       INTEGER,
    duration     REAL,                     -- seconds (videos)
    fps          REAL,
    codec        TEXT,
    n_faces      INTEGER,                  -- 0 = no people found
    taken_at     TEXT,                     -- ISO 8601, local time (+offset when known)
    date_source  TEXT,                     -- exif | video | filename | mtime
    lat          REAL,
    lon          REAL,
    alt          REAL,
    place        TEXT,                     -- "City, Region, CC" (offline reverse geocoding)
    camera_make  TEXT,
    camera_model TEXT,
    phash        INTEGER,                  -- 64-bit dHash: equal/close values = duplicates
    error        TEXT,
    scanned_at   TEXT
);
CREATE INDEX IF NOT EXISTS files_taken ON files(taken_at);
CREATE INDEX IF NOT EXISTS files_phash ON files(phash);

-- small JPEG previews (1 per photo, a few per video): later classification works without re-reading originals
CREATE TABLE IF NOT EXISTS previews (
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    t_sec   REAL,
    jpeg    BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS previews_file ON previews(file_id);
CREATE TABLE IF NOT EXISTS persons (
    id       INTEGER PRIMARY KEY,
    name     TEXT,
    hidden   INTEGER NOT NULL DEFAULT 0,
    n_faces  INTEGER NOT NULL DEFAULT 0,
    n_files  INTEGER NOT NULL DEFAULT 0,
    centroid BLOB                          -- float32[512]
);
CREATE TABLE IF NOT EXISTS faces (
    id        INTEGER PRIMARY KEY,
    file_id   INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    person_id INTEGER REFERENCES persons(id) ON DELETE SET NULL,  -- NULL = not grouped (stranger / bad face)
    t_sec     REAL,                        -- position in video, NULL for photos
    x1 REAL, y1 REAL, x2 REAL, y2 REAL,
    det_score REAL,
    size      REAL,                        -- face size in px (shorter side)
    emb_norm  REAL,                        -- raw embedding norm, a rough quality signal
    embedding BLOB NOT NULL,               -- float16[512], L2-normalized
    thumb     BLOB,                        -- JPEG
    manual    INTEGER NOT NULL DEFAULT 0   -- person_id set by hand: `cluster` keeps it
);
CREATE INDEX IF NOT EXISTS faces_file ON faces(file_id);
CREATE INDEX IF NOT EXISTS faces_person ON faces(person_id);

-- who is in each file
CREATE VIEW IF NOT EXISTS file_people AS
SELECT f.id AS file_id, f.path, f.kind, f.n_faces,
       COUNT(DISTINCT fa.person_id) AS n_people,
       group_concat(DISTINCT COALESCE(p.name, 'person_' || p.id)) AS people
FROM files f
LEFT JOIN faces fa ON fa.file_id = f.id
LEFT JOIN persons p ON p.id = fa.person_id
GROUP BY f.id;

-- every (person, file) pair
CREATE VIEW IF NOT EXISTS person_files AS
SELECT p.id AS person_id, p.name, f.id AS file_id, f.path, f.kind, f.n_faces,
       COUNT(*) AS faces_of_person
FROM persons p
JOIN faces fa ON fa.person_id = p.id
JOIN files f ON f.id = fa.file_id
GROUP BY p.id, f.id;

-- files with no faces at all (landscapes, documents, pets...) for separate classification
CREATE VIEW IF NOT EXISTS files_without_people AS
SELECT * FROM files WHERE n_faces = 0 AND error IS NULL;

-- files whose faces are all ungrouped (strangers, crowds, tiny faces)
CREATE VIEW IF NOT EXISTS files_strangers_only AS
SELECT f.* FROM files f
WHERE f.n_faces > 0 AND NOT EXISTS (SELECT 1 FROM faces x WHERE x.file_id = f.id AND x.person_id IS NOT NULL);

-- exact duplicates by picture hash (e.g. same photo in MobileBackup and media)
CREATE VIEW IF NOT EXISTS duplicates AS
SELECT phash, COUNT(*) AS n, group_concat(path, ' | ') AS paths
FROM files WHERE phash IS NOT NULL AND kind = 'image' GROUP BY phash HAVING n > 1;

-- pairs of people the user marked as different (hidden from the "similar people" page)
CREATE TABLE IF NOT EXISTS not_same (
    a INTEGER NOT NULL,                    -- smaller person id
    b INTEGER NOT NULL,
    PRIMARY KEY (a, b)
);

-- deletion log: where each file went and its DB rows (pickled), for undo / `restore`
CREATE TABLE IF NOT EXISTS deleted (
    path       TEXT NOT NULL,
    moved_to   TEXT,                       -- quarantine path (NAS); NULL = Windows Recycle Bin
    deleted_at TEXT NOT NULL,
    snapshot   BLOB
);

CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT);

-- files `scan` wants analyzed, shared with remote GPU workers (queue_api.py). A row is taken by a lease
-- (lease + lease_until); an expired lease means its worker is gone and the file is free again.
CREATE TABLE IF NOT EXISTS work_queue (
    path        TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    size        INTEGER,
    mtime       INTEGER,
    ord         INTEGER,                   -- scan order: photos first
    queued_at   REAL NOT NULL,
    lease       TEXT,
    lease_until REAL NOT NULL DEFAULT 0,
    remote_ok   INTEGER NOT NULL DEFAULT 1 -- 0 = a worker failed on it: only the server analyzes it
);

-- collections: files moved out of the archive into local folders, to zip and send to someone.
-- A file may be in several collections: the first one gets the moved file, the others NTFS hard links.
CREATE TABLE IF NOT EXISTS collections (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    folder     TEXT NOT NULL,
    created_at TEXT NOT NULL,
    person_id  INTEGER                     -- set for collections made by "by people"
);
CREATE TABLE IF NOT EXISTS collection_files (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    file_id       INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    link_path     TEXT NOT NULL,           -- the file inside this collection's folder
    original_path TEXT NOT NULL,           -- where it lived in the archive (to return it)
    added_at      TEXT NOT NULL,
    PRIMARY KEY (collection_id, file_id)
);
"""

FILE_META = ("width", "height", "duration", "fps", "codec", "taken_at", "date_source", "lat", "lon", "alt",
             "camera_make", "camera_model", "phash")


def open_db(path):
    # check_same_thread=False: Streamlit runs widget callbacks on a different thread than the script
    # timeout: scan, the queue API and the UI write to the same DB, wait for each other's short transactions
    con = sqlite3.connect(path, check_same_thread=False, timeout=60)
    con.execute("PRAGMA foreign_keys = ON")
    con.execute("PRAGMA journal_mode = WAL")
    con.executescript(SCHEMA)
    # DBs from older versions
    if "snapshot" not in [r[1] for r in con.execute("PRAGMA table_info(deleted)")]:
        con.execute("ALTER TABLE deleted ADD COLUMN snapshot BLOB")
    if "skip_collect" not in [r[1] for r in con.execute("PRAGMA table_info(persons)")]:
        con.execute("ALTER TABLE persons ADD COLUMN skip_collect INTEGER NOT NULL DEFAULT 0")  # "this is me"
    if "manual" not in [r[1] for r in con.execute("PRAGMA table_info(faces)")]:
        con.execute("ALTER TABLE faces ADD COLUMN manual INTEGER NOT NULL DEFAULT 0")
    if "person_id" not in [r[1] for r in con.execute("PRAGMA table_info(collections)")]:
        con.execute("ALTER TABLE collections ADD COLUMN person_id INTEGER")
    if "link_path" not in [r[1] for r in con.execute("PRAGMA table_info(collection_files)")]:
        con.executescript("""
            ALTER TABLE collection_files RENAME TO collection_files_old;
            CREATE TABLE collection_files (
                collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
                file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
                link_path TEXT NOT NULL, original_path TEXT NOT NULL, added_at TEXT NOT NULL,
                PRIMARY KEY (collection_id, file_id));
            INSERT INTO collection_files SELECT o.collection_id, o.file_id, f.path, o.original_path, o.added_at
                FROM collection_files_old o JOIN files f ON f.id = o.file_id;
            DROP TABLE collection_files_old;
        """)
    con.commit()
    return con


def to_blob(v):
    return np.asarray(v, np.float16).tobytes()


def from_blobs(blobs):
    return np.frombuffer(b"".join(blobs), np.float16).reshape(-1, 512).astype(np.float32)


def resolve_person(con, ref):
    """Person by id or by (case-insensitive) name."""
    row = con.execute("SELECT id FROM persons WHERE id = ? OR lower(name) = lower(?)",
                      (int(ref) if str(ref).isdigit() else -1, str(ref))).fetchone()
    if not row:
        sys.exit(f"No person '{ref}'")
    return row[0]


def refresh_person_stats(con):
    con.execute("""
        UPDATE persons SET
            n_faces = (SELECT COUNT(*) FROM faces WHERE person_id = persons.id),
            n_files = (SELECT COUNT(DISTINCT file_id) FROM faces WHERE person_id = persons.id)
    """)
    con.execute("DELETE FROM persons WHERE n_faces = 0 AND name IS NULL")


# ================================================================ scan

QUEUE_LEASE = int(os.environ.get("PS_QUEUE_LEASE", "90"))  # s: a worker's heartbeat renews it, a lost worker's expires
QUEUE_WORKER_SEEN = "queue_worker_seen"  # settings key: time a remote worker last asked for work / sent a heartbeat
QUEUE_PARAMS = "queue_params"            # settings key: analysis parameters of the running scan, for the workers


def store_file(con, path, kind, size, mtime, meta, faces, previews, err, clips=None):
    """Write one analyzed file, replacing its old rows (faces, previews...). `faces` as from FaceEngine.faces
    plus "t"; `previews` [(t_sec, jpeg)]; `clips`: SigLIP2 vectors (float16 bytes) of the previews, if known
    (otherwise `scenes.py embed` computes them). Doesn't commit."""
    if not meta.get("taken_at"):
        d = date_from_name(os.path.basename(path))
        meta["taken_at"], meta["date_source"] = (d, "filename") if d else (
            datetime.fromtimestamp(mtime).isoformat(timespec="seconds"), "mtime")
    cols = ("path", "kind", "size", "mtime", "n_faces", "error", "scanned_at") + FILE_META
    con.execute("DELETE FROM files WHERE path = ?", (path,))
    fid = con.execute(f"INSERT INTO files ({', '.join(cols)}) VALUES ({', '.join('?' * len(cols))})", (
        path, kind, size, mtime, len(faces), err,
        datetime.now().isoformat(timespec="seconds"), *(meta.get(k) for k in FILE_META),
    )).lastrowid
    con.executemany("INSERT INTO previews (file_id, t_sec, jpeg) VALUES (?,?,?)", [(fid, t, j) for t, j in previews])
    con.executemany(
        "INSERT INTO faces (file_id, t_sec, x1, y1, x2, y2, det_score, size, emb_norm, embedding, thumb) "
        "VALUES (?,?,?,?,?,?,?,?,?,?,?)",
        [(fid, f["t"], *map(float, f["box"]), f["det_score"], f["size"], f["norm"], to_blob(f["emb"]), f["thumb"])
         for f in faces],
    )
    if clips:
        con.executemany("INSERT INTO clip (file_id, t_sec, emb) VALUES (?,?,?)",
                        [(fid, t, e) for (t, _), e in zip(previews, clips)])
    return fid


def cmd_scan(args):
    src = Path(args.src).resolve()
    if not src.is_dir():  # an unmounted NAS would look empty and every file under src would be dropped from the DB
        sys.exit(f"{src} is not a folder (NAS not mounted?): nothing scanned")
    con = open_db(args.db)
    engine = FaceEngine(args.det_size, args.min_face)
    print(f"Providers: {engine.det.sess.get_providers()}")

    rows = con.execute("SELECT path, size, mtime, error FROM files").fetchall()
    known = {p: (s, m) for p, s, m, err in rows if err is None}  # files with errors are retried
    todo, seen = [], set()
    for dirpath, dirnames, filenames in os.walk(src):
        dirnames[:] = [n for n in dirnames if not n.startswith((".", "_review"))]
        for n in filenames:
            if n.startswith("._"):  # macOS AppleDouble metadata, not real images
                continue
            ext = os.path.splitext(n)[1].lower()
            kind = "image" if ext in IMAGE_EXT else "video" if ext in VIDEO_EXT else None
            if not kind or (args.no_video and kind == "video"):
                continue
            p = os.path.join(dirpath, n)
            st = os.stat(p)
            seen.add(p)
            if known.get(p) != (st.st_size, int(st.st_mtime)):
                todo.append((Path(p), kind, st))

    # forget files under src that no longer exist
    gone = [p for p, *_ in rows if p.startswith(str(src) + os.sep) and p not in seen]
    if gone:
        con.executemany("DELETE FROM files WHERE path = ?", [(p,) for p in gone])
        print(f"Removed {len(gone)} missing files from DB")
    print(f"{len(seen)} media files in {src}, {len(todo)} new/changed to scan")
    todo.sort(key=lambda it: (it[1] != "image", str(it[0])))  # photos first: usable while videos still run

    # Everything goes through work_queue: a remote GPU worker (queue_api.py) may take part of it.
    # The server analyzes a file itself once no worker was seen lately, or the file waited --queue-wait minutes.
    prefix = str(src) + os.sep
    in_src = "substr(path, 1, ?) = ?"
    con.execute(f"DELETE FROM work_queue WHERE {in_src}", (len(prefix), prefix))  # leftovers of a broken run
    t0 = time.time()
    con.executemany("INSERT OR REPLACE INTO work_queue (path, kind, size, mtime, ord, queued_at) VALUES (?,?,?,?,?,?)",
                    [(str(p), kind, st.st_size, int(st.st_mtime), i, t0) for i, (p, kind, st) in enumerate(todo)])
    set_setting(con, QUEUE_PARAMS, f'{{"det_size": {args.det_size}, "min_face": {args.min_face}, '
                                   f'"video_step": {args.video_step}}}')  # the worker must analyze the same way
    n_faces, errors, n_local = 0, 0, 0
    with ThreadPoolExecutor(args.workers) as pool, tqdm(total=len(todo), unit="file", smoothing=0.05) as bar:
        while True:
            now = time.time()
            left = con.execute(f"SELECT COUNT(*) FROM work_queue WHERE {in_src}", (len(prefix), prefix)).fetchone()[0]
            bar.update(len(todo) - left - bar.n)
            if not left:
                break
            worker_seen = float(get_setting(con, QUEUE_WORKER_SEEN, 0))
            if now - worker_seen < QUEUE_LEASE and now - t0 < args.queue_wait * 60:
                time.sleep(5)  # a worker is active: leave the queue to it for a while
                continue
            lease = "local:" + os.urandom(6).hex()
            # local lease: long, this process holds it; one UPDATE = atomic against the queue API
            con.execute(f"""UPDATE work_queue SET lease = ?, lease_until = ? WHERE path IN (
                SELECT path FROM work_queue WHERE lease_until < ? AND {in_src} ORDER BY ord LIMIT ?)""",
                        (lease, now + 86400, now, len(prefix), prefix, args.workers * 4))
            con.commit()
            rows = con.execute("SELECT path, kind, size, mtime FROM work_queue WHERE lease = ?", (lease,)).fetchall()
            if not rows:
                time.sleep(5)  # the rest is leased by a worker: wait for its results (or its lease to expire)
                continue
            futs = {pool.submit(analyze, engine, Path(p), kind, args.video_step): (p, kind, size, mtime)
                    for p, kind, size, mtime in rows}
            for fut in as_completed(futs):
                p, kind, size, mtime = futs[fut]
                try:
                    meta, faces, previews = fut.result()
                    err = None
                except Exception as e:
                    meta, faces, previews, err = {}, [], [], repr(e)
                    errors += 1
                store_file(con, p, kind, size, mtime, meta, faces, previews, err)
                con.execute("DELETE FROM work_queue WHERE path = ? AND lease = ?", (p, lease))
                con.commit()  # per file: the queue API writes to the same DB
                n_faces += len(faces)
                n_local += 1
                bar.update(1)
    refresh_person_stats(con)
    con.commit()
    print(f"Scanned {len(todo)} files in {time.time() - t0:.0f}s: {n_local} here ({n_faces} faces, {errors} errors), "
          f"{len(todo) - n_local} by remote workers")
    geocode(con)
    print("Next: python photosorter.py cluster")


def cmd_recheck(args):
    """Find files where a big face is visible on the stored preview but missing in the DB
    (e.g. selfies missed by older versions) and mark them so the next `scan` re-reads them."""
    con = open_db(args.db)
    det = Detector(args.det_size)
    stored = {}
    for fid, size, w, h in con.execute("SELECT fa.file_id, fa.size, f.width, f.height FROM faces fa "
                                       "JOIN files f ON f.id = fa.file_id"):
        if w and h:
            stored.setdefault(fid, []).append(size / max(w, h))
    rows = con.execute("SELECT p.file_id, p.jpeg FROM previews p JOIN files f ON f.id = p.file_id "
                       "WHERE f.error IS NULL ORDER BY p.file_id").fetchall()
    marked = set()
    for fid, jpeg in tqdm(rows, unit="preview"):
        if fid in marked:
            continue
        img = cv2.imdecode(np.frombuffer(jpeg, np.uint8), cv2.IMREAD_COLOR)
        boxes, _ = det.detect(img)
        rel = [min(b[2] - b[0], b[3] - b[1]) / max(img.shape[:2]) for b in boxes]
        big_preview = sum(r >= 0.15 for r in rel)
        big_stored = sum(r >= 0.10 for r in stored.get(fid, []))
        if big_preview > big_stored:
            marked.add(fid)
    con.executemany("UPDATE files SET mtime = -1 WHERE id = ?", [(f,) for f in marked])
    con.commit()
    print(f"{len(marked)} files have faces missing in the DB; run `scan` again to re-read them")


def geocode(con):
    """Fill files.place from GPS coordinates, offline (GeoNames cities > 1000 people)."""
    rows = con.execute("SELECT id, lat, lon FROM files WHERE lat IS NOT NULL AND place IS NULL").fetchall()
    if not rows:
        return
    import reverse_geocoder as rg
    res = rg.search([(lat, lon) for _, lat, lon in rows], mode=1, verbose=False)
    con.executemany("UPDATE files SET place = ? WHERE id = ?",
                    [(", ".join(x for x in (r["name"], r["admin1"], r["cc"]) if x), fid)
                     for (fid, _, _), r in zip(rows, res)])
    con.commit()
    print(f"Geocoded {len(rows)} files")


# ================================================================ cluster

def knn(E, k, chunk=1024):
    """Top-k cosine neighbours of every row (excluding itself)."""
    n = len(E)
    k = min(k, n - 1)
    idx = np.empty((n, k), np.int32)
    sims = np.empty((n, k), np.float32)
    for s in range(0, n, chunk):
        S = E[s:s + chunk] @ E.T
        m = len(S)
        S[np.arange(m), s + np.arange(m)] = -2
        part = np.argpartition(-S, k - 1, axis=1)[:, :k]
        idx[s:s + m] = part
        sims[s:s + m] = np.take_along_axis(S, part, 1)
    return idx, sims


def chinese_whispers(idx, sims, thr, iters=30, seed=0):
    """Graph clustering on the kNN graph (edges with similarity >= thr), as used by dlib for faces."""
    n = len(idx)
    rows = np.repeat(np.arange(n), idx.shape[1])
    cols, w = idx.reshape(-1), sims.reshape(-1)
    m = w >= thr
    rows, cols, w = rows[m], cols[m], w[m]
    # symmetric adjacency lists
    r = np.concatenate([rows, cols])
    c = np.concatenate([cols, rows])
    ww = np.concatenate([w, w])
    order = np.argsort(r, kind="stable")
    r, c, ww = r[order], c[order], ww[order]
    bounds = np.searchsorted(r, np.arange(n + 1))

    labels = np.arange(n)
    rng = np.random.default_rng(seed)
    for _ in range(iters):
        changed = 0
        for i in rng.permutation(n):
            a, b = bounds[i], bounds[i + 1]
            if a == b:
                continue
            u, inv = np.unique(labels[c[a:b]], return_inverse=True)
            best = u[np.bincount(inv, weights=ww[a:b]).argmax()]
            if best != labels[i]:
                labels[i] = best
                changed += 1
        if not changed:
            break
    return labels


def centroids_of(E, labels):
    uniq, inv = np.unique(labels, return_inverse=True)
    C = np.zeros((len(uniq), E.shape[1]), np.float32)
    np.add.at(C, inv, E)
    C /= np.linalg.norm(C, axis=1, keepdims=True)
    return uniq, inv, C


def merge_close_clusters(E, labels, thr):
    """Union clusters whose centroids are more similar than thr (repeat until stable)."""
    while True:
        uniq, inv, C = centroids_of(E, labels)
        S = C @ C.T
        np.fill_diagonal(S, -1)
        a, b = np.where(np.triu(S >= thr))
        if not len(a):
            return labels
        parent = np.arange(len(uniq))

        def find(x):
            while parent[x] != x:
                parent[x] = parent[parent[x]]
                x = parent[x]
            return x

        for i, j in zip(a, b):
            parent[find(i)] = find(j)
        roots = np.array([find(i) for i in range(len(uniq))])
        labels = uniq[roots[inv]]


def cmd_cluster(args):
    con = open_db(args.db)
    rows = con.execute("SELECT id, file_id, person_id, size, det_score, embedding, manual FROM faces ORDER BY id").fetchall()
    if not rows:
        sys.exit("No faces in DB, run scan first")
    face_ids = np.array([r[0] for r in rows])
    file_ids = np.array([r[1] for r in rows])
    old_pid = np.array([r[2] if r[2] is not None else -1 for r in rows])
    size = np.array([r[3] for r in rows])
    det = np.array([r[4] for r in rows])
    E = from_blobs([r[5] for r in rows])
    n = len(E)
    t0 = time.time()

    # 1) cluster good-quality faces only; small/blurry faces make bridges between different people
    core = np.where((size >= args.core_size) & (det >= args.core_det))[0]
    print(f"{n} faces, {len(core)} good enough for clustering")
    labels = np.full(n, -1)
    if len(core) >= 2:
        idx, sims = knn(E[core], args.k)
        lab = chinese_whispers(idx, sims, args.edge)
        lab = merge_close_clusters(E[core], lab, args.merge)
        labels[core] = lab

    # 2) keep clusters seen in >= min_files files, drop the rest (strangers)
    def prune(labels):
        files_per = {}
        for l, f in zip(labels, file_ids):
            if l >= 0:
                files_per.setdefault(l, set()).add(f)
        good = {l for l, fs in files_per.items() if len(fs) >= args.min_files}
        return np.where(np.isin(labels, list(good)), labels, -1)

    labels = prune(labels)

    # 3) attach remaining faces (small ones, strangers' leftovers) to the nearest person if similar enough
    if (labels >= 0).any():
        uniq, _, C = centroids_of(E[labels >= 0], labels[labels >= 0])
        rest = np.where(labels < 0)[0]
        for s in range(0, len(rest), 4096):
            part = rest[s:s + 4096]
            S = E[part] @ C.T
            best = S.argmax(1)
            ok = S[np.arange(len(part)), best] >= args.assign
            labels[part[ok]] = uniq[best[ok]]
    labels = prune(labels)

    # 4) keep person ids/names stable: map each new cluster to the old person most of its faces had
    clusters = {}
    for i, l in enumerate(labels):
        if l >= 0:
            clusters.setdefault(l, []).append(i)
    new_pid = np.full(n, -1)
    next_id = (con.execute("SELECT MAX(id) FROM persons").fetchone()[0] or 0) + 1
    # named people by their stored mean face: keeps names even if their faces were re-scanned
    named = con.execute("SELECT id, centroid FROM persons WHERE name IS NOT NULL AND centroid IS NOT NULL").fetchall()
    named_ids = [i for i, _ in named]
    named_C = np.stack([np.frombuffer(c, np.float32) for _, c in named]) if named else np.zeros((0, 512), np.float32)
    for l, members in sorted(clusters.items(), key=lambda kv: -len(kv[1])):
        olds = Counter(old_pid[m] for m in members if old_pid[m] >= 0)
        c = E[members].mean(0)
        sims = named_C @ (c / np.linalg.norm(c))
        if olds and olds.most_common(1)[0][1] * 2 >= sum(olds.values()):
            pid = int(olds.most_common(1)[0][0])
        elif len(sims) and sims.max() >= 0.6:
            pid = named_ids[int(sims.argmax())]
        else:
            pid = next_id
            next_id += 1
            con.execute("INSERT INTO persons (id) VALUES (?)", (pid,))
        new_pid[members] = pid

    # faces assigned by hand (UI "Кто на фото") keep their person, or stay ungrouped
    manual = np.array([bool(r[6]) for r in rows])
    new_pid[manual] = old_pid[manual]
    print(f"{int(manual.sum())} faces assigned by hand, kept as is")

    con.executemany("UPDATE faces SET person_id = ? WHERE id = ?",
                    [(int(p) if p >= 0 else None, int(f)) for p, f in zip(new_pid, face_ids)])
    for pid in np.unique(new_pid[new_pid >= 0]):
        c = E[new_pid == pid].mean(0)
        con.execute("UPDATE persons SET centroid = ? WHERE id = ?", ((c / np.linalg.norm(c)).astype(np.float32).tobytes(), int(pid)))
    refresh_person_stats(con)
    con.commit()

    n_people = con.execute("SELECT COUNT(*) FROM persons WHERE n_faces > 0").fetchone()[0]
    print(f"Clustered in {time.time() - t0:.0f}s: {n_people} people, "
          f"{int((new_pid >= 0).sum())} faces grouped, {int((new_pid < 0).sum())} left ungrouped")
    print("Next: python photosorter.py gallery   (then open people.html)")


# ================================================================ review / naming

def person_label(pid, name):
    return name or f"person_{pid}"


def cmd_people(args):
    con = open_db(args.db)
    rows = con.execute("""
        SELECT p.id, p.name, p.hidden, p.n_files, p.n_faces,
               (SELECT COUNT(DISTINCT pf.file_id) FROM person_files pf WHERE pf.person_id = p.id AND pf.kind = 'video')
        FROM persons p WHERE p.n_faces > 0 AND (p.hidden = 0 OR ?) ORDER BY p.n_files DESC LIMIT ?
    """, (args.all, args.limit)).fetchall()
    print(f"{'id':>6}  {'name':<24} {'files':>6} {'videos':>6} {'faces':>6}")
    for pid, name, hidden, nf, nfa, nv in rows:
        print(f"{pid:>6}  {person_label(pid, name) + (' (hidden)' if hidden else ''):<24} {nf:>6} {nv:>6} {nfa:>6}")


def cmd_gallery(args):
    con = open_db(args.db)
    persons = con.execute("SELECT id, name, n_files, n_faces FROM persons "
                          "WHERE n_faces > 0 AND hidden = 0 ORDER BY n_files DESC").fetchall()
    parts = []
    for pid, name, nf, nfa in tqdm(persons, unit="person"):
        # best faces first, then a random sample to reveal mistakes inside the cluster
        best = con.execute("SELECT f.thumb, fi.path FROM faces f JOIN files fi ON fi.id = f.file_id "
                           "WHERE f.person_id = ? AND f.thumb IS NOT NULL ORDER BY f.size * f.det_score DESC LIMIT ?",
                           (pid, args.samples // 2)).fetchall()
        rnd = con.execute("SELECT f.thumb, fi.path FROM faces f JOIN files fi ON fi.id = f.file_id "
                          "WHERE f.person_id = ? AND f.thumb IS NOT NULL ORDER BY random() LIMIT ?",
                          (pid, args.samples - len(best))).fetchall()
        imgs = "".join(
            f'<img loading="lazy" title="{html.escape(p)}" src="data:image/jpeg;base64,{base64.b64encode(t).decode()}">'
            for t, p in best + rnd)
        parts.append(f'<section><h2><span class="id">#{pid}</span> {html.escape(person_label(pid, name))}'
                     f' <small>{nf} files, {nfa} faces</small></h2><div class="grid">{imgs}</div></section>')
    page = f"""<!doctype html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>People</title>
<style>
:root {{ --bg:#fafafa; --fg:#222; --muted:#777; --card:#fff; --line:#e5e5e5; }}
@media (prefers-color-scheme: dark) {{ :root {{ --bg:#161616; --fg:#eee; --muted:#999; --card:#202020; --line:#333; }} }}
body {{ margin:0; padding:16px; background:var(--bg); color:var(--fg); font:14px system-ui, sans-serif; }}
header {{ max-width:1200px; margin:0 auto 16px; }}
code {{ background:var(--card); border:1px solid var(--line); padding:1px 5px; border-radius:4px; }}
section {{ max-width:1200px; margin:0 auto 12px; background:var(--card); border:1px solid var(--line);
          border-radius:8px; padding:10px 12px; }}
h2 {{ margin:0 0 8px; font-size:16px; }} .id {{ color:var(--muted); }} small {{ color:var(--muted); font-weight:400; }}
.grid {{ display:flex; flex-wrap:wrap; gap:4px; }}
.grid img {{ width:96px; height:96px; object-fit:cover; border-radius:4px; }}
</style></head><body>
<header><h1>People: {len(persons)}</h1>
<p>Наведи на лицо, чтобы увидеть путь к файлу. Команды:
<code>name ID "Имя"</code> · <code>merge ID1 ID2 ...</code> (один человек разбит на несколько) ·
<code>hide ID</code> (посторонние) · <code>export ID</code> → <code>apply</code> (удаление)</p></header>
{''.join(parts)}
</body></html>"""
    Path(args.out).write_text(page, encoding="utf-8")
    print(f"Wrote {args.out} ({len(persons)} people)")


def cmd_name(args):
    con = open_db(args.db)
    pid = resolve_person(con, args.person)
    con.execute("UPDATE persons SET name = ? WHERE id = ?", (args.name, pid))
    con.commit()
    print(f"#{pid} -> {args.name}")


def merge_persons(con, ids):
    """Merge persons into ids[0]; the first non-empty name wins."""
    target, others = ids[0], ids[1:]
    if not others:
        return
    names = [n for (n,) in con.execute(f"SELECT name FROM persons WHERE id IN ({','.join('?' * len(ids))})", ids) if n]
    q = ",".join("?" * len(others))
    con.execute(f"UPDATE faces SET person_id = ? WHERE person_id IN ({q})", [target, *others])
    con.execute(f"DELETE FROM persons WHERE id IN ({q})", others)
    tname = con.execute("SELECT name FROM persons WHERE id = ?", (target,)).fetchone()[0]
    if not tname and names:
        con.execute("UPDATE persons SET name = ? WHERE id = ?", (names[0], target))
    refresh_person_stats(con)
    con.commit()


def create_person(con, name):
    """A named person (an existing one with this name, case-insensitive, is reused)."""
    row = con.execute("SELECT id FROM persons WHERE lower(name) = lower(?)", (name,)).fetchone()
    if row:
        return row[0]
    pid = con.execute("INSERT INTO persons (name) VALUES (?)", (name,)).lastrowid
    con.commit()
    return pid


def file_faces(con, file_ids):
    """Who is where on these files, for correcting by hand: one entry per person per file
    (a video has many frames of the same face), ungrouped faces one by one.
    [{"file_id", "path", "person_id", "face_ids", "thumb"}] — the biggest face of the group gives the thumb."""
    q = ",".join("?" * len(file_ids))
    rows = con.execute(f"""
        SELECT x.file_id, f.path, x.person_id, group_concat(x.id), (SELECT thumb FROM faces t
               WHERE t.file_id = x.file_id AND t.person_id IS x.person_id ORDER BY t.size DESC LIMIT 1)
        FROM faces x JOIN files f ON f.id = x.file_id WHERE x.file_id IN ({q}) AND x.person_id IS NOT NULL
        GROUP BY x.file_id, x.person_id
        UNION ALL
        SELECT x.file_id, f.path, NULL, x.id, x.thumb
        FROM faces x JOIN files f ON f.id = x.file_id WHERE x.file_id IN ({q}) AND x.person_id IS NULL
    """, [*file_ids, *file_ids]).fetchall()
    order = {fid: i for i, fid in enumerate(file_ids)}
    out = [{"file_id": fid, "path": path, "person_id": pid, "face_ids": [int(i) for i in str(ids).split(",")],
            "thumb": thumb} for fid, path, pid, ids, thumb in rows]
    return sorted(out, key=lambda r: (order[r["file_id"]], r["person_id"] is None, r["face_ids"][0]))


def assign_faces(con, face_ids, person_id):
    """Set the person of these faces by hand (None = not anybody known); kept by `cluster`.
    Returns an undo record for `unassign_faces`."""
    q = ",".join("?" * len(face_ids))
    before = con.execute(f"SELECT x.id, x.person_id, x.manual, p.name, p.hidden FROM faces x "
                         f"LEFT JOIN persons p ON p.id = x.person_id WHERE x.id IN ({q})", face_ids).fetchall()
    con.execute(f"UPDATE faces SET person_id = ?, manual = 1 WHERE id IN ({q})", [person_id, *face_ids])
    refresh_person_stats(con)  # an unnamed person left without faces disappears
    con.commit()
    return before


def unassign_faces(con, before):
    """Undo of `assign_faces`."""
    for fid, pid, manual, name, hidden in before:
        if pid is not None:
            con.execute("INSERT OR IGNORE INTO persons (id, name, hidden) VALUES (?, ?, ?)", (pid, name, hidden))
        con.execute("UPDATE faces SET person_id = ?, manual = ? WHERE id = ?", (pid, manual, fid))
    refresh_person_stats(con)
    con.commit()


PROTECTED_FILE = ROOT / "protected.txt"


def protected_roots():
    """Folders listed in protected.txt (one per line, # comments) are never deleted from."""
    if not PROTECTED_FILE.exists():
        return []
    lines = PROTECTED_FILE.read_text(encoding="utf-8").splitlines()
    # entries may be Windows paths (\\nas\home\...): mapped to this machine by PS_PATH_MAP
    return [os.path.normcase(os.path.normpath(map_path(l.strip()))) for l in lines
            if l.strip() and not l.lstrip().startswith("#")]


def is_protected(path):
    p = os.path.normcase(os.path.normpath(path))
    return any(p == r or p.startswith(r.rstrip("\\/") + os.sep) for r in protected_roots())


QUARANTINE_DIR = "_photosorter_deleted"


def quarantine_path(path):
    r"""Network files have no Recycle Bin: they are moved to \\server\share\_photosorter_deleted\<same path>.
    Returns None for local files (those go to the normal Recycle Bin).
    Linux (no Recycle Bin): every file is quarantined: NAS files at the root of their mount (PS_PATH_MAP),
    others in _photosorter_deleted next to the DB."""
    if os.name != "nt":
        path = os.path.normpath(path)
        roots = [l.rstrip("/") for _, l in PATH_MAP if path.startswith(l.rstrip("/") + "/")]
        root = max(roots, key=len) if roots else os.path.dirname(os.path.abspath(DEFAULT_DB))
        return os.path.join(root, QUARANTINE_DIR, path[len(root):].lstrip("/") if roots else path.lstrip("/"))
    drive, rest = os.path.splitdrive(os.path.normpath(path))
    if not drive.startswith("\\\\"):
        return None
    return os.path.join(drive + os.sep, QUARANTINE_DIR, rest.lstrip("\\/"))


# rows of a file that a deletion removes (with ON DELETE CASCADE) and an undo puts back;
# clip/tags exist only once scenes.py was used
_SNAP_TABLES = (("files", "id"), ("faces", "file_id"), ("previews", "file_id"), ("clip", "file_id"), ("tags", "file_id"),
                ("stacks", "file_id"), ("quality", "file_id"), ("copies", "file_id"))


def _snapshot(con, fid):
    snap = {}
    for table, col in _SNAP_TABLES:
        try:
            cur = con.execute(f"SELECT * FROM {table} WHERE {col} = ?", (fid,))
        except sqlite3.OperationalError:
            continue
        snap[table] = ([d[0] for d in cur.description], cur.fetchall())
    # people of these faces: an unnamed person left without faces is removed by refresh_person_stats
    pids = {r[snap["faces"][0].index("person_id")] for r in snap.get("faces", ([], []))[1]} - {None}
    if pids:
        cur = con.execute(f"SELECT * FROM persons WHERE id IN ({','.join('?' * len(pids))})", list(pids))
        snap["persons"] = ([d[0] for d in cur.description], cur.fetchall())
    return pickle.dumps(snap)


def _restore_snapshot(con, blob):
    snap = pickle.loads(blob)
    for table in ("persons",) + tuple(t for t, _ in _SNAP_TABLES):
        if table in snap:
            cols, rows = snap[table]
            verb = "INSERT OR IGNORE" if table == "persons" else "INSERT"  # a person may still exist
            con.executemany(f"{verb} INTO {table} ({', '.join(cols)}) VALUES ({', '.join('?' * len(cols))})", rows)


def _quarantine(p):
    """Move a file to its quarantine path (a free name there); returns where it went."""
    q = quarantine_path(p)
    base, ext = os.path.splitext(q)
    n = 1
    while os.path.exists(q):  # same name deleted before
        q, n = f"{base} ({n}){ext}", n + 1
    os.makedirs(os.path.dirname(q), exist_ok=True)
    os.rename(p, q)  # same share: instant server-side move
    return q


def send2trash(p):
    """Windows: Recycle Bin. Linux has none here: quarantine."""
    if os.name == "nt":
        from send2trash import send2trash as to_bin
        return to_bin(p)
    _quarantine(p)


def trash_files(con, paths):
    """Remove files: local ones to the Recycle Bin, network ones to the quarantine folder.
    The DB rows are saved in `deleted.snapshot`, so `restore_deleted` brings back everything (faces, names...).
    Returns (done, [(path, error)], [deleted-log rowids])."""
    blocked = [p for p in paths if is_protected(p)]
    if blocked:
        raise PermissionError(f"{len(blocked)} files are in a protected folder (see {PROTECTED_FILE}), "
                              f"e.g. {blocked[0]}. Nothing was deleted.")
    done, failed, log_ids = 0, [], []
    for p in paths:
        try:
            row = con.execute("SELECT id FROM files WHERE path = ?", (p,)).fetchone()
            snap = _snapshot(con, row[0]) if row else None
            if row:  # its hard links in other collections go too, or they would be left behind
                for (link,) in con.execute("SELECT link_path FROM collection_files WHERE file_id = ?", (row[0],)):
                    if os.path.normcase(link) != os.path.normcase(p) and os.path.exists(link):
                        send2trash(link)
            moved_to = None
            if os.path.exists(p):
                if quarantine_path(p):
                    moved_to = _quarantine(p)
                else:
                    send2trash(p)
            log_ids.append(con.execute("INSERT INTO deleted (path, moved_to, deleted_at, snapshot) VALUES (?,?,?,?)",
                                       (p, moved_to, datetime.now().isoformat(timespec="seconds"), snap)).lastrowid)
            con.execute("DELETE FROM files WHERE path = ?", (p,))
            done += 1
        except Exception as e:
            failed.append((p, str(e)))
    refresh_person_stats(con)
    con.commit()
    return done, failed, log_ids


def restore_deleted(con, log_ids):
    """Undo deletions: move files back from quarantine and put their DB rows back.
    Returns (restored, [(path, problem)])."""
    done, problems = 0, []
    for rowid in log_ids:
        row = con.execute("SELECT path, moved_to, snapshot FROM deleted WHERE rowid = ?", (rowid,)).fetchone()
        if not row:
            continue
        path, moved_to, snap = row
        if not os.path.exists(path):
            if not (moved_to and os.path.exists(moved_to)):
                problems.append((path, "в Корзине Windows — восстанови вручную" if not moved_to
                                 else f"нет в карантине: {moved_to}"))
                continue
            os.makedirs(os.path.dirname(path), exist_ok=True)
            os.rename(moved_to, path)
        if not snap:  # deleted by an older version: no saved DB rows
            problems.append((path, "файл возвращён; чтобы он снова появился в базе, запусти scan"))
        elif not con.execute("SELECT 1 FROM files WHERE path = ?", (path,)).fetchone():
            con.execute("SAVEPOINT undo")
            try:
                _restore_snapshot(con, snap)
                con.execute("RELEASE undo")
            except sqlite3.IntegrityError:
                con.execute("ROLLBACK TO undo")
                con.execute("RELEASE undo")
                problems.append((path, "файл возвращён, но в базу не встал — запусти scan"))
        con.execute("DELETE FROM deleted WHERE rowid = ?", (rowid,))
        done += 1
    refresh_person_stats(con)
    con.commit()
    return done, problems


# ---------------------------------------------------------------- collections (to send to people)

def get_setting(con, key, default=None):
    row = con.execute("SELECT value FROM settings WHERE key = ?", (key,)).fetchone()
    return row[0] if row else default


def set_setting(con, key, value):
    con.execute("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", (key, value))
    con.commit()


def collections_root(con):
    return get_setting(con, "collections_root", str(Path.home() / "Pictures" / "Photosorter"))


def _free_path(path):
    """path, or 'name (1).ext', 'name (2).ext'... if taken."""
    base, ext = os.path.splitext(path)
    n = 1
    while os.path.exists(path):
        path, n = f"{base} ({n}){ext}", n + 1
    return path


def create_collection(con, name, person_id=None):
    name = name.strip()
    folder = _free_path(os.path.join(collections_root(con),
                                     re.sub(r'[\\/:*?"<>|]+', "_", name).strip(" .") or "Подборка"))
    os.makedirs(folder, exist_ok=True)
    cid = con.execute("INSERT INTO collections (name, folder, created_at, person_id) VALUES (?, ?, ?, ?)",
                      (name, folder, datetime.now().isoformat(timespec="seconds"), person_id)).lastrowid
    con.commit()
    return cid


def _link_or_copy(src, dst):
    """Hard link (no extra disk space; NTFS, same volume), falling back to a copy."""
    try:
        os.link(src, dst)
    except OSError:
        shutil.copy2(src, dst)


def add_to_collection(con, cid, fid):
    """Put one file into a collection. Not collected yet: MOVE it out of the archive into the collection folder
    (files.path follows). Already in another collection: hard-link it here. Returns True if added."""
    if con.execute("SELECT 1 FROM collection_files WHERE collection_id = ? AND file_id = ?", (cid, fid)).fetchone():
        return False
    path = con.execute("SELECT path FROM files WHERE id = ?", (fid,)).fetchone()[0]
    if is_protected(path):
        raise PermissionError("защищённая папка (protected.txt)")
    folder = con.execute("SELECT folder FROM collections WHERE id = ?", (cid,)).fetchone()[0]
    os.makedirs(folder, exist_ok=True)
    dst = _free_path(os.path.join(folder, os.path.basename(path)))
    prev = con.execute("SELECT original_path FROM collection_files WHERE file_id = ? LIMIT 1", (fid,)).fetchone()
    if prev:
        _link_or_copy(path, dst)
        original = prev[0]
    else:
        shutil.move(path, dst)  # NAS -> local: copy + delete; keeps the modification time
        con.execute("UPDATE files SET path = ? WHERE id = ?", (dst, fid))
        original = path
    con.execute("INSERT INTO collection_files (collection_id, file_id, link_path, original_path, added_at) "
                "VALUES (?, ?, ?, ?, ?)", (cid, fid, dst, original, datetime.now().isoformat(timespec="seconds")))
    con.commit()
    return True


def move_to_collection(con, cid, paths):
    """Add files (by current path) to a collection. Returns (done, [(path, error)], [(cid, fid) added])."""
    done, failed, added = 0, [], []
    for p in paths:
        try:
            fid = con.execute("SELECT id FROM files WHERE path = ?", (p,)).fetchone()[0]
            if add_to_collection(con, cid, fid):
                done += 1
                added.append((cid, fid))
        except Exception as e:
            failed.append((p, str(e)))
    return done, failed, added


def collection_for_person(con, pid):
    """The person's own collection (named after them), created on first use."""
    row = con.execute("SELECT id FROM collections WHERE person_id = ?", (pid,)).fetchone()
    if row:
        return row[0]
    name = con.execute("SELECT name FROM persons WHERE id = ?", (pid,)).fetchone()[0]
    if con.execute("SELECT 1 FROM collections WHERE name = ?", (name,)).fetchone():
        name = f"{name} ({pid})"
    return create_collection(con, name, person_id=pid)


def collect_by_people(con, file_ids):
    """Put every file into the collections of all NAMED people on it (except people marked 'this is me').
    Returns ({person name: files added}, [file ids with nobody to send to], [(path, error)], [(cid, fid) added])."""
    per_person, nobody, failed, added = {}, [], [], []
    for fid in file_ids:
        people = con.execute("""
            SELECT DISTINCT p.id, p.name FROM faces f JOIN persons p ON p.id = f.person_id
            WHERE f.file_id = ? AND p.name IS NOT NULL AND p.skip_collect = 0 ORDER BY p.name""", (fid,)).fetchall()
        if not people:
            nobody.append(fid)
            continue
        for pid, name in people:
            try:
                cid = collection_for_person(con, pid)
                if add_to_collection(con, cid, fid):
                    per_person[name] = per_person.get(name, 0) + 1
                    added.append((cid, fid))
            except Exception as e:
                path = con.execute("SELECT path FROM files WHERE id = ?", (fid,)).fetchone()
                failed.append((path[0] if path else str(fid), str(e)))
    return per_person, nobody, failed, added


def remove_from_collection(con, cid, fid):
    """Take a file out of one collection. If it is still in other collections only this copy (link) goes away;
    if this was the last one, the file is moved back to its place in the archive."""
    row = con.execute("SELECT link_path, original_path FROM collection_files WHERE collection_id = ? AND file_id = ?",
                      (cid, fid)).fetchone()
    if not row:
        return
    link, original = row
    path = con.execute("SELECT path FROM files WHERE id = ?", (fid,)).fetchone()[0]
    others = [p for (p,) in con.execute("SELECT link_path FROM collection_files WHERE file_id = ? AND collection_id != ?",
                                        (fid, cid))]
    if others:
        if os.path.exists(link):
            os.remove(link)  # one of several hard links: the data stays in the other collections
        if os.path.normcase(path) == os.path.normcase(link):
            con.execute("UPDATE files SET path = ? WHERE id = ?", (others[0], fid))
    else:
        if os.path.exists(original):
            raise FileExistsError(f"на старом месте уже есть файл: {original}")
        os.makedirs(os.path.dirname(original), exist_ok=True)
        shutil.move(path, original)
        con.execute("UPDATE files SET path = ? WHERE id = ?", (original, fid))
    con.execute("DELETE FROM collection_files WHERE collection_id = ? AND file_id = ?", (cid, fid))
    con.commit()


def return_from_collection(con, pairs):
    """Undo of collecting: [(collection id, file id)], newest first. Returns (done, [(path, error)])."""
    done, failed = 0, []
    for cid, fid in reversed(pairs):
        try:
            remove_from_collection(con, cid, fid)
            done += 1
        except Exception as e:
            failed.append((str(fid), str(e)))
    return done, failed


def collection_zip_path(con, cid):
    folder = con.execute("SELECT folder FROM collections WHERE id = ?", (cid,)).fetchone()[0]
    return folder.rstrip("\\/") + ".zip"


def zip_collection(con, cid):
    """Pack the collection into <folder>.zip next to it (stored, no compression: photos/videos are compressed already)."""
    import zipfile
    out = collection_zip_path(con, cid)
    paths = [p for (p,) in con.execute("SELECT c.link_path FROM collection_files c JOIN files f ON f.id = c.file_id "
                                       "WHERE c.collection_id = ? ORDER BY f.taken_at, f.path", (cid,))]
    tmp = out + ".part"
    with zipfile.ZipFile(tmp, "w", zipfile.ZIP_STORED, allowZip64=True) as z:
        for p in paths:
            if os.path.exists(p):
                z.write(p, arcname=os.path.basename(p))
    os.replace(tmp, out)
    return out, len(paths)


def finish_collection(con, cid, delete_zip=True):
    """'Sent': delete the collection's files and close it. Files that are also in other collections stay there
    (only this collection's link goes to the Recycle Bin); the rest are deleted for good (Recycle Bin + DB snapshot)."""
    folder = con.execute("SELECT folder FROM collections WHERE id = ?", (cid,)).fetchone()[0]
    done, failed, last = 0, [], []
    for fid, link in con.execute("SELECT file_id, link_path FROM collection_files WHERE collection_id = ?",
                                 (cid,)).fetchall():
        others = [p for (p,) in con.execute(
            "SELECT link_path FROM collection_files WHERE file_id = ? AND collection_id != ?", (fid, cid))]
        if not others:
            last.append(con.execute("SELECT path FROM files WHERE id = ?", (fid,)).fetchone()[0])
            continue
        try:
            if os.path.exists(link):
                send2trash(link)
            path = con.execute("SELECT path FROM files WHERE id = ?", (fid,)).fetchone()[0]
            if os.path.normcase(path) == os.path.normcase(link):
                con.execute("UPDATE files SET path = ? WHERE id = ?", (others[0], fid))
            con.execute("DELETE FROM collection_files WHERE collection_id = ? AND file_id = ?", (cid, fid))
            con.commit()
            done += 1
        except Exception as e:
            failed.append((link, str(e)))
    d, f, _ = trash_files(con, last)
    done, failed = done + d, failed + f
    z = collection_zip_path(con, cid)
    if delete_zip and os.path.exists(z):
        send2trash(z)
    if not failed:
        con.execute("DELETE FROM collections WHERE id = ?", (cid,))
        con.commit()
        try:
            os.rmdir(folder)  # only if empty
        except OSError:
            pass
    return done, failed


def cmd_restore(args):
    """Move files from the quarantine folder back to where they were, with their DB data."""
    con = open_db(args.db)
    ids = [r for (r,) in con.execute("SELECT rowid FROM deleted" + (" WHERE deleted_at >= ?" if args.since else "")
                                     + " ORDER BY rowid DESC", [args.since] if args.since else [])]
    done, problems = restore_deleted(con, ids)
    for p, why in problems:
        print(f"  {p}: {why}")
    print(f"Restored {done} of {len(ids)} files.")


def cmd_merge(args):
    con = open_db(args.db)
    ids = [resolve_person(con, p) for p in args.persons]
    merge_persons(con, ids)
    print(f"Merged {ids[1:]} into #{ids[0]}")


def cmd_hide(args):
    con = open_db(args.db)
    for p in args.persons:
        con.execute("UPDATE persons SET hidden = ? WHERE id = ?", (0 if args.unhide else 1, resolve_person(con, p)))
    con.commit()


# ================================================================ export / delete

def safe_name(rel):
    return re.sub(r'[\\/:*?"<>|]+', "__", str(rel).lstrip("\\/"))


def cmd_export(args):
    con = open_db(args.db)
    pid = resolve_person(con, args.person)
    out = Path(args.out).resolve()
    rows = con.execute("""
        SELECT f.path, f.kind,
               (SELECT COUNT(DISTINCT person_id) FROM faces WHERE file_id = f.id AND person_id IS NOT NULL),
               (SELECT group_concat(DISTINCT COALESCE(t_sec, '')) FROM faces WHERE file_id = f.id AND person_id = ?)
        FROM files f WHERE f.id IN (SELECT file_id FROM faces WHERE person_id = ?)
        ORDER BY f.path
    """, (pid, pid)).fetchall()
    if out.exists() and any(out.iterdir()):
        sys.exit(f"{out} is not empty: finish (apply) or clear the previous review first")
    manifest = []
    for path, kind, n_people, times in tqdm(rows, unit="file"):
        if not os.path.exists(path):
            continue
        sub = out / ("alone" if n_people <= 1 else "with_others")
        sub.mkdir(parents=True, exist_ok=True)
        dst = sub / safe_name(os.path.splitdrive(path)[1])
        shutil.copy2(path, dst)
        manifest.append({"copy": str(dst.relative_to(out)), "original": path, "kind": kind,
                         "people_in_file": n_people, "video_times": times or ""})
    with open(out / "manifest.csv", "w", newline="", encoding="utf-8-sig") as f:
        w = csv.DictWriter(f, fieldnames=["copy", "original", "kind", "people_in_file", "video_times"])
        w.writeheader()
        w.writerows(manifest)
    alone = sum(m["people_in_file"] <= 1 for m in manifest)
    print(f"Copied {len(manifest)} files: {alone} -> {out / 'alone'}, {len(manifest) - alone} -> {out / 'with_others'}")
    print("Look through them, DELETE COPIES you want to KEEP the originals of, then: python photosorter.py apply")


def cmd_apply(args):
    out = Path(args.out).resolve()
    with open(out / "manifest.csv", encoding="utf-8-sig") as f:
        rows = list(csv.DictReader(f))
    todo = [r for r in rows if (out / r["copy"]).exists() and Path(r["original"]).exists()]
    print(f"{len(todo)} originals still have a copy in {out} (of {len(rows)} in manifest).")
    if not todo:
        return
    for r in todo[:10]:
        print("   ", r["original"])
    if len(todo) > 10:
        print(f"    ... and {len(todo) - 10} more")
    if not args.yes and input("Move these ORIGINALS to the Recycle Bin? [y/N] ").strip().lower() != "y":
        print("Cancelled.")
        return
    done, failed, _ = trash_files(open_db(args.db), [r["original"] for r in todo])
    for p, e in failed:
        print(f"  failed: {p}: {e}")
    shutil.rmtree(out)
    print(f"Moved {done} files to the Recycle Bin, review folder removed.")


# ================================================================ cli

def build_parser():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--db", default=DEFAULT_DB)
    sub = ap.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("scan", help="find faces in all photos/videos")
    s.add_argument("--src", required=True, help="folder with photos/videos (recursive)")
    s.add_argument("--det-size", type=int, default=960, help="detector input size; larger finds smaller faces")
    s.add_argument("--min-face", type=int, default=24, help="ignore faces smaller than this many pixels")
    s.add_argument("--video-step", type=float, default=2.0, help="seconds between sampled video frames")
    s.add_argument("--no-video", action="store_true")
    s.add_argument("--workers", type=int, default=8)
    s.add_argument("--queue-wait", type=float, default=float(os.environ.get("PS_QUEUE_WAIT", "10")),
                   help="minutes to leave the queue to an active remote worker before helping (queue_api.py)")
    s.set_defaults(func=cmd_scan)

    r = sub.add_parser("recheck", help="find files with faces missed by earlier scans (uses stored previews)")
    r.add_argument("--det-size", type=int, default=960)
    r.set_defaults(func=cmd_recheck)

    c = sub.add_parser("cluster", help="group faces into people")
    c.add_argument("--edge", type=float, default=0.45, help="min similarity for two faces to be linked")
    c.add_argument("--merge", type=float, default=0.55, help="merge clusters with centroid similarity above this")
    c.add_argument("--assign", type=float, default=0.45, help="attach leftover faces to a person above this")
    c.add_argument("--k", type=int, default=30, help="neighbours per face in the graph")
    c.add_argument("--core-size", type=float, default=40, help="min face size (px) used to build clusters")
    c.add_argument("--core-det", type=float, default=0.65, help="min detector score used to build clusters")
    c.add_argument("--min-files", type=int, default=2, help="a person must appear in at least this many files")
    c.set_defaults(func=cmd_cluster)

    g = sub.add_parser("gallery", help="write an HTML page with every person's faces")
    g.add_argument("--out", default=str(ROOT / "people.html"))
    g.add_argument("--samples", type=int, default=16)
    g.set_defaults(func=cmd_gallery)

    p = sub.add_parser("people", help="list people")
    p.add_argument("--limit", type=int, default=100)
    p.add_argument("--all", action="store_true", help="include hidden")
    p.set_defaults(func=cmd_people)

    n = sub.add_parser("name", help="name a person")
    n.add_argument("person")
    n.add_argument("name")
    n.set_defaults(func=cmd_name)

    m = sub.add_parser("merge", help="merge people into the first one")
    m.add_argument("persons", nargs="+")
    m.set_defaults(func=cmd_merge)

    h = sub.add_parser("hide", help="hide people (strangers)")
    h.add_argument("persons", nargs="+")
    h.add_argument("--unhide", action="store_true")
    h.set_defaults(func=cmd_hide)

    e = sub.add_parser("export", help="copy all files with a person into a review folder")
    e.add_argument("person", help="id or name")
    e.add_argument("--out", default=str(ROOT / "_review"))
    e.set_defaults(func=cmd_export)

    a = sub.add_parser("apply", help="move originals of files left in the review folder to the Recycle Bin")
    a.add_argument("--out", default=str(ROOT / "_review"))
    a.add_argument("--yes", action="store_true")
    a.set_defaults(func=cmd_apply)

    rs = sub.add_parser("restore", help=f"move files back from the network quarantine folder ({QUARANTINE_DIR})")
    rs.add_argument("--since", help="only files deleted at/after this time, e.g. 2026-10-14T03:00")
    rs.set_defaults(func=cmd_restore)
    return ap


def main():
    args = build_parser().parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
