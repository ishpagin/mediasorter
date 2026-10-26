package scan

import (
	"database/sql"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"sync"
)

// ClusterOptions: the Python defaults.
type ClusterOptions struct {
	Edge     float32 // 0.45: min similarity for two faces to be linked
	Merge    float32 // 0.55: merge clusters with centroid similarity above this
	Assign   float32 // 0.45: attach leftover faces to a person above this
	K        int     // 30 neighbours per face
	CoreSize float64 // 40 px: min face size used to build clusters
	CoreDet  float64 // 0.65: min detector score used to build clusters
	MinFiles int     // 2: a person must appear in at least this many files
}

func DefaultClusterOptions() ClusterOptions {
	return ClusterOptions{Edge: 0.45, Merge: 0.55, Assign: 0.45, K: 30, CoreSize: 40, CoreDet: 0.65, MinFiles: 2}
}

// Cluster groups faces into people (no reference photos needed), keeping person ids and names stable
// and faces assigned by hand as they are. Like Python's cmd_cluster. Returns the number of people.
func Cluster(db *sql.DB, o ClusterOptions, progress Progress) (int, error) {
	rows, err := db.Query("SELECT id, file_id, person_id, size, det_score, embedding, manual FROM faces ORDER BY id")
	if err != nil {
		return 0, err
	}
	var (
		faceIDs, fileIDs, oldPID []int64
		size, det                []float64
		manual                   []bool
		E                        [][]float32
	)
	for rows.Next() {
		var id, file int64
		var pid sql.NullInt64
		var sz, ds sql.NullFloat64
		var emb []byte
		var man bool
		if err := rows.Scan(&id, &file, &pid, &sz, &ds, &emb, &man); err != nil {
			rows.Close()
			return 0, err
		}
		faceIDs, fileIDs = append(faceIDs, id), append(fileIDs, file)
		p := int64(-1)
		if pid.Valid {
			p = pid.Int64
		}
		oldPID = append(oldPID, p)
		size, det, manual = append(size, sz.Float64), append(det, ds.Float64), append(manual, man)
		E = append(E, Vectors(emb, 512)[0])
	}
	rows.Close()
	n := len(E)
	if n == 0 {
		return 0, nil
	}

	// 1) cluster good-quality faces only: small/blurry faces make bridges between different people
	var core []int
	for i := range E {
		if size[i] >= o.CoreSize && det[i] >= o.CoreDet {
			core = append(core, i)
		}
	}
	logf(progress, 0, 4, "%d faces, %d good enough for clustering", n, len(core))
	labels := make([]int, n)
	for i := range labels {
		labels[i] = -1
	}
	if len(core) >= 2 {
		Ec := make([][]float32, len(core))
		for i, c := range core {
			Ec[i] = E[c]
		}
		idx, sims := knn(Ec, o.K)
		logf(progress, 1, 4, "neighbours found")
		lab := chineseWhispers(idx, sims, o.Edge, 30)
		lab = mergeClose(Ec, lab, o.Merge)
		for i, c := range core {
			labels[c] = lab[i]
		}
	}

	// 2) keep clusters seen in >= MinFiles files, drop the rest (strangers)
	prune := func() {
		files := map[int]map[int64]bool{}
		for i, l := range labels {
			if l >= 0 {
				if files[l] == nil {
					files[l] = map[int64]bool{}
				}
				files[l][fileIDs[i]] = true
			}
		}
		for i, l := range labels {
			if l >= 0 && len(files[l]) < o.MinFiles {
				labels[i] = -1
			}
		}
	}
	prune()

	// 3) attach the remaining faces (small ones, leftovers) to the nearest person if similar enough
	var lab []int
	var labeled [][]float32
	for i, l := range labels {
		if l >= 0 {
			lab, labeled = append(lab, l), append(labeled, E[i])
		}
	}
	if len(lab) > 0 {
		uniq, C := centroids(labeled, lab)
		parallel(n, func(i int) {
			if labels[i] >= 0 {
				return
			}
			best, bj := float32(-2), -1
			for j, c := range C {
				if s := dot(E[i], c); s > best {
					best, bj = s, j
				}
			}
			if best >= o.Assign {
				labels[i] = uniq[bj]
			}
		})
	}
	prune()
	logf(progress, 2, 4, "people grouped")

	// 4) stable ids/names: a new cluster gets the old person most of its faces had
	clusters := map[int][]int{}
	for i, l := range labels {
		if l >= 0 {
			clusters[l] = append(clusters[l], i)
		}
	}
	var maxID sql.NullInt64
	if err := db.QueryRow("SELECT MAX(id) FROM persons").Scan(&maxID); err != nil {
		return 0, err
	}
	nextID := maxID.Int64 + 1
	var namedIDs []int64
	var namedC [][]float32
	nr, err := db.Query("SELECT id, centroid FROM persons WHERE name IS NOT NULL AND centroid IS NOT NULL")
	if err != nil {
		return 0, err
	}
	for nr.Next() {
		var id int64
		var c []byte
		if err := nr.Scan(&id, &c); err != nil {
			nr.Close()
			return 0, err
		}
		if len(c) == 512*4 {
			v := make([]float32, 512)
			for k := range v {
				v[k] = math.Float32frombits(binary.LittleEndian.Uint32(c[k*4:]))
			}
			namedIDs, namedC = append(namedIDs, id), append(namedC, v)
		}
	}
	nr.Close()
	order := make([]int, 0, len(clusters))
	for l := range clusters {
		order = append(order, l)
	}
	sort.Slice(order, func(a, b int) bool { // biggest first; ties by label for a stable order
		la, lb := len(clusters[order[a]]), len(clusters[order[b]])
		return la > lb || la == lb && order[a] < order[b]
	})
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	newPID := make([]int64, n)
	for i := range newPID {
		newPID[i] = -1
	}
	for _, l := range order {
		members := clusters[l]
		olds := map[int64]int{}
		total := 0
		for _, m := range members {
			if oldPID[m] >= 0 {
				olds[oldPID[m]]++
				total++
			}
		}
		var top int64 = -1
		topN := 0
		for p, c := range olds {
			if c > topN || c == topN && p < top {
				top, topN = p, c
			}
		}
		pid := int64(-1)
		if total > 0 && topN*2 >= total {
			pid = top
		} else {
			c := mean(E, members)
			best, bj := float32(-2), -1
			for j, v := range namedC {
				if s := dot(v, c); s > best {
					best, bj = s, j
				}
			}
			if bj >= 0 && best >= 0.6 {
				pid = namedIDs[bj]
			} else {
				pid = nextID
				nextID++
				if _, err := tx.Exec("INSERT INTO persons (id) VALUES (?)", pid); err != nil {
					return 0, err
				}
			}
		}
		for _, m := range members {
			newPID[m] = pid
		}
	}
	// faces assigned by hand ("Кто на фото") keep their person, or stay ungrouped
	for i := range newPID {
		if manual[i] {
			newPID[i] = oldPID[i]
		}
	}
	upd, err := tx.Prepare("UPDATE faces SET person_id = ? WHERE id = ?")
	if err != nil {
		return 0, err
	}
	defer upd.Close()
	for i, p := range newPID {
		if p != oldPID[i] {
			var v any
			if p >= 0 {
				v = p
			}
			if _, err := upd.Exec(v, faceIDs[i]); err != nil {
				return 0, err
			}
		}
	}
	byPerson := map[int64][]int{}
	for i, p := range newPID {
		if p >= 0 {
			byPerson[p] = append(byPerson[p], i)
		}
	}
	for p, ms := range byPerson {
		c := mean(E, ms)
		b := make([]byte, 512*4)
		for k, x := range c {
			binary.LittleEndian.PutUint32(b[k*4:], math.Float32bits(x))
		}
		if _, err := tx.Exec("UPDATE persons SET centroid = ? WHERE id = ?", b, p); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`UPDATE persons SET
		n_faces = (SELECT COUNT(*) FROM faces WHERE person_id = persons.id),
		n_files = (SELECT COUNT(DISTINCT file_id) FROM faces WHERE person_id = persons.id)`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("DELETE FROM persons WHERE n_faces = 0 AND name IS NULL"); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	var people int
	err = db.QueryRow("SELECT COUNT(*) FROM persons WHERE n_faces > 0").Scan(&people)
	logf(progress, 4, 4, "%d people", people)
	return people, err
}

// dot: 8 independent sums, so the CPU overlaps the multiplications.
func dot(a, b []float32) float32 {
	var s0, s1, s2, s3, s4, s5, s6, s7 float32
	b = b[:len(a)]
	i := 0
	for ; i+8 <= len(a); i += 8 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
		s4 += a[i+4] * b[i+4]
		s5 += a[i+5] * b[i+5]
		s6 += a[i+6] * b[i+6]
		s7 += a[i+7] * b[i+7]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3 + s4 + s5 + s6 + s7
}

// parallel runs f(0..n-1) on all cores.
func parallel(n int, f func(i int)) {
	w := runtime.NumCPU()
	var wg sync.WaitGroup
	next := make(chan int, 256)
	for k := 0; k < w; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				f(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
}

// knn: top-k cosine neighbours of every row (excluding itself).
func knn(E [][]float32, k int) ([][]int32, [][]float32) {
	n := len(E)
	k = min(k, n-1)
	idx, sims := make([][]int32, n), make([][]float32, n)
	parallel(n, func(i int) {
		// a small min-heap of the best k so far
		hi, hs := make([]int32, 0, k), make([]float32, 0, k)
		for j := 0; j < n; j++ {
			if j == i {
				continue
			}
			s := dot(E[i], E[j])
			if len(hs) < k {
				hi, hs = append(hi, int32(j)), append(hs, s)
				for c := len(hs) - 1; c > 0; { // sift up
					p := (c - 1) / 2
					if hs[p] <= hs[c] {
						break
					}
					hs[p], hs[c], hi[p], hi[c] = hs[c], hs[p], hi[c], hi[p]
					c = p
				}
			} else if s > hs[0] {
				hs[0], hi[0] = s, int32(j)
				for c := 0; ; { // sift down
					l, r, m := 2*c+1, 2*c+2, c
					if l < k && hs[l] < hs[m] {
						m = l
					}
					if r < k && hs[r] < hs[m] {
						m = r
					}
					if m == c {
						break
					}
					hs[m], hs[c], hi[m], hi[c] = hs[c], hs[m], hi[c], hi[m]
					c = m
				}
			}
		}
		idx[i], sims[i] = hi, hs
	})
	return idx, sims
}

// chineseWhispers: graph clustering on the kNN graph (edges with similarity >= thr), as dlib does for faces.
func chineseWhispers(idx [][]int32, sims [][]float32, thr float32, iters int) []int {
	n := len(idx)
	type edge struct {
		to int32
		w  float32
	}
	adj := make([][]edge, n)
	for i := range idx {
		for k, j := range idx[i] {
			if s := sims[i][k]; s >= thr {
				adj[i] = append(adj[i], edge{j, s})
				adj[j] = append(adj[j], edge{int32(i), s})
			}
		}
	}
	labels := make([]int, n)
	for i := range labels {
		labels[i] = i
	}
	rng := rand.New(rand.NewPCG(0, 0))
	weight := map[int]float32{}
	for it := 0; it < iters; it++ {
		changed := 0
		for _, i := range rng.Perm(n) {
			if len(adj[i]) == 0 {
				continue
			}
			clear(weight)
			for _, e := range adj[i] {
				weight[labels[e.to]] += e.w
			}
			best, bw := -1, float32(-1)
			for l, w := range weight {
				if w > bw || w == bw && l < best {
					best, bw = l, w
				}
			}
			if best != labels[i] {
				labels[i] = best
				changed++
			}
		}
		if changed == 0 {
			break
		}
	}
	return labels
}

// centroids: the distinct labels and their normalized mean vectors.
func centroids(E [][]float32, labels []int) ([]int, [][]float32) {
	pos := map[int]int{}
	var uniq []int
	for _, l := range labels {
		if _, ok := pos[l]; !ok {
			pos[l] = len(uniq)
			uniq = append(uniq, l)
		}
	}
	C := make([][]float32, len(uniq))
	for i := range C {
		C[i] = make([]float32, len(E[0]))
	}
	for i, l := range labels {
		c := C[pos[l]]
		for k, x := range E[i] {
			c[k] += x
		}
	}
	for _, c := range C {
		normalize(c)
	}
	return uniq, C
}

func normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(s))
	if n > 0 {
		for k := range v {
			v[k] /= n
		}
	}
}

