package api

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var browserNative = map[string]bool{ // sent as they are; videos get Range (seeking) from http.ServeContent
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true,
	".mp4": true, ".m4v": true, ".mov": true, ".webm": true,
}

func fileID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// preview: GET /preview/{id} — the stored JPEG preview (the first frame of a video); never reads the originals.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var jpeg []byte
	err := s.DB.QueryRow("SELECT jpeg FROM previews WHERE file_id = ? ORDER BY t_sec LIMIT 1", id).Scan(&jpeg)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		fail(w, err, 500)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400") // a rescan rarely changes a preview
	_, _ = w.Write(jpeg)
}

// original: GET /original/{id} — the file itself for the viewer. Only paths from the DB are served.
func (s *Server) original(w http.ResponseWriter, r *http.Request) {
	id, ok := fileID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var path string
	if err := s.DB.QueryRow("SELECT path FROM files WHERE id = ?", id).Scan(&path); err != nil {
		http.NotFound(w, r)
		return
	}
	if !browserNative[strings.ToLower(filepath.Ext(path))] {
		// HEIC / RAW / TIFF: the Python side converts (as the old viewer did); if it cannot, the stored preview
		jpeg, err := s.ML.Convert(path)
		if err != nil {
			w.Header().Set("X-Fallback", "preview")
			s.preview(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(jpeg)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "оригинал недоступен", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fail(w, err, 500)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}
