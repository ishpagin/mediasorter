package scan

import (
	"context"
	"database/sql"
	"math"
	"math/bits"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ishpagin/mediasorter/web/internal/media"
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

// similarity needed to stack two photos, by the time between them (seconds): one phone's burst changes
// little, the same scene from another device / after a while has to look much closer
var timeThresholds = [][2]float64{{2, 0.85}, {10, 0.88}, {60, 0.91}, {300, 0.93}, {2 * 3600, 0.95}}

const (
	copySim, copyHam         = 0.985, 4 // the network sees the same picture
	copyHashHam, copyHashSim = 2, 0.93  // or the dHash is (almost) equal and the proportions are the same
	maxLookback              = 400      // photos compared back in time
	anchorSim                = 0.92     // ...and to the first photo of the stack (keeps stacks tight)
)

// screenshots/documents look alike to the network but each one has its own text: only true copies stack
var noSeries = []string{"Скриншот", "Документы и текст", "Чеки и квитанции"}

var (
	goodPrompts = []string{"a sharp, well-composed, high quality photo", "a beautiful photo", "people smiling and looking at the camera"}
	badPrompts  = []string{"a blurry photo", "an out of focus photo", "a motion-blurred photo", "a dark underexposed photo",
		"an overexposed photo", "a person with closed eyes", "a photo taken by accident"}
)

func ham(a, b sql.NullInt64) int {
	if !a.Valid || !b.Valid {
		return 64
	}
	return bits.OnesCount64(uint64(a.Int64 ^ b.Int64))
}

// ts: seconds of a stored local time (the offset is ignored, like Python's naive fromisoformat).
func ts(s string) float64 {
	if len(s) > 19 {
		s = s[:19]
	}
	t, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.Local)
	if err != nil {
		return 0
	}
	return float64(t.Unix())
}

