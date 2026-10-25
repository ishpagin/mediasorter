"""Self-check of move/copy into collections on a temp DB + temp folders (never the real archive)."""
import os
import sys
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
import photosorter as ps

t = tempfile.mkdtemp()
arch = os.path.join(t, "archive")
os.makedirs(arch)
con = ps.open_db(os.path.join(t, "t.db"))
ps.set_setting(con, "collections_root", os.path.join(t, "colls"))
paths = []
for n in ("a.jpg", "b.jpg"):
    p = os.path.join(arch, n)
    open(p, "w").write(n)
    con.execute("INSERT INTO files (path, kind) VALUES (?, 'image')", (p,))
    paths.append(p)
con.commit()
a, b = paths
fa = con.execute("SELECT id FROM files WHERE path = ?", (a,)).fetchone()[0]
c1 = ps.create_collection(con, "copy1")
c2 = ps.create_collection(con, "move2")
c3 = ps.create_collection(con, "copy3")

# copy: original stays, files.path unchanged
done, failed, added = ps.move_to_collection(con, c1, [a], copy=True)
assert done == 1 and not failed, failed
link1 = con.execute("SELECT link_path FROM collection_files WHERE collection_id = ?", (c1,)).fetchone()[0]
assert os.path.exists(a) and os.path.exists(link1)
assert con.execute("SELECT path FROM files WHERE id = ?", (fa,)).fetchone()[0] == a

# move after a copy: the archive file moves, the copy stays
ps.move_to_collection(con, c2, [a])
link2 = con.execute("SELECT link_path FROM collection_files WHERE collection_id = ?", (c2,)).fetchone()[0]
assert not os.path.exists(a) and os.path.exists(link2) and os.path.exists(link1)
assert con.execute("SELECT path FROM files WHERE id = ?", (fa,)).fetchone()[0] == link2

# undo the move: file back in the archive, copy still in copy1
ps.return_from_collection(con, [(c2, fa)])
assert os.path.exists(a) and not os.path.exists(link2) and os.path.exists(link1)
assert con.execute("SELECT path FROM files WHERE id = ?", (fa,)).fetchone()[0] == a

# undo the copy: only the copy goes
ps.return_from_collection(con, [(c1, fa)])
assert os.path.exists(a) and not os.path.exists(link1)

# "sent - delete" on a copy collection must not touch the archive original
ps.move_to_collection(con, c3, [a, b], copy=True)
ps.finish_collection(con, c3)
assert os.path.exists(a) and os.path.exists(b), "archive originals deleted!"
assert con.execute("SELECT COUNT(*) FROM files").fetchone()[0] == 2

# plain move + undo still works
ps.move_to_collection(con, c2, [b])
assert not os.path.exists(b)
fb = con.execute("SELECT id FROM files WHERE path LIKE ?", ("%b.jpg",)).fetchone()[0]
ps.return_from_collection(con, [(c2, fb)])
assert os.path.exists(b)
print("collections self-check ok")
