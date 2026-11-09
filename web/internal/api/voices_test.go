package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			panic(err)
		}
	}

	var files FilesResp
	get("/api/person/1/files?video=1", &files)
	if len(files.Items) != 2 {
		t.Errorf("person files: %d, want 2", len(files.Items))
	}
	files = FilesResp{}
	get("/api/person/2/files?whole=0&video=1", &files) // no face on a voice-only file: still 200
	if len(files.Items) != 1 {
		t.Errorf("whole=0: %d, want 1", len(files.Items))
	}
	var people []Person
	get("/api/persons?min=1", &people) // videos hidden: the voice-only person stays
	if len(people) != 2 || people[0].N != 1 || people[1].ID != 2 || people[1].N != 1 {
		t.Errorf("persons, videos hidden: %+v", people)
	}
	people = nil
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

func voiceServer(t *testing.T, stmts ...string) (*http.ServeMux, *sql.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	for _, q := range append([]string{
		"INSERT INTO files (id, path, kind, n_faces) VALUES (1, '/v/a.mp4', 'video', 0), (2, '/v/b.mp4', 'video', 0)",
		"INSERT INTO persons (id, name) VALUES (1, 'Петя'), (2, 'Вася')",
	}, stmts...) {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	mux := http.NewServeMux()
	(&Server{DB: d, Cfg: &core.Config{}, ML: &stubML{}}).Routes(mux)
	return mux, d
}

func do(mux *http.ServeMux, method, url, body string, out any) int {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, url, strings.NewReader(body)))
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			panic(err)
		}
	}
	return w.Code
}

func TestSpeechWithVoices(t *testing.T) {
	mux, _ := voiceServer(t,
		"INSERT INTO voices (id, file_id, label, seconds, model, person_id) VALUES (1, 1, 's1', 9, 'm', 1), (2, 1, 's2', 9, 'm', NULL)",
		"INSERT INTO speech (file_id, t_start, t_end, text, low, voice_id) VALUES (1, 1, 2, 'раз', 'раз', 2), (1, 5, 6, 'два', 'два', 1)")
	var r struct {
		Segments []struct{ Voice int64 }
		Voices   []VoiceInfo
	}
	if c := do(mux, "GET", "/api/speech?id=1", "", &r); c != 200 || len(r.Segments) != 2 || r.Segments[0].Voice != 2 {
		t.Fatalf("%d %+v", c, r)
	}
	if len(r.Voices) != 2 || r.Voices[0].ID != 2 || r.Voices[0].N != 1 || r.Voices[1].ID != 1 || r.Voices[1].N != 2 || r.Voices[1].Name != "Петя" {
		t.Errorf("voices: %+v", r.Voices)
	}
}

func TestVoiceAssign(t *testing.T) {
	mux, d := voiceServer(t,
		"INSERT INTO voices (id, file_id, label, seconds, model, cluster) VALUES (1, 1, 's1', 9, 'm', 7), (2, 2, 's1', 9, 'm', 7), (3, 2, 's2', 9, 'm', 8)")
	if c := do(mux, "POST", "/api/voices/1/person", `{"person":1}`, nil); c != 200 {
		t.Fatal(c)
	}
	var man [3]int
	var per [3]sql.NullInt64
	read := func() {
		for i := range per {
			d.QueryRow("SELECT manual, person_id FROM voices WHERE id = ?", i+1).Scan(&man[i], &per[i])
		}
	}
	read()
	if man[0] != 1 || per[0].Int64 != 1 || man[1] != 0 || per[1].Int64 != 1 || per[2].Valid {
		t.Errorf("assign: %v %v", man, per)
	}
	var n int
	d.QueryRow("SELECT n_voices FROM persons WHERE id = 1").Scan(&n)
	if n != 2 {
		t.Errorf("n_voices = %d, want 2", n)
	}
	if c := do(mux, "POST", "/api/voices/1/person", `{"clear":true}`, nil); c != 200 {
		t.Fatal(c)
	}
	read()
	d.QueryRow("SELECT COUNT(*) FROM voice_not_person WHERE voice_id = 1 AND person_id = 1").Scan(&n)
	if per[0].Valid || man[0] != 1 || n != 1 {
		t.Errorf("clear: %v %v marks=%d", man, per, n)
	}
	if c := do(mux, "POST", "/api/voices/3/person", `{"create":true}`, nil); c != 200 {
		t.Fatal(c)
	}
	read()
	if !per[2].Valid || per[2].Int64 < 3 || man[2] != 1 {
		t.Errorf("create: %v %v", man, per)
	}
}

