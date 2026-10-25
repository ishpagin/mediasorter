"""Score face clustering against LFW ground truth (made by make_testset.py).

Usage: python tools/evaluate.py <db> <truth.json>
"""
import sys, json, sqlite3
from collections import Counter, defaultdict
from pathlib import Path
db = sys.argv[1]; truth = {str(Path(k).resolve()): v for k, v in json.load(open(sys.argv[2])).items()}
con = sqlite3.connect(db)
rows = con.execute("SELECT f.path, fa.person_id, fa.x1, fa.y1, fa.x2, fa.y2 FROM faces fa JOIN files f ON f.id=fa.file_id").fetchall()
files_with_faces = {r[0] for r in rows}
labeled = []  # (true_name, cluster)
for path, pid, x1, y1, x2, y2 in rows:
    names = truth.get(path)
    if not names: continue
    cx, cy = (x1 + x2) / 2, (y1 + y2) / 2
    if path.endswith('.mp4'):
        labeled.append((names[0], pid)); continue
    tile = int(cx // 250)
    if tile >= len(names) or abs(cx - (tile * 250 + 125)) > 50 or abs(cy - 125) > 50: continue  # background face
    labeled.append((names[tile], pid))
counts = Counter(n for t in truth.values() for n in t)
multi = {n for n, c in counts.items() if c >= 5}
found = sum(1 for p in truth if p in files_with_faces)
print(f"files with a detected face: {found}/{len(truth)}")
by_person = defaultdict(list); by_cluster = defaultdict(list)
for n, pid in labeled:
    by_person[n].append(pid)
    if pid is not None: by_cluster[pid].append(n)
# recall: share of a recurring person's faces landing in their main cluster
rec, split = [], 0
for n in multi:
    pids = by_person.get(n, [])
    if not pids: continue
    c = Counter(p for p in pids if p is not None)
    rec.append((c.most_common(1)[0][1] if c else 0) / len(pids))
    if sum(1 for p, k in c.items() if k >= 2) > 1: split += 1
# purity: share of a cluster that is its majority person
pur_faces = sum(Counter(v).most_common(1)[0][1] for v in by_cluster.values())
tot = sum(len(v) for v in by_cluster.values())
impure = [(pid, Counter(v).most_common(3)) for pid, v in by_cluster.items() if Counter(v).most_common(1)[0][1] / len(v) < 0.9]
singles = [pid for n, pid in labeled if counts[n] == 1]
print(f"recurring people: {len(multi)}, clusters: {len(by_cluster)}")
print(f"recall (faces in person's main cluster): {sum(rec)/len(rec):.3f}")
print(f"people split across >1 cluster: {split}")
print(f"purity (faces in clusters that belong to the cluster's majority person): {pur_faces/tot:.4f}")
print(f"impure clusters (<90% one person): {len(impure)}", impure[:5])
print(f"one-off strangers wrongly put in a cluster: {sum(p is not None for p in singles)}/{len(singles)}")
