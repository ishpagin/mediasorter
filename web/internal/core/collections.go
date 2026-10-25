package core

import (
	"archive/zip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// CollectionsRoot: where collection folders are created (setting collections_root, else ~/Pictures/Photosorter).
func CollectionsRoot(db *sql.DB) string {
	var v string
	if err := db.QueryRow("SELECT value FROM settings WHERE key = 'collections_root'").Scan(&v); err == nil && v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Pictures", "Photosorter")
}

var unsafeName = regexp.MustCompile(`[\\/:*?"<>|]+`)

// CreateCollection makes the folder and the row; returns its id.
func CreateCollection(db *sql.DB, name string, personID *int64) (int64, error) {
	name = strings.TrimSpace(name)
	folderName := strings.Trim(unsafeName.ReplaceAllString(name, "_"), " .")
	if folderName == "" {
		folderName = "Подборка"
	}
	folder := FreePath(filepath.Join(CollectionsRoot(db), folderName))
	if err := os.MkdirAll(folder, 0o775); err != nil {
		return 0, err
	}
	res, err := db.Exec("INSERT INTO collections (name, folder, created_at, person_id) VALUES (?, ?, ?, ?)", name, folder, now(), personID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CollectionForPerson: the person's own collection (named after them), created on first use.
func CollectionForPerson(db *sql.DB, pid int64) (int64, error) {
	var cid int64
	if err := db.QueryRow("SELECT id FROM collections WHERE person_id = ?", pid).Scan(&cid); err == nil {
		return cid, nil
	}
	var name string
	if err := db.QueryRow("SELECT COALESCE(name, 'person_' || id) FROM persons WHERE id = ?", pid).Scan(&name); err != nil {
		return 0, err
	}
	var taken int
	_ = db.QueryRow("SELECT COUNT(*) FROM collections WHERE name = ?", name).Scan(&taken)
	if taken > 0 {
		name = fmt.Sprintf("%s (%d)", name, pid)
	}
	return CreateCollection(db, name, &pid)
}

// Pair: a file added to a collection (what an undo takes out again).
type Pair struct {
	Collection int64 `json:"c"`
	File       int64 `json:"f"`
}

// AddToCollection puts one file into a collection. copy=false and the file is in the archive: it is MOVED into the
// collection folder (files.path follows). copy=true, or the file already lives in another collection: hard link or
// copy, the original stays. Returns false if it was there already.
func (c *Config) AddToCollection(db *sql.DB, cid, fid int64, copy bool) (bool, error) {
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM collection_files WHERE collection_id = ? AND file_id = ?", cid, fid).Scan(&n)
	if n > 0 {
		return false, nil
	}
	var path, folder string
	if err := db.QueryRow("SELECT path FROM files WHERE id = ?", fid).Scan(&path); err != nil {
		return false, err
	}
	if !copy && c.IsProtected(path) {
		return false, errors.New("защищённая папка (protected.txt)")
	}
	if err := db.QueryRow("SELECT folder FROM collections WHERE id = ?", cid).Scan(&folder); err != nil {
		return false, err
	}
	dst := FreePath(filepath.Join(folder, filepath.Base(path)))
	var prev sql.NullString
	_ = db.QueryRow("SELECT original_path FROM collection_files WHERE file_id = ? LIMIT 1", fid).Scan(&prev)
	links, err := strings1(db, "SELECT link_path FROM collection_files WHERE file_id = ?", fid)
	if err != nil {
		return false, err
	}
	inCollection := false
	for _, l := range links {
		inCollection = inCollection || filepath.Clean(l) == filepath.Clean(path)
	}
	original := path
	if copy || inCollection {
		if err := LinkOrCopy(path, dst); err != nil {
			return false, err
		}
		if prev.Valid {
			original = prev.String
		}
	} else {
		if err := MoveFile(path, dst); err != nil {
			return false, err
		}
		if _, err := db.Exec("UPDATE files SET path = ? WHERE id = ?", dst, fid); err != nil {
			_ = MoveFile(dst, path) // keep the file where the DB says it is
			return false, err
		}
	}
	if _, err := db.Exec(`INSERT INTO collection_files (collection_id, file_id, link_path, original_path, added_at)
		VALUES (?, ?, ?, ?, ?)`, cid, fid, dst, original, now()); err != nil {
		return false, err
	}
	return true, nil
}

// AddFiles adds files (by id) to a collection. Returns done, failures and the pairs added (for undo).
func (c *Config) AddFiles(db *sql.DB, cid int64, fids []int64, copy bool) (int, []Failure, []Pair) {
	done, failed, added := 0, []Failure{}, []Pair{}
	for _, fid := range fids {
		ok, err := c.AddToCollection(db, cid, fid, copy)
		if err != nil {
			failed = append(failed, Failure{fileName(db, fid), err.Error()})
		} else if ok {
			done++
			added = append(added, Pair{cid, fid})
		}
	}
	return done, failed, added
}

// ByPeople puts every file into the collections of all NAMED people on it (except "this is me").
// Returns {person: files added}, files with nobody to send to, failures and the pairs added.
func (c *Config) ByPeople(db *sql.DB, fids []int64, copy bool) (map[string]int, []int64, []Failure, []Pair) {
	perPerson, nobody, failed, added := map[string]int{}, []int64{}, []Failure{}, []Pair{}
	for _, fid := range fids {
		rows, err := db.Query(`SELECT DISTINCT p.id, p.name FROM faces f JOIN persons p ON p.id = f.person_id
			WHERE f.file_id = ? AND p.name IS NOT NULL AND p.skip_collect = 0 ORDER BY p.name`, fid)
		if err != nil {
			failed = append(failed, Failure{fileName(db, fid), err.Error()})
			continue
		}
		type person struct {
			id   int64
			name string
		}
		var people []person
		for rows.Next() {
			var p person
			if rows.Scan(&p.id, &p.name) == nil {
				people = append(people, p)
			}
		}
		rows.Close()
		if len(people) == 0 {
			nobody = append(nobody, fid)
			continue
		}
		for _, p := range people {
			cid, err := CollectionForPerson(db, p.id)
			if err == nil {
				var ok bool
				if ok, err = c.AddToCollection(db, cid, fid, copy); ok {
					perPerson[p.name]++
					added = append(added, Pair{cid, fid})
				}
			}
			if err != nil {
				failed = append(failed, Failure{fileName(db, fid), err.Error()})
			}
		}
	}
	return perPerson, nobody, failed, added
}

// RemoveFromCollection: a copy / link just goes away; the file itself (moved here from the archive) goes back
// to its place in the archive, its copies in other collections stay.
func RemoveFromCollection(db *sql.DB, cid, fid int64) error {
	var link, original, path string
	err := db.QueryRow("SELECT link_path, original_path FROM collection_files WHERE collection_id = ? AND file_id = ?",
		cid, fid).Scan(&link, &original)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if err := db.QueryRow("SELECT path FROM files WHERE id = ?", fid).Scan(&path); err != nil {
		return err
	}
	if filepath.Clean(path) != filepath.Clean(link) {
		if exists(link) {
			if err := os.Remove(link); err != nil {
				return err
			}
		}
	} else {
		if exists(original) {
			return fmt.Errorf("на старом месте уже есть файл: %s", original)
		}
		if err := MoveFile(path, original); err != nil {
			return err
		}
		if _, err := db.Exec("UPDATE files SET path = ? WHERE id = ?", original, fid); err != nil {
			_ = MoveFile(original, path)
			return err
		}
	}
	_, err = db.Exec("DELETE FROM collection_files WHERE collection_id = ? AND file_id = ?", cid, fid)
	return err
}

// ReturnFromCollection: undo of collecting, newest first. Returns done and failures.
func ReturnFromCollection(db *sql.DB, pairs []Pair) (int, []Failure) {
	done, failed := 0, []Failure{}
	for i := len(pairs) - 1; i >= 0; i-- {
		if err := RemoveFromCollection(db, pairs[i].Collection, pairs[i].File); err != nil {
			failed = append(failed, Failure{fileName(db, pairs[i].File), err.Error()})
		} else {
			done++
		}
	}
	return done, failed
}

func fileName(db *sql.DB, fid int64) string {
	var p string
	if db.QueryRow("SELECT path FROM files WHERE id = ?", fid).Scan(&p) != nil {
		return fmt.Sprint(fid)
	}
	return p
}

// MapToWindows: a path here -> the Windows share path the user knows (\nas\home\...), for showing folders.
func (c *Config) MapToWindows(p string) string {
	best := PathMapping{}
	for _, m := range c.PathMap {
		if strings.HasPrefix(p, m.Local) && len(m.Local) > len(best.Local) {
			best = m
		}
	}
	if best.Local == "" {
		return p
	}
	return strings.ReplaceAll(best.Win+p[len(best.Local):], "/", `\`)
}

// ZipPath: <folder>.zip next to the collection folder.
func ZipPath(folder string) string { return strings.TrimRight(folder, `\/`) + ".zip" }

// ZipCollection packs the collection into <folder>.zip (stored: photos and videos are compressed already).
func ZipCollection(db *sql.DB, cid int64) (string, int, error) {
	var folder string
	if err := db.QueryRow("SELECT folder FROM collections WHERE id = ?", cid).Scan(&folder); err != nil {
		return "", 0, err
	}
	links, err := strings1(db, `SELECT c.link_path FROM collection_files c JOIN files f ON f.id = c.file_id
		WHERE c.collection_id = ? ORDER BY f.taken_at, f.path`, cid)
	if err != nil {
		return "", 0, err
	}
	out := ZipPath(folder)
	tmp := out + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", 0, err
	}
	zw := zip.NewWriter(f)
	n := 0
	used := map[string]bool{}
	for _, l := range links {
		src, err := os.Open(l)
		if err != nil {
			continue // gone meanwhile
		}
		st, _ := src.Stat()
		name := filepath.Base(l)
		for i := 1; used[name]; i++ { // same name from two folders
			ext := filepath.Ext(filepath.Base(l))
			name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(filepath.Base(l), ext), i, ext)
		}
		used[name] = true
		h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: st.ModTime()}
		w, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(w, src)
		}
		src.Close()
		if err != nil {
			zw.Close()
			f.Close()
			os.Remove(tmp)
			return "", 0, err
		}
		n++
	}
	if err := zw.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", 0, err
	}
	return out, n, os.Rename(tmp, out)
}

// FinishCollection: "sent — delete". A file that lives only here (moved from the archive) is deleted (quarantine +
// snapshot, restorable); copies and links just go away, the originals stay in the archive / other collections.
// The collection closes when nothing failed. Returns done, failures and the deleted-log ids (undo).
func (c *Config) FinishCollection(db *sql.DB, cid int64, deleteZip bool) (int, []Failure, []int64, error) {
	var folder string
	if err := db.QueryRow("SELECT folder FROM collections WHERE id = ?", cid).Scan(&folder); err != nil {
		return 0, nil, nil, err
	}
	type row struct {
		fid  int64
		link string
	}
	rows, err := db.Query("SELECT file_id, link_path FROM collection_files WHERE collection_id = ?", cid)
	if err != nil {
		return 0, nil, nil, err
	}
	var all []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.fid, &r.link) == nil {
			all = append(all, r)
		}
	}
	rows.Close()
	done, failed, last := 0, []Failure{}, []string{}
	for _, r := range all {
		others, err := strings1(db, "SELECT link_path FROM collection_files WHERE file_id = ? AND collection_id != ?", r.fid, cid)
		if err != nil {
			return done, failed, nil, err
		}
		var path string
		if err := db.QueryRow("SELECT path FROM files WHERE id = ?", r.fid).Scan(&path); err != nil {
			continue
		}
		movedHere := filepath.Clean(path) == filepath.Clean(r.link)
		if movedHere && len(others) == 0 {
			last = append(last, path)
			continue
		}
		if exists(r.link) {
			if _, err := c.quarantine(r.link); err != nil {
				failed = append(failed, Failure{r.link, err.Error()})
				continue
			}
		}
		if movedHere {
			if _, err := db.Exec("UPDATE files SET path = ? WHERE id = ?", others[0], r.fid); err != nil {
				return done, failed, nil, err
			}
		}
		if _, err := db.Exec("DELETE FROM collection_files WHERE collection_id = ? AND file_id = ?", cid, r.fid); err != nil {
			return done, failed, nil, err
		}
		done++
	}
	d, f, logIDs, err := c.Trash(db, last)
	if err != nil {
		return done, failed, nil, err
	}
	done, failed = done+d, append(failed, f...)
	if z := ZipPath(folder); deleteZip && exists(z) {
		if _, err := c.quarantine(z); err != nil {
			failed = append(failed, Failure{z, err.Error()})
		}
	}
	if len(failed) == 0 {
		if _, err := db.Exec("DELETE FROM collections WHERE id = ?", cid); err != nil {
			return done, failed, logIDs, err
		}
		_ = os.Remove(folder) // only if empty
	}
	return done, failed, logIDs, nil
}
