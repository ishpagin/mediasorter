// Package server: the HTTP side shared by the server and the desktop app: JSON API + the Vue app.
package server

import (
	"database/sql"
	"io/fs"
	"net/http"
	"strings"

	web "github.com/ishpagin/mediasorter/web"
	"github.com/ishpagin/mediasorter/web/internal/api"
	"github.com/ishpagin/mediasorter/web/internal/core"
)

// Mux: every API endpoint and the single-page app (unknown paths get index.html, so F5 on /people works).
func Mux(d *sql.DB, cfg *core.Config, ml api.ML) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	(&api.Server{DB: d, Cfg: cfg, ML: ml}).Routes(mux)
	dist, err := fs.Sub(web.Dist, "frontend/dist")
	if err != nil {
		return nil, err
	}
	files := http.FileServerFS(dist)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if _, err := fs.Stat(dist, name); err == nil {
				if strings.HasPrefix(name, "assets/") { // hashed file names: cache forever
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, dist, "index.html")
	})
	return mux, nil
}
