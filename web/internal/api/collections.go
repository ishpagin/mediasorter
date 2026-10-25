package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ishpagin/mediasorter/web/internal/core"
)

// CollectionCard: a collection on the collections page.
type CollectionCard struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Folder    string  `json:"folder"` // as the user knows it (\\nas\home\...)
	Files     int     `json:"files"`
	Videos    int     `json:"videos"`
	MB        float64 `json:"mb"`
	ZipMB     float64 `json:"zipMB"` // 0 = no ZIP yet
	ZipFolder string  `json:"zipFolder,omitempty"`
}

type CollectionsResp struct {
	Root        string           `json:"root"`
	RootShown   string           `json:"rootShown"`
	Collections []CollectionCard `json:"collections"`
}

// GET /api/collections: every collection, newest first.
func (s *Server) collectionsList(w http.ResponseWriter, r *http.Request) {
	root := core.CollectionsRoot(s.DB)
	resp := CollectionsResp{Root: root, RootShown: s.Cfg.MapToWindows(root), Collections: []CollectionCard{}}
	rows, err := s.DB.Query(`SELECT k.id, k.name, k.folder, COUNT(c.file_id), COALESCE(SUM(f.size), 0) / 1e6,
		COALESCE(SUM(f.kind = 'video'), 0)
		FROM collections k LEFT JOIN collection_files c ON c.collection_id = k.id
		LEFT JOIN files f ON f.id = c.file_id GROUP BY k.id ORDER BY k.created_at DESC`)
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var c CollectionCard
		if err := rows.Scan(&c.ID, &c.Name, &c.Folder, &c.Files, &c.MB, &c.Videos); err != nil {
			fail(w, err, 500)
			return
		}
		if st, err := os.Stat(core.ZipPath(c.Folder)); err == nil {
			c.ZipMB = float64(st.Size()) / 1e6
			c.ZipFolder = s.Cfg.MapToWindows(filepath.Dir(c.Folder))
		}
		c.Folder = s.Cfg.MapToWindows(c.Folder)
		resp.Collections = append(resp.Collections, c)
	}
	writeJSON(w, r, resp)
}

// PUT /api/collections/root {root}: where new collection folders go (a Windows path is mapped to this machine).
func (s *Server) collectionsRoot(w http.ResponseWriter, r *http.Request) {
	var req struct{ Root string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Root) == "" {
		http.Error(w, "нужна папка", http.StatusBadRequest)
		return
	}
	root := s.Cfg.MapToLocal(strings.TrimSpace(req.Root))
	if err := os.MkdirAll(root, 0o775); err != nil {
		http.Error(w, "не получилось создать папку: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('collections_root', ?)", root); err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, ActionResp{Message: "Новые подборки будут в " + s.Cfg.MapToWindows(root)})
}

// GET /api/collections/{id}/files: its files; Coll = the other collections it is in, Note = where it came from.
func (s *Server) collectionFiles(w http.ResponseWriter, r *http.Request) {
	cid, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rows, err := s.DB.Query(`SELECT f.id, f.path, f.kind, COALESCE(f.taken_at, ''), COALESCE(f.place, ''), c.original_path,
		COALESCE((SELECT group_concat(k.name, ', ') FROM collection_files o JOIN collections k ON k.id = o.collection_id
		          WHERE o.file_id = f.id AND o.collection_id != c.collection_id), '')
		FROM collection_files c JOIN files f ON f.id = c.file_id WHERE c.collection_id = ? ORDER BY f.taken_at, f.path`, cid)
	if err != nil {
		fail(w, err, 500)
		return
	}
	var items []Item
	others := map[int64]string{}
	for rows.Next() {
		var it Item
		var orig, also string
		if err := rows.Scan(&it.ID, &it.Path, &it.Kind, &it.Taken, &it.Place, &orig, &also); err != nil {
			rows.Close()
			fail(w, err, 500)
			return
		}
		it.Note = "↩ " + s.Cfg.MapToWindows(filepath.Dir(orig))
		others[it.ID] = also
		items = append(items, it)
	}
	rows.Close()
	var resp FilesResp
	// everything in the collection is shown, videos and files moved here included
	q := r.URL.Query()
	q.Set("video", "1")
	q.Set("moved", "1")
	r.URL.RawQuery = q.Encode()
	if resp.Items, resp.HiddenVideo, resp.HiddenMoved, err = s.filterList(r, items, true); err != nil {
		fail(w, err, 500)
		return
	}
	for i := range resp.Items {
		resp.Items[i].Coll = others[resp.Items[i].ID]
	}
	resp.Years, resp.Cats = []string{}, []Count{}
	writeJSON(w, r, resp)
}

// POST /api/collections/{id}/remove {files}: out of the collection (a copy goes; a moved file returns to the archive).
func (s *Server) collectionRemove(w http.ResponseWriter, r *http.Request) {
	cid, ok := fileID(r)
	var req struct{ Files []int64 }
	if !ok || readJSON(r, &req) != nil || len(req.Files) == 0 {
		http.Error(w, "нужны файлы", http.StatusBadRequest)
		return
	}
	pairs := make([]core.Pair, len(req.Files))
	for i, f := range req.Files {
		pairs[i] = core.Pair{Collection: cid, File: f}
	}
	done, failed := core.ReturnFromCollection(s.DB, pairs)
	writeJSON(w, r, ActionResp{Message: "Убрано из подборки: " + files(done), Failed: failed})
}

// POST /api/collections/{id}/zip: <folder>.zip next to the folder.
func (s *Server) collectionZip(w http.ResponseWriter, r *http.Request) {
	cid, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	out, n, err := core.ZipCollection(s.DB, cid)
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, ActionResp{Message: fmt.Sprintf("ZIP готов: %s (%s)", s.Cfg.MapToWindows(out), files(n))})
}

// POST /api/collections/{id}/finish {keepZip}: "sent — delete"; the deleted files are one Ctrl+Z step.
func (s *Server) collectionFinish(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	cid, ok := fileID(r)
	var req struct{ KeepZip bool }
	if !ok || readJSON(r, &req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var name string
	_ = s.DB.QueryRow("SELECT name FROM collections WHERE id = ?", cid).Scan(&name)
	done, failed, ids, err := s.Cfg.FinishCollection(s.DB, cid, !req.KeepZip)
	if err != nil {
		fail(w, err, 500)
		return
	}
	if len(ids) > 0 {
		if err := core.PushUndo(s.DB, sid, "delete", fmt.Sprintf("удаление подборки «%s»", name), ids); err != nil {
			fail(w, err, 500)
			return
		}
	}
	writeJSON(w, r, ActionResp{Message: fmt.Sprintf("«%s»: удалено %s", name, files(done)), Failed: failed,
		Undo: core.LastUndo(s.DB, sid)})
}

// DELETE /api/collections/{id}: an empty collection.
func (s *Server) collectionDelete(w http.ResponseWriter, r *http.Request) {
	cid, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var n int
	var folder string
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM collection_files WHERE collection_id = ?", cid).Scan(&n)
	if n > 0 {
		http.Error(w, "в подборке есть файлы", http.StatusConflict)
		return
	}
	_ = s.DB.QueryRow("SELECT folder FROM collections WHERE id = ?", cid).Scan(&folder)
	if _, err := s.DB.Exec("DELETE FROM collections WHERE id = ?", cid); err != nil {
		fail(w, err, 500)
		return
	}
	if folder != "" {
		_ = os.Remove(folder) // only if empty
	}
	writeJSON(w, r, ActionResp{Message: "Пустая подборка удалена"})
}
