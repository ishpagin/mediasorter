package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/db"
)

// A ~2 s video is a live photo: it shows with photos, not on the videos page.
func TestLivePhotos(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO files (id, path, kind, n_faces, duration) VALUES
		(1, '/a.jpg', 'image', 0, NULL), (2, '/live.mov', 'video', 0, 2.1), (3, '/long.mp4', 'video', 0, 30),
		(4, '/nodur.mp4', 'video', 0, NULL), (5, '/IMG_1.HEIC', 'image', 0, NULL), (6, '/IMG_1.mov', 'video', 0, 2.5)`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Server{DB: d, Cfg: &core.Config{}, ML: &stubML{}}).Routes(mux)
	get := func(url string, out any) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", url, w.Code, w.Body)
		}
		json.Unmarshal(w.Body.Bytes(), out)
	}
	ids := func(view string) (l []int64) {
		var r FilesResp
		get("/api/files?view="+view, &r)
		for _, it := range r.Items {
			l = append(l, it.ID)
		}
		return l
	}
	// 6 is the video half of the live photo 5: one shot, one tile
	if got := ids("files"); !reflect.DeepEqual(got, []int64{5, 1, 2}) {
		t.Errorf("photos = %v, want [5 1 2]", got)
	}
	if got := ids("videos"); !reflect.DeepEqual(got, []int64{3, 4}) {
		t.Errorf("videos = %v, want [3 4]", got)
	}
	var m MetaResp
	get("/api/meta", &m)
	if m.Videos != 2 {
		t.Errorf("meta videos = %d, want 2", m.Videos)
	}
}
