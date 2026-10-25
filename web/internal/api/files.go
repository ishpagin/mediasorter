package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

// Item is one file in a list; the UI builds its caption from these fields.
type Item struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Taken string `json:"taken,omitempty"`
	Place string `json:"place,omitempty"`
	Who   string `json:"who,omitempty"`  // named people on it, "+N" = other faces
	Coll  string `json:"coll,omitempty"` // collections it is in
	Tag   string `json:"tag,omitempty"`  // top scene category
	Face  int64  `json:"face,omitempty"` // show this face instead of the whole frame
	Group int64  `json:"group,omitempty"`
	Head  string `json:"head,omitempty"` // group header (stacks, copies), on the first item of a group
	Note  string `json:"note,omitempty"` // a page-specific first caption line (rank in a stack, search score...)
}

type Count struct {
	Label string `json:"label"`
	N     int    `json:"n"`
}

type FilesResp struct {
	Items       []Item   `json:"items"`
	Years       []string `json:"years"`
	Cats        []Count  `json:"cats"`
	HiddenVideo int      `json:"hiddenVideo"`
	HiddenMoved int      `json:"hiddenMoved"`
}

// views of the files pages: SQL view + only videos
var views = map[string]struct {
	table     string
	onlyVideo bool
}{
	"files":     {"files", false},
	"nopeople":  {"files_without_people", false},
	"strangers": {"files_strangers_only", false},
	"videos":    {"files", true},
}

// files: GET /api/files?view=files|nopeople|strangers|videos&year=&cat=&place=&video=1&moved=1
// video=1 keeps videos in the list (always on the videos page); moved=1 keeps files moved into collections.
func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, ok := views[q.Get("view")]
	if !ok {
		http.Error(w, "unknown view", http.StatusBadRequest)
		return
	}
	kindCond := "1"
	if v.onlyVideo {
		kindCond = "kind = 'video'"
	}
	var resp FilesResp
	var err error
	if resp.Years, err = strings1(s.DB, fmt.Sprintf(
		"SELECT DISTINCT substr(taken_at, 1, 4) FROM %s WHERE %s AND taken_at IS NOT NULL ORDER BY 1 DESC", v.table, kindCond)); err != nil {
		fail(w, err, 500)
		return
	}
	tags := s.hasTable("tags") // scene categories exist after `scenes.py tag`; a fresh archive has none yet
	resp.Cats = []Count{}
	if tags {
		rows, err := s.DB.Query(fmt.Sprintf(`SELECT label, COUNT(*) FROM tags WHERE rank = 1
			AND file_id IN (SELECT id FROM %s WHERE %s) GROUP BY label ORDER BY 2 DESC`, v.table, kindCond))
		if err != nil {
			fail(w, err, 500)
			return
		}
		for rows.Next() {
			var c Count
			if err := rows.Scan(&c.Label, &c.N); err != nil {
				rows.Close()
				fail(w, err, 500)
				return
			}
			resp.Cats = append(resp.Cats, c)
		}
		rows.Close()
	}

	where, args := []string{kindCond}, []any{}
	if c := q.Get("cat"); c != "" && tags {
		where = append(where, "id IN (SELECT file_id FROM tags WHERE rank = 1 AND label = ?)")
		args = append(args, c)
	}
	if y := q.Get("year"); y != "" {
		where = append(where, "substr(taken_at, 1, 4) = ?")
		args = append(args, y)
	}
	if p := q.Get("place"); p != "" {
		where = append(where, "lower(place) LIKE lower(?)")
		args = append(args, "%"+p+"%")
	}
	tagCol := "''"
	if tags {
		tagCol = "COALESCE((SELECT label FROM tags WHERE file_id = v.id AND rank = 1), '')"
	}
	items, err := s.queryItems(fmt.Sprintf(`SELECT id, path, kind, COALESCE(taken_at, ''), COALESCE(place, ''), %s, 0
		FROM %s v WHERE %s ORDER BY taken_at, path`, tagCol, v.table, strings.Join(where, " AND ")), args...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	s.finishList(w, r, items, resp, v.onlyVideo)
}

