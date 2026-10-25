"""Stacks of similar photos (bursts, retakes, the same moment shot by several phones/cameras, copies)
and a quality score inside each stack, to delete the weaker duplicates.

    build : group photos into stacks (SigLIP2 similarity, the time between shots only loosens/tightens it)
    score : per-photo quality from the originals (sharpness of the whole frame and of the faces, exposure)
            + SigLIP2 "aesthetics"; reads the files, changes nothing

Similarity is the neural one (SigLIP2 image embeddings from `scenes.py embed`), so photos from different
devices stack too; their clocks may be off, hence a wide time window with a stricter threshold.
"""
import argparse
import sys
import time
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
from pathlib import Path

import cv2
import numpy as np
from tqdm import tqdm

import photosorter as ps
import scenes

SCHEMA = """
CREATE TABLE IF NOT EXISTS stacks (
    file_id  INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    stack_id INTEGER NOT NULL,             -- the smallest file id of the stack
    copies   INTEGER NOT NULL DEFAULT 0    -- 1 = the stack is copies of one shot (resized, re-sent, RAW+JPEG)
);
CREATE INDEX IF NOT EXISTS stacks_stack ON stacks(stack_id);
CREATE TABLE IF NOT EXISTS copies (     -- copies of one picture (also inside a series stack)
    file_id  INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    group_id INTEGER NOT NULL              -- the smallest file id of the group
);
CREATE INDEX IF NOT EXISTS copies_group ON copies(group_id);
CREATE TABLE IF NOT EXISTS quality (
    file_id    INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    sharp      REAL,                       -- detail of the sharpest parts of the frame (bokeh-tolerant)
    face_sharp REAL,                       -- the same on the faces, NULL = no faces
    exposure   REAL,                       -- 0 = fine, up to 1 = blown out / black
    aesthetic  REAL,                       -- SigLIP2: "good photo" minus "bad photo" prompts
    error      TEXT
);
"""

# similarity needed to stack two photos, by the time between them (seconds); one phone's burst
# changes little, the same scene from another device / after a while has to look much closer
TIME_THRESHOLDS = [(2, 0.85), (10, 0.88), (60, 0.91), (300, 0.93), (2 * 3600, 0.95)]
# copies anywhere in the archive (forwarded, resized, converted): the network sees the same picture, or the
# 64-bit dHash is (almost) equal, the network agrees roughly and the proportions are the same
COPY_SIM, COPY_HAM = 0.985, 4
COPY_HASH_HAM, COPY_HASH_SIM = 2, 0.93
MAX_LOOKBACK = 400             # photos compared back in time
ANCHOR_SIM = 0.92               # ...and to the first photo of the stack (keeps stacks tight)

# screenshots/documents look alike to the network but each one has its own text: only true copies stack
NO_SERIES = ("Скриншот", "Документы и текст", "Чеки и квитанции")

GOOD = ["a sharp, well-composed, high quality photo", "a beautiful photo", "people smiling and looking at the camera"]
BAD = ["a blurry photo", "an out of focus photo", "a motion-blurred photo", "a dark underexposed photo",
       "an overexposed photo", "a person with closed eyes", "a photo taken by accident"]


def open_db(path):
    con = scenes.open_db(path)
    con.executescript(SCHEMA)
    return con


def _ts(s):
    return datetime.fromisoformat(s[:19]).timestamp()


def _ham(a, b):
    return bin((a ^ b) & 0xFFFFFFFFFFFFFFFF).count("1") if a is not None and b is not None else 64


