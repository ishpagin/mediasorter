package scan

import (
	"database/sql"
	"sort"
)

// VoiceOptions: how voices are grouped across the archive and when a person is suggested.
type VoiceOptions struct {
	Thr          float32 // min similarity for two voices to be linked
	MinFiles     int     // a group heard in this many files becomes a person without a name
	SuggestMin   int     // a suggestion needs this many videos "one voice, one known face"
	SuggestShare float64 // ... and this share of such videos of the group
}

func DefaultVoiceOptions() VoiceOptions {
	return VoiceOptions{Thr: 0.6, MinFiles: 2, SuggestMin: 2, SuggestShare: 0.6}
}

// ClusterVoices groups voices of the archive like faces and links each group to a person: the person most manual
// voices of the group have, else the person its voices had before (stable ids), else a suggestion by faces
// ("Петя?", no person yet), else a new unnamed person if the group is heard in MinFiles files. A group whose person
// is automatic and unnamed gets a suggestion too. Manual voices keep their person, automatic voices of a named
// person stay with it (people are never merged by the program), "different" pairs are never linked,
// "not his voice" marks are respected.
func ClusterVoices(db *sql.DB, o VoiceOptions) error {
	var model string
	err := db.QueryRow(`SELECT model FROM voices WHERE emb IS NOT NULL
		GROUP BY model ORDER BY COUNT(*) DESC, model LIMIT 1`).Scan(&model)
	if err == sql.ErrNoRows {
		return refreshPersonStats(db)
	} else if err != nil {
		return err
	}
	rows, err := db.Query("SELECT id, file_id, person_id, manual, emb FROM voices WHERE emb IS NOT NULL AND model = ? ORDER BY id", model)
	if err != nil {
		return err
	}
	var (
		ids, files, oldPID []int64 // oldPID: -1 = nobody
		manual             []bool
		E                  [][]float32
	)
	pos := map[int64]int{}
	for rows.Next() {
		var id, file int64
		var pid sql.NullInt64
		var man bool
		var emb []byte
		if err := rows.Scan(&id, &file, &pid, &man, &emb); err != nil {
			rows.Close()
			return err
		}
		pos[id] = len(ids)
		p := int64(-1)
		if pid.Valid {
			p = pid.Int64
		}
		ids, files, oldPID, manual = append(ids, id), append(files, file), append(oldPID, p), append(manual, man)
		v := Vectors(emb, len(emb)/2)[0]
		normalize(v)
		E = append(E, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	notSame := map[[2]int]bool{}
	if err := eachPair(db, "SELECT a, b FROM voice_not_same", func(a, b int64) {
		i, ok1 := pos[a]
		j, ok2 := pos[b]
		if ok1 && ok2 {
			notSame[[2]int{i, j}], notSame[[2]int{j, i}] = true, true
		}
	}); err != nil {
		return err
	}
	notPerson := map[[2]int64]bool{}
	if err := eachPair(db, "SELECT voice_id, person_id FROM voice_not_person", func(v, p int64) {
		notPerson[[2]int64{v, p}] = true
	}); err != nil {
		return err
	}

	// 1) groups: Chinese Whispers on the kNN graph without the "different" pairs
	idx, sims := knn(E, 30)
	for i := range idx {
		for k, j := range idx[i] {
			if notSame[[2]int{i, int(j)}] {
				sims[i][k] = -1
			}
		}
	}
	labels := chineseWhispers(idx, sims, o.Thr, 30)
	groups := map[int][]int{}
	for i, l := range labels {
		groups[l] = append(groups[l], i)
	}
	order := make([]int, 0, len(groups))
	for l := range groups {
		order = append(order, l)
	}
	sort.Ints(order)

	named := map[int64]bool{}
	if err := eachPair(db, "SELECT id, 0 FROM persons WHERE name IS NOT NULL", func(p, _ int64) { named[p] = true }); err != nil {
		return err
	}

	// 2) videos for a suggestion: one voice with a vector and exactly one named person in the frame
	oneVoice := map[int64]bool{}
	if err := eachPair(db, "SELECT file_id, 0 FROM voices WHERE emb IS NOT NULL GROUP BY file_id HAVING COUNT(*) = 1",
		func(f, _ int64) { oneVoice[f] = true }); err != nil {
		return err
	}
	oneFace := map[int64]int64{}
	if err := eachPair(db, `SELECT x.file_id, MIN(x.person_id) FROM faces x JOIN persons p ON p.id = x.person_id
		WHERE p.name IS NOT NULL GROUP BY x.file_id HAVING COUNT(DISTINCT x.person_id) = 1`,
		func(f, p int64) { oneFace[f] = p }); err != nil {
		return err
	}
	// suggestFor: the named person seen alone in the group's one-voice videos often enough, or -1;
	// a video whose voice the user marked "not his voice" for that person does not count
	suggestFor := func(members []int) int64 {
		seen := map[int64]bool{}
		count := map[int64]int{}
		total := 0
		for _, m := range members {
			f := files[m]
			p, ok := oneFace[f]
			if seen[f] || !oneVoice[f] || !ok || notPerson[[2]int64{ids[m], p}] {
				continue
			}
			seen[f] = true
			count[p]++
			total++
		}
		best, bn := int64(-1), 0
		for p, c := range count {
			if c > bn || c == bn && p < best {
				best, bn = p, c
			}
		}
		if bn >= o.SuggestMin && float64(bn) >= o.SuggestShare*float64(total) {
			return best
		}
		return -1
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE voices SET cluster = NULL, suggest_id = NULL WHERE manual = 0"); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE voices SET cluster = NULL WHERE manual = 1"); err != nil {
		return err
	}
	// manual = 0 again: the user may have marked the voice after it was read above
	upd, err := tx.Prepare("UPDATE voices SET cluster = ?, person_id = ?, suggest_id = ? WHERE id = ? AND manual = 0")
	if err != nil {
		return err
	}
	defer upd.Close()
	cl, err := tx.Prepare("UPDATE voices SET cluster = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer cl.Close()
	for _, l := range order {
		members := groups[l]
		suggest := int64(-1)
		pid := majority(members, func(m int) int64 { return pick(manual[m], oldPID[m]) })
		if pid < 0 {
			pid = majority(members, func(m int) int64 { return pick(!manual[m], oldPID[m]) })
			if pid >= 0 && !named[pid] {
				suggest = suggestFor(members)
			}
		}
		if pid < 0 {
			// a suggestion is a question to the user, not a person: a stranger's voice is never glued to someone
			suggest = suggestFor(members)
			fs := map[int64]bool{}
			for _, m := range members {
				fs[files[m]] = true
			}
			if suggest < 0 && len(fs) >= o.MinFiles {
				res, err := tx.Exec("INSERT INTO persons DEFAULT VALUES")
				if err != nil {
					return err
				}
				if pid, err = res.LastInsertId(); err != nil {
					return err
				}
			}
		}
		for _, m := range members {
			if manual[m] { // the user's choice stays as it is
				if _, err := cl.Exec(l, ids[m]); err != nil {
					return err
				}
				continue
			}
			var p, s any
			if old := oldPID[m]; old >= 0 && old != pid && named[old] && !notPerson[[2]int64{ids[m], old}] {
				p = old // a named person keeps its voice; joining two people is the user's call
			} else {
				if pid >= 0 && !notPerson[[2]int64{ids[m], pid}] {
					p = pid
				}
				if suggest >= 0 && !notPerson[[2]int64{ids[m], suggest}] {
					s = suggest
				}
			}
			if _, err := upd.Exec(l, p, s, ids[m]); err != nil {
				return err
			}
		}
	}
	if err := refreshPersonStats(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func pick(ok bool, p int64) int64 {
	if ok {
		return p
	}
	return -1
}

// majority: the most frequent person among members (-1 = nobody), ties go to the smaller id.
func majority(members []int, person func(int) int64) int64 {
	count := map[int64]int{}
	best, bn := int64(-1), 0
	for _, m := range members {
		if p := person(m); p >= 0 {
			count[p]++
			if c := count[p]; c > bn || c == bn && p < best {
				best, bn = p, c
			}
		}
	}
	return best
}

// eachPair runs f on every row of a two-integer query.
func eachPair(db *sql.DB, q string, f func(a, b int64)) error {
	rows, err := db.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return err
		}
		f(a, b)
	}
	return rows.Err()
}
