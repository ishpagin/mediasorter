package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ishpagin/mediasorter/web/internal/media"
)

var importMu sync.Mutex // one import at a time: picking a free file name and writing it must not race

// importMail: POST /api/import from inbox: mail attachments (parts "file") and meta {mail_id, from, date}.
// Saved to <ImportDir>/<year>/<date> <sender>/, the same file (sha256) only once, then a scan picks them up.
func (s *Server) importMail(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.ImportDir == "" || s.Cfg.ImportToken == "" {
		http.NotFound(w, r)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Cfg.ImportToken)) != 1 {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	var meta struct {
		MailID string    `json:"mail_id"`
		From   string    `json:"from"`
		Date   time.Time `json:"date"`
	}
	if err := json.Unmarshal([]byte(r.FormValue("meta")), &meta); err != nil {
		fail(w, fmt.Errorf("meta: %w", err), http.StatusBadRequest)
		return
	}
	parts := r.MultipartForm.File["file"]
	if len(parts) == 0 {
		fail(w, errors.New("no file parts"), http.StatusBadRequest)
		return
	}
	for _, p := range parts { // check them all first: a bad one rejects the whole request, nothing is saved
		if media.Kind(safeName(p.Filename)) == "" {
			fail(w, fmt.Errorf("%s: not a photo or video", p.Filename), http.StatusUnsupportedMediaType)
			return
		}
	}
	if meta.Date.IsZero() {
		meta.Date = time.Now()
	}
	sender := meta.From
	if a, err := mail.ParseAddress(sender); err == nil {
		sender = a.Address
		if a.Name != "" {
			sender = a.Name
		}
	}
	dir := filepath.Join(s.Cfg.ImportDir, meta.Date.Format("2006"), meta.Date.Format("2006-01-02")+" "+safeName(sender))

	importMu.Lock()
	defer importMu.Unlock()
	imported, dup := 0, 0
	for _, p := range parts {
		isNew, err := s.importOne(p, dir, meta.MailID, meta.Date)
		if err != nil {
			fail(w, fmt.Errorf("import %s: %w", p.Filename, err), http.StatusInternalServerError)
			return
		}
		if isNew {
			imported++
		} else {
			dup++
		}
	}
	if imported > 0 {
		_, _ = s.ML.StartJob("scan") // a scan already running: the nightly one picks them up
	}
	writeJSON(w, r, map[string]int{"imported": imported, "dup": dup})
}

// importOne saves one part unless a file with the same sha256 was imported before.
func (s *Server) importOne(p *multipart.FileHeader, dir, mailID string, date time.Time) (bool, error) {
	f, err := p.Open()
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM imports WHERE sha256 = ?", sum).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".import-*.part") // no media extension: a scan meanwhile skips it
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := io.Copy(tmp, f); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	_ = os.Chtimes(tmp.Name(), date, date) // photos without EXIF get the mail date
	path := freeName(filepath.Join(dir, safeName(p.Filename)))
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	_, err = s.DB.Exec("INSERT INTO imports(sha256, path, mail_id, at) VALUES (?, ?, ?, ?)",
		sum, path, mailID, time.Now().Format(time.RFC3339))
	return err == nil, err
}

// safeName: a file or folder name from mail headers, without path separators, characters
// Windows (SMB) forbids, and leading dots (no hidden files, no "..").
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimLeft(strings.TrimSpace(s), ".")
	if r := []rune(s); len(r) > 100 {
		ext := filepath.Ext(s)
		s = string(r[:100-len([]rune(ext))]) + ext
	}
	s = strings.TrimRight(s, ". ") // Windows drops trailing dots and spaces
	if s == "" {
		return "_"
	}
	return s
}

// freeName: path itself, or "name (2).ext", "name (3).ext"... when it is taken.
func freeName(path string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
		path = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}
