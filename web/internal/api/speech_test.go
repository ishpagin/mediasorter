package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/db"
)

func TestSearchSpeech(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO files (id, path, kind, n_faces) VALUES (1, '/v/a.mp4', 'video', 0), (2, '/v/b.mp4', 'video', 2), (3, '/v/c.mp4', 'video', 0)",
		`INSERT INTO speech (file_id, t_start, t_end, text, low) VALUES
			(1, 0, 2, 'Несите торт со свечами!', 'несите торт со свечами!'),
			(2, 0, 2, 'Где торты?', 'где торты?'), (2, 3, 4, 'Торт уже тут, ёлка тоже', 'торт уже тут, елка тоже'),
			(3, 0, 1, 'Тортик', 'тортик')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	(&Server{DB: d, Cfg: &core.Config{}, ML: &stubML{}}).Routes(mux)
	get := func(url string) (int, FilesResp) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		var out FilesResp
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	ids := func(r FilesResp) (l []int64) {
		for _, it := range r.Items {
			l = append(l, it.ID)
		}
		return
	}

	code, r := get("/api/search?q=" + "%D0%A2%D0%9E%D0%A0%D0%A2") // "ТОРТ": case folded, parts of words count
	if code != 200 || len(r.Items) != 3 || r.Items[0].ID != 2 {   // b.mp4 says it twice
		t.Fatalf("торт: %d %v", code, ids(r))
	}
	if r.Items[0].Note != `в речи: "Где торты?"` {
		t.Errorf("note %q", r.Items[0].Note)
	}
	if _, r = get("/api/search?q=%D1%81%D0%B2%D0%B5%D1%87%D0%B8+%D1%82%D0%BE%D1%80%D1%82"); len(r.Items) != 1 || r.Items[0].ID != 1 { // "свечи торт": every word
		t.Errorf("свечи торт: %v", ids(r))
	}
	if _, r = get("/api/search?q=%D1%91%D0%BB%D0%BA%D0%B0"); len(r.Items) != 1 || r.Items[0].ID != 2 { // "ёлка" finds "елка"
		t.Errorf("ёлка: %v", ids(r))
	}
	if _, r = get("/api/search?q=%D1%82%25%D0%BA+%D1%82_%D1%80"); len(r.Items) != 0 { // "т%к т_р": wildcards are plain characters
		t.Errorf("wildcards matched: %v", ids(r))
	}
	if _, r = get("/api/search?nopeople=1&q=%D1%82%D0%BE%D1%80%D1%82"); len(r.Items) != 2 {
		t.Errorf("nopeople: %v", ids(r))
	}
	if code, _ = get("/api/search?q=%D0%BA%D0%BE%D1%82"); code != http.StatusConflict { // nothing said, no picture vectors
		t.Errorf("кот: %d", code)
	}
}
