package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/db"
)

// A person seen on a photo and only heard in a video: both files are theirs.
func TestPersonMedia(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	photo, video := filepath.Join(dir, "a.jpg"), filepath.Join(dir, "b.mp4")
	for _, p := range []string{photo, video} {
		if err := os.WriteFile(p, []byte("x"), 0o664); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"INSERT INTO settings VALUES ('collections_root', '" + filepath.Join(dir, "colls") + "')",
		"INSERT INTO files (id, path, kind, n_faces) VALUES (1, '" + photo + "', 'image', 1), (2, '" + video + "', 'video', 0)",
		"INSERT INTO persons (id, name) VALUES (1, 'Петя'), (2, 'Голос')",
		"INSERT INTO faces (file_id, person_id, embedding) VALUES (1, 1, x'0102')",
		"INSERT INTO voices (file_id, label, seconds, model, person_id) VALUES (2, 's1', 9, 'm', 1), (2, 's2', 9, 'm', 2)",
		"UPDATE persons SET n_faces = 1, n_voices = 1, n_files = 2 WHERE id = 1",
		"UPDATE persons SET n_voices = 1, n_files = 1 WHERE id = 2",
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	mux := http.NewServeMux()
	(&Server{DB: d, Cfg: &core.Config{}, ML: &stubML{}}).Routes(mux)
	get := func(url string, out any) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", url, w.Code)
		}
		json.Unmarshal(w.Body.Bytes(), out)
	}

	var files FilesResp
	get("/api/person/1/files?video=1", &files)
	if len(files.Items) != 2 {
		t.Errorf("person files: %d, want 2", len(files.Items))
	}
	var people []Person
	get("/api/persons?min=1&video=1", &people)
	if len(people) != 2 || people[0].ID != 1 || people[0].N != 2 || people[1].Face != 0 {
		t.Errorf("persons: %+v", people)
	}
	people = nil
	get("/api/persons/all", &people)
	if len(people) != 2 {
		t.Errorf("persons/all: %+v", people)
	}
	who, err := (&Server{DB: d}).peopleIn()
	if err != nil || who[2] != "Голос, Петя" && who[2] != "Петя, Голос" {
		t.Errorf("peopleIn: %v %v", who, err)
	}
	cfg := &core.Config{}
	per, nobody, failed, _ := cfg.ByPeople(d, []int64{2}, true)
	if per["Петя"] != 1 || per["Голос"] != 1 || len(nobody) != 0 {
		t.Errorf("ByPeople: %v %v %v", per, nobody, failed)
	}
}
