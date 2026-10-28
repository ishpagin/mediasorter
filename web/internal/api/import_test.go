package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/db"
	"github.com/ishpagin/mediasorter/web/internal/ml"
)

type stubML struct{ jobs []string }

func (m *stubML) EmbedText(string) ([]float32, error) { return nil, nil }
func (m *stubML) Convert(string) ([]byte, error)      { return nil, nil }
func (m *stubML) StartJob(name string) (ml.Job, error) {
	m.jobs = append(m.jobs, name)
	return ml.Job{}, nil
}

func importReq(t *testing.T, token string, files map[string]string) *http.Request {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write([]byte(data))
	}
	mw.WriteField("meta", `{"source":"inbox","mail_id":"m1","from":"Иван Петров <ivan@example.com>","date":"2026-10-27T15:04:05+03:00"}`)
	mw.Close()
	r := httptest.NewRequest("POST", "/api/import", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestImportMail(t *testing.T) {
	tmp := t.TempDir()
	d, err := db.Open(filepath.Join(tmp, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	stub := &stubML{}
	root := filepath.Join(tmp, "Почта")
	mux := http.NewServeMux()
	(&Server{DB: d, Cfg: &core.Config{ImportDir: root, ImportToken: "secret"}, ML: stub}).Routes(mux)
	do := func(r *http.Request) (int, map[string]int) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out map[string]int
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	if code, _ := do(importReq(t, "wrong", map[string]string{"a.jpg": "A"})); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	if code, _ := do(importReq(t, "secret", map[string]string{"a.jpg": "A", "doc.pdf": "P"})); code != 415 {
		t.Fatalf("pdf: %d", code)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("415 must save nothing")
	}

	code, out := do(importReq(t, "secret", map[string]string{"../../a.jpg": "A", "b.png": "B"}))
	if code != 200 || out["imported"] != 2 || out["dup"] != 0 {
		t.Fatalf("import: %d %v", code, out)
	}
	dir := filepath.Join(root, "2026", "2026-10-27 Иван Петров")
	for _, name := range []string{"a.jpg", "b.png"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if want := time.Date(2026, 10, 27, 12, 4, 5, 0, time.UTC); !st.ModTime().Equal(want) {
			t.Fatalf("%s mtime %v", name, st.ModTime())
		}
	}

	// the same bytes again: dup; same name, other bytes: a second file next to it
	code, out = do(importReq(t, "secret", map[string]string{"a.jpg": "A", "b.png": "B2"}))
	if code != 200 || out["imported"] != 1 || out["dup"] != 1 {
		t.Fatalf("repeat: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "b (2).png")); err != nil {
		t.Fatal(err)
	}
	if len(stub.jobs) != 2 || stub.jobs[0] != "scan" {
		t.Fatalf("scan jobs: %v", stub.jobs)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".import-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestImportOff(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{Cfg: &core.Config{}}).Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/import", nil))
	if w.Code != 404 {
		t.Fatalf("import off: %d", w.Code)
	}
}
