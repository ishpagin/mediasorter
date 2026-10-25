package api

import "net/http"

type Collection struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type MetaResp struct {
	Files       int          `json:"files"`
	Faces       int          `json:"faces"`
	People      int          `json:"people"`
	Collections []Collection `json:"collections"` // newest first, like the sidebar
}

// meta: GET /api/meta — sidebar numbers and the collections list.
func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	var m MetaResp
	if err := s.DB.QueryRow("SELECT COUNT(*), COALESCE(SUM(n_faces), 0) FROM files").Scan(&m.Files, &m.Faces); err != nil {
		fail(w, err, 500)
		return
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM persons WHERE n_faces > 0").Scan(&m.People); err != nil {
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