def build(con, progress=None):
    """Recompute the stacks table. Returns (stacks, photos in them)."""
    fids, M = scenes.file_embeddings(con)
    row = {int(f): i for i, f in enumerate(fids)}
    files = [r for r in con.execute("SELECT id, taken_at, phash, CAST(width AS REAL) / MAX(height, 1), "
                                    "date_source = 'exif', width, height, size, lower(path) FROM files "
                                    "WHERE kind = 'image' AND error IS NULL "
                                    "AND taken_at IS NOT NULL ORDER BY taken_at, path") if r[0] in row]
    ids = [r[0] for r in files]
    E = M[[row[i] for i in ids]]
    T = np.array([_ts(r[1]) for r in files])
    H = [r[2] for r in files]
    A = [r[3] or 0 for r in files]
    X = [bool(r[4]) for r in files]  # the date is the camera's own (EXIF): to the second
    F = [(r[5], r[6], Path(r[8]).suffix.replace(".jpeg", ".jpg"), r[7]) for r in files]  # w, h, format, bytes
    q = ",".join("?" * len(NO_SERIES))
    texty = {f for (f,) in con.execute(f"SELECT file_id FROM tags WHERE rank = 1 AND label IN ({q})", NO_SERIES)}
    series_ok = np.array([i not in texty for i in ids])
    parent = list(range(len(ids)))

    def find(i):
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i

    def union(i, j):
        a, b = find(i), find(j)
        if a != b:
            parent[max(a, b)] = min(a, b)

    # 1) shots close in time (any device): threshold by the time gap. A photo joins the stack of its best
    # match only if it also looks like the stack's first photo: a long burst whose scene slowly changes
    # becomes several stacks (moments), not one chain of 200 shots
    window = TIME_THRESHOLDS[-1][0]
    leader = list(range(len(ids)))  # stack (by its first photo) of each photo
    for i in range(1, len(ids)):
        lo = max(0, i - MAX_LOOKBACK, int(np.searchsorted(T, T[i] - window)))
        if lo == i or not series_ok[i]:
            continue
        sims = E[lo:i] @ E[i]
        dt = T[i] - T[lo:i]
        thr = np.full(i - lo, 2.0)
        for limit, t in reversed(TIME_THRESHOLDS):
            thr[dt <= limit] = t
        thr[~series_ok[lo:i]] = 2.0
        for j in np.argsort(-sims):
            if sims[j] < thr[j]:
                break
            first = leader[lo + int(j)]
            if float(E[first] @ E[i]) >= ANCHOR_SIM:
                leader[i] = first
                union(first, i)
                break
        if progress and i % 2000 == 0:
            progress(i / len(ids) / 2)

    # 2) copies anywhere (a photo sent back from a messenger years later, an edited copy...)
    def other_shots(i, j):
        """Look alike but are two different shots, not copies of one."""
        if X[i] and X[j] and abs(T[i] - T[j]) >= 1:
            return True  # both dated by the camera, different moments: two frames of a burst
        # same size and format but other bytes: another shot (a copy would be resized or converted)
        return F[i][:3] == F[j][:3] and F[i][3] != F[j][3]

    copy_pairs = set()
    for s in range(0, len(ids), 1024):
        S = E[s:s + 1024] @ E.T
        for a, b in zip(*np.nonzero(S >= COPY_HASH_SIM)):
            i, j = s + int(a), int(b)
            if i >= j:
                continue
            ham = _ham(H[i], H[j])
            if other_shots(i, j):
                continue
            same_shape = abs(A[i] - A[j]) <= 0.01 * max(A[i], A[j])
            if (S[a, b] >= COPY_SIM and ham <= COPY_HAM) or (ham <= COPY_HASH_HAM and same_shape):
                copy_pairs.add((i, j, ham, float(S[a, b])))
                union(i, j)
        if progress:
            progress(0.5 + s / len(ids) / 2)

    copy_set = {(i, j) for i, j, _, _ in copy_pairs}
    groups = {}
    for i in range(len(ids)):
        groups.setdefault(find(i), []).append(i)
    rows = []
    for members in groups.values():
        if len(members) < 2:
            continue
        sid = min(ids[m] for m in members)
        # copies of one shot: every member is a near-identical copy of the first one
        copies = all((min(members[0], m), max(members[0], m)) in copy_set or
                     (float(E[members[0]] @ E[m]) >= 0.985 and _ham(H[members[0]], H[m]) <= 6) for m in members[1:])
        rows += [(ids[m], sid, int(copies)) for m in members]
    con.execute("DELETE FROM stacks")
    con.executemany("INSERT INTO stacks (file_id, stack_id, copies) VALUES (?,?,?)", rows)

    # copy groups on their own (only near-identical pairs, not the series around them)
    # closest pairs first; two groups merge only if no pair across them is "other shots" (no chains
    # like frame A ~ RAW ~ frame B)
    group = {}
    for i, j, _, _ in sorted(copy_pairs, key=lambda p: (p[2], -p[3])):
        gi, gj = group.get(i, [i]), group.get(j, [j])
        if gi is gj or any(other_shots(a, b) for a in gi for b in gj):
            continue
        gi += gj
        for k in gi:
            group[k] = gi
    groups = {id(g): g for g in group.values()}
    con.execute("DELETE FROM copies")
    con.executemany("INSERT INTO copies (file_id, group_id) VALUES (?,?)",
                    [(ids[m], min(ids[k] for k in members)) for members in groups.values() for m in members])
    con.commit()
    return len({r[1] for r in rows}), len(rows)


# ---------------------------------------------------------------- quality