// BuildStacks recomputes the stacks (series of similar shots) and copy groups. Like stacks.py build().
func BuildStacks(db *sql.DB, progress Progress) (int, int, error) {
	fids, M, err := FileEmbeddings(db)
	if err != nil {
		return 0, 0, err
	}
	row := make(map[int64]int, len(fids))
	for i, f := range fids {
		row[f] = i
	}
	rows, err := db.Query(`SELECT id, taken_at, phash, CAST(width AS REAL) / MAX(height, 1), date_source = 'exif',
		width, height, size, lower(path) FROM files WHERE kind = 'image' AND error IS NULL AND taken_at IS NOT NULL
		ORDER BY taken_at, path`)
	if err != nil {
		return 0, 0, err
	}
	type shape struct {
		w, h   sql.NullInt64
		format string
		size   sql.NullInt64
	}
	var (
		ids []int64
		E   [][]float32
		T   []float64
		H   []sql.NullInt64
		A   []float64
		X   []bool
		F   []shape
	)
	for rows.Next() {
		var id int64
		var taken, path string
		var ph sql.NullInt64
		var aspect sql.NullFloat64
		var exif sql.NullBool
		var sh shape
		if err := rows.Scan(&id, &taken, &ph, &aspect, &exif, &sh.w, &sh.h, &sh.size, &path); err != nil {
			rows.Close()
			return 0, 0, err
		}
		r, ok := row[id]
		if !ok {
			continue
		}
		sh.format = strings.ReplaceAll(filepath.Ext(path), ".jpeg", ".jpg")
		ids, E, T, H, A, X, F = append(ids, id), append(E, M[r]), append(T, ts(taken)), append(H, ph),
			append(A, aspect.Float64), append(X, exif.Bool), append(F, sh)
	}
	rows.Close()
	n := len(ids)
	texty := map[int64]bool{}
	tr, err := db.Query("SELECT file_id FROM tags WHERE rank = 1 AND label IN (?,?,?)", noSeries[0], noSeries[1], noSeries[2])
	if err != nil {
		return 0, 0, err
	}
	for tr.Next() {
		var f int64
		tr.Scan(&f)
		texty[f] = true
	}
	tr.Close()
	seriesOK := make([]bool, n)
	for i, id := range ids {
		seriesOK[i] = !texty[id]
	}
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(i, j int) {
		a, b := find(i), find(j)
		if a != b {
			parent[max(a, b)] = min(a, b)
		}
	}

	// 1) shots close in time (any device). A photo joins the stack of its best match only if it also looks
	// like the stack's first photo: a long burst whose scene slowly changes becomes several moments
	window := timeThresholds[len(timeThresholds)-1][0]
	leader := make([]int, n)
	for i := range leader {
		leader[i] = i
	}
	for i := 1; i < n; i++ {
		lo := sort.SearchFloat64s(T[:i], T[i]-window) // T is sorted (taken_at order)
		lo = max(lo, i-maxLookback, 0)
		if lo == i || !seriesOK[i] {
			continue
		}
		type cand struct {
			j   int
			sim float32
			thr float32
		}
		cs := make([]cand, 0, i-lo)
		for j := lo; j < i; j++ {
			thr := float32(2)
			dt := T[i] - T[j]
			for k := len(timeThresholds) - 1; k >= 0; k-- {
				if dt <= timeThresholds[k][0] {
					thr = float32(timeThresholds[k][1])
				}
			}
			if !seriesOK[j] {
				thr = 2
			}
			cs = append(cs, cand{j, dot(E[j], E[i]), thr})
		}
		sort.SliceStable(cs, func(a, b int) bool { return cs[a].sim > cs[b].sim })
		for _, c := range cs {
			if c.sim < c.thr {
				break
			}
			first := leader[c.j]
			if dot(E[first], E[i]) >= anchorSim {
				leader[i] = first
				union(first, i)
				break
			}
		}
		if progress != nil && i%2000 == 0 {
			progress(i, 2*n, "stacks: series")
		}
	}

	// 2) copies anywhere (a photo sent back from a messenger years later, an edited copy...)
	otherShots := func(i, j int) bool { // look alike but are two different shots, not copies of one
		if X[i] && X[j] && math.Abs(T[i]-T[j]) >= 1 {
			return true // both dated by the camera, different moments: two frames of a burst
		}
		// same size and format but other bytes: another shot (a copy would be resized or converted)
		return F[i].w == F[j].w && F[i].h == F[j].h && F[i].format == F[j].format && F[i].size != F[j].size
	}
	type pair struct {
		i, j, ham int
		sim       float32
	}
	var mu sync.Mutex
	var pairs []pair
	parallel(n, func(i int) {
		for j := i + 1; j < n; j++ {
			s := dot(E[i], E[j])
			if s < copyHashSim {
				continue
			}
			h := ham(H[i], H[j])
			if otherShots(i, j) {
				continue
			}
			sameShape := math.Abs(A[i]-A[j]) <= 0.01*math.Max(A[i], A[j])
			if s >= copySim && h <= copyHam || h <= copyHashHam && sameShape {
				mu.Lock()
				pairs = append(pairs, pair{i, j, h, s})
				mu.Unlock()
			}
		}
	})
	sort.Slice(pairs, func(a, b int) bool {
		return pairs[a].i < pairs[b].i || pairs[a].i == pairs[b].i && pairs[a].j < pairs[b].j
	})
	copySet := map[[2]int]bool{}
	for _, p := range pairs {
		union(p.i, p.j)
		copySet[[2]int{p.i, p.j}] = true
	}
	groups := map[int][]int{}
	for i := 0; i < n; i++ {
		groups[find(i)] = append(groups[find(i)], i)
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM stacks"); err != nil {
		return 0, 0, err
	}
	nStacks, nPhotos := 0, 0
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		sid := ids[members[0]]
		for _, m := range members {
			sid = min(sid, ids[m])
		}
		copies := true // copies of one shot: every member is a near-identical copy of the first one
		for _, m := range members[1:] {
			a, b := min(members[0], m), max(members[0], m)
			if !copySet[[2]int{a, b}] && !(dot(E[members[0]], E[m]) >= 0.985 && ham(H[members[0]], H[m]) <= 6) {
				copies = false
				break
			}
		}
		for _, m := range members {
			if _, err := tx.Exec("INSERT INTO stacks (file_id, stack_id, copies) VALUES (?,?,?)", ids[m], sid, copies); err != nil {
				return 0, 0, err
			}
		}
		nStacks++
		nPhotos += len(members)
	}

	// copy groups on their own (only near-identical pairs): closest pairs first; two groups merge only if
	// no pair across them is "other shots" (no chains like frame A ~ RAW ~ frame B)
	sort.SliceStable(pairs, func(a, b int) bool {
		return pairs[a].ham < pairs[b].ham || pairs[a].ham == pairs[b].ham && pairs[a].sim > pairs[b].sim
	})
	group := map[int]*[]int{}
	get := func(i int) *[]int {
		if g, ok := group[i]; ok {
			return g
		}
		return &[]int{i}
	}
	for _, p := range pairs {
		gi, gj := get(p.i), get(p.j)
		if gi == gj {
			continue
		}
		conflict := false
		for _, a := range *gi {
			for _, b := range *gj {
				if otherShots(a, b) {
					conflict = true
				}
			}
		}
		if conflict {
			continue
		}
		*gi = append(*gi, *gj...)
		for _, k := range *gi {
			group[k] = gi
		}
	}
	if _, err := tx.Exec("DELETE FROM copies"); err != nil {
		return 0, 0, err
	}
	done := map[*[]int]bool{}
	for _, g := range group {
		if done[g] {
			continue
		}
		done[g] = true
		gid := ids[(*g)[0]]
		for _, m := range *g {
			gid = min(gid, ids[m])
		}
		for _, m := range *g {
			if _, err := tx.Exec("INSERT INTO copies (file_id, group_id) VALUES (?,?)", ids[m], gid); err != nil {
				return 0, 0, err
			}
		}
	}
	return nStacks, nPhotos, tx.Commit()
}

