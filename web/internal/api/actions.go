package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ishpagin/mediasorter/web/internal/core"
)

// session: a random id in a cookie; the undo history is kept per session.
func session(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("ms_sid"); err == nil && len(c.Value) == 32 {
		return c.Value
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	sid := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: "ms_sid", Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 365 * 24 * 3600})
	return sid
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(v)
}

// ActionResp: the result of an action, shown as a toast.
type ActionResp struct {
	Message string         `json:"message"`
	Failed  []core.Failure `json:"failed"`
	Undo    string         `json:"undo"` // what Ctrl+Z undoes now
	Coll    *Collection    `json:"collection,omitempty"`
	Person  int64          `json:"person,omitempty"` // where the faces went (remap)
}

func (s *Server) paths(ids []int64) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		var p string
		if err := s.DB.QueryRow("SELECT path FROM files WHERE id = ?", id).Scan(&p); err == nil {
			out = append(out, p) // ids already gone (deleted meanwhile) are skipped
		}
	}
	return out, nil
}

func plural(n int, one, few, many string) string {
	n10, n100 := n%10, n%100
	switch {
	case n10 == 1 && n100 != 11:
		return one
	case n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14):
		return few
	}
	return many
}

func files(n int) string {
	return fmt.Sprintf("%d %s", n, plural(n, "файл", "файла", "файлов"))
}

// POST /api/delete {ids}: to the quarantine at once; Ctrl+Z brings them back.
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths, _ := s.paths(req.IDs)
	done, failed, logIDs, err := s.Cfg.Trash(s.DB, paths)
	var pe core.ErrProtected
	if errors.As(err, &pe) {
		http.Error(w, pe.Error(), http.StatusForbidden)
		return
	} else if err != nil {
		fail(w, err, 500)
		return
	}
	name := core.Names(paths)
	if len(logIDs) > 0 {
		if err := core.PushUndo(s.DB, sid, "delete", "удаление: "+name, logIDs); err != nil {
			fail(w, err, 500)
			return
		}
	}
	msg := "Удалено: " + name
	if done != len(paths) {
		msg = fmt.Sprintf("Удалено: %s из %d", files(done), len(paths))
	}
	writeJSON(w, r, ActionResp{Message: msg, Failed: failed, Undo: core.LastUndo(s.DB, sid)})
}

// POST /api/undo: undo the last step of this browser; GET: what it would undo.
func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	if r.Method == http.MethodGet {
		writeJSON(w, r, ActionResp{Undo: core.LastUndo(s.DB, sid)})
		return
	}
	msg, problems, err := s.Cfg.Undo(s.DB, sid)
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, r, ActionResp{Message: msg, Failed: problems, Undo: core.LastUndo(s.DB, sid)})
}

// GET /api/collect/people?ids=1,2: named people on the selected files, most frequent first (the "to collection" dialog).
func (s *Server) collectPeople(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query().Get("ids")
	args := []any{}
	for _, x := range strings.Split(ids, ",") {
		if x != "" {
			args = append(args, x)
		}
	}
	type person struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	out := []person{}
	if len(args) > 0 {
		rows, err := s.DB.Query(`SELECT p.id, p.name, COUNT(DISTINCT x.file_id) FROM faces x JOIN persons p ON p.id = x.person_id
			WHERE x.file_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`) AND p.name IS NOT NULL
			GROUP BY p.id ORDER BY 3 DESC, lower(p.name)`, args...)
		if err != nil {
			fail(w, err, 500)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var p person
			if err := rows.Scan(&p.ID, &p.Name, &p.N); err == nil {
				out = append(out, p)
			}
		}
	}
	writeJSON(w, r, out)
}

// POST /api/collections {name}: a new collection, or the existing one with that name.
func (s *Server) newCollection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "нужно название", http.StatusBadRequest)
		return
	}
	c, existed, err := s.collectionByName(strings.TrimSpace(req.Name))
	if err != nil {
		fail(w, err, 500)
		return
	}
	msg := fmt.Sprintf("Подборка «%s» создана", c.Name)
	if existed {
		msg = fmt.Sprintf("Подборка «%s» уже есть", c.Name)
	}
	writeJSON(w, r, ActionResp{Message: msg, Coll: c})
}

func (s *Server) collectionByName(name string) (*Collection, bool, error) {
	var id int64
	if err := s.DB.QueryRow("SELECT id FROM collections WHERE name = ?", name).Scan(&id); err == nil {
		return &Collection{id, name}, true, nil
	}
	id, err := core.CreateCollection(s.DB, name, nil)
	return &Collection{id, name}, false, err
}

// POST /api/collect {ids, copy, collection | person | new}: move or copy files into a collection
// (no target: a new "Подборка <date>"). Ctrl+Z takes them out again.
func (s *Server) collect(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	var req struct {
		IDs        []int64 `json:"ids"`
		Copy       bool    `json:"copy"`
		Collection *int64  `json:"collection"`
		Person     *int64  `json:"person"`
		New        string  `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var c *Collection
	var err error
	switch {
	case req.Person != nil:
		var cid int64
		if cid, err = core.CollectionForPerson(s.DB, *req.Person); err == nil {
			c = &Collection{ID: cid}
		}
	case strings.TrimSpace(req.New) != "":
		c, _, err = s.collectionByName(strings.TrimSpace(req.New))
	case req.Collection != nil:
		c = &Collection{ID: *req.Collection}
	default:
		c, _, err = s.collectionByName("Подборка " + time.Now().Format("2006-01-02 15-04"))
	}
	if err == nil {
		err = s.DB.QueryRow("SELECT name FROM collections WHERE id = ?", c.ID).Scan(&c.Name)
	}
	if err != nil {
		fail(w, err, 500)
		return
	}
	done, failed, added := s.Cfg.AddFiles(s.DB, c.ID, req.IDs, req.Copy)
	verb := "Перемещено"
	if req.Copy {
		verb = "Скопировано"
	}
	if len(added) > 0 {
		if err := core.PushUndo(s.DB, sid, "collect", fmt.Sprintf("%s в «%s»", strings.ToLower(verb), c.Name), added); err != nil {
			fail(w, err, 500)
			return
		}
	}
	writeJSON(w, r, ActionResp{Message: fmt.Sprintf("%s в «%s»: %s", verb, c.Name, files(done)), Failed: failed,
		Undo: core.LastUndo(s.DB, sid), Coll: c})
}

// POST /api/collect/by-people {ids, copy}: each file into the collections of all named people on it.
func (s *Server) collectByPeople(w http.ResponseWriter, r *http.Request) {
	sid := session(w, r)
	var req struct {
		IDs  []int64 `json:"ids"`
		Copy bool    `json:"copy"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	per, nobody, failed, added := s.Cfg.ByPeople(s.DB, req.IDs, req.Copy)
	if len(added) > 0 {
		if err := core.PushUndo(s.DB, sid, "collect", "раскладку по людям", added); err != nil {
			fail(w, err, 500)
			return
		}
	}
	names := make([]string, 0, len(per))
	for n := range per {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return per[names[i]] > per[names[j]] })
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s: %d", n, per[n])
	}
	msg := "Разложено по людям: " + strings.Join(parts, ", ")
	if len(parts) == 0 {
		msg = "Никуда не разложено"
	}
	if len(nobody) > 0 {
		msg += fmt.Sprintf(" · без подписанных людей: %s", files(len(nobody)))
	}
	writeJSON(w, r, ActionResp{Message: msg, Failed: failed, Undo: core.LastUndo(s.DB, sid)})
}