def _detail(gray):
    """Mean of the strongest 5% Laplacian responses: the sharpest parts decide (a blurred background is fine)."""
    lap = np.abs(cv2.Laplacian(gray, cv2.CV_32F, ksize=3))
    k = max(1, lap.size // 20)
    return float(np.partition(lap.ravel(), -k)[-k:].mean())


def measure(path, boxes):
    """(sharp, face_sharp, exposure) of one image file; boxes are face boxes in the original pixels."""
    img = ps.load_image(Path(path))
    if img is None:
        raise ValueError("не читается")
    h, w = img.shape[:2]
    gray = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)
    scale = 1600 / max(h, w)  # the same scale for every photo: sizes differ between devices
    small = cv2.resize(gray, (round(w * scale), round(h * scale)), interpolation=cv2.INTER_AREA) if scale < 1 else gray
    sharp = _detail(small)
    hist = np.bincount(small.ravel(), minlength=256) / small.size
    exposure = float(min(1.0, hist[:6].sum() + hist[250:].sum()) + abs(small.mean() / 255 - 0.45) * 0.5)
    face_vals, weights = [], []
    for x1, y1, x2, y2 in boxes:
        x1, y1, x2, y2 = (int(round(v)) for v in (max(0, x1), max(0, y1), min(w, x2), min(h, y2)))
        if x2 - x1 < 24 or y2 - y1 < 24:
            continue
        face = cv2.resize(gray[y1:y2, x1:x2], (128, 128), interpolation=cv2.INTER_AREA)
        face_vals.append(_detail(face))
        weights.append((x2 - x1) * (y2 - y1))
    face_sharp = float(np.average(face_vals, weights=weights)) if face_vals else None
    return sharp, face_sharp, exposure


def score(con, workers=8, progress=None, limit=None):
    """Quality of every stacked photo that has none yet (reads the originals, writes only the DB)."""
    todo = con.execute("""SELECT f.id, f.path FROM stacks s JOIN files f ON f.id = s.file_id
                          WHERE NOT EXISTS (SELECT 1 FROM quality q WHERE q.file_id = f.id)""").fetchall()
    if limit:
        todo = todo[:limit]
    aesthetic = _aesthetic(con, [fid for fid, _ in todo])
    boxes = {}
    for fid, x1, y1, x2, y2 in con.execute("SELECT file_id, x1, y1, x2, y2 FROM faces WHERE t_sec IS NULL"):
        boxes.setdefault(fid, []).append((x1, y1, x2, y2))

    def one(item):
        fid, path = item
        try:
            return fid, measure(path, boxes.get(fid, [])), None
        except Exception as e:  # a broken file must not stop the rest
            return fid, (None, None, None), str(e)[:200]

    done = 0
    with ThreadPoolExecutor(workers) as pool:
        for fid, (sharp, face_sharp, exposure), err in pool.map(one, todo):
            con.execute("INSERT OR REPLACE INTO quality (file_id, sharp, face_sharp, exposure, aesthetic, error) "
                        "VALUES (?,?,?,?,?,?)", (fid, sharp, face_sharp, exposure, aesthetic.get(fid), err))
            done += 1
            if done % 100 == 0:
                con.commit()
                if progress:
                    progress(done / len(todo))
    con.commit()
    return len(todo)


def _aesthetic(con, file_ids):
    if not file_ids:
        return {}
    fids, M = scenes.file_embeddings(con)
    clip = scenes.Clip(image=False)
    g, b = clip.text(GOOD).mean(0), clip.text(BAD).mean(0)
    s = M @ (g / np.linalg.norm(g)) - M @ (b / np.linalg.norm(b))
    want = set(file_ids)
    return {int(f): float(v) for f, v in zip(fids, s) if int(f) in want}


# ---------------------------------------------------------------- ranking inside a stack

def _z(v):
    v = np.array([np.nan if x is None else x for x in v], np.float64)
    if np.isnan(v).all():
        return np.zeros(len(v))
    v = np.where(np.isnan(v), np.nanmin(v), v)
    sd = v.std()
    return (v - v.mean()) / sd if sd > 1e-9 else np.zeros(len(v))


def rank_stack(members, copies):
    """members: [{"id", "pixels", "size", "exif", "sharp", "face_sharp", "exposure", "aesthetic"}].
    Returns {id: score 0..100} — relative to the stack, the best one gets 100."""
    col = lambda k: [m[k] for m in members]
    if copies:  # the same shot: keep the biggest, least compressed original
        s = 0.5 * _z(col("pixels")) + 0.3 * _z(col("size")) + 0.2 * _z(col("exif")) + 0.1 * _z(col("sharp"))
    else:
        has_faces = any(m["face_sharp"] is not None for m in members)
        s = (0.35 * _z(col("face_sharp")) if has_faces else 0) + (0.25 if has_faces else 0.45) * _z(col("sharp")) \
            + 0.25 * _z(col("aesthetic")) - 0.15 * _z(col("exposure")) + 0.05 * _z(col("pixels"))
    s = np.asarray(s, np.float64) * np.ones(len(members))
    lo, hi = s.min(), s.max()
    norm = (s - lo) / (hi - lo) * 100 if hi > lo else np.full(len(members), 100.0)
    return {m["id"]: float(v) for m, v in zip(members, norm)}