// detail: mean of the strongest 5% Laplacian responses: the sharpest parts decide (a blurred background is fine).
// OpenCV's Laplacian with ksize=3 is the kernel [2 0 2; 0 -8 0; 2 0 2], borders reflected (BORDER_REFLECT_101).
func detail(g []byte, w, h int) float64 {
	if w == 0 || h == 0 {
		return 0
	}
	at := func(x, y int) float32 {
		if x < 0 {
			x = -x
		} else if x >= w {
			x = 2*w - 2 - x
		}
		if y < 0 {
			y = -y
		} else if y >= h {
			y = 2*h - 2 - y
		}
		x, y = min(max(x, 0), w-1), min(max(y, 0), h-1)
		return float32(g[y*w+x])
	}
	lap := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 2*(at(x-1, y-1)+at(x+1, y-1)+at(x-1, y+1)+at(x+1, y+1)) - 8*at(x, y)
			if v < 0 {
				v = -v
			}
			lap[y*w+x] = v
		}
	}
	k := max(1, len(lap)/20)
	sort.Slice(lap, func(a, b int) bool { return lap[a] > lap[b] })
	var s float64
	for _, v := range lap[:k] {
		s += float64(v)
	}
	return s / float64(k)
}

// Measure: (sharp, face_sharp, exposure) of one picture; boxes are face boxes in its pixels.
func Measure(img *vision.RGB, boxes [][4]float64) (float64, *float64, float64) {
	w, h := img.W, img.H
	gray := img.Gray()
	small, sw, sh := gray, w, h
	if scale := 1600 / float64(max(w, h)); scale < 1 { // the same scale for every photo: devices differ
		sw, sh = int(math.RoundToEven(float64(w)*scale)), int(math.RoundToEven(float64(h)*scale))
		small = vision.ResizeAreaGray(gray, w, h, sw, sh)
	}
	sharp := detail(small, sw, sh)
	var hist [256]int
	var sum float64
	for _, v := range small {
		hist[v]++
		sum += float64(v)
	}
	dark := 0
	for v := 0; v < 6; v++ {
		dark += hist[v]
	}
	for v := 250; v < 256; v++ {
		dark += hist[v]
	}
	exposure := math.Min(1, float64(dark)/float64(len(small))) + math.Abs(sum/float64(len(small))/255-0.45)*0.5
	var vals, weights []float64
	for _, b := range boxes {
		x1, y1 := int(math.RoundToEven(math.Max(0, b[0]))), int(math.RoundToEven(math.Max(0, b[1])))
		x2, y2 := int(math.RoundToEven(math.Min(float64(w), b[2]))), int(math.RoundToEven(math.Min(float64(h), b[3])))
		if x2-x1 < 24 || y2-y1 < 24 {
			continue
		}
		crop := make([]byte, (x2-x1)*(y2-y1))
		for y := y1; y < y2; y++ {
			copy(crop[(y-y1)*(x2-x1):], gray[y*w+x1:y*w+x2])
		}
		vals = append(vals, detail(vision.ResizeAreaGray(crop, x2-x1, y2-y1, 128, 128), 128, 128))
		weights = append(weights, float64((x2-x1)*(y2-y1)))
	}
	if len(vals) == 0 {
		return sharp, nil, exposure
	}
	var s, ws float64
	for i := range vals {
		s += vals[i] * weights[i]
		ws += weights[i]
	}
	fs := s / ws
	return sharp, &fs, exposure
}

