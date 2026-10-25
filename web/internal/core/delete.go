package core

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// rows of a file that a deletion removes (ON DELETE CASCADE) and an undo puts back; persons go first on restore.
// Same list as photosorter._SNAP_TABLES. Tables that do not exist yet (no scenes / stacks run) are skipped.
var snapTables = [][2]string{{"files", "id"}, {"faces", "file_id"}, {"previews", "file_id"}, {"clip", "file_id"},
	{"tags", "file_id"}, {"stacks", "file_id"}, {"quality", "file_id"}, {"copies", "file_id"}}

type snapTable struct {
	Cols []string `json:"cols"`
	Rows [][]any  `json:"rows"`
}

type snapshot struct {
	V      int                   `json:"v"`
	Tables map[string]*snapTable `json:"tables"`
}

type blob struct {
	B64 string `json:"b64"`
}

func queryTable(q querier, query string, args ...any) (*snapTable, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	t := &snapTable{Cols: cols, Rows: [][]any{}}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = blob{base64.StdEncoding.EncodeToString(b)}
			}
		}
		t.Rows = append(t.Rows, vals)
	}
	return t, rows.Err()
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// takeSnapshot: every row of the file (and the persons of its faces, an unnamed one disappears with its last face).
func takeSnapshot(q querier, fid int64) ([]byte, error) {
	s := snapshot{V: 1, Tables: map[string]*snapTable{}}
	for _, tc := range snapTables {
		t, err := queryTable(q, fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", tc[0], tc[1]), fid)
		if err != nil {
			if strings.Contains(err.Error(), "no such table") {
				continue
			}
			return nil, err
		}
		s.Tables[tc[0]] = t
	}
	t, err := queryTable(q, `SELECT * FROM persons WHERE id IN (SELECT person_id FROM faces WHERE file_id = ? AND person_id IS NOT NULL)`, fid)
	if err != nil {
		return nil, err
	}
	if len(t.Rows) > 0 {
		s.Tables["persons"] = t
	}
	return json.Marshal(s)
}

func restoreSnapshot(q querier, data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		return errLegacySnapshot
	}
	var s struct {
		Tables map[string]struct {
			Cols []string            `json:"cols"`
			Rows [][]json.RawMessage `json:"rows"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	order := []string{"persons"}
	for _, tc := range snapTables {
		order = append(order, tc[0])
	}
	for _, name := range order {
		t, ok := s.Tables[name]
		if !ok {
			continue
		}
		verb := "INSERT"
		if name == "persons" {
			verb = "INSERT OR IGNORE" // the person may still exist
		}
		stmt := fmt.Sprintf("%s INTO %s (%s) VALUES (%s)", verb, name, strings.Join(t.Cols, ", "),
			strings.TrimSuffix(strings.Repeat("?, ", len(t.Cols)), ", "))
		for _, r := range t.Rows {
			args := make([]any, len(r))
			for i, raw := range r {
				var b blob
				if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &b) == nil {
					v, err := base64.StdEncoding.DecodeString(b.B64)
					if err != nil {
						return err
					}
					args[i] = v
					continue
				}
				var v any
				dec := json.NewDecoder(strings.NewReader(string(raw)))
				dec.UseNumber() // ids stay exact integers
				if err := dec.Decode(&v); err != nil {
					return err
				}
				if n, ok := v.(json.Number); ok {
					if iv, err := n.Int64(); err == nil {
						v = iv
					} else if fv, err := n.Float64(); err == nil {
						v = fv
					}
				}
				args[i] = v
			}
			if _, err := q.Exec(stmt, args...); err != nil {
				return err
			}
		}
	}
	return nil
}

var errLegacySnapshot = errors.New("старое удаление (до нового интерфейса): верни его командой photosorter.py restore")

func refreshPersonStats(q querier) error {
	if _, err := q.Exec(`UPDATE persons SET
		n_faces = (SELECT COUNT(*) FROM faces WHERE person_id = persons.id),
		n_files = (SELECT COUNT(DISTINCT file_id) FROM faces WHERE person_id = persons.id)`); err != nil {
		return err
	}
	_, err := q.Exec("DELETE FROM persons WHERE n_faces = 0 AND name IS NULL")
	return err
}

// Failure: a file an action could not handle, and why (shown to the user).
type Failure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// ErrProtected: some files are in a folder from protected.txt; nothing was deleted.
type ErrProtected struct {
	N     int
	First string
}

func (e ErrProtected) Error() string {
	return fmt.Sprintf("%d файл(ов) в защищённой папке (protected.txt), например %s. Ничего не удалено.", e.N, e.First)
}

func now() string { return time.Now().Format("2006-01-02T15:04:05") }

// Trash moves files to the quarantine and removes their rows; each row set is kept in `deleted.snapshot` (JSON),
// so Restore (or photosorter.py restore) brings everything back. Returns done, failures and the log ids (undo).
func (c *Config) Trash(db *sql.DB, paths []string) (int, []Failure, []int64, error) {
	var blocked []string
	for _, p := range paths {
		if c.IsProtected(p) {
			blocked = append(blocked, p)
		}
	}
	if len(blocked) > 0 {
		return 0, nil, nil, ErrProtected{len(blocked), blocked[0]}
	}
	done, failed, logIDs := 0, []Failure{}, []int64{}
	for _, p := range paths {
		id, err := c.trashOne(db, p)
		if err != nil {
			failed = append(failed, Failure{p, err.Error()})
			continue
		}
		logIDs = append(logIDs, id)
		done++
	}
	if err := refreshPersonStats(db); err != nil {
		return done, failed, logIDs, err
	}
	return done, failed, logIDs, nil
}

func (c *Config) trashOne(db *sql.DB, p string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var fid int64
	var snap []byte
	err = tx.QueryRow("SELECT id FROM files WHERE path = ?", p).Scan(&fid)
	if err == nil {
		if snap, err = takeSnapshot(tx, fid); err != nil {
			return 0, err
		}
		links, err := strings1(tx, "SELECT link_path FROM collection_files WHERE file_id = ?", fid)
		if err != nil {
			return 0, err
		}
		for _, l := range links { // its copies / hard links in other collections go too, or they would stay behind
			if filepath.Clean(l) != filepath.Clean(p) && exists(l) {
				if _, err := c.quarantine(l); err != nil {
					return 0, err
				}
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var movedTo any
	if exists(p) {
		q, err := c.quarantine(p)
		if err != nil {
			return 0, err
		}
		movedTo = q
	}
	res, err := tx.Exec("INSERT INTO deleted (path, moved_to, deleted_at, snapshot) VALUES (?, ?, ?, ?)", p, movedTo, now(), snap)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec("DELETE FROM files WHERE path = ?", p); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		if s, ok := movedTo.(string); ok { // keep the file where the DB says it is
			_ = MoveFile(s, p)
		}
		return 0, err
	}
	return res.LastInsertId()
}

// Restore undoes deletions: moves files back from the quarantine and puts their rows back.
func (c *Config) Restore(db *sql.DB, logIDs []int64) (int, []Failure, error) {
	done, problems := 0, []Failure{}
	for _, id := range logIDs {
		ok, prob, err := c.restoreOne(db, id)
		if err != nil {
			return done, problems, err
		}
		if prob != nil {
			problems = append(problems, *prob)
		}
		if ok {
			done++
		}
	}
	return done, problems, refreshPersonStats(db)
}

func (c *Config) restoreOne(db *sql.DB, id int64) (bool, *Failure, error) {
	var path string
	var movedTo sql.NullString
	var snap []byte
	err := db.QueryRow("SELECT path, moved_to, snapshot FROM deleted WHERE rowid = ?", id).Scan(&path, &movedTo, &snap)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	} else if err != nil {
		return false, nil, err
	}
	if len(snap) > 0 && snap[0] != '{' {
		return false, &Failure{path, errLegacySnapshot.Error()}, nil
	}
	if !exists(path) {
		if !movedTo.Valid || !exists(movedTo.String) {
			return false, &Failure{path, "нет в карантине: " + movedTo.String}, nil
		}
		if err := MoveFile(movedTo.String, path); err != nil {
			return false, &Failure{path, err.Error()}, nil
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback()
	var prob *Failure
	var inDB int
	_ = tx.QueryRow("SELECT COUNT(*) FROM files WHERE path = ?", path).Scan(&inDB)
	switch {
	case len(snap) == 0:
		prob = &Failure{path, "файл возвращён; чтобы он снова появился в базе, запусти scan"}
	case inDB == 0:
		if _, err := tx.Exec("SAVEPOINT undo"); err != nil {
			return false, nil, err
		}
		if err := restoreSnapshot(tx, snap); err != nil {
			_, _ = tx.Exec("ROLLBACK TO undo")
			prob = &Failure{path, "файл возвращён, но в базу не встал — запусти scan"}
		}
		_, _ = tx.Exec("RELEASE undo")
	}
	if _, err := tx.Exec("DELETE FROM deleted WHERE rowid = ?", id); err != nil {
		return false, nil, err
	}
	return true, prob, tx.Commit()
}

func strings1(q querier, query string, args ...any) ([]string, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
