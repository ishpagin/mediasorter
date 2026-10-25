package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ishpagin/mediasorter/web/internal/core"
)

func label(id int64, name sql.NullString) string {
	if name.Valid && name.String != "" {
		return name.String
	}
	return fmt.Sprintf("person_%d", id)
}

func idList(s string) []int64 {
	var out []int64
	for _, x := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func ph(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// GET /thumb/face/{id}: the stored face crop (JPEG).
func (s *Server) faceThumb(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var jpeg []byte
	if err := s.DB.QueryRow("SELECT thumb FROM faces WHERE id = ? AND thumb IS NOT NULL", id).Scan(&jpeg); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(jpeg)
}

// Person: a card on the people page.
type Person struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"` // "" = unnamed
	N      int    `json:"n"`    // files (photos only while videos are hidden)
	Face   int64  `json:"face"` // the best face (thumbnail)
	Years  string `json:"years"`
	Hidden bool   `json:"hidden,omitempty"`
}

// GET /api/persons?q=&min=2&hidden=1&video=1: people with at least `min` files, most files first.
func (s *Server) persons(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	min, err := strconv.Atoi(q.Get("min"))
	if err != nil || min < 1 {
		min = 2
	}
	kind := "AND f.kind = 'image'" // with videos hidden a person seen only in videos would open as an empty page
	if q.Get("video") == "1" {
		kind = ""
	}
	hidden := 0
	if q.Get("hidden") == "1" {
		hidden = 1
	}
	rows, err := s.DB.Query(`WITH pc AS (
		SELECT x.person_id AS pid, COUNT(DISTINCT x.file_id) AS n FROM faces x JOIN files f ON f.id = x.file_id
		WHERE x.person_id IS NOT NULL `+kind+` GROUP BY x.person_id)
		SELECT p.id, p.name, pc.n,
		       COALESCE((SELECT id FROM faces WHERE person_id = p.id AND thumb IS NOT NULL ORDER BY size * det_score DESC LIMIT 1), 0),
		       COALESCE((SELECT substr(MIN(f.taken_at), 1, 4) || '–' || substr(MAX(f.taken_at), 1, 4)
		                 FROM faces x JOIN files f ON f.id = x.file_id WHERE x.person_id = p.id), '')
		FROM persons p JOIN pc ON pc.pid = p.id WHERE pc.n >= ? AND p.hidden = ? ORDER BY pc.n DESC, p.id`, min, hidden)
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer rows.Close()
	needle := strings.ToLower(strings.TrimSpace(q.Get("q"))) // filtered here: SQLite's lower() is ASCII-only
	out := []Person{}
	for rows.Next() {
		var p Person
		var name sql.NullString
		if err := rows.Scan(&p.ID, &name, &p.N, &p.Face, &p.Years); err != nil {
			fail(w, err, 500)
			return
		}
		p.Name, p.Hidden = name.String, hidden == 1
		if needle != "" && !strings.Contains(strings.ToLower(p.Name), needle) && strconv.FormatInt(p.ID, 10) != needle {
			continue
		}
		out = append(out, p)
	}
	writeJSON(w, r, out)
}

// GET /api/persons/all: every person with faces (for "who is this" pickers): named first, then by files.
func (s *Server) personsAll(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Query(`SELECT id, name, n_files FROM persons WHERE n_faces > 0
		ORDER BY name IS NULL, lower(name), n_files DESC`)
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		var name sql.NullString
		if err := rows.Scan(&p.ID, &name, &p.N); err != nil {
			fail(w, err, 500)
			return
		}
		p.Name = name.String
		out = append(out, p)
	}
	writeJSON(w, r, out)
}

