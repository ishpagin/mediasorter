// Package core: actions that change files and the database (delete / restore, collections), ported from
// photosorter.py with the same rules, so both UIs and the CLI agree.
package core

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// QuarantineDir: deleted files are moved here (at the root of their share), not erased.
const QuarantineDir = "_photosorter_deleted"

// Config: the same environment as the Python side (PS_DB, PS_PATH_MAP) plus the protected.txt location.
type Config struct {
	DBPath    string
	PathMap   []PathMapping
	Protected string // protected.txt: folders nothing may be deleted from

	ImportDir   string // MS_IMPORT_DIR: where /api/import puts mail photos; empty = import off
	ImportToken string // MS_IMPORT_TOKEN: Bearer token of /api/import; empty = import off
}

// PathMapping: a Windows share prefix of old DB rows (//nas/home/) and its mount point here.
type PathMapping struct{ Win, Local string }

// ParsePathMap parses PS_PATH_MAP: "//nas/home/=/mnt/nas-homes/user/;//nas/homes/=/mnt/nas-homes/".
func ParsePathMap(s string) []PathMapping {
	var out []PathMapping
	for _, m := range strings.Split(s, ";") {
		w, l, ok := strings.Cut(m, "=")
		if ok {
			out = append(out, PathMapping{strings.ReplaceAll(w, `\`, "/"), l})
		}
	}
	return out
}

// MapToLocal: a Windows path of the old DB / protected.txt -> this machine (longest prefix wins).
func (c *Config) MapToLocal(p string) string {
	m := append([]PathMapping(nil), c.PathMap...)
	sort.Slice(m, func(i, j int) bool { return len(m[i].Win) > len(m[j].Win) })
	q := strings.ReplaceAll(p, `\`, "/")
	for _, x := range m {
		if strings.HasPrefix(q, x.Win) {
			return x.Local + q[len(x.Win):]
		}
	}
	return p
}

func (c *Config) protectedRoots() []string {
	data, err := os.ReadFile(c.Protected)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, filepath.Clean(c.MapToLocal(l)))
		}
	}
	return out
}

// slash: a cleaned path with forward slashes, so comparisons work the same on Linux and Windows (tests)
func slash(p string) string { return filepath.ToSlash(filepath.Clean(p)) }

// IsProtected: the path is inside a folder listed in protected.txt.
func (c *Config) IsProtected(path string) bool {
	p := slash(path)
	for _, r := range c.protectedRoots() {
		r = slash(r)
		if p == r || strings.HasPrefix(p, strings.TrimRight(r, "/")+"/") {
			return true
		}
	}
	return false
}

// QuarantinePath: <share root>/_photosorter_deleted/<same path>; a file outside the shares goes next to the DB.
func (c *Config) QuarantinePath(path string) string {
	p := slash(path)
	root := ""
	for _, m := range c.PathMap {
		l := strings.TrimRight(slash(m.Local), "/")
		if strings.HasPrefix(p, l+"/") && len(l) > len(root) {
			root = l
		}
	}
	if root == "" {
		abs, _ := filepath.Abs(c.DBPath)
		rel := strings.TrimLeft(strings.TrimPrefix(p, filepath.VolumeName(path)), "/")
		return filepath.Join(filepath.Dir(abs), QuarantineDir, filepath.FromSlash(rel))
	}
	return filepath.FromSlash(root + "/" + QuarantineDir + "/" + strings.TrimLeft(p[len(root):], "/"))
}

// FreePath: path itself, or "name (1).ext", "name (2).ext"... if taken.
func FreePath(path string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for n := 1; ; n++ {
		// free, or not checkable (bad name, no access): the write that follows reports the real error
		if _, err := os.Lstat(path); err != nil || n > 10000 {
			return path
		}
		path = fmt.Sprintf("%s (%d)%s", base, n, ext)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// MoveFile: a rename on the same disk; otherwise copy (keeping the modification time) and delete.
func MoveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := CopyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// CopyFile copies data and the modification time; a half-written copy is removed.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if exists(dst) {
		return fmt.Errorf("уже есть файл: %s", dst)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}

// LinkOrCopy: a hard link (no extra space, same disk), falling back to a copy.
func LinkOrCopy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return err
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return CopyFile(src, dst)
}

// quarantine moves a file to its quarantine path (a free name there) and returns where it went.
func (c *Config) quarantine(p string) (string, error) {
	q := FreePath(c.QuarantinePath(p))
	if err := MoveFile(p, q); err != nil {
		return "", err
	}
	return q, nil
}
