package core

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishpagin/mediasorter/web/internal/db"
	"github.com/ishpagin/mediasorter/web/internal/scan"
)

// the tables these actions touch, as photosorter.py creates them (FKs cascade like the real DB)
const testSchema = `
CREATE TABLE files (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, size INTEGER, taken_at TEXT,
  n_faces INTEGER);
CREATE TABLE previews (file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE, t_sec REAL, jpeg BLOB NOT NULL);
CREATE TABLE persons (id INTEGER PRIMARY KEY, name TEXT, hidden INTEGER NOT NULL DEFAULT 0, n_faces INTEGER NOT NULL DEFAULT 0,
  n_files INTEGER NOT NULL DEFAULT 0, centroid BLOB, skip_collect INTEGER NOT NULL DEFAULT 0, n_voices INTEGER NOT NULL DEFAULT 0);
CREATE TABLE faces (id INTEGER PRIMARY KEY, file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  person_id INTEGER REFERENCES persons(id) ON DELETE SET NULL, t_sec REAL, size REAL, embedding BLOB NOT NULL,
  thumb BLOB, manual INTEGER NOT NULL DEFAULT 0);
CREATE TABLE speech_done (file_id INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE, mtime INTEGER, error TEXT);
CREATE TABLE voices (id INTEGER PRIMARY KEY, file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE, label TEXT NOT NULL,
  seconds REAL NOT NULL, model TEXT NOT NULL, emb BLOB, cluster INTEGER, person_id INTEGER REFERENCES persons(id) ON DELETE SET NULL,
  manual INTEGER NOT NULL DEFAULT 0, suggest_id INTEGER REFERENCES persons(id) ON DELETE SET NULL);
CREATE TABLE voice_not_same (a INTEGER NOT NULL, b INTEGER NOT NULL, PRIMARY KEY (a, b));
CREATE TABLE voice_not_person (voice_id INTEGER NOT NULL REFERENCES voices(id) ON DELETE CASCADE, person_id INTEGER NOT NULL,
  PRIMARY KEY (voice_id, person_id));
CREATE TABLE speech (file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE, t_start REAL NOT NULL, t_end REAL NOT NULL,
  text TEXT NOT NULL, low TEXT NOT NULL, voice_id INTEGER REFERENCES voices(id) ON DELETE SET NULL);
CREATE VIEW person_media AS SELECT person_id, file_id FROM faces WHERE person_id IS NOT NULL
  UNION SELECT person_id, file_id FROM voices WHERE person_id IS NOT NULL;
CREATE TABLE tags (file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE, rank INTEGER, label TEXT, score REAL);
CREATE TABLE deleted (path TEXT NOT NULL, moved_to TEXT, deleted_at TEXT NOT NULL, snapshot BLOB);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE collections (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, folder TEXT NOT NULL, created_at TEXT NOT NULL,
  person_id INTEGER);
CREATE TABLE collection_files (collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE, link_path TEXT NOT NULL, original_path TEXT NOT NULL,
  added_at TEXT NOT NULL, PRIMARY KEY (collection_id, file_id));`

