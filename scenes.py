"""Scene classification and text search over stored previews (no access to the originals).

    embed   : compute SigLIP2 image embeddings for all previews in the DB (incremental)
    tag     : zero-shot categories (sea, mountains, documents, screenshots, cats...) -> table tags
    search  : find photos/videos by a free-text query, in Russian or English

Model: SigLIP2 ViT-B/16 (multilingual), ONNX export by Immich, run with ONNX Runtime.
"""
import argparse
import re
import sys
import time

import cv2
import numpy as np
from tqdm import tqdm

import photosorter as ps

MODEL = ps.MODELS / "siglip2"
DIM = 768
CONTEXT = 64

SCHEMA = """
CREATE TABLE IF NOT EXISTS clip (
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    t_sec   REAL,                          -- preview time for videos
    emb     BLOB NOT NULL                  -- float16[768], L2-normalized
);
CREATE INDEX IF NOT EXISTS clip_file ON clip(file_id);
CREATE TABLE IF NOT EXISTS tags (
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    rank    INTEGER NOT NULL,              -- 1 = best category
    label   TEXT NOT NULL,
    score   REAL NOT NULL                  -- probability among the categories
);
CREATE INDEX IF NOT EXISTS tags_file ON tags(file_id);
CREATE INDEX IF NOT EXISTS tags_label ON tags(label, rank);
"""

# category -> prompts (English prompts work best; the label is what you see)
CATEGORIES = {
    "Море и пляж": ["a photo of a beach", "a photo of the sea", "people swimming in the sea"],
    "Горы": ["a photo of mountains", "a mountain landscape", "hiking in the mountains"],
    "Лес и природа": ["a photo of a forest", "a nature landscape", "a field and trees", "rocks and a cave",
                      "the ground with plants", "a park with trees and paths", "autumn leaves", "mushrooms"],
    "Река и озеро": ["a photo of a river", "a photo of a lake", "a waterfall"],
    "Город и улицы": ["a city street", "buildings in a city", "a town square"],
    "Достопримечательности": ["a famous landmark", "a monument", "a church or cathedral", "a museum exhibit"],
    "Дом и интерьер": ["a room interior", "a kitchen", "a living room", "a bedroom", "a bathroom", "a bed",
                       "a wall with wallpaper", "a hallway"],
    "Еда и напитки": ["a photo of food", "a dish on a plate", "drinks in glasses", "a restaurant table with food",
                      "food cooking on a grill"],
    "Торт и праздничный стол": ["a birthday cake with candles", "a festive dinner table"],
    "Новый год": ["a christmas tree", "new year decorations", "new year celebration"],
    "Документы и текст": ["a document", "a sheet of paper with text", "a passport or id card", "a contract",
                          "a hand-drawn sketch on paper"],
    "Чеки и квитанции": ["a receipt", "a bill or invoice"],
    "Скриншот": ["a screenshot of a phone screen", "a screenshot of an app", "a screenshot of a chat"],
    "Картинки и мемы": ["a meme", "a cartoon picture", "a digital illustration", "a poster or advertisement"],
    "Кошки": ["a photo of a cat", "a kitten"],
    "Собаки": ["a photo of a dog", "a puppy"],
    "Другие животные": ["a photo of a horse", "a photo of birds", "farm animals", "a wild animal"],
    "Цветы и растения": ["a photo of flowers", "a garden with plants", "a bouquet"],
    "Автомобили": ["a photo of a car", "a car interior", "a car dashboard"],
    "Поездки и транспорт": ["an airplane", "a view from an airplane window", "a train", "an airport", "a bus",
                            "a road", "a view from a car window", "a highway"],
    "Закат и небо": ["a sunset", "a sunrise", "the sky with clouds"],
    "Ночь и огни": ["city lights at night", "fireworks", "a night scene"],
    "Зима и снег": ["snow in winter", "a snowy landscape"],
    "Концерт и мероприятие": ["a concert stage", "a crowd at an event", "a performance"],
    "Спорт": ["people playing sports", "a gym", "a stadium"],
    "Дети и игрушки": ["children's toys", "a playground", "a child playing"],
    "Ремонт и стройка": ["a construction site", "home renovation", "tools and building materials"],
    "Вещи и покупки": ["a product photo", "electronics", "clothes", "items for sale", "a product package",
                       "cosmetics bottles and tubes", "a snack package", "medicine", "a wallet or a bag",
                       "goods on a store shelf"],
    "Портрет и селфи": ["a selfie", "a portrait of a person", "a person posing for a photo"],
    "Группа людей": ["a group photo of people", "friends together", "a family gathering"],
    "Неудачный кадр": ["a completely black image", "an extremely blurry photo with motion blur",
                       "a photo taken accidentally inside a pocket", "an overexposed white image"],
    "Прочее": ["a photo", "an everyday snapshot", "a random photo"],  # catch-all, so nothing is forced elsewhere
}


PEOPLE_CATEGORIES = ("Портрет и селфи", "Группа людей")
OTHER = "Прочее"


def canonicalize(text):
    """open_clip 'canonicalize' cleaning used by this model's text tower."""
    text = re.sub(r"[^\w\s]", " ", text.lower())
    return re.sub(r"\s+", " ", text).strip()


