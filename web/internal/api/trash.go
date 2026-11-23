package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/ishpagin/mediasorter/web/internal/core"
)

// canEmpty: erasing for good is for the owner only. The board's proxy sets X-Webauth-User (and strips it from
// outside); without MS_OWNER (the desktop app, a local run) there is only one user anyway.
func (s *Server) canEmpty(r *http.Request) bool {
	return s.Cfg.Owner == "" || r.Header.Get("X-Webauth-User") == s.Cfg.Owner
}

type trashResp struct {
	Items    []core.TrashItem `json:"items"`
	Bytes    int64            `json:"bytes"`
	CanEmpty bool             `json:"canEmpty"`
}

// GET /api/trash: what is in the quarantine; deletions whose file is gone from it are forgotten first.
func (s *Server) trash(w http.ResponseWriter, r *http.Request) {
	if _, err := core.PruneTrash(s.DB); err != nil {
		fail(w, err, 500)
		return
	}
	items, n, err := core.TrashList(s.DB)
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, trashResp{items, n, s.canEmpty(r)})
}

// POST /api/trash/restore {ids}: back to where they lived (ids are trash item ids).
func (s *Server) trashRestore(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	done, failed, err := s.Cfg.Restore(s.DB, req.IDs)
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, ActionResp{Message: "Возвращено: " + files(done), Failed: failed, Undo: core.LastUndo(s.DB, sid)})
}

// POST /api/trash/empty: erase everything in the quarantine for good.
func (s *Server) trashEmpty(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	if !s.canEmpty(r) {
		http.Error(w, "очистить корзину может только владелец", http.StatusForbidden)
		return
	}
	done, failed, err := core.EmptyTrash(s.DB)
	if err != nil {
		fail(w, err, 500)
		return
	}
	msg := "Корзина очищена: удалено навсегда " + files(done)
	if len(failed) > 0 {
		msg = fmt.Sprintf("%s, не удалось: %d", msg, len(failed))
	}
	writeJSON(w, r, ActionResp{Message: msg, Failed: failed, Undo: core.LastUndo(s.DB, sid)})
}

// GET /trash/preview/{id}: the preview kept in the deletion's snapshot.
func (s *Server) trashPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	jpeg, err := core.TrashPreview(s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		fail(w, err, 500)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(jpeg)
}