type env struct {
	t       *testing.T
	db      *sql.DB
	cfg     *Config
	archive string
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive")
	must(t, os.MkdirAll(filepath.Join(archive, "Photos"), 0o775))
	d, err := db.Open(filepath.Join(dir, "t.db"))
	must(t, err)
	t.Cleanup(func() { d.Close() })
	d.SetMaxOpenConns(1) // a temp DB: one connection keeps PRAGMAs simple
	_, err = d.Exec(testSchema)
	must(t, err)
	must(t, InitUndo(d))
	_, err = d.Exec("INSERT INTO settings VALUES ('collections_root', ?)", filepath.Join(dir, "colls"))
	must(t, err)
	protected := filepath.Join(dir, "protected.txt")
	must(t, os.WriteFile(protected, []byte("# keep\n\\\\nas\\home\\Keep\n"), 0o664))
	cfg := &Config{DBPath: filepath.Join(dir, "t.db"), Protected: protected,
		PathMap: ParsePathMap("//nas/home/=" + filepath.ToSlash(archive) + "/")}
	return &env{t, d, cfg, archive}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// file adds a file on disk and its rows: a named person's face, an unnamed person's face, a preview, a tag
func (e *env) file(rel string, id int64) string {
	p := filepath.Join(e.archive, rel)
	must(e.t, os.MkdirAll(filepath.Dir(p), 0o775))
	must(e.t, os.WriteFile(p, []byte("photo "+rel), 0o664))
	for _, q := range []string{
		"INSERT INTO files (id, path, kind, n_faces) VALUES (?, ?, 'image', 2)",
	} {
		_, err := e.db.Exec(q, id, p)
		must(e.t, err)
	}
	_, err := e.db.Exec("INSERT OR IGNORE INTO persons (id, name) VALUES (1, 'Петя')")
	must(e.t, err)
	_, err = e.db.Exec("INSERT INTO persons (id, name) VALUES (?, NULL)", 100+id)
	must(e.t, err)
	_, err = e.db.Exec("INSERT INTO faces (file_id, person_id, embedding, thumb) VALUES (?, 1, ?, ?), (?, ?, ?, NULL)",
		id, []byte{1, 2, 3}, []byte{0xff, 0xd8}, id, 100+id, []byte{4, 5})
	must(e.t, err)
	_, err = e.db.Exec("INSERT INTO previews (file_id, t_sec, jpeg) VALUES (?, NULL, ?)", id, []byte{0xff, 0xd8, byte(id)})
	must(e.t, err)
	_, err = e.db.Exec("INSERT INTO tags VALUES (?, 1, 'Море', 0.5)", id)
	must(e.t, err)
	must(e.t, refreshPersonStats(e.db))
	return p
}

func (e *env) count(q string, args ...any) int {
	var n int
	must(e.t, e.db.QueryRow(q, args...).Scan(&n))
	return n
}

func TestTrashRestore(t *testing.T) {
	e := setup(t)
	a := e.file("Photos/a.jpg", 1)
	e.file("Photos/b.jpg", 2)
	cid, err := CreateCollection(e.db, "Маме", nil)
	must(t, err)
	_, err = e.cfg.AddToCollection(e.db, cid, 1, true) // a copy of a.jpg in a collection
	must(t, err)
	var link string
	must(t, e.db.QueryRow("SELECT link_path FROM collection_files").Scan(&link))

	done, failed, ids, err := e.cfg.Trash(e.db, []string{a})
	must(t, err)
	if done != 1 || len(failed) != 0 || len(ids) != 1 {
		t.Fatalf("trash: done %d failed %v ids %v", done, failed, ids)
	}
	q := filepath.Join(e.archive, QuarantineDir, "Photos", "a.jpg")
	if exists(a) || !exists(q) || exists(link) {
		t.Fatalf("after trash: original %v quarantine %v link %v", exists(a), exists(q), exists(link))
	}
	if e.count("SELECT COUNT(*) FROM files WHERE id = 1") != 0 || e.count("SELECT COUNT(*) FROM faces WHERE file_id = 1") != 0 {
		t.Fatal("rows of the deleted file are still there")
	}
	if e.count("SELECT COUNT(*) FROM persons WHERE id = 101") != 0 {
		t.Fatal("an unnamed person without faces must go")
	}

	done, probs, err := e.cfg.Restore(e.db, ids)
	must(t, err)
	if done != 1 || len(probs) != 0 {
		t.Fatalf("restore: done %d problems %v", done, probs)
	}
	if !exists(a) || exists(q) {
		t.Fatal("the file did not come back from the quarantine")
	}
	var jpeg, emb []byte
	must(t, e.db.QueryRow("SELECT jpeg FROM previews WHERE file_id = 1").Scan(&jpeg))
	must(t, e.db.QueryRow("SELECT embedding FROM faces WHERE file_id = 1 AND person_id = 1").Scan(&emb))
	if !bytes.Equal(jpeg, []byte{0xff, 0xd8, 1}) || !bytes.Equal(emb, []byte{1, 2, 3}) {
		t.Fatalf("blobs changed: %v %v", jpeg, emb)
	}
	if e.count("SELECT COUNT(*) FROM persons WHERE id = 101 AND n_faces = 1") != 1 ||
		e.count("SELECT n_files FROM persons WHERE id = 1") != 2 || e.count("SELECT COUNT(*) FROM tags WHERE file_id = 1") != 1 {
		t.Fatal("persons / stats / tags not restored")
	}
	if e.count("SELECT COUNT(*) FROM deleted") != 0 {
		t.Fatal("the log entry must go after a restore")
	}
}

func TestProtected(t *testing.T) {
	e := setup(t)
	k := e.file("Keep/k.jpg", 1)
	_, _, _, err := e.cfg.Trash(e.db, []string{k})
	var pe ErrProtected
	if !errors.As(err, &pe) || !exists(k) {
		t.Fatalf("a protected file must not be deleted: err %v exists %v", err, exists(k))
	}
	if ok, err := e.cfg.AddToCollection(e.db, mustColl(t, e, "x"), 1, false); ok || err == nil {
		t.Fatal("a protected file must not be moved into a collection")
	}
	if ok, err := e.cfg.AddToCollection(e.db, mustColl(t, e, "y"), 1, true); !ok || err != nil {
		t.Fatalf("copying from a protected folder is fine: %v %v", ok, err)
	}
}

func mustColl(t *testing.T, e *env, name string) int64 {
	cid, err := CreateCollection(e.db, name, nil)
	must(t, err)
	return cid
}

func TestCollectionsMoveCopyUndo(t *testing.T) {
	e := setup(t)
	a := e.file("Photos/a.jpg", 1)
	c1, c2 := mustColl(t, e, "copy1"), mustColl(t, e, "move2")
	path := func() string {
		var p string
		must(t, e.db.QueryRow("SELECT path FROM files WHERE id = 1").Scan(&p))
		return p
	}
	_, _, added1 := e.cfg.AddFiles(e.db, c1, []int64{1}, true)
	if !exists(a) || path() != a || len(added1) != 1 {
		t.Fatal("copy: the original must stay where it is")
	}
	_, _, added2 := e.cfg.AddFiles(e.db, c2, []int64{1}, false)
	moved := path()
	if exists(a) || !exists(moved) || moved == a {
		t.Fatal("move: the file must leave the archive and files.path follow it")
	}
	if _, f := ReturnFromCollection(e.db, added2); len(f) != 0 || !exists(a) || path() != a {
		t.Fatalf("undo move: back in the archive %v (%v)", exists(a), f)
	}
	if _, f := ReturnFromCollection(e.db, added1); len(f) != 0 || !exists(a) || e.count("SELECT COUNT(*) FROM collection_files") != 0 {
		t.Fatalf("undo copy: only the copy goes (%v)", f)
	}
	// by people: the named person gets their own collection
	per, nobody, f, added := e.cfg.ByPeople(e.db, []int64{1}, true)
	if per["Петя"] != 1 || len(nobody) != 0 || len(f) != 0 || len(added) != 1 {
		t.Fatalf("by people: %v %v %v", per, nobody, f)
	}
}

func TestUndoLog(t *testing.T) {
	e := setup(t)
	a := e.file("Photos/a.jpg", 1)
	_, _, ids, err := e.cfg.Trash(e.db, []string{a})
	must(t, err)
	must(t, PushUndo(e.db, "s1", "delete", "Удалено: a.jpg", ids))
	if LastUndo(e.db, "s2") != "" || LastUndo(e.db, "s1") == "" {
		t.Fatal("undo history is per session")
	}
	msg, probs, err := e.cfg.Undo(e.db, "s1")
	must(t, err)
	if msg != "Возвращено: 1" || len(probs) != 0 || !exists(a) {
		t.Fatalf("undo: %q %v", msg, probs)
	}
	if m, _, _ := e.cfg.Undo(e.db, "s1"); m != "Нечего возвращать" {
		t.Fatalf("empty history: %q", m)
	}
}

func TestF16(t *testing.T) {
	for h, want := range map[uint16]float32{0x3c00: 1, 0xc000: -2, 0x3800: 0.5, 0: 0, 0x7bff: 65504} {
		if got := f16(h); got != want {
			t.Errorf("f16(%#x) = %v, want %v", h, got, want)
		}
	}
}

func TestPeople(t *testing.T) {
	e := setup(t)
	e.file("Photos/a.jpg", 1) // faces: person 1 (Петя) and unnamed 101
	e.file("Photos/b.jpg", 2) // faces: person 1 and unnamed 102
	units, err := FileFaces(e.db, []int64{1})
	must(t, err)
	if len(units) != 2 || units[0].Person == nil || *units[0].Person != 1 {
		t.Fatalf("units: known people first: %+v", units)
	}
	vasya, err := CreatePerson(e.db, "Вася")
	must(t, err)
	if again, _ := CreatePerson(e.db, "вася"); again != vasya {
		t.Fatal("an existing name is reused, case-insensitive")
	}
	// the unnamed face on a.jpg is Вася
	before, err := AssignFaces(e.db, units[1].Faces, &vasya)
	must(t, err)
	if e.count("SELECT COUNT(*) FROM persons WHERE id = 101") != 0 || e.count("SELECT n_faces FROM persons WHERE id = ?", vasya) != 1 {
		t.Fatal("assign: the emptied unnamed person goes, Вася gets the face")
	}
	must(t, UnassignFaces(e.db, before))
	if e.count("SELECT COUNT(*) FROM faces WHERE person_id = 101 AND manual = 0") != 1 {
		t.Fatal("unassign: the face and its unnamed person come back as they were")
	}
	// merge 102 into Петя: Петя keeps the name, 102 is gone
	must(t, MergePersons(e.db, []int64{1, 102}))
	if e.count("SELECT n_faces FROM persons WHERE id = 1") != 3 || e.count("SELECT COUNT(*) FROM persons WHERE id = 102") != 0 {
		t.Fatal("merge")
	}
}

func TestZipFinish(t *testing.T) {
	e := setup(t)
	a := e.file("Photos/a.jpg", 1)
	b := e.file("Photos/b.jpg", 2)
	c1, c2 := mustColl(t, e, "Маме"), mustColl(t, e, "Другая")
	e.cfg.AddFiles(e.db, c1, []int64{1}, false) // a: moved here, only here -> deleted on finish
	e.cfg.AddFiles(e.db, c1, []int64{2}, true)  // b: a copy -> only the copy goes, the original stays
	e.cfg.AddFiles(e.db, c2, []int64{2}, true)
	zp, n, err := ZipCollection(e.db, c1)
	must(t, err)
	if n != 2 || !exists(zp) {
		t.Fatalf("zip: %d files, exists %v", n, exists(zp))
	}
	done, failed, ids, err := e.cfg.FinishCollection(e.db, c1, true)
	must(t, err)
	if done != 2 || len(failed) != 0 || len(ids) != 1 {
		t.Fatalf("finish: done %d failed %v ids %v", done, failed, ids)
	}
	if !exists(b) || exists(zp) || e.count("SELECT COUNT(*) FROM collections WHERE id = ?", c1) != 0 ||
		e.count("SELECT COUNT(*) FROM files WHERE id = 1") != 0 || e.count("SELECT COUNT(*) FROM collection_files WHERE collection_id = ?", c2) != 1 {
		t.Fatal("finish: the moved file is deleted, the copy's original stays, the other collection keeps its copy")
	}
	_ = a // it was moved into the collection: a restore brings it back there (as in Python), not into the archive
	if done, _, err := e.cfg.Restore(e.db, ids); err != nil || done != 1 || e.count("SELECT COUNT(*) FROM files WHERE id = 1") != 1 {
		t.Fatalf("the deleted file of a finished collection is restorable: %v", err)
	}
	if got, want := e.cfg.MapToWindows(filepath.ToSlash(e.archive)+"/Photos/x.jpg"), `\\nas\home\Photos\x.jpg`; got != want {
		t.Fatalf("windows path: %q want %q", got, want)
	}
}

// a person only heard (no faces) is kept by every recount of people
func TestVoicePersonSurvives(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	must(t, err)
	t.Cleanup(func() { d.Close() })
	must(t, db.Migrate(d))
	for _, q := range []string{
		"INSERT INTO files (id, path, kind) VALUES (1, '/a.mp4', 'video'), (2, '/b.jpg', 'image')",
		"INSERT INTO persons (id) VALUES (5)",
		"INSERT INTO voices (file_id, label, seconds, model, person_id) VALUES (1, 's1', 10, 'm', 5)",
	} {
		_, err := d.Exec(q)
		must(t, err)
	}
	emb := make([]float32, 512)
	emb[0] = 1
	_, err = d.Exec("INSERT INTO faces (file_id, size, det_score, embedding) VALUES (2, 50, 0.9, ?)", scan.F16Blob(emb))
	must(t, err)
	must(t, refreshPersonStats(d))
	people, err := scan.Cluster(d, scan.DefaultClusterOptions(), nil)
	must(t, err)
	var nv, nf int
	must(t, d.QueryRow("SELECT n_voices, n_files FROM persons WHERE id = 5").Scan(&nv, &nf))
	if people != 1 || nv != 1 || nf != 1 {
		t.Fatalf("people %d, n_voices %d, n_files %d", people, nv, nf)
	}
}

func TestMergeMovesVoices(t *testing.T) {
	e := setup(t)
	e.file("Photos/a.jpg", 1) // faces: Петя (1) and unnamed 101
	_, err := e.db.Exec(`INSERT INTO voices (id, file_id, label, seconds, model, person_id, manual, suggest_id)
		VALUES (1, 1, 's1', 10, 'm', 101, 1, NULL), (2, 1, 's2', 10, 'm', NULL, 0, 101)`)
	must(t, err)
	_, err = e.db.Exec("INSERT INTO voice_not_person VALUES (2, 101)")
	must(t, err)
	must(t, MergePersons(e.db, []int64{1, 101}))
	if e.count("SELECT COUNT(*) FROM voices WHERE id = 1 AND person_id = 1 AND manual = 1") != 1 ||
		e.count("SELECT COUNT(*) FROM voices WHERE id = 2 AND suggest_id = 1") != 1 ||
		e.count("SELECT COUNT(*) FROM voice_not_person WHERE voice_id = 2 AND person_id = 1") != 1 ||
		e.count("SELECT n_voices FROM persons WHERE id = 1") != 1 {
		t.Fatal("merge: voices, suggestions and 'not his voice' marks go to the target")
	}
}

func TestTrashKeepsVoices(t *testing.T) {
	e := setup(t)
	a := e.file("Photos/a.jpg", 1)
	for _, q := range []string{
		"INSERT INTO persons (id) VALUES (9)", // heard only, in this file only: it goes with the file and comes back
		"INSERT INTO voices (id, file_id, label, seconds, model, person_id, manual) VALUES (1, 1, 's1', 10, 'm', 9, 1), (2, 1, 's2', 10, 'm', 1, 0)",
		"INSERT INTO voice_not_person VALUES (2, 9)",
		"INSERT INTO speech_done (file_id, mtime) VALUES (1, 7)",
		"INSERT INTO speech (file_id, t_start, t_end, text, low, voice_id) VALUES (1, 0, 1, 'Привет', 'привет', 1)",
	} {
		_, err := e.db.Exec(q)
		must(t, err)
	}
	must(t, refreshPersonStats(e.db))
	_, _, ids, err := e.cfg.Trash(e.db, []string{a})
	must(t, err)
	if e.count("SELECT COUNT(*) FROM voices") != 0 || e.count("SELECT COUNT(*) FROM persons WHERE id = 9") != 0 {
		t.Fatal("trash: the file's voices go, the person heard only there too")
	}
	_, _, err = e.cfg.Restore(e.db, ids)
	must(t, err)
	if e.count("SELECT COUNT(*) FROM voices WHERE id = 1 AND person_id = 9 AND manual = 1") != 1 ||
		e.count("SELECT COUNT(*) FROM voices WHERE id = 2 AND person_id = 1") != 1 ||
		e.count("SELECT COUNT(*) FROM voice_not_person WHERE voice_id = 2 AND person_id = 9") != 1 ||
		e.count("SELECT COUNT(*) FROM speech_done WHERE file_id = 1") != 1 ||
		e.count("SELECT COUNT(*) FROM speech WHERE file_id = 1 AND voice_id = 1") != 1 ||
		e.count("SELECT n_voices FROM persons WHERE id = 9") != 1 {
		t.Fatal("restore: voices with their people and marks, phrases and the 'heard' mark come back")
	}
}
