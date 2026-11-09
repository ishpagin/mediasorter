package api

import (
	"net/http"
	"strconv"

	"github.com/ishpagin/mediasorter/web/internal/speech"
)

type Collection struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type MetaResp struct {
	Files       int          `json:"files"`
	Faces       int          `json:"faces"`
	People      int          `json:"people"`
	Collections []Collection `json:"collections"` // newest first, like the sidebar
	Videos      int          `json:"videos"`
	Speech      string       `json:"speech"` // "remote", "local", "" = off (the model can be downloaded), "none" = not here
}

// speechMode: see MetaResp.Speech (the Python side has no speech at all).
func (s *Server) speechMode() string {
	if m, ok := s.ML.(interface{ SpeechMode() string }); ok {
		return m.SpeechMode()
	}
	return "none"
}

// GET /api/speech?id=: what is said in a video, phrase by phrase, and who says it.
func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "id?", http.StatusBadRequest)
		return
	}
	segs, err := speech.Of(s.DB, id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	voices, err := s.voicesOf(id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, struct {
		Segments []speech.Segment `json:"segments"`
		Voices   []VoiceInfo      `json:"voices"`
	}{segs, voices})
}

// meta: GET /api/meta — sidebar numbers and the collections list.
func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	var m MetaResp
	if err := s.DB.QueryRow("SELECT COUNT(*), COALESCE(SUM(n_faces), 0) FROM files").Scan(&m.Files, &m.Faces); err != nil {
		fail(w, err, 500)
		return
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM files WHERE kind = 'video'").Scan(&m.Videos); err != nil {
		fail(w, err, 500)
		return
	}
	m.Speech = s.speechMode()
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM persons WHERE n_faces > 0 OR n_voices > 0").Scan(&m.People); err != nil {
		fail(w, err, 500)
		return
	}
	rows, err := s.DB.Query("SELECT id, name FROM collections ORDER BY created_at DESC")
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer rows.Close()
	m.Collections = []Collection{}
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			fail(w, err, 500)
			return
		}
		m.Collections = append(m.Collections, c)
	}
	writeJSON(w, r, m)
}
