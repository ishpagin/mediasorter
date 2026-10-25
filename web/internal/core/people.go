package core

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strings"
)

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// MergePersons merges persons into ids[0]; the first non-empty name wins (no undo, like the Python UI).
func MergePersons(db *sql.DB, ids []int64) error {
	if len(ids) < 2 {
		return nil
	}
	target, others := ids[0], ids[1:]
	names, err := strings1(db, "SELECT name FROM persons WHERE name IS NOT NULL AND id IN ("+placeholders(len(ids))+")", anys(ids)...)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := placeholders(len(others))
	if _, err := tx.Exec("UPDATE faces SET person_id = ? WHERE person_id IN ("+q+")", append([]any{target}, anys(others)...)...); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM persons WHERE id IN ("+q+")", anys(others)...); err != nil {
		return err
	}
	var tname sql.NullString
	_ = tx.QueryRow("SELECT name FROM persons WHERE id = ?", target).Scan(&tname)
	if (!tname.Valid || tname.String == "") && len(names) > 0 {
		if _, err := tx.Exec("UPDATE persons SET name = ? WHERE id = ?", names[0], target); err != nil {
			return err
		}
	}
	if err := refreshPersonStats(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// CreatePerson: a named person (an existing one with this name, case-insensitive, is reused).
func CreatePerson(db *sql.DB, name string) (int64, error) {
	// SQLite's lower() knows only ASCII: compare here so "Вася" and "вася" are one person
	rows, err := db.Query("SELECT id, name FROM persons WHERE name IS NOT NULL")
	if err != nil {
		return 0, err
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for rows.Next() {
		var id int64
		var n string
		if rows.Scan(&id, &n) == nil && strings.ToLower(strings.TrimSpace(n)) == want {
			rows.Close()
			return id, nil
		}
	}
	rows.Close()
	res, err := db.Exec("INSERT INTO persons (name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FaceBefore: a face's person before a change by hand (the undo of AssignFaces).
type FaceBefore struct {
	Face   int64   `json:"f"`
	Person *int64  `json:"p"`
	Manual int     `json:"m"`
	Name   *string `json:"n"`
	Hidden *int    `json:"h"`
}

// AssignFaces sets the person of faces by hand (nil = not anybody known); `cluster` keeps them.
func AssignFaces(db *sql.DB, faceIDs []int64, person *int64) ([]FaceBefore, error) {
	if len(faceIDs) == 0 {
		return nil, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var before []FaceBefore
	for start := 0; start < len(faceIDs); start += 500 { // SQLite limits the number of variables
		chunk := faceIDs[start:min(start+500, len(faceIDs))]
		rows, err := tx.Query(`SELECT x.id, x.person_id, x.manual, p.name, p.hidden FROM faces x
			LEFT JOIN persons p ON p.id = x.person_id WHERE x.id IN (`+placeholders(len(chunk))+`)`, anys(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var b FaceBefore
			if err := rows.Scan(&b.Face, &b.Person, &b.Manual, &b.Name, &b.Hidden); err != nil {
				rows.Close()
				return nil, err
			}
			before = append(before, b)
		}
		rows.Close()
		if _, err := tx.Exec("UPDATE faces SET person_id = ?, manual = 1 WHERE id IN ("+placeholders(len(chunk))+")",
			append([]any{person}, anys(chunk)...)...); err != nil {
			return nil, err
		}
	}
	if err := refreshPersonStats(tx); err != nil { // an unnamed person left without faces disappears
		return nil, err
	}
	return before, tx.Commit()
}

// UnassignFaces undoes AssignFaces.
func UnassignFaces(db *sql.DB, before []FaceBefore) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, b := range before {
		if b.Person != nil {
			hidden := 0
			if b.Hidden != nil {
				hidden = *b.Hidden
			}
			if _, err := tx.Exec("INSERT OR IGNORE INTO persons (id, name, hidden) VALUES (?, ?, ?)", *b.Person, b.Name, hidden); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("UPDATE faces SET person_id = ?, manual = ? WHERE id = ?", b.Person, b.Manual, b.Face); err != nil {
			return err
		}
	}
	if err := refreshPersonStats(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Unit: who is where on a file, for correcting by hand: one per person per file (a video has many frames of the
// same face), ungrouped faces one by one; Face is the biggest face of the unit (its thumbnail).
type Unit struct {
	File   int64   `json:"file"`
	Path   string  `json:"path"`
	Person *int64  `json:"person"`
	Faces  []int64 `json:"faces"`
	Face   int64   `json:"face"`
}

// FileFaces: the units of these files, in file order, known people first.
func FileFaces(db *sql.DB, fileIDs []int64) ([]Unit, error) {
	if len(fileIDs) == 0 {
		return []Unit{}, nil
	}
	q := placeholders(len(fileIDs))
	rows, err := db.Query(`
		SELECT x.file_id, f.path, x.person_id, group_concat(x.id), (SELECT t.id FROM faces t
		       WHERE t.file_id = x.file_id AND t.person_id IS x.person_id ORDER BY t.size DESC LIMIT 1)
		FROM faces x JOIN files f ON f.id = x.file_id WHERE x.file_id IN (`+q+`) AND x.person_id IS NOT NULL
		GROUP BY x.file_id, x.person_id
		UNION ALL
		SELECT x.file_id, f.path, NULL, x.id, x.id
		FROM faces x JOIN files f ON f.id = x.file_id WHERE x.file_id IN (`+q+`) AND x.person_id IS NULL`,
		append(anys(fileIDs), anys(fileIDs)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	order := map[int64]int{}
	for i, f := range fileIDs {
		order[f] = i
	}
	out := []Unit{}
	for rows.Next() {
		var u Unit
		var ids string
		if err := rows.Scan(&u.File, &u.Path, &u.Person, &ids, &u.Face); err != nil {
			return nil, err
		}
		for _, s := range strings.Split(ids, ",") {
			var id int64
			for _, ch := range s {
				id = id*10 + int64(ch-'0')
			}
			u.Faces = append(u.Faces, id)
		}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if order[a.File] != order[b.File] {
			return order[a.File] < order[b.File]
		}
		if (a.Person == nil) != (b.Person == nil) {
			return a.Person != nil
		}
		return a.Faces[0] < b.Faces[0]
	})
	return out, rows.Err()
}

// Centroids: the mean (L2-normalized) face embedding of every grouped person. Embeddings are float16[512].
func Centroids(db *sql.DB) (map[int64][]float32, error) {
	rows, err := db.Query("SELECT person_id, embedding FROM faces WHERE person_id IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sum := map[int64][]float32{}
	for rows.Next() {
		var pid int64
		var emb []byte
		if err := rows.Scan(&pid, &emb); err != nil {
			return nil, err
		}
		if len(emb) != 1024 {
			return nil, errors.New("unexpected embedding size")
		}
		s := sum[pid]
		if s == nil {
			s = make([]float32, 512)
			sum[pid] = s
		}
		for i := 0; i < 512; i++ {
			s[i] += f16(binary.LittleEndian.Uint16(emb[2*i:]))
		}
	}
	for _, s := range sum {
		var n float64
		for _, v := range s {
			n += float64(v) * float64(v)
		}
		if n = math.Sqrt(n); n > 0 {
			for i := range s {
				s[i] = float32(float64(s[i]) / n)
			}
		}
	}
	return sum, rows.Err()
}

// f16 decodes an IEEE 754 half-precision float.
func f16(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch {
	case exp == 0 && frac == 0:
		return math.Float32frombits(sign)
	case exp == 0: // subnormal
		f := float32(frac) / 1024 * float32(math.Pow(2, -14))
		if sign != 0 {
			return -f
		}
		return f
	case exp == 31:
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}