// queryItems runs a list query with the columns: id, path, kind, taken_at, place, tag, face (0 = whole frame).
func (s *Server) queryItems(query string, args ...any) ([]Item, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Path, &it.Kind, &it.Taken, &it.Place, &it.Tag, &it.Face); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// filterList applies the sidebar filters (videos off, files moved into collections hidden) and fills names,
// people and collections. alwaysVideo: the videos page and pages that never hide them.
func (s *Server) filterList(r *http.Request, items []Item, alwaysVideo bool) (out []Item, hiddenVideo, hiddenMoved int, err error) {
	moved, err := s.movedToCollection()
	if err != nil {
		return nil, 0, 0, err
	}
	keepVideo := alwaysVideo || r.URL.Query().Get("video") == "1"
	keepMoved := r.URL.Query().Get("moved") == "1"
	out = []Item{}
	for _, it := range items {
		if !keepVideo && it.Kind == "video" {
			hiddenVideo++
			continue
		}
		if !keepMoved && moved[it.ID] {
			hiddenMoved++
			continue
		}
		out = append(out, it)
	}
	return out, hiddenVideo, hiddenMoved, s.decorate(out)
}

// finishList filters the list (filterList) and writes it.
func (s *Server) finishList(w http.ResponseWriter, r *http.Request, items []Item, resp FilesResp, alwaysVideo bool) {
	var err error
	if resp.Items, resp.HiddenVideo, resp.HiddenMoved, err = s.filterList(r, items, alwaysVideo); err != nil {
		fail(w, err, 500)
		return
	}
	if resp.Years == nil {
		resp.Years = []string{}
	}
	if resp.Cats == nil {
		resp.Cats = []Count{}
	}
	writeJSON(w, r, resp)
}

// decorate fills Name, Who and Coll.
func (s *Server) decorate(items []Item) error {
	who, err := s.peopleIn()
	if err != nil {
		return err
	}
	coll, err := s.inCollection()
	if err != nil {
		return err
	}
	for i := range items {
		it := &items[i]
		it.Name = filepath.Base(it.Path)
		it.Who = who[it.ID]
		it.Coll = coll[it.ID]
	}
	return nil
}

// peopleIn: {file id: "Петя, Вася, +2"}: named people, +N = other faces (in videos only grouped ones:
// a video has many stray faces). Same rule as the Python UI.
func (s *Server) peopleIn() (map[int64]string, error) {
	rows, err := s.DB.Query(`
		SELECT g.file_id, COALESCE(group_concat(g.name, ', '), ''), SUM(g.name IS NULL)
		FROM (SELECT x.file_id, p.name FROM faces x JOIN files f ON f.id = x.file_id
		      LEFT JOIN persons p ON p.id = x.person_id
		      WHERE x.person_id IS NOT NULL OR f.kind != 'video'
		      GROUP BY x.file_id, COALESCE(x.person_id, -x.id)) g
		GROUP BY g.file_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var names string
		var n int
		if err := rows.Scan(&id, &names, &n); err != nil {
			return nil, err
		}
		parts := []string{}
		if names != "" {
			parts = append(parts, names)
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("+%d", n))
		}
		out[id] = strings.Join(parts, ", ")
	}
	return out, rows.Err()
}

func (s *Server) inCollection() (map[int64]string, error) {
	rows, err := s.DB.Query(`SELECT c.file_id, group_concat(k.name, ', ') FROM collection_files c
		JOIN collections k ON k.id = c.collection_id GROUP BY c.file_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var names string
		if err := rows.Scan(&id, &names); err != nil {
			return nil, err
		}
		out[id] = names
	}
	return out, rows.Err()
}

// movedToCollection: files moved out of the archive into a collection (copies stay in the archive: not here).
func (s *Server) movedToCollection() (map[int64]bool, error) {
	rows, err := s.DB.Query(`SELECT f.id FROM files f JOIN collection_files c ON c.file_id = f.id AND c.link_path = f.path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func strings1(d *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v.Valid && v.String != "" {
			out = append(out, v.String)
		}
	}
	return out, rows.Err()
}

func (s *Server) hasTable(name string) bool {
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name = ?", name).Scan(&n)
	return n > 0
}