func TestVoicesSameConflict(t *testing.T) {
	mux, d := voiceServer(t,
		"INSERT INTO voices (id, file_id, label, seconds, model, person_id) VALUES (1, 1, 's1', 9, 'm', 1), (2, 2, 's1', 9, 'm', 2), (3, 2, 's2', 9, 'm', NULL)")
	var r struct{ Conflict struct{ A, B Person } }
	if c := do(mux, "POST", "/api/voices/same", `{"a":1,"b":2}`, &r); c != 409 || r.Conflict.A.ID != 1 || r.Conflict.B.Name != "Вася" {
		t.Fatalf("%d %+v", c, r)
	}
	var p1, p2 int64
	d.QueryRow("SELECT person_id FROM voices WHERE id = 1").Scan(&p1)
	d.QueryRow("SELECT person_id FROM voices WHERE id = 2").Scan(&p2)
	if p1 != 1 || p2 != 2 {
		t.Errorf("changed: %d %d", p1, p2)
	}
	d.Exec("INSERT INTO voice_not_person VALUES (3, 1)")
	if c := do(mux, "POST", "/api/voices/same", `{"a":1,"b":3}`, nil); c != 200 {
		t.Fatal(c)
	}
	d.QueryRow("SELECT person_id FROM voices WHERE id = 3").Scan(&p1)
	if p1 != 1 {
		t.Errorf("same: voice 3 has person %d, want 1", p1)
	}
	var marks int
	d.QueryRow("SELECT COUNT(*) FROM voice_not_person WHERE voice_id = 3").Scan(&marks)
	if marks != 0 {
		t.Errorf("same left a contradicting mark")
	}
}

func TestVoicePairs(t *testing.T) {
	// a = [1, 0], b = [0.5, 0.866], c = [0.5, -0.866] in float16: a~b and a~c are 0.5, b~c is -0.5
	mux, _ := voiceServer(t,
		"INSERT INTO voices (id, file_id, label, seconds, model, cluster, emb) VALUES (1, 1, 's1', 9, 'm', 1, x'003C0000'), (2, 1, 's2', 9, 'm', 2, x'0038ED3A'), (3, 2, 's1', 9, 'm', 3, x'0038EDBA')")
	var r struct{ Pairs []pairOut }
	do(mux, "GET", "/api/voices/pairs", "", &r)
	if len(r.Pairs) != 2 {
		t.Fatalf("pairs: %+v", r.Pairs)
	}
	do(mux, "POST", "/api/voices/notsame", `{"a":2,"b":1}`, nil)
	r.Pairs = nil
	do(mux, "GET", "/api/voices/pairs", "", &r)
	if len(r.Pairs) != 1 {
		t.Errorf("after notsame: %+v", r.Pairs)
	}
}

func TestVoiceAssignSpread(t *testing.T) {
	mux, d := voiceServer(t,
		"INSERT INTO voices (id, file_id, label, seconds, model, cluster, person_id) VALUES (1, 1, 's1', 9, 'm', 7, NULL), (2, 2, 's1', 9, 'm', 7, NULL), (3, 2, 's2', 9, 'm', 7, 2), (4, 1, 's2', 9, 'm', 7, NULL)",
		"INSERT INTO voice_not_person VALUES (2, 1)")
	if c := do(mux, "POST", "/api/voices/1/person", `{"person":1}`, nil); c != 200 {
		t.Fatal(c)
	}
	got := map[int]int64{}
	for i := 2; i <= 4; i++ {
		var p sql.NullInt64
		d.QueryRow("SELECT person_id FROM voices WHERE id = ?", i).Scan(&p)
		got[i] = p.Int64
	}
	if got[2] != 0 || got[3] != 2 || got[4] != 1 {
		t.Errorf("spread: %v (marked voice and named other person stay, free one follows)", got)
	}
}