func mean(E [][]float32, members []int) []float32 {
	c := make([]float32, len(E[members[0]]))
	for _, m := range members {
		for k, x := range E[m] {
			c[k] += x
		}
	}
	normalize(c)
	return c
}

// mergeClose: union clusters whose centroids are more similar than thr (repeated until stable).
func mergeClose(E [][]float32, labels []int, thr float32) []int {
	for {
		uniq, C := centroids(E, labels)
		parent := make([]int, len(uniq))
		for i := range parent {
			parent[i] = i
		}
		var find func(int) int
		find = func(x int) int {
			for parent[x] != x {
				parent[x] = parent[parent[x]]
				x = parent[x]
			}
			return x
		}
		var mu sync.Mutex
		var pairs [][2]int
		parallel(len(C), func(i int) {
			for j := i + 1; j < len(C); j++ {
				if dot(C[i], C[j]) >= thr {
					mu.Lock()
					pairs = append(pairs, [2]int{i, j})
					mu.Unlock()
				}
			}
		})
		if len(pairs) == 0 {
			return labels
		}
		sort.Slice(pairs, func(a, b int) bool {
			return pairs[a][0] < pairs[b][0] || pairs[a][0] == pairs[b][0] && pairs[a][1] < pairs[b][1]
		})
		for _, p := range pairs {
			parent[find(p[0])] = find(p[1])
		}
		pos := map[int]int{}
		for i, l := range uniq {
			pos[l] = i
		}
		for i, l := range labels {
			labels[i] = uniq[find(pos[l])]
		}
	}
}