// PATCH /api/persons/{id} {name?, hidden?, skipCollect?}
func (s *Server) personPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Hidden      *bool   `json:"hidden"`
		SkipCollect *bool   `json:"skipCollect"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var err error
	if req.Name != nil {
		var v any // empty = unnamed
		if n := strings.TrimSpace(*req.Name); n != "" {
			v = n
		}
		_, err = s.DB.Exec("UPDATE persons SET name = ? WHERE id = ?", v, id)
	}
	if err == nil && req.Hidden != nil {
		_, err = s.DB.Exec("UPDATE persons SET hidden = ? WHERE id = ?", *req.Hidden, id)
	}
	if err == nil && req.SkipCollect != nil {
		_, err = s.DB.Exec("UPDATE persons SET skip_collect = ? WHERE id = ?", *req.SkipCollect, id)
	}
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, ActionResp{Message: "Сохранено"})
}

// POST /api/persons/merge {ids}: all of them are one person; the first one (most files) stays.
func (s *Server) personsMerge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) < 2 {
		http.Error(w, "нужно минимум два человека", http.StatusBadRequest)
		return
	}
	if err := core.MergePersons(s.DB, req.IDs); err != nil {
		fail(w, err, 500)
		return
	}
	s.similarCache.Store((*centroidCache)(nil))
	writeJSON(w, r, ActionResp{Message: fmt.Sprintf("Объединено: %d → #%d", len(req.IDs), req.IDs[0])})
}

// POST /api/persons/hide {ids, hidden}
func (s *Server) personsHide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Hidden bool    `json:"hidden"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 {
		http.Error(w, "некого скрывать", http.StatusBadRequest)
		return
	}
	if _, err := s.DB.Exec("UPDATE persons SET hidden = ? WHERE id IN ("+ph(len(req.IDs))+")",
		append([]any{req.Hidden}, anys(req.IDs)...)...); err != nil {
		fail(w, err, 500)
		return
	}
	verb := "Показаны"
	if req.Hidden {
		verb = "Скрыты"
	}
	writeJSON(w, r, ActionResp{Message: fmt.Sprintf("%s: %d", verb, len(req.IDs))})
}

// POST /api/persons {name}: a new person (an existing one with this name is reused).
func (s *Server) personCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "нужно имя", http.StatusBadRequest)
		return
	}
	id, err := core.CreatePerson(s.DB, strings.TrimSpace(req.Name))
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, map[string]int64{"id": id})
}

// PersonInfo: the person page header.
type PersonInfo struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Files       int      `json:"files"`
	Hidden      bool     `json:"hidden"`
	SkipCollect bool     `json:"skipCollect"`
	Co          []Person `json:"co"` // who appears together with them, most often first (N = files together)
}

// GET /api/person/{id}
func (s *Server) person(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var p PersonInfo
	var name sql.NullString
	err := s.DB.QueryRow("SELECT id, name, n_files, hidden, skip_collect FROM persons WHERE id = ?", id).
		Scan(&p.ID, &name, &p.Files, &p.Hidden, &p.SkipCollect)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "такого человека нет (возможно, его объединили с другим или пересчитали кластеры)", http.StatusNotFound)
		return
	} else if err != nil {
		fail(w, err, 500)
		return
	}
	p.Name = name.String
	rows, err := s.DB.Query(`SELECT p.id, p.name, COUNT(DISTINCT x.file_id) FROM faces x JOIN persons p ON p.id = x.person_id
		WHERE x.file_id IN (SELECT file_id FROM faces WHERE person_id = ?) AND p.id != ? GROUP BY p.id ORDER BY 3 DESC`, id, id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer rows.Close()
	p.Co = []Person{}
	for rows.Next() {
		var c Person
		var cn sql.NullString
		if rows.Scan(&c.ID, &cn, &c.N) == nil {
			c.Name = label(c.ID, cn)
			p.Co = append(p.Co, c)
		}
	}
	writeJSON(w, r, p)
}

var personModes = map[string]string{ // ?mode= -> HAVING
	"all":     "",
	"alone":   "HAVING foreign_faces = 0", // no other face at all, even strangers
	"noknown": "HAVING others = 0",        // strangers in the background allowed
	"with":    "HAVING others > 0",
}

