package core

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// TrashItem: one deleted file as the trash page shows it.
type TrashItem struct {
	ID      int64  `json:"id"` // deleted.rowid, the same id Restore takes
	Name    string `json:"name"`
	Path    string `json:"path"` // where it lived
	Kind    string `json:"kind"` // image | video | "" (deleted before the snapshot had a files row)
	Size    int64  `json:"size"`
	At      string `json:"at"`
	InQuar  bool   `json:"inQuarantine"` // false = only the DB rows were removed, there is no file to show
	Preview bool   `json:"preview"`
}

// snapCol: a column of the first row of a snapshot table, looked up by name (snapshots are SELECT *).
func snapCol(table, col string) string {
	return fmt.Sprintf(`json_extract(CAST(snapshot AS TEXT), '$.tables.%[1]s.rows[0][' ||
		(SELECT key FROM json_each(CAST(snapshot AS TEXT), '$.tables.%[1]s.cols') WHERE value = '%[2]s') || ']')`, table, col)
}

// TrashList: everything in the trash, newest first, and the bytes it holds.
func TrashList(db *sql.DB) ([]TrashItem, int64, error) {
	rows, err := db.Query(fmt.Sprintf(`SELECT rowid, path, moved_to IS NOT NULL, deleted_at,
		coalesce(%s, ''), coalesce(%s, 0), %s IS NOT NULL FROM deleted ORDER BY rowid DESC`,
		snapCol("files", "kind"), snapCol("files", "size"), snapCol("previews", "jpeg")))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, total := []TrashItem{}, int64(0)
	for rows.Next() {
		var it TrashItem
		if err := rows.Scan(&it.ID, &it.Path, &it.InQuar, &it.At, &it.Kind, &it.Size, &it.Preview); err != nil {
			return nil, 0, err
		}
		it.Name = filepath.Base(it.Path)
		if it.InQuar {
			total += it.Size
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// TrashPreview: the stored preview JPEG of a deleted file (kept in its snapshot).
func TrashPreview(db *sql.DB, id int64) ([]byte, error) {
	var b64 sql.NullString
	err := db.QueryRow(fmt.Sprintf("SELECT json_extract(%s, '$.b64') FROM deleted WHERE rowid = ?",
		snapCol("previews", "jpeg")), id).Scan(&b64)
	if err != nil {
		return nil, err
	}
	if !b64.Valid {
		return nil, sql.ErrNoRows
	}
	return base64.StdEncoding.DecodeString(b64.String)
}

// dropDeadUndo: delete steps of Ctrl+Z whose deletions are all gone from the trash.
func dropDeadUndo(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM undo_log WHERE kind = 'delete' AND NOT EXISTS
		(SELECT 1 FROM json_each(undo_log.data) j JOIN deleted d ON d.rowid = j.value)`)
	return err
}

// PruneTrash forgets deletions whose file is no longer in the quarantine (someone cleaned it by hand).
// Rows-only deletions (moved_to NULL) stay: their undo puts the rows back and needs no file.
func PruneTrash(db *sql.DB) (int, error) {
	rows, err := db.Query("SELECT rowid, moved_to FROM deleted WHERE moved_to IS NOT NULL")
	if err != nil {
		return 0, err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return 0, err
		}
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) { // only a sure "not there": a share that is down is not
			gone = append(gone, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range gone {
		if _, err := db.Exec("DELETE FROM deleted WHERE rowid = ?", id); err != nil {
			return 0, err
		}
	}
	return len(gone), dropDeadUndo(db)
}

// EmptyTrash erases the quarantined files for good and forgets every deletion.
// Returns the files erased and the failures; a file that could not be erased keeps its row.
func EmptyTrash(db *sql.DB) (int, []Failure, error) {
	rows, err := db.Query("SELECT rowid, path, moved_to FROM deleted")
	if err != nil {
		return 0, nil, err
	}
	type entry struct {
		id      int64
		path    string
		movedTo sql.NullString
	}
	var all []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.path, &e.movedTo); err != nil {
			rows.Close()
			return 0, nil, err
		}
		all = append(all, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	done, failed := 0, []Failure{}
	for _, e := range all {
		if e.movedTo.Valid {
			if err := os.Remove(e.movedTo.String); err != nil && !errors.Is(err, os.ErrNotExist) {
				failed = append(failed, Failure{e.path, err.Error()})
				continue
			}
			dropRecycled(e.movedTo.String)
			done++
		}
		if _, err := db.Exec("DELETE FROM deleted WHERE rowid = ?", e.id); err != nil {
			return done, failed, err
		}
	}
	return done, failed, dropDeadUndo(db)
}

// dropRecycled: a NAS share with a recycle bin (Synology) moves a file deleted over SMB to
// <share>/#recycle/<same path> instead of freeing the space; a delete inside #recycle is final.
// The share root is unknown here, so every parent is tried.
func dropRecycled(p string) {
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		rel, err := filepath.Rel(dir, p)
		if err == nil {
			if os.Remove(filepath.Join(dir, "#recycle", rel)) == nil {
				return
			}
		}
		if parent := filepath.Dir(dir); parent == dir {
			return
		}
	}
}
