// mediasorter-web: the web UI (Vue app + JSON API) over the photosorter database.
//
//	PS_DB=/data/faces.db PS_PATH_MAP=... MS_PROTECTED=/ps/protected.txt MS_ML_URL=http://...:8503 MS_WEB_ADDR=:8511 mediasorter-web
package main

import (
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	web "github.com/ishpagin/mediasorter/web"
	"github.com/ishpagin/mediasorter/web/internal/api"
	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/db"
	"github.com/ishpagin/mediasorter/web/internal/ml"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	dbPath := env("PS_DB", "/data/faces.db")
	d, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	cfg := &core.Config{DBPath: dbPath, PathMap: core.ParsePathMap(os.Getenv("PS_PATH_MAP")),
		Protected: env("MS_PROTECTED", "/ps/protected.txt")}
	if err := core.InitUndo(d); err != nil {
		log.Fatalf("undo table: %v", err)
	}
	mux := http.NewServeMux()
	mlc := ml.New(env("MS_ML_URL", "http://127.0.0.1:8503"), os.Getenv("PS_QUEUE_TOKEN"),
		filepath.Join(filepath.Dir(dbPath), "queue_token"))
	(&api.Server{DB: d, Cfg: cfg, ML: mlc}).Routes(mux)

	dist, err := fs.Sub(web.Dist, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	files := http.FileServerFS(dist)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// the Vue router owns every other path: unknown ones get index.html (F5 on /people works)
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

	addr := env("MS_WEB_ADDR", ":8511")
	log.Printf("mediasorter-web on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
