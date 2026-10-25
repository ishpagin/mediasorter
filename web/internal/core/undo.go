package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// The undo history lives in the DB per browser session (a cookie), so Ctrl+Z survives a restart of the service.
const undoSchema = `CREATE TABLE IF NOT EXISTS undo_log (
	id      INTEGER PRIMARY KEY,
	session TEXT NOT NULL,
	at      TEXT NOT NULL,
	kind    TEXT NOT NULL,          -- delete | collect | faces
	label   TEXT NOT NULL,          -- what Ctrl+Z will undo, for the button
	data    TEXT NOT NULL           -- delete: [deleted rowid...]; collect: [{c, f}...]
);
CREATE INDEX IF NOT EXISTS undo_log_session ON undo_log(session, id);`

const undoKeep = 200 // steps per session

// InitUndo creates the undo table (the Python side does not use it).
func InitUndo(db *sql.DB) error {
	_, err := db.Exec(undoSchema)
	return err
}

// PushUndo records a step that Ctrl+Z can undo.
func PushUndo(db *sql.DB, session, kind, label string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := db.Exec("INSERT INTO undo_log (session, at, kind, label, data) VALUES (?, ?, ?, ?, ?)",
		session, now(), kind, label, string(b)); err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM undo_log WHERE session = ? AND id NOT IN
		(SELECT id FROM undo_log WHERE session = ? ORDER BY id DESC LIMIT ?)`, session, session, undoKeep)
	return err
}

// LastUndo: the label of the step Ctrl+Z would undo now ("" = nothing to undo).
func LastUndo(db *sql.DB, session string) string {
	var label string
	_ = db.QueryRow("SELECT label FROM undo_log WHERE session = ? ORDER BY id DESC LIMIT 1", session).Scan(&label)
	return label
}

// Undo undoes the last step of the session; returns a message for the user.
func (c *Config) Undo(db *sql.DB, session string) (string, []Failure, error) {
	var id int64
	var kind, data string
	err := db.QueryRow("SELECT id, kind, data FROM undo_log WHERE session = ? ORDER BY id DESC LIMIT 1", session).
		Scan(&id, &kind, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "Нечего возвращать", nil, nil
	} else if err != nil {
		return "", nil, err
	}
	if _, err := db.Exec("DELETE FROM undo_log WHERE id = ?", id); err != nil {
		return "", nil, err
	}
	switch kind {
	case "delete":
		var ids []int64
		if err := json.Unmarshal([]byte(data), &ids); err != nil {
			return "", nil, err
		}
		done, problems, err := c.Restore(db, ids)
		return fmt.Sprintf("Возвращено: %d", done), problems, err
	case "faces":
		var before []FaceBefore
		if err := json.Unmarshal([]byte(data), &before); err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("Вернул как было: %d %s", len(before), plural(len(before), "лицо", "лица", "лиц")), nil,
			UnassignFaces(db, before)
	case "collect":
		var pairs []Pair
		if err := json.Unmarshal([]byte(data), &pairs); err != nil {
			return "", nil, err
		}
		done, failed := ReturnFromCollection(db, pairs)
		return fmt.Sprintf("Убрано из подборок: %d", done), failed, nil
	}
	return "", nil, fmt.Errorf("unknown undo step %q", kind)
}

// Short names for messages: "a.jpg" or "3 файла".
func Names(paths []string) string {
	if len(paths) == 1 {
		return filepath.Base(paths[0])
	}
	return fmt.Sprintf("%d %s", len(paths), plural(len(paths), "файл", "файла", "файлов"))
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
