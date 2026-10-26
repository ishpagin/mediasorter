package db

import (
	"database/sql"
	"strings"
)

// Schema: every table, the same as the Python scanner creates (photosorter.py, scenes.py, stacks.py),
// so a database made by either side works with the other.
const Schema = `
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
    centroid BLOB,                         -- float32[512]
    skip_collect INTEGER NOT NULL DEFAULT 0 -- "this is me": not collected "by people"
);
CREATE TABLE IF NOT EXISTS faces (
    id        INTEGER PRIMARY KEY,
    file_id   INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    person_id INTEGER REFERENCES persons(id) ON DELETE SET NULL,
    t_sec     REAL,
    x1 REAL, y1 REAL, x2 REAL, y2 REAL,
    det_score REAL,
    size      REAL,
    emb_norm  REAL,
    embedding BLOB NOT NULL,               -- float16[512], L2-normalized
    thumb     BLOB,
    manual    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS faces_file ON faces(file_id);
CREATE INDEX IF NOT EXISTS faces_person ON faces(person_id);

CREATE VIEW IF NOT EXISTS file_people AS
SELECT f.id AS file_id, f.path, f.kind, f.n_faces,
       COUNT(DISTINCT fa.person_id) AS n_people,
       group_concat(DISTINCT COALESCE(p.name, 'person_' || p.id)) AS people
FROM files f
LEFT JOIN faces fa ON fa.file_id = f.id
LEFT JOIN persons p ON p.id = fa.person_id
GROUP BY f.id;
CREATE VIEW IF NOT EXISTS person_files AS
SELECT p.id AS person_id, p.name, f.id AS file_id, f.path, f.kind, f.n_faces,
       COUNT(*) AS faces_of_person
FROM persons p
JOIN faces fa ON fa.person_id = p.id
JOIN files f ON f.id = fa.file_id
GROUP BY p.id, f.id;
CREATE VIEW IF NOT EXISTS files_without_people AS
SELECT * FROM files WHERE n_faces = 0 AND error IS NULL;
CREATE VIEW IF NOT EXISTS files_strangers_only AS
SELECT f.* FROM files f
WHERE f.n_faces > 0 AND NOT EXISTS (SELECT 1 FROM faces x WHERE x.file_id = f.id AND x.person_id IS NOT NULL);
CREATE VIEW IF NOT EXISTS duplicates AS
SELECT phash, COUNT(*) AS n, group_concat(path, ' | ') AS paths
FROM files WHERE phash IS NOT NULL AND kind = 'image' GROUP BY phash HAVING n > 1;

CREATE TABLE IF NOT EXISTS not_same (
    a INTEGER NOT NULL,
    b INTEGER NOT NULL,
    PRIMARY KEY (a, b)
);
CREATE TABLE IF NOT EXISTS deleted (
    path       TEXT NOT NULL,
    moved_to   TEXT,
    deleted_at TEXT NOT NULL,
    snapshot   BLOB
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS work_queue (
    path        TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    size        INTEGER,
    mtime       INTEGER,
    ord         INTEGER,
    queued_at   REAL NOT NULL,
    lease       TEXT,
    lease_until REAL NOT NULL DEFAULT 0,
    remote_ok   INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS collections (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    folder     TEXT NOT NULL,
    created_at TEXT NOT NULL,
    person_id  INTEGER
);
CREATE TABLE IF NOT EXISTS collection_files (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    file_id       INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    link_path     TEXT NOT NULL,
    original_path TEXT NOT NULL,
    added_at      TEXT NOT NULL,
    PRIMARY KEY (collection_id, file_id)
);

CREATE TABLE IF NOT EXISTS clip (
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    t_sec   REAL,
    emb     BLOB NOT NULL                  -- float16[768], L2-normalized
);
CREATE INDEX IF NOT EXISTS clip_file ON clip(file_id);
CREATE TABLE IF NOT EXISTS tags (
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    rank    INTEGER NOT NULL,
    label   TEXT NOT NULL,
    score   REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS tags_file ON tags(file_id);
CREATE INDEX IF NOT EXISTS tags_label ON tags(label, rank);

CREATE TABLE IF NOT EXISTS stacks (
    file_id  INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    stack_id INTEGER NOT NULL,
    copies   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS stacks_stack ON stacks(stack_id);
CREATE TABLE IF NOT EXISTS copies (
    file_id  INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    group_id INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS copies_group ON copies(group_id);
CREATE TABLE IF NOT EXISTS quality (
    file_id    INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    sharp      REAL,
    face_sharp REAL,
    exposure   REAL,
    aesthetic  REAL,
    error      TEXT
);
`

// Migrate creates missing tables and adds columns older databases lack.
func Migrate(d *sql.DB) error {
	if _, err := d.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return err
	}
	if _, err := d.Exec(Schema); err != nil {
		return err
	}
	add := []struct{ table, col, ddl string }{
		{"deleted", "snapshot", "ALTER TABLE deleted ADD COLUMN snapshot BLOB"},
		{"persons", "skip_collect", "ALTER TABLE persons ADD COLUMN skip_collect INTEGER NOT NULL DEFAULT 0"},
		{"faces", "manual", "ALTER TABLE faces ADD COLUMN manual INTEGER NOT NULL DEFAULT 0"},
		{"collections", "person_id", "ALTER TABLE collections ADD COLUMN person_id INTEGER"},
	}
	for _, a := range add {
		ok, err := hasColumn(d, a.table, a.col)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := d.Exec(a.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasColumn(d *sql.DB, table, col string) (bool, error) {
	rows, err := d.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if strings.EqualFold(n, col) {
			return true, nil
		}
	}
	return false, rows.Err()
}
