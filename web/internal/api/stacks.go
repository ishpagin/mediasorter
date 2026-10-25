package api

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
)

// GroupsResp: stacks / copies. Items carry Group and Head; Keep is the file each group keeps ("keep the best").
type GroupsResp struct {
	FilesResp
	Groups int             `json:"groups"`
	Extra  int             `json:"extra"` // files that "keep the best" would delete
	Bytes  int64           `json:"bytes"` // their size (copies)
	Todo   int             `json:"todo"`  // photos in stacks without a quality score yet
	Keep   map[int64]int64 `json:"keep"`  // group -> file to keep (absent: not scored yet)
	State  string          `json:"state"` // "" | "noclip" (no SigLIP vectors) | "empty" (nothing built yet)
}

type member struct {
	id                   int64
	path, kind           string
	taken, place, camera string
	pixels, size, w, h   float64
	exif                 bool
	sharp, faceSharp     *float64
	exposure, aesthetic  *float64
	score                *float64
	original, identical  bool
}

// z: standard scores; missing values count as the worst one (the minimum), like stacks._z
func z(v []*float64) []float64 {
	out := make([]float64, len(v))
	lo, any := math.Inf(1), false
	for _, x := range v {
		if x != nil {
			lo, any = math.Min(lo, *x), true
		}
	}
	if !any {
		return out
	}
	var mean float64
	for i, x := range v {
		if x != nil {
			out[i] = *x
		} else {
			out[i] = lo
		}
		mean += out[i]
	}
	mean /= float64(len(v))
	var sd float64
	for _, x := range out {
		sd += (x - mean) * (x - mean)
	}
	sd = math.Sqrt(sd / float64(len(v)))
	for i := range out {
		if sd > 1e-9 {
			out[i] = (out[i] - mean) / sd
		} else {
			out[i] = 0
		}
	}
	return out
}

func vals(ms []*member, f func(*member) *float64) []*float64 {
	out := make([]*float64, len(ms))
	for i, m := range ms {
		out[i] = f(m)
	}
	return out
}

func ptr(v float64) *float64 { return &v }

