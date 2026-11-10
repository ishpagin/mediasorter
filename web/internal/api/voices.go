package api

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"

	"github.com/ishpagin/mediasorter/web/internal/core"
)

// VoiceInfo: a speaker of a video. N counts from 1 in order of first appearance.
type VoiceInfo struct {
	ID          int64  `json:"id"`
	N           int    `json:"n"`
	Person      int64  `json:"person,omitempty"`
	Name        string `json:"name,omitempty"`
	Suggest     int64  `json:"suggest,omitempty"`
	SuggestName string `json:"suggestName,omitempty"`
	Manual      bool   `json:"manual"`
}

func (s *Server) voicesOf(file int64) ([]VoiceInfo, error) {
	rows, err := s.DB.Query(`SELECT v.id, COALESCE(v.person_id, 0), COALESCE(p.name, ''), COALESCE(v.suggest_id, 0),
		COALESCE(q.name, ''), v.manual FROM voices v
		LEFT JOIN persons p ON p.id = v.person_id LEFT JOIN persons q ON q.id = v.suggest_id
		WHERE v.file_id = ?
		ORDER BY (SELECT MIN(t_start) FROM speech WHERE voice_id = v.id) IS NULL,
		         (SELECT MIN(t_start) FROM speech WHERE voice_id = v.id), v.id`, file)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VoiceInfo{}
	for rows.Next() {
		v := VoiceInfo{N: len(out) + 1}
		if err := rows.Scan(&v.ID, &v.Person, &v.Name, &v.Suggest, &v.SuggestName, &v.Manual); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Server) personCard(id int64) (Person, error) {
	p := Person{ID: id}
	var n sql.NullString
	err := s.DB.QueryRow(`SELECT name, n_files, COALESCE((SELECT id FROM faces WHERE person_id = persons.id AND thumb IS NOT NULL
		ORDER BY size * det_score DESC LIMIT 1), 0) FROM persons WHERE id = ?`, id).Scan(&n, &p.N, &p.Face)
	p.Name = n.String
	return p, err
}

func (s *Server) voiceMarkDone(w http.ResponseWriter, r *http.Request, err error, msg string) {
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, ActionResp{Message: msg})
}