// Score: quality of every stacked photo that has none yet (reads the originals, writes only the DB).
func Score(ctx context.Context, db *sql.DB, text *vision.Clip, mapPath func(string) string, workers int, progress Progress) (int, error) {
	rows, err := db.Query(`SELECT f.id, f.path FROM stacks s JOIN files f ON f.id = s.file_id
		WHERE NOT EXISTS (SELECT 1 FROM quality q WHERE q.file_id = f.id)`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id   int64
		path string
	}
	var todo []item
	for rows.Next() {
		var it item
		rows.Scan(&it.id, &it.path)
		todo = append(todo, it)
	}
	rows.Close()
	if len(todo) == 0 {
		return 0, nil
	}
	aesthetic, err := aestheticScores(db, text)
	if err != nil {
		return 0, err
	}
	boxes := map[int64][][4]float64{}
	br, err := db.Query("SELECT file_id, x1, y1, x2, y2 FROM faces WHERE t_sec IS NULL")
	if err != nil {
		return 0, err
	}
	for br.Next() {
		var f int64
		var b [4]float64
		br.Scan(&f, &b[0], &b[1], &b[2], &b[3])
		boxes[f] = append(boxes[f], b)
	}
	br.Close()

	type res struct {
		id                    int64
		sharp, face, exposure any
		err                   any
	}
	jobs, out := make(chan item), make(chan res)
	var wg sync.WaitGroup
	for w := 0; w < max(1, workers); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				img, _, err := media.ReadImage(mapPath(it.path))
				if err != nil { // a broken file must not stop the rest
					msg := err.Error()
					if len(msg) > 200 {
						msg = msg[:200]
					}
					out <- res{id: it.id, err: msg}
					continue
				}
				s, f, e := Measure(img, boxes[it.id])
				r := res{id: it.id, sharp: s, exposure: e}
				if f != nil {
					r.face = *f
				}
				out <- r
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, it := range todo {
			select {
			case jobs <- it:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(out) }()
	n := 0
	for r := range out {
		var a any
		if v, ok := aesthetic[r.id]; ok {
			a = v
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO quality (file_id, sharp, face_sharp, exposure, aesthetic, error)
			VALUES (?,?,?,?,?,?)`, r.id, r.sharp, r.face, r.exposure, a, r.err); err != nil {
			return n, err
		}
		n++
		if n%100 == 0 {
			logf(progress, n, len(todo), "quality: %d of %d", n, len(todo))
		}
	}
	return n, ctx.Err()
}

// aestheticScores: SigLIP2 "good photo" minus "bad photo" prompts, per file.
func aestheticScores(db *sql.DB, text *vision.Clip) (map[int64]float64, error) {
	fids, M, err := FileEmbeddings(db)
	if err != nil {
		return nil, err
	}
	g, err := text.TextMean(goodPrompts)
	if err != nil {
		return nil, err
	}
	b, err := text.TextMean(badPrompts)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]float64, len(fids))
	for i, f := range fids {
		out[f] = float64(dot(M[i], g)) - float64(dot(M[i], b))
	}
	return out, nil
}
