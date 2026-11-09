package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"

	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/scan"
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

// VoicePick: a voice with a phrase to listen to (its longest one).
type VoicePick struct {
	Voice  int64   `json:"voice"`
	File   int64   `json:"file"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Person int64   `json:"person,omitempty"`
	Name   string  `json:"name,omitempty"`
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

// pick: where to listen to a voice; person is what the caller knows about whose it is.
func (s *Server) pick(voice, person int64) (VoicePick, error) {
	v := VoicePick{Voice: voice, Person: person}
	err := s.DB.QueryRow("SELECT file_id FROM voices WHERE id = ?", voice).Scan(&v.File)
	if err != nil {
		return v, err
	}
	err = s.DB.QueryRow("SELECT t_start, t_end FROM speech WHERE voice_id = ? ORDER BY t_end - t_start DESC LIMIT 1", voice).Scan(&v.Start, &v.End)
	if err == sql.ErrNoRows {
		err = nil
	}
	if person != 0 {
		var n sql.NullString
		_ = s.DB.QueryRow("SELECT name FROM persons WHERE id = ?", person).Scan(&n)
		v.Name = n.String
	}
	return v, err
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

type voicePair struct{ A, B int64 }

func readPair(r *http.Request) (voicePair, bool) {
	var req voicePair
	return req, readJSON(r, &req) == nil && req.A != 0 && req.B != 0 && req.A != req.B
}

// POST /api/voices/same {a, b}: one person. Two different people are a conflict (409): the program never merges them.
func (s *Server) voicesSame(w http.ResponseWriter, r *http.Request) {
	req, ok := readPair(r)
	if !ok {
		http.Error(w, "нужны два разных голоса", http.StatusBadRequest)
		return
	}
	var pa, pb sql.NullInt64
	errA := s.DB.QueryRow("SELECT person_id FROM voices WHERE id = ?", req.A).Scan(&pa)
	errB := s.DB.QueryRow("SELECT person_id FROM voices WHERE id = ?", req.B).Scan(&pb)
	if errors.Is(errA, sql.ErrNoRows) || errors.Is(errB, sql.ErrNoRows) {
		http.Error(w, "такого голоса нет", http.StatusNotFound)
		return
	} else if err := errors.Join(errA, errB); err != nil {
		fail(w, err, 500)
		return
	}
	if pa.Valid && pb.Valid && pa.Int64 != pb.Int64 {
		a, err1 := s.personCard(pa.Int64)
		b, err2 := s.personCard(pb.Int64)
		if err1 != nil || err2 != nil {
			fail(w, fmt.Errorf("conflict persons: %v %v", err1, err2), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"conflict": map[string]Person{"a": a, "b": b}})
		return
	}
	tx, err := s.DB.Begin()
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer tx.Rollback()
	pid := pa.Int64
	if !pa.Valid {
		pid = pb.Int64
	}
	if !pa.Valid && !pb.Valid {
		var res sql.Result
		if res, err = tx.Exec("INSERT INTO persons (name) VALUES (NULL)"); err == nil {
			pid, err = res.LastInsertId()
		}
	}
	if err == nil {
		_, err = tx.Exec("UPDATE voices SET person_id = ?, suggest_id = NULL, manual = 1 WHERE id IN (?, ?)", pid, req.A, req.B)
	}
	if err == nil { // the user's "same" beats an older "not his voice"
		_, err = tx.Exec("DELETE FROM voice_not_person WHERE voice_id IN (?, ?) AND person_id = ?", req.A, req.B, pid)
	}
	if err == nil {
		_, err = tx.Exec("DELETE FROM voice_not_same WHERE a = ? AND b = ?", min(req.A, req.B), max(req.A, req.B))
	}
	if err == nil {
		err = core.RefreshPersonStats(tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	s.voiceMarkDone(w, r, err, "Запомнил: это один человек")
}

// POST /api/voices/notsame {a, b}
func (s *Server) voicesNotSame(w http.ResponseWriter, r *http.Request) {
	req, ok := readPair(r)
	if !ok {
		http.Error(w, "нужны два разных голоса", http.StatusBadRequest)
		return
	}
	_, err := s.DB.Exec("INSERT OR IGNORE INTO voice_not_same (a, b) VALUES (?, ?)", min(req.A, req.B), max(req.A, req.B))
	s.voiceMarkDone(w, r, err, "Запомнил: это разные люди")
}

type pairOut struct {
	Sim float64   `json:"sim"`
	A   VoicePick `json:"a"`
	B   VoicePick `json:"b"`
}

type sugOut struct {
	Voice  VoicePick `json:"voice"`
	Person Person    `json:"person"`
}

type voiceGroup struct {
	mean    []float32
	best    int64 // the voice with most speech: its phrase is played
	bestSec float64
	persons map[int64]int
	ids     []int64
}

func (g *voiceGroup) person() (best int64) {
	n := 0
	for p, c := range g.persons {
		if c > n || c == n && p < best {
			best, n = p, c
		}
	}
	return
}

// GET /api/voices/pairs?min=0.45: groups of voices that sound alike but are not linked (different people or none),
// plus the voices with a suggested person. The user listens and decides.
func (s *Server) voicePairs(w http.ResponseWriter, r *http.Request) {
	lo, err := strconv.ParseFloat(r.URL.Query().Get("min"), 32)
	if err != nil || lo <= 0 {
		lo = 0.45
	}
	hi := float64(scan.DefaultVoiceOptions().Thr)
	rows, err := s.DB.Query(`SELECT id, COALESCE(cluster, -id), COALESCE(person_id, 0), seconds, emb FROM voices
		WHERE emb IS NOT NULL AND model = (SELECT model FROM voices WHERE emb IS NOT NULL GROUP BY model ORDER BY COUNT(*) DESC, model LIMIT 1)`)
	if err != nil {
		fail(w, err, 500)
		return
	}
	groups := map[int64]*voiceGroup{}
	var order []int64
	for rows.Next() {
		var id, cl, pid int64
		var sec float64
		var emb []byte
		if err := rows.Scan(&id, &cl, &pid, &sec, &emb); err != nil {
			rows.Close()
			fail(w, err, 500)
			return
		}
		if len(emb) < 2 {
			continue
		}
		v := scan.Vectors(emb, len(emb)/2)[0]
		g := groups[cl]
		if g == nil {
			g = &voiceGroup{mean: make([]float32, len(v)), persons: map[int64]int{}}
			groups[cl], order = g, append(order, cl)
		}
		if len(v) != len(g.mean) {
			continue
		}
		for i, x := range v {
			g.mean[i] += x
		}
		if pid != 0 {
			g.persons[pid]++
		}
		if g.best == 0 || sec > g.bestSec {
			g.best, g.bestSec = id, sec
		}
		g.ids = append(g.ids, id)
	}
	rows.Close()
	for _, g := range groups {
		var n float64
		for _, x := range g.mean {
			n += float64(x) * float64(x)
		}
		if n = math.Sqrt(n); n > 0 {
			for i := range g.mean {
				g.mean[i] /= float32(n)
			}
		}
	}
	notSame := map[voicePair]bool{} // marks may name voices already gone: only live ones are ever looked up
	nr, err := s.DB.Query("SELECT a, b FROM voice_not_same")
	if err != nil {
		fail(w, err, 500)
		return
	}
	for nr.Next() {
		var p voicePair
		if nr.Scan(&p.A, &p.B) == nil {
			notSame[p] = true
		}
	}
	nr.Close()
	type cand struct {
		sim  float64
		a, b *voiceGroup
	}
	var cands []cand
	for i, ka := range order {
		for _, kb := range order[i+1:] {
			a, b := groups[ka], groups[kb]
			pa, pb := a.person(), b.person()
			if pa != 0 && pa == pb {
				continue
			}
			var dot float64
			for k := range a.mean {
				dot += float64(a.mean[k]) * float64(b.mean[k])
			}
			if dot < lo || dot >= hi || marked(notSame, a.ids, b.ids) {
				continue
			}
			cands = append(cands, cand{dot, a, b})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	cands = cands[:min(len(cands), 200)]
	out := struct {
		Pairs    []pairOut `json:"pairs"`
		Suggests []sugOut  `json:"suggests"`
	}{[]pairOut{}, []sugOut{}}
	for _, c := range cands {
		a, err1 := s.pick(c.a.best, c.a.person())
		b, err2 := s.pick(c.b.best, c.b.person())
		if err1 != nil || err2 != nil {
			continue
		}
		out.Pairs = append(out.Pairs, pairOut{c.sim, a, b})
	}
	sr, err := s.DB.Query(`SELECT id, suggest_id FROM voices WHERE person_id IS NULL AND suggest_id IS NOT NULL
		AND suggest_id IN (SELECT id FROM persons)
		AND NOT EXISTS (SELECT 1 FROM voice_not_person n WHERE n.voice_id = voices.id AND n.person_id = voices.suggest_id)`)
	if err != nil {
		fail(w, err, 500)
		return
	}
	type sug struct{ voice, person int64 }
	var sugs []sug
	for sr.Next() {
		var x sug
		if sr.Scan(&x.voice, &x.person) == nil {
			sugs = append(sugs, x)
		}
	}
	sr.Close()
	for _, x := range sugs {
		v, err1 := s.pick(x.voice, 0)
		p, err2 := s.personCard(x.person)
		if err1 == nil && err2 == nil {
			out.Suggests = append(out.Suggests, sugOut{v, p})
		}
	}
	writeJSON(w, r, out)
}

func marked(m map[voicePair]bool, as, bs []int64) bool {
	for _, a := range as {
		for _, b := range bs {
			if m[voicePair{min(a, b), max(a, b)}] {
				return true
			}
		}
	}
	return false
}
