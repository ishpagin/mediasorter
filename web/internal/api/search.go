package api

import (
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/ishpagin/mediasorter/web/internal/ml"
)

const clipDim = 768 // SigLIP2 ViT-B/16

// clipIndex: the mean (normalized) SigLIP2 vector of every file (videos average their previews), in memory.
type clipIndex struct {
	sig  string
	ids  []int64
	vecs []float32 // len(ids) * clipDim
}

func (s *Server) clip() (*clipIndex, error) {
	var n, last int64
	_ = s.DB.QueryRow("SELECT COUNT(*), COALESCE(MAX(rowid), 0) FROM clip").Scan(&n, &last)
	sig := fmt.Sprint(n, ":", last) // `scenes.py embed` adds rows; a deleted file takes its rows away
	if c, ok := s.clipCache.Load().(*clipIndex); ok && c != nil && c.sig == sig {
		return c, nil
	}
	s.clipMu.Lock()
	defer s.clipMu.Unlock()
	rows, err := s.DB.Query("SELECT file_id, emb FROM clip ORDER BY file_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := &clipIndex{sig: sig}
	var cur int64 = -1
	var acc []float32
	flush := func() {
		if cur < 0 {
			return
		}
		var norm float64
		for _, v := range acc {
			norm += float64(v) * float64(v)
		}
		norm = math.Sqrt(norm)
		for i := range acc {
			acc[i] = float32(float64(acc[i]) / norm)
		}
		idx.ids = append(idx.ids, cur)
		idx.vecs = append(idx.vecs, acc...)
	}
	for rows.Next() {
		var fid int64
		var emb []byte
		if err := rows.Scan(&fid, &emb); err != nil {
			return nil, err
		}
		if len(emb) != 2*clipDim {
			continue
		}
		if fid != cur {
			flush()
			cur, acc = fid, make([]float32, clipDim)
		}
		for i := 0; i < clipDim; i++ {
			acc[i] += f16(binary.LittleEndian.Uint16(emb[2*i:]))
		}
	}
	flush()
	s.clipCache.Store(idx)
	return idx, rows.Err()
}

// f16 decodes an IEEE 754 half-precision float.
func f16(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch {
	case exp == 0 && frac == 0:
		return math.Float32frombits(sign)
	case exp == 0:
		f := float32(frac) / 1024 / 16384
		if sign != 0 {
			return -f
		}
		return f
	case exp == 31:
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}

// GET /api/search?q=&nopeople=1: the files that best match a text (Russian or English), best first.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		s.finishList(w, r, nil, FilesResp{}, false)
		return
	}
	idx, err := s.clip()
	if err != nil {
		fail(w, err, 500)
		return
	}
	if len(idx.ids) == 0 {
		http.Error(w, "векторы картинок ещё не посчитаны (scenes.py embed)", http.StatusConflict)
		return
	}
	vec, err := s.ML.EmbedText(q)
	if err != nil {
		code := 500
		if err == ml.ErrUnavailable {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), code)
		return
	}
	type hit struct {
		id    int64
		score float32
	}
	hits := make([]hit, len(idx.ids))
	for i, id := range idx.ids {
		v := idx.vecs[i*clipDim : (i+1)*clipDim]
		var dot float32
		for k := 0; k < clipDim && k < len(vec); k++ {
			dot += v[k] * vec[k]
		}
		hits[i] = hit{id, dot}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > 600 {
		hits = hits[:600]
	}
	ids := make([]int64, len(hits))
	score := map[int64]float32{}
	for i, h := range hits {
		ids[i] = h.id
		score[h.id] = h.score
	}
	cond := ""
	if r.URL.Query().Get("nopeople") == "1" {
		cond = " AND n_faces = 0"
	}
	got, err := s.queryItems(`SELECT id, path, kind, COALESCE(taken_at, ''), COALESCE(place, ''), '', 0
		FROM files WHERE id IN (`+ph(len(ids))+`)`+cond, anys(ids)...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	sort.Slice(got, func(i, j int) bool { return score[got[i].ID] > score[got[j].ID] })
	for i := range got {
		got[i].Note = fmt.Sprintf("совпадение %.3f", score[got[i].ID])
	}
	s.finishList(w, r, got, FilesResp{}, false)
}

// GET /api/jobs: the long job on the Python side; POST {name}: start one.
func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	name := ""
	if r.Method == http.MethodPost {
		var req struct{ Name string }
		if err := readJSON(r, &req); err != nil || req.Name == "" {
			http.Error(w, "какую задачу?", http.StatusBadRequest)
			return
		}
		name = req.Name
	}
	j, err := s.ML.StartJob(name)
	if err != nil && name == "" { // just the status: no Python side, no job
		writeJSON(w, r, ml.Job{})
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, r, j)
}