// GET /api/person/{id}/files?mode=&with=1,2&whole=0 (+ video, moved): their files; whole=0 shows their face.
func (s *Server) personFiles(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	having, ok := personModes[q.Get("mode")]
	if !ok {
		having = ""
	}
	with := idList(q.Get("with"))
	cond := strings.Repeat(" AND f.id IN (SELECT file_id FROM faces WHERE person_id = ?)", len(with))
	face := "(SELECT t.id FROM faces t WHERE t.file_id = f.id AND t.person_id = fa.person_id ORDER BY t.size DESC LIMIT 1)"
	if q.Get("whole") != "0" {
		face = "0"
	}
	tags := "''"
	if s.hasTable("tags") {
		tags = "COALESCE((SELECT label FROM tags WHERE file_id = f.id AND rank = 1), '')"
	}
	items, err := s.queryItems(`SELECT id, path, kind, taken, place, tag, face FROM (
		SELECT f.id, f.path, f.kind, COALESCE(f.taken_at, '') AS taken, COALESCE(f.place, '') AS place, `+tags+` AS tag,
		       `+face+` AS face,
		       (SELECT COUNT(DISTINCT person_id) FROM faces WHERE file_id = f.id AND person_id IS NOT NULL AND person_id != fa.person_id) AS others,
		       (SELECT COUNT(*) FROM faces WHERE file_id = f.id AND (person_id IS NULL OR person_id != fa.person_id)) AS foreign_faces,
		       f.taken_at AS t0
		FROM faces fa JOIN files f ON f.id = fa.file_id
		WHERE fa.person_id = ?`+cond+` GROUP BY f.id `+having+`) ORDER BY t0, path`,
		append([]any{id}, anys(with)...)...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	s.finishList(w, r, items, FilesResp{}, false)
}

// GET /api/only?ids=1,2&all=1&strangers=1&whole=0: files where only the chosen people are in the frame.
// Candidates come from the faces index (files with any of them); the rules are checked here, per file.
func (s *Server) only(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	chosen := idList(q.Get("ids"))
	if len(chosen) == 0 {
		s.finishList(w, r, nil, FilesResp{}, false)
		return
	}
	isChosen := map[int64]bool{}
	for _, id := range chosen {
		isChosen[id] = true
	}
	cands, err := int64s(s.DB, "SELECT DISTINCT file_id FROM faces WHERE person_id IN ("+ph(len(chosen))+")", anys(chosen)...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	type stat struct {
		people         map[int64]bool
		others, strang bool
		face           int64
		size           float64
	}
	stats := map[int64]*stat{}
	for start := 0; start < len(cands); start += 500 {
		chunk := cands[start:min(start+500, len(cands))]
		rows, err := s.DB.Query("SELECT file_id, person_id, id, COALESCE(size, 0) FROM faces WHERE file_id IN ("+ph(len(chunk))+")", anys(chunk)...)
		if err != nil {
			fail(w, err, 500)
			return
		}
		for rows.Next() {
			var fid, face int64
			var pid sql.NullInt64
			var size float64
			if rows.Scan(&fid, &pid, &face, &size) != nil {
				continue
			}
			st := stats[fid]
			if st == nil {
				st = &stat{people: map[int64]bool{}}
				stats[fid] = st
			}
			switch {
			case !pid.Valid:
				st.strang = true
			case isChosen[pid.Int64]:
				st.people[pid.Int64] = true
			default:
				st.others = true
			}
			if size >= st.size {
				st.size, st.face = size, face
			}
		}
		rows.Close()
	}
	var keep []int64
	for fid, st := range stats {
		if st.others || (st.strang && q.Get("strangers") != "1") || (q.Get("all") == "1" && len(st.people) < len(chosen)) {
			continue
		}
		keep = append(keep, fid)
	}
	items := []Item{}
	for start := 0; start < len(keep); start += 500 {
		chunk := keep[start:min(start+500, len(keep))]
		part, err := s.queryItems(`SELECT id, path, kind, COALESCE(taken_at, ''), COALESCE(place, ''), '', 0
			FROM files WHERE id IN (`+ph(len(chunk))+`)`, anys(chunk)...)
		if err != nil {
			fail(w, err, 500)
			return
		}
		items = append(items, part...)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Taken != items[j].Taken {
			return items[i].Taken < items[j].Taken
		}
		return items[i].Path < items[j].Path
	})
	if q.Get("whole") == "0" {
		for i := range items {
			items[i].Face = stats[items[i].ID].face
		}
	}
	s.finishList(w, r, items, FilesResp{}, false)
}

// POST /api/person/{id}/unlink {files}: "this is not them" on these files (faces become nobody; Ctrl+Z).
func (s *Server) personUnlink(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	id, ok := fileID(r)
	var req struct {
		Files []int64 `json:"files"`
	}
	if !ok || readJSON(r, &req) != nil || len(req.Files) == 0 {
		http.Error(w, "нужны файлы", http.StatusBadRequest)
		return
	}
	faces, err := int64s(s.DB, "SELECT id FROM faces WHERE person_id = ? AND file_id IN ("+ph(len(req.Files))+")",
		append([]any{id}, anys(req.Files)...)...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	s.assignAndRespond(w, r, sid, faces, nil, fmt.Sprintf("Отвязано: %s", files(len(req.Files))), "отвязку лиц")
}

// POST /api/person/{id}/remap {to | new}: all their faces are really someone else (Ctrl+Z).
func (s *Server) personRemap(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	id, ok := fileID(r)
	var req struct {
		To  *int64 `json:"to"`
		New string `json:"new"`
	}
	if !ok || readJSON(r, &req) != nil || (req.To == nil && strings.TrimSpace(req.New) == "") {
		http.Error(w, "кому переносить?", http.StatusBadRequest)
		return
	}
	to := req.To
	if strings.TrimSpace(req.New) != "" {
		nid, err := core.CreatePerson(s.DB, strings.TrimSpace(req.New))
		if err != nil {
			fail(w, err, 500)
			return
		}
		to = &nid
	}
	faces, err := int64s(s.DB, "SELECT id FROM faces WHERE person_id = ?", id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	var toName sql.NullString
	_ = s.DB.QueryRow("SELECT name FROM persons WHERE id = ?", *to).Scan(&toName)
	s.assignAndRespond(w, r, sid, faces, to, fmt.Sprintf("Перенесено к «%s»: %d лиц", label(*to, toName), len(faces)), "перенос лиц")
}

func (s *Server) assignAndRespond(w http.ResponseWriter, r *http.Request, sid string, faces []int64, to *int64, msg, undo string) {
	before, err := core.AssignFaces(s.DB, faces, to)
	if err != nil {
		fail(w, err, 500)
		return
	}
	if len(before) > 0 {
		if err := core.PushUndo(s.DB, sid, "faces", undo, before); err != nil {
			fail(w, err, 500)
			return
		}
	}
	s.similarCache.Store((*centroidCache)(nil))
	resp := ActionResp{Message: msg, Undo: core.LastUndo(s.DB, sid)}
	if to != nil {
		resp.Person = *to
	}
	writeJSON(w, r, resp)
}

func int64s(d *sql.DB, query string, args ...any) ([]int64, error) {
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GET /api/faces?files=1,2: who is where on the selected files ("who is on the photo").
func (s *Server) faces(w http.ResponseWriter, r *http.Request) {
	units, err := core.FileFaces(s.DB, idList(r.URL.Query().Get("files")))
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, units)
}

// POST /api/faces/assign {changes: [{faces, person | null}]}: fix by hand; one Ctrl+Z step.
func (s *Server) facesAssign(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	var req struct {
		Changes []struct {
			Faces  []int64 `json:"faces"`
			Person *int64  `json:"person"`
		} `json:"changes"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Changes) == 0 {
		http.Error(w, "нечего менять", http.StatusBadRequest)
		return
	}
	var all []core.FaceBefore
	n := 0
	for _, c := range req.Changes {
		before, err := core.AssignFaces(s.DB, c.Faces, c.Person)
		if err != nil {
			fail(w, err, 500)
			return
		}
		all = append(all, before...)
		n += len(c.Faces)
	}
	if err := core.PushUndo(s.DB, sid, "faces", "правку лиц", all); err != nil {
		fail(w, err, 500)
		return
	}
	s.similarCache.Store((*centroidCache)(nil))
	writeJSON(w, r, ActionResp{Message: fmt.Sprintf("Исправлено: %d лиц", n), Undo: core.LastUndo(s.DB, sid)})
}

// ---------------------------------------------------------------- similar people

type SimilarPair struct {
	Sim float32 `json:"sim"`
	A   Person  `json:"a"`
	B   Person  `json:"b"`
	FA  []int64 `json:"facesA"` // a few best faces of each
	FB  []int64 `json:"facesB"`
}

type centroidCache struct {
	sig string
	c   map[int64][]float32
}

func (s *Server) centroids() (map[int64][]float32, error) {
	var n, sum int64
	_ = s.DB.QueryRow("SELECT COUNT(*), COALESCE(SUM(person_id), 0) FROM faces WHERE person_id IS NOT NULL").Scan(&n, &sum)
	sig := fmt.Sprint(n, ":", sum) // changes whenever faces / persons change
	if c, ok := s.similarCache.Load().(*centroidCache); ok && c != nil && c.sig == sig {
		return c.c, nil
	}
	s.simMu.Lock() // one computation at a time (a few seconds on a big archive)
	defer s.simMu.Unlock()
	c, err := core.Centroids(s.DB)
	if err != nil {
		return nil, err
	}
	s.similarCache.Store(&centroidCache{sig, c})
	return c, nil
}

// GET /api/similar?min=0.45&files=3: pairs of people with similar mean faces (often one person split in two).
func (s *Server) similar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	thr, err := strconv.ParseFloat(q.Get("min"), 32)
	if err != nil {
		thr = 0.45
	}
	minFiles, err := strconv.Atoi(q.Get("files"))
	if err != nil {
		minFiles = 3
	}
	cents, err := s.centroids()
	if err != nil {
		fail(w, err, 500)
		return
	}
	info := map[int64]Person{}
	rows, err := s.DB.Query("SELECT id, name, n_files, hidden FROM persons WHERE n_faces > 0")
	if err != nil {
		fail(w, err, 500)
		return
	}
	for rows.Next() {
		var p Person
		var name sql.NullString
		if rows.Scan(&p.ID, &name, &p.N, &p.Hidden) == nil {
			p.Name = name.String
			info[p.ID] = p
		}
	}
	rows.Close()
	notSame := map[[2]int64]bool{}
	rows, err = s.DB.Query("SELECT a, b FROM not_same")
	if err == nil {
		for rows.Next() {
			var a, b int64
			if rows.Scan(&a, &b) == nil {
				notSame[[2]int64{a, b}] = true
			}
		}
		rows.Close()
	}
	var ids []int64
	for id := range cents {
		if p, ok := info[id]; ok && p.N >= minFiles && !p.Hidden {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	pairs := []SimilarPair{}
	for i := 0; i < len(ids); i++ {
		ci := cents[ids[i]]
		for j := i + 1; j < len(ids); j++ {
			var dot float32
			cj := cents[ids[j]]
			for k := range ci {
				dot += ci[k] * cj[k]
			}
			if float64(dot) < thr || notSame[[2]int64{ids[i], ids[j]}] {
				continue
			}
			a, b := info[ids[i]], info[ids[j]]
			if a.N < b.N { // the bigger cluster first: it keeps its id on merge
				a, b = b, a
			}
			pairs = append(pairs, SimilarPair{Sim: dot, A: a, B: b})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Sim > pairs[j].Sim })
	if len(pairs) > 300 {
		pairs = pairs[:300]
	}
	for i := range pairs {
		pairs[i].FA, _ = int64s(s.DB, "SELECT id FROM faces WHERE person_id = ? AND thumb IS NOT NULL ORDER BY size * det_score DESC LIMIT 4", pairs[i].A.ID)
		pairs[i].FB, _ = int64s(s.DB, "SELECT id FROM faces WHERE person_id = ? AND thumb IS NOT NULL ORDER BY size * det_score DESC LIMIT 4", pairs[i].B.ID)
	}
	writeJSON(w, r, pairs)
}

// POST /api/notsame {a, b}: these two are different people (the pair leaves the similar page).
func (s *Server) notSame(w http.ResponseWriter, r *http.Request) {
	var req struct{ A, B int64 }
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a, b := min(req.A, req.B), max(req.A, req.B)
	if _, err := s.DB.Exec("INSERT OR IGNORE INTO not_same (a, b) VALUES (?, ?)", a, b); err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, ActionResp{Message: "Запомнил: это разные люди"})
}
