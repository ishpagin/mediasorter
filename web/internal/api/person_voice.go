package api

import (
	"math"
	"net/http"
	"strings"

	"github.com/ishpagin/mediasorter/web/internal/core"
)

// personVoiceFiles: GET /api/person/{id}/files?mode=voice&with=: videos where their voice is heard, each with one
// of their phrases (note) and where it starts (at), so the video opens on them speaking. Videos are always shown.
func (s *Server) personVoiceFiles(w http.ResponseWriter, r *http.Request, id int64) {
	with := idList(r.URL.Query().Get("with"))
	cond := strings.Repeat(" AND f.id IN (SELECT file_id FROM person_media WHERE person_id = ?)", len(with))
	tags := "''"
	if s.hasTable("tags") {
		tags = "COALESCE((SELECT label FROM tags WHERE file_id = f.id AND rank = 1), '')"
	}
	items, err := s.queryItems(`SELECT f.id, f.path, f.kind, COALESCE(f.taken_at, ''), COALESCE(f.place, ''), `+tags+`, 0
		FROM files f WHERE f.id IN (SELECT file_id FROM voices WHERE person_id = ?)`+cond+` ORDER BY f.taken_at, f.path`,
		append([]any{id}, anys(with)...)...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	type phrase struct {
		at, fit float64
		text    string
	}
	best := map[int64]phrase{}
	rows, err := s.DB.Query(`SELECT v.file_id, s.t_start, s.t_end, s.text FROM speech s JOIN voices v ON v.id = s.voice_id
		WHERE v.person_id = ?`, id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	for rows.Next() {
		var f int64
		var a, b float64
		var t string
		if rows.Scan(&f, &a, &b, &t) != nil {
			continue
		}
		fit := math.Abs(b - a - 5) // a few seconds: enough to recognise, short enough to read
		if p, ok := best[f]; !ok || fit < p.fit {
			best[f] = phrase{a, fit, t}
		}
	}
	rows.Close()
	for i := range items {
		if p, ok := best[items[i].ID]; ok {
			t := []rune(p.text)
			if len(t) > 90 {
				t = append(t[:90], '…')
			}
			items[i].Note, items[i].At = `"`+string(t)+`"`, max(0, p.at-1)
		}
	}
	s.finishList(w, r, items, FilesResp{}, true)
}

// POST /api/person/{id}/voices/unlink {files}: "not their voice" in these videos.
func (s *Server) personVoicesUnlink(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	var req struct{ Files []int64 }
	if !ok || readJSON(r, &req) != nil || len(req.Files) == 0 {
		http.Error(w, "files?", http.StatusBadRequest)
		return
	}
	tx, err := s.DB.Begin()
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer tx.Rollback()
	in := "file_id IN (" + ph(len(req.Files)) + ") AND person_id = ?"
	args := append(anys(req.Files), id)
	_, err = tx.Exec("INSERT OR IGNORE INTO voice_not_person (voice_id, person_id) SELECT id, person_id FROM voices WHERE "+in, args...)
	if err == nil {
		_, err = tx.Exec("UPDATE voices SET person_id = NULL, suggest_id = NULL, manual = 1 WHERE "+in, args...)
	}
	if err == nil {
		err = core.RefreshPersonStats(tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	s.voiceMarkDone(w, r, err, "Запомнил: в этих видео не его голос")
}

// POST /api/person/{id}/voices/reject {person}: their voice is not that person's, the suggestion goes away.
func (s *Server) personVoicesReject(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	var req struct{ Person int64 }
	if !ok || readJSON(r, &req) != nil || req.Person == 0 {
		http.Error(w, "person?", http.StatusBadRequest)
		return
	}
	tx, err := s.DB.Begin()
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT OR IGNORE INTO voice_not_person (voice_id, person_id) SELECT id, ? FROM voices WHERE person_id = ?", req.Person, id)
	if err == nil {
		_, err = tx.Exec("UPDATE voices SET suggest_id = NULL WHERE person_id = ? AND suggest_id = ?", id, req.Person)
	}
	if err == nil {
		err = tx.Commit()
	}
	s.voiceMarkDone(w, r, err, "Запомнил: это не он")
}
