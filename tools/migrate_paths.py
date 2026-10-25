r"""Rewrite file paths in a DB copy by PS_PATH_MAP (photosorter.map_path): \\nas\home\... -> /mnt/...

    PS_PATH_MAP='\\nas\home\=/mnt/nas-homes/user/;\\nas\homes\=/mnt/nas-homes/' python tools/migrate_paths.py --db faces.db

Also works Linux -> Linux (another mount point): PS_PATH_MAP='/mnt/nas-homes/user/=/mnt/nas-home/'.
Touches files.path, deleted (path, moved_to, the files rows in snapshot), collection_files, collections.folder.
Paths outside the map (C:\Users\...) stay as they are. Run on a copy, never on the Windows DB.
"""
import argparse
import os
import pickle
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import photosorter as ps  # noqa: E402


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", default=ps.DEFAULT_DB)
    args = ap.parse_args()
    if not ps.PATH_MAP:
        sys.exit("PS_PATH_MAP is empty")
    m = lambda p: p if p is None else ps.map_path(p)  # noqa: E731
    con = ps.open_db(args.db)
    con.create_function("map_path", 1, m, deterministic=True)
    for table, col in (("files", "path"), ("deleted", "path"), ("deleted", "moved_to"),
                       ("collection_files", "link_path"), ("collection_files", "original_path"),
                       ("collections", "folder")):
        n = con.execute(f"UPDATE {table} SET {col} = map_path({col}) WHERE {col} IS NOT map_path({col})").rowcount
        print(f"{table}.{col}: {n}")
    n = 0
    for rowid, blob in con.execute("SELECT rowid, snapshot FROM deleted WHERE snapshot IS NOT NULL").fetchall():
        snap = pickle.loads(blob)
        cols, rows = snap.get("files", ([], []))
        if "path" in cols:
            i = cols.index("path")
            snap["files"] = (cols, [r[:i] + (m(r[i]),) + r[i + 1:] for r in rows])
            con.execute("UPDATE deleted SET snapshot = ? WHERE rowid = ?", (pickle.dumps(snap), rowid))
            n += 1
    print(f"deleted.snapshot: {n}")
    con.commit()


if __name__ == "__main__":
    main()
