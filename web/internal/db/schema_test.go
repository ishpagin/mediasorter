package db

import (
	"path/filepath"
	"testing"
)

func TestMigrateVoices(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// an old database: speech without voice_id, persons without n_voices
	if _, err := d.Exec(`CREATE TABLE persons (id INTEGER PRIMARY KEY, name TEXT, n_faces INTEGER NOT NULL DEFAULT 0);
CREATE TABLE speech (file_id INTEGER NOT NULL, t_start REAL NOT NULL, t_end REAL NOT NULL, text TEXT NOT NULL, low TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(d); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"speech", "voice_id"}, {"persons", "n_voices"}} {
		if ok, err := hasColumn(d, c[0], c[1]); err != nil || !ok {
			t.Fatalf("%s.%s missing (err %v)", c[0], c[1], err)
		}
	}
	// file 1: person only in voices, file 2: only in faces, file 3: both (still one row)
	for _, q := range []string{
		`INSERT INTO persons(id, name) VALUES (1, 'a')`,
		`INSERT INTO files(id, path, kind) VALUES (1,'1','video'),(2,'2','photo'),(3,'3','video')`,
		`INSERT INTO voices(file_id, label, seconds, model, person_id) VALUES (1,'S0',5,'m',1),(3,'S0',5,'m',1)`,
		`INSERT INTO faces(file_id, person_id, embedding) VALUES (2,1,x''),(3,1,x'')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rows, err := d.Query(`SELECT file_id FROM person_media WHERE person_id = 1 ORDER BY file_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var f int
		rows.Scan(&f)
		got = append(got, f)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("person_media files = %v", got)
	}
}