class Clip:
    def __init__(self, text=True, image=True):
        self.visual = ps.make_session(MODEL / "visual" / "model.onnx") if image else None
        self.textual = ps.make_session(MODEL / "textual" / "model.onnx") if text else None
        if text:
            from tokenizers import Tokenizer
            self.tok = Tokenizer.from_file(str(MODEL / "textual" / "tokenizer.json"))
            self.tok.enable_truncation(CONTEXT)
            self.tok.enable_padding(length=CONTEXT, pad_id=self.tok.token_to_id("<pad>"), pad_token="<pad>")

    def image(self, jpeg):
        img = cv2.imdecode(np.frombuffer(jpeg, np.uint8), cv2.IMREAD_COLOR)
        img = cv2.resize(img, (224, 224), interpolation=cv2.INTER_CUBIC)  # 'squash' resize
        blob = cv2.dnn.blobFromImage(img, 1 / 127.5, (224, 224), (127.5, 127.5, 127.5), swapRB=True)
        e = self.visual.run(None, {"image": blob})[0][0]
        return e / np.linalg.norm(e)

    def text(self, texts):
        out = []
        for t in texts:
            ids = np.array([self.tok.encode(canonicalize(t)).ids], np.int32)
            e = self.textual.run(None, {"text": ids})[0][0]
            out.append(e / np.linalg.norm(e))
        return np.stack(out)


def open_db(path):
    con = ps.open_db(path)
    con.executescript(SCHEMA)
    return con


def file_embeddings(con):
    """(file_ids, mean embedding per file) — videos average their previews."""
    rows = con.execute("SELECT file_id, emb FROM clip ORDER BY file_id").fetchall()
    if not rows:
        sys.exit("No embeddings, run: python scenes.py embed")
    fids = np.array([r[0] for r in rows])
    E = np.frombuffer(b"".join(r[1] for r in rows), np.float16).reshape(-1, DIM).astype(np.float32)
    uniq, inv = np.unique(fids, return_inverse=True)
    M = np.zeros((len(uniq), DIM), np.float32)
    np.add.at(M, inv, E)
    return uniq, M / np.linalg.norm(M, axis=1, keepdims=True)


def cmd_embed(args):
    con = open_db(args.db)
    todo = con.execute("""
        SELECT p.rowid, p.file_id, p.t_sec FROM previews p
        WHERE NOT EXISTS (SELECT 1 FROM clip c WHERE c.file_id = p.file_id)
        ORDER BY p.file_id
    """).fetchall()
    print(f"{len(todo)} previews to embed")
    clip = Clip(text=False)
    t0 = time.time()
    for i, (rowid, fid, t) in enumerate(tqdm(todo, unit="img")):
        jpeg = con.execute("SELECT jpeg FROM previews WHERE rowid = ?", (rowid,)).fetchone()[0]
        con.execute("INSERT INTO clip (file_id, t_sec, emb) VALUES (?,?,?)",
                    (fid, t, clip.image(jpeg).astype(np.float16).tobytes()))
        if i % 500 == 0:
            con.commit()
    con.commit()
    print(f"Embedded {len(todo)} previews in {time.time() - t0:.0f}s")


def cmd_tag(args):
    con = open_db(args.db)
    clip = Clip(image=False)
    labels = list(CATEGORIES)
    T = np.stack([clip.text(CATEGORIES[l]).mean(0) for l in labels])
    T /= np.linalg.norm(T, axis=1, keepdims=True)
    fids, M = file_embeddings(con)
    logits = args.temperature * (M @ T.T)
    # no faces detected -> people categories can't apply (otherwise e.g. a back view of a person wins)
    n_faces = dict(con.execute("SELECT id, n_faces FROM files"))
    no_people = np.array([not n_faces.get(int(f)) for f in fids])
    people_cols = [labels.index(l) for l in PEOPLE_CATEGORIES]
    logits[np.ix_(no_people, people_cols)] = -np.inf
    # generic prompts match everything a bit: make "other" a fallback, not a competitor
    logits[:, labels.index(OTHER)] -= args.other_penalty
    P = np.exp(logits - logits.max(1, keepdims=True))
    P /= P.sum(1, keepdims=True)
    top = np.argsort(-P, axis=1)[:, :3]
    con.execute("DELETE FROM tags")
    con.executemany("INSERT INTO tags (file_id, rank, label, score) VALUES (?,?,?,?)",
                    [(int(f), r + 1, labels[j], float(P[i, j])) for i, f in enumerate(fids) for r, j in enumerate(top[i])])
    con.commit()
    for label, n in con.execute("SELECT label, COUNT(*) FROM tags WHERE rank = 1 GROUP BY label ORDER BY 2 DESC"):
        print(f"  {n:>6}  {label}")


def search(con, clip, query, limit=60, fids=None, M=None):
    """[(file_id, score)] best matches for a text query."""
    if M is None:
        fids, M = file_embeddings(con)
    s = M @ clip.text([query])[0]
    best = np.argsort(-s)[:limit]
    return [(int(fids[i]), float(s[i])) for i in best]


def cmd_search(args):
    con = open_db(args.db)
    for fid, s in search(con, Clip(image=False), args.query, args.limit):
        path, taken = con.execute("SELECT path, taken_at FROM files WHERE id = ?", (fid,)).fetchone()
        print(f"{s:.3f}  {(taken or '')[:10]}  {path}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--db", default=ps.DEFAULT_DB)
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("embed").set_defaults(func=cmd_embed)
    t = sub.add_parser("tag")
    t.add_argument("--temperature", type=float, default=100.0)
    t.add_argument("--other-penalty", type=float, default=1.5, help="logit penalty for the catch-all category")
    t.set_defaults(func=cmd_tag)
    s = sub.add_parser("search")
    s.add_argument("query")
    s.add_argument("--limit", type=int, default=20)
    s.set_defaults(func=cmd_search)
    args = ap.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