ORIGINAL_EXT = {".heic", ".heif"} | {e for e in ps.RAW_EXT}


def load_copies(con, where="", params=(), prefer="jpeg"):
    """[(group_id, [member dicts, the one to keep first])], groups in time order. The keeper: the biggest
    picture; of the same size — JPEG or the original HEIC/RAW (`prefer`: "jpeg" | "original"); then with EXIF,
    the biggest file (least compressed), the shortest name (not "X(0).jpg")."""
    rows = con.execute(f"""
        SELECT c.group_id, f.id, f.path, f.kind, f.taken_at, f.place, f.width, f.height, f.size,
               f.date_source = 'exif', f.camera_model
        FROM copies c JOIN files f ON f.id = c.file_id {where} ORDER BY f.taken_at, f.path""", params).fetchall()
    groups = {}
    for gid, fid, path, kind, taken, place, w, h, size, exif, cam in rows:
        groups.setdefault(gid, []).append(
            {"id": fid, "path": path, "kind": kind, "taken_at": taken, "place": place, "w": w or 0, "h": h or 0,
             "size": size or 0, "exif": bool(exif), "camera": cam})
    out = []
    for gid, members in groups.items():
        if len(members) < 2:
            continue
        for m in members:
            m["original"] = Path(m["path"]).suffix.lower() in ORIGINAL_EXT
            m["identical"] = len({(x["w"], x["h"], x["size"]) for x in members}) == 1
        members.sort(key=lambda m: (-m["w"] * m["h"], m["original"] != (prefer == "original"), not m["exif"],
                                    -m["size"], len(Path(m["path"]).name), m["path"]))
        out.append((gid, members))
    return out


def load_stacks(con, where="", params=()):
    """[(stack_id, copies, [member dicts sorted best first, each with "score"])], stacks in time order."""
    rows = con.execute(f"""
        SELECT s.stack_id, s.copies, f.id, f.path, f.kind, f.taken_at, f.place, f.width * f.height, f.size,
               f.date_source = 'exif', q.sharp, q.face_sharp, q.exposure, q.aesthetic, f.camera_model
        FROM stacks s JOIN files f ON f.id = s.file_id LEFT JOIN quality q ON q.file_id = f.id
        {where} ORDER BY f.taken_at, f.path""", params).fetchall()
    stacks = {}
    for (sid, copies, fid, path, kind, taken, place, px, size, exif, sharp, fs, expo, aes, cam) in rows:
        stacks.setdefault(sid, (copies, []))[1].append(
            {"id": fid, "path": path, "kind": kind, "taken_at": taken, "place": place, "pixels": px or 0,
             "size": size or 0, "exif": int(bool(exif)), "sharp": sharp, "face_sharp": fs, "exposure": expo,
             "aesthetic": aes, "camera": cam, "scored": sharp is not None})
    out = []
    for sid, (copies, members) in stacks.items():
        if len(members) < 2:  # the rest was deleted
            continue
        sc = rank_stack(members, copies) if all(m["scored"] for m in members) else {}
        for m in members:
            m["score"] = sc.get(m["id"])
        members.sort(key=lambda m: -(m["score"] if m["score"] is not None else -1))
        out.append((sid, copies, members))
    return out


# ---------------------------------------------------------------- CLI

def cmd_build(args):
    con = open_db(args.db)
    t0 = time.time()
    n, k = build(con)
    print(f"{n} stacks, {k} photos in them ({time.time() - t0:.0f}s)")
    sizes = [len(m) for _, _, m in load_stacks(con)]
    print("sizes:", {s: sizes.count(s) for s in sorted(set(sizes))[:12]}, "max", max(sizes) if sizes else 0)


def cmd_score(args):
    con = open_db(args.db)
    t0 = time.time()
    bar = tqdm(total=100, unit="%")
    n = score(con, args.workers, progress=lambda f: bar.update(round(f * 100) - bar.n), limit=args.limit)
    bar.close()
    print(f"Scored {n} photos in {time.time() - t0:.0f}s")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--db", default=str(ps.DEFAULT_DB) if hasattr(ps, "DEFAULT_DB") else "faces.db")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("build").set_defaults(fn=cmd_build)
    p = sub.add_parser("score")
    p.add_argument("--workers", type=int, default=8)
    p.add_argument("--limit", type=int)
    p.set_defaults(fn=cmd_score)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