// POST /api/voices/{id}/person {person?, create?, clear?}: whose voice this is, by hand. The voice and the automatic
// voices of its group get the person; clear takes it off and remembers "not his voice".
func (s *Server) voicePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	var req struct {
		Person int64 `json:"person"`
		Create bool  `json:"create"`
		Clear  bool  `json:"clear"`
	}
	if !ok || readJSON(r, &req) != nil {
		http.Error(w, "id?", http.StatusBadRequest)
		return
	}
	tx, err := s.DB.Begin()
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer tx.Rollback()
	var cur, cluster sql.NullInt64
	if err := tx.QueryRow("SELECT person_id, cluster FROM voices WHERE id = ?", id).Scan(&cur, &cluster); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "такого голоса нет", http.StatusNotFound)
		return
	} else if err != nil {
		fail(w, err, 500)
		return
	}
	msg := "Запомнил"
	switch {
	case req.Clear:
		_, err = tx.Exec("UPDATE voices SET person_id = NULL, suggest_id = NULL, manual = 1 WHERE id = ?", id)
		if err == nil && cur.Valid {
			_, err = tx.Exec("INSERT OR IGNORE INTO voice_not_person (voice_id, person_id) VALUES (?, ?)", id, cur.Int64)
		}
		msg = "Голос больше не его"
	default:
		pid := req.Person
		if req.Create {
			var res sql.Result
			if res, err = tx.Exec("INSERT INTO persons (name) VALUES (NULL)"); err == nil {
				pid, err = res.LastInsertId()
			}
		} else if err = tx.QueryRow("SELECT id FROM persons WHERE id = ?", pid).Scan(&pid); err != nil {
			http.Error(w, "такого человека нет", http.StatusBadRequest)
			return
		}
		if err == nil {
			_, err = tx.Exec("UPDATE voices SET person_id = ?, suggest_id = NULL, manual = 1 WHERE id = ?", pid, id)
		}
		if err == nil {
			_, err = tx.Exec("DELETE FROM voice_not_person WHERE voice_id = ? AND person_id = ?", id, pid)
		}
		if err == nil && cluster.Valid {
			_, err = tx.Exec(`UPDATE voices SET person_id = ?1, suggest_id = NULL WHERE cluster = ?2 AND manual = 0 AND id != ?3
				AND (person_id IS NULL OR person_id = ?1 OR person_id IN (SELECT id FROM persons WHERE name IS NULL))
				AND NOT EXISTS (SELECT 1 FROM voice_not_person n WHERE n.voice_id = voices.id AND n.person_id = ?1)`, pid, cluster.Int64, id)
		}
		msg = "Голос закреплён за человеком"
	}
	if err == nil {
		err = core.RefreshPersonStats(tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	s.voiceMarkDone(w, r, err, msg)
}

// POST /api/voices/{id}/reject {person}: "not his voice", the suggestion goes away.
func (s *Server) voiceReject(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	var req struct{ Person int64 }
	if !ok || readJSON(r, &req) != nil || req.Person == 0 {
		http.Error(w, "id?", http.StatusBadRequest)
		return
	}
	_, err := s.DB.Exec("INSERT OR IGNORE INTO voice_not_person (voice_id, person_id) VALUES (?, ?)", id, req.Person)
	if err == nil {
		_, err = s.DB.Exec("UPDATE voices SET suggest_id = NULL WHERE id = ?", id)
	}
	s.voiceMarkDone(w, r, err, "Запомнил: это не его голос")
}

// VoiceClip: one short phrase of a voice to listen to.
type VoiceClip struct {
	Voice int64   `json:"voice"`
	File  int64   `json:"file"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	Taken string  `json:"taken"`
}

// VoiceGroup: one voice heard in several videos, still without a name.
type VoiceGroup struct {
	Cluster int64       `json:"cluster"`
	Files   int         `json:"files"`
	Person  int64       `json:"person,omitempty"` // the unnamed person it already is
	Suggest *Person     `json:"suggest,omitempty"`
	Clips   []VoiceClip `json:"clips"`
}

// GET /api/voices/groups: voices heard in 2+ videos without a named person, most heard first, each with up to
// 3 short phrases from across the years. The answer goes to the whole group: POST /api/voices/{clip voice}/person
// spreads to the group, POST /api/voices/groups/{cluster}/reject says "not him" for all of it.
func (s *Server) voiceGroups(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Query(`SELECT v.cluster, v.id, v.file_id, COALESCE(v.person_id, 0), p.name IS NOT NULL,
		COALESCE(v.suggest_id, 0), COALESCE(f.taken_at, '')
		FROM voices v JOIN files f ON f.id = v.file_id LEFT JOIN persons p ON p.id = v.person_id
		WHERE v.cluster IS NOT NULL ORDER BY v.cluster, f.taken_at, v.id`)
	if err != nil {
		fail(w, err, 500)
		return
	}
	type member struct {
		id, file int64
		taken    string
	}
	type group struct {
		files             map[int64]bool
		persons, suggests map[int64]int
		named             map[int64]bool
		members           []member
	}
	groups := map[int64]*group{}
	var order []int64
	for rows.Next() {
		var cl, id, file, pid, sug int64
		var named bool
		var taken string
		if err := rows.Scan(&cl, &id, &file, &pid, &named, &sug, &taken); err != nil {
			rows.Close()
			fail(w, err, 500)
			return
		}
		g := groups[cl]
		if g == nil {
			g = &group{files: map[int64]bool{}, persons: map[int64]int{}, suggests: map[int64]int{}, named: map[int64]bool{}}
			groups[cl], order = g, append(order, cl)
		}
		g.files[file] = true
		if pid != 0 {
			g.persons[pid]++
			g.named[pid] = named
		}
		if sug != 0 {
			g.suggests[sug]++
		}
		g.members = append(g.members, member{id, file, taken})
	}
	rows.Close()
	top := func(m map[int64]int) (best int64) {
		n := 0
		for p, c := range m {
			if c > n || c == n && p < best {
				best, n = p, c
			}
		}
		return
	}
	out := []VoiceGroup{}
	cards := map[int64]*Person{}
	for _, cl := range order {
		g := groups[cl]
		pid := top(g.persons)
		if len(g.files) < 2 || g.named[pid] {
			continue
		}
		vg := VoiceGroup{Cluster: cl, Files: len(g.files), Person: pid, Clips: []VoiceClip{}}
		if sug := top(g.suggests); sug != 0 {
			if cards[sug] == nil {
				if p, err := s.personCard(sug); err == nil {
					cards[sug] = &p
				}
			}
			vg.Suggest = cards[sug]
		}
		// the oldest, the middle and the newest video first, then the rest if those have no usable phrase
		n := len(g.members)
		try := []int{0, n / 2, n - 1}
		for i := range n {
			try = append(try, i)
		}
		seen := map[int64]bool{}
		for _, i := range try {
			m := g.members[i]
			if len(vg.Clips) == 3 || seen[m.file] {
				continue
			}
			c := VoiceClip{Voice: m.id, File: m.file, Taken: m.taken[:min(len(m.taken), 10)]}
			// a phrase of a few seconds: long enough to know the voice, short enough to listen to
			err := s.DB.QueryRow(`SELECT t_start, t_end, text FROM speech WHERE voice_id = ? AND t_end - t_start >= 1.5
				ORDER BY ABS(t_end - t_start - 5) LIMIT 1`, m.id).Scan(&c.Start, &c.End, &c.Text)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				fail(w, err, 500)
				return
			}
			seen[m.file] = true
			vg.Clips = append(vg.Clips, c)
		}
		if len(vg.Clips) > 0 {
			out = append(out, vg)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Files > out[j].Files })
	writeJSON(w, r, out)
}

// POST /api/voices/groups/{id}/reject {person}: "not him" for every voice of the group, the suggestion goes away.
func (s *Server) voiceGroupReject(w http.ResponseWriter, r *http.Request) {
	cl, ok := fileID(r)
	var req struct{ Person int64 }
	if !ok || readJSON(r, &req) != nil || req.Person == 0 {
		http.Error(w, "id?", http.StatusBadRequest)
		return
	}
	tx, err := s.DB.Begin()
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT OR IGNORE INTO voice_not_person (voice_id, person_id) SELECT id, ? FROM voices WHERE cluster = ?", req.Person, cl)
	if err == nil {
		_, err = tx.Exec("UPDATE voices SET suggest_id = NULL WHERE cluster = ? AND suggest_id = ?", cl, req.Person)
	}
	if err == nil {
		err = tx.Commit()
	}
	s.voiceMarkDone(w, r, err, "Запомнил: это не он")
}