// rankStack: 0..100 relative to the stack, the best gets 100 (stacks.rank_stack).
func rankStack(ms []*member, copies bool) {
	n := len(ms)
	s := make([]float64, n)
	add := func(w float64, zs []float64) {
		for i := range s {
			s[i] += w * zs[i]
		}
	}
	px := z(vals(ms, func(m *member) *float64 { return ptr(m.pixels) }))
	sharp := z(vals(ms, func(m *member) *float64 { return m.sharp }))
	if copies { // the same shot: keep the biggest, least compressed original
		add(0.5, px)
		add(0.3, z(vals(ms, func(m *member) *float64 { return ptr(m.size) })))
		add(0.2, z(vals(ms, func(m *member) *float64 {
			if m.exif {
				return ptr(1)
			}
			return ptr(0)
		})))
		add(0.1, sharp)
	} else {
		hasFaces := false
		for _, m := range ms {
			hasFaces = hasFaces || m.faceSharp != nil
		}
		if hasFaces {
			add(0.35, z(vals(ms, func(m *member) *float64 { return m.faceSharp })))
			add(0.25, sharp)
		} else {
			add(0.45, sharp)
		}
		add(0.25, z(vals(ms, func(m *member) *float64 { return m.aesthetic })))
		add(-0.15, z(vals(ms, func(m *member) *float64 { return m.exposure })))
		add(0.05, px)
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range s {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	for i, m := range ms {
		v := 100.0
		if hi > lo {
			v = (s[i] - lo) / (hi - lo) * 100
		}
		m.score = ptr(v)
	}
}

func (s *Server) yearsOf(table string) []string {
	ys, _ := strings1(s.DB, "SELECT DISTINCT substr(f.taken_at, 1, 4) FROM "+table+" t JOIN files f ON f.id = t.file_id "+
		"WHERE f.taken_at IS NOT NULL ORDER BY 1 DESC")
	return ys
}

func when(ms []*member) string { // the group's first shot
	w := "9999"
	for _, m := range ms {
		t := m.taken
		if t == "" {
			t = "9999"
		}
		if t < w {
			w = t
		}
	}
	return w
}

func (s *Server) writeGroups(w http.ResponseWriter, r *http.Request, order []int64, groups map[int64][]*member,
	head func(int64, []*member) string, note func(int, *member) string, resp GroupsResp) {
	var items []Item
	for _, gid := range order {
		h := head(gid, groups[gid])
		for rank, m := range groups[gid] {
			items = append(items, Item{ID: m.id, Path: m.path, Kind: m.kind, Taken: m.taken, Place: m.place,
				Group: gid, Head: h, Note: note(rank, m)})
		}
	}
	var err error
	if resp.Items, resp.HiddenVideo, resp.HiddenMoved, err = s.filterList(r, items, false); err != nil {
		fail(w, err, 500)
		return
	}
	if resp.Cats == nil {
		resp.Cats = []Count{}
	}
	if resp.Keep == nil {
		resp.Keep = map[int64]int64{}
	}
	writeJSON(w, r, resp)
}

// GET /api/stacks?sort=new|old|big&year=: series of similar shots, the best one first in each.
func (s *Server) stacks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var resp GroupsResp
	if !s.hasTable("clip") || !s.hasTable("stacks") {
		resp.State = "noclip"
		s.writeGroups(w, r, nil, nil, nil, nil, resp)
		return
	}
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM stacks s WHERE NOT EXISTS (SELECT 1 FROM quality q WHERE q.file_id = s.file_id)`).Scan(&resp.Todo)
	resp.Years = s.yearsOf("stacks")
	where, args := "WHERE s.copies = 0", []any{}
	if y := q.Get("year"); y != "" {
		where += " AND substr(f.taken_at, 1, 4) = ?"
		args = append(args, y)
	}
	rows, err := s.DB.Query(`SELECT s.stack_id, f.id, f.path, f.kind, COALESCE(f.taken_at, ''), COALESCE(f.place, ''),
		COALESCE(f.width * f.height, 0), COALESCE(f.size, 0), f.date_source = 'exif', q.sharp, q.face_sharp, q.exposure,
		q.aesthetic, COALESCE(f.camera_model, '')
		FROM stacks s JOIN files f ON f.id = s.file_id LEFT JOIN quality q ON q.file_id = f.id `+where+` ORDER BY f.taken_at, f.path`, args...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	groups := map[int64][]*member{}
	var order []int64
	for rows.Next() {
		var sid int64
		var exif sql.NullBool
		m := &member{}
		if err := rows.Scan(&sid, &m.id, &m.path, &m.kind, &m.taken, &m.place, &m.pixels, &m.size, &exif,
			&m.sharp, &m.faceSharp, &m.exposure, &m.aesthetic, &m.camera); err != nil {
			rows.Close()
			fail(w, err, 500)
			return
		}
		m.exif = exif.Bool
		if _, ok := groups[sid]; !ok {
			order = append(order, sid)
		}
		groups[sid] = append(groups[sid], m)
	}
	rows.Close()
	resp.Keep = map[int64]int64{}
	kept := order[:0]
	for _, sid := range order {
		ms := groups[sid]
		if len(ms) < 2 { // the rest was deleted
			continue
		}
		scored := true
		for _, m := range ms {
			scored = scored && m.sharp != nil
		}
		if scored {
			rankStack(ms, false)
			sort.SliceStable(ms, func(i, j int) bool { return *ms[i].score > *ms[j].score })
			resp.Keep[sid] = ms[0].id
			resp.Extra += len(ms) - 1
		}
		kept = append(kept, sid)
	}
	order = kept
	resp.Groups = len(order)
	switch q.Get("sort") {
	case "old":
		sort.SliceStable(order, func(i, j int) bool { return when(groups[order[i]]) < when(groups[order[j]]) })
	case "big": // most to clean up first; same size: newest first
		sort.SliceStable(order, func(i, j int) bool {
			a, b := groups[order[i]], groups[order[j]]
			if len(a) != len(b) {
				return len(a) > len(b)
			}
			return when(a) > when(b)
		})
	default:
		sort.SliceStable(order, func(i, j int) bool { return when(groups[order[i]]) > when(groups[order[j]]) })
	}
	if resp.Groups == 0 && q.Get("year") == "" {
		resp.State = "empty"
	}
	head := func(_ int64, ms []*member) string {
		cams := map[string]bool{}
		for _, m := range ms {
			if m.camera != "" {
				cams[m.camera] = true
			}
		}
		h := fmt.Sprintf("%s · %d фото · серия", strings.Replace(clip16(ms[0].taken), "T", " ", 1), len(ms))
		if len(cams) > 1 {
			names := make([]string, 0, len(cams))
			for c := range cams {
				names = append(names, c)
			}
			sort.Strings(names)
			h += " · " + strings.Join(names, ", ")
		}
		return h
	}
	note := func(rank int, m *member) string {
		if m.score == nil {
			return "оценка не посчитана"
		}
		bits := []string{}
		if m.sharp != nil {
			bits = append(bits, fmt.Sprintf("резкость %.0f", *m.sharp))
		}
		if m.faceSharp != nil {
			bits = append(bits, fmt.Sprintf("лица %.0f", *m.faceSharp))
		}
		h := fmt.Sprintf("#%d · %.0f", rank+1, *m.score)
		if rank == 0 {
			h = "★ лучший"
		}
		if len(bits) > 0 {
			h += " · " + strings.Join(bits, " · ")
		}
		return h
	}
	s.writeGroups(w, r, order, groups, head, note, resp)
}

func clip16(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

var originalExt = map[string]bool{".heic": true, ".heif": true, ".cr3": true, ".cr2": true, ".nef": true, ".arw": true,
	".dng": true, ".raf": true, ".orf": true, ".rw2": true}

// GET /api/copies?kind=all|same|diff&keep=jpeg|original&year=: copies of one shot; the one to keep first.
func (s *Server) copies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var resp GroupsResp
	if !s.hasTable("clip") || !s.hasTable("copies") {
		resp.State = "noclip"
		s.writeGroups(w, r, nil, nil, nil, nil, resp)
		return
	}
	resp.Years = s.yearsOf("copies")
	where, args := "", []any{}
	if y := q.Get("year"); y != "" {
		where = "WHERE substr(f.taken_at, 1, 4) = ?"
		args = append(args, y)
	}
	preferOriginal := q.Get("keep") == "original"
	rows, err := s.DB.Query(`SELECT c.group_id, f.id, f.path, f.kind, COALESCE(f.taken_at, ''), COALESCE(f.place, ''),
		COALESCE(f.width, 0), COALESCE(f.height, 0), COALESCE(f.size, 0), f.date_source = 'exif'
		FROM copies c JOIN files f ON f.id = c.file_id `+where+` ORDER BY f.taken_at, f.path`, args...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	groups := map[int64][]*member{}
	var order []int64
	for rows.Next() {
		var gid int64
		var exif sql.NullBool
		m := &member{}
		if err := rows.Scan(&gid, &m.id, &m.path, &m.kind, &m.taken, &m.place, &m.w, &m.h, &m.size, &exif); err != nil {
			rows.Close()
			fail(w, err, 500)
			return
		}
		m.exif, m.original = exif.Bool, originalExt[strings.ToLower(filepath.Ext(m.path))]
		if _, ok := groups[gid]; !ok {
			order = append(order, gid)
		}
		groups[gid] = append(groups[gid], m)
	}
	rows.Close()
	kept := order[:0]
	resp.Keep = map[int64]int64{}
	for _, gid := range order {
		ms := groups[gid]
		if len(ms) < 2 {
			continue
		}
		type key struct{ w, h, size float64 }
		sizes := map[key]bool{}
		for _, m := range ms {
			sizes[key{m.w, m.h, m.size}] = true
		}
		identical := len(sizes) == 1
		if (q.Get("kind") == "same" && !identical) || (q.Get("kind") == "diff" && identical) {
			continue
		}
		for _, m := range ms {
			m.identical = identical
		}
		// the keeper: the biggest picture; same size — JPEG or the original (keep=); then with EXIF, the biggest
		// file (least compressed), the shortest name (not "X(0).jpg")
		sort.SliceStable(ms, func(i, j int) bool {
			a, b := ms[i], ms[j]
			if a.w*a.h != b.w*b.h {
				return a.w*a.h > b.w*b.h
			}
			if (a.original == preferOriginal) != (b.original == preferOriginal) {
				return a.original == preferOriginal
			}
			if a.exif != b.exif {
				return a.exif
			}
			if a.size != b.size {
				return a.size > b.size
			}
			if len(filepath.Base(a.path)) != len(filepath.Base(b.path)) {
				return len(filepath.Base(a.path)) < len(filepath.Base(b.path))
			}
			return a.path < b.path
		})
		resp.Keep[gid] = ms[0].id
		resp.Extra += len(ms) - 1
		for _, m := range ms[1:] {
			resp.Bytes += int64(m.size)
		}
		kept = append(kept, gid)
	}
	order = kept
	resp.Groups = len(order)
	if resp.Groups == 0 && q.Get("year") == "" && q.Get("kind") == "" {
		resp.State = "empty"
	}
	head := func(_ int64, ms []*member) string {
		h := fmt.Sprintf("%s · %d копии", strings.Replace(clip16(ms[0].taken), "T", " ", 1), len(ms))
		if ms[0].identical {
			return h + " · одинаковые файлы"
		}
		sz := map[string]bool{}
		var list []string
		for _, m := range ms {
			k := fmt.Sprintf("%.0f×%.0f", m.w, m.h)
			if !sz[k] {
				sz[k] = true
				list = append(list, k)
			}
		}
		sort.Strings(list)
		return h + " · " + strings.Join(list, ", ")
	}
	note := func(rank int, m *member) string {
		h := "копия"
		if rank == 0 {
			h = "★ оставить"
		}
		return fmt.Sprintf("%s · %.0f×%.0f · %.1f МБ · %s", h, m.w, m.h, m.size/(1<<20),
			strings.ToUpper(strings.TrimPrefix(filepath.Ext(m.path), ".")))
	}
	s.writeGroups(w, r, order, groups, head, note, resp)
}
