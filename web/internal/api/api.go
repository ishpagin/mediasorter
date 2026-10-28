// Package api is the JSON API of the web UI and the preview / original file endpoints.
package api

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/ml"
)

// ML: the analysis side (the Go engine in-process, or the Python service).
type ML interface {
	EmbedText(text string) ([]float32, error)
	Convert(path string) ([]byte, error)
	StartJob(name string) (ml.Job, error)
}

type Server struct {
	DB  *sql.DB
	Cfg *core.Config

	ML ML

	similarCache atomic.Value // *centroidCache: mean faces of people, recomputed when faces change
	simMu        sync.Mutex
	clipCache    atomic.Value // *clipIndex: SigLIP2 vectors of files, recomputed when the clip table changes
	clipMu       sync.Mutex
}

// Routes registers every endpoint on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/meta", s.meta)
	mux.HandleFunc("GET /api/files", s.files)
	mux.HandleFunc("POST /api/delete", s.delete)
	mux.HandleFunc("GET /api/undo", s.undo)
	mux.HandleFunc("POST /api/undo", s.undo)
	mux.HandleFunc("GET /api/collect/people", s.collectPeople)
	mux.HandleFunc("POST /api/collect", s.collect)
	mux.HandleFunc("POST /api/collect/by-people", s.collectByPeople)
	mux.HandleFunc("POST /api/collections", s.newCollection)
	mux.HandleFunc("GET /api/collections", s.collectionsList)
	mux.HandleFunc("PUT /api/collections/root", s.collectionsRoot)
	mux.HandleFunc("GET /api/collections/{id}/files", s.collectionFiles)
	mux.HandleFunc("POST /api/collections/{id}/remove", s.collectionRemove)
	mux.HandleFunc("POST /api/collections/{id}/zip", s.collectionZip)
	mux.HandleFunc("POST /api/collections/{id}/finish", s.collectionFinish)
	mux.HandleFunc("DELETE /api/collections/{id}", s.collectionDelete)
	mux.HandleFunc("GET /api/persons", s.persons)
	mux.HandleFunc("GET /api/persons/all", s.personsAll)
	mux.HandleFunc("POST /api/persons", s.personCreate)
	mux.HandleFunc("PATCH /api/persons/{id}", s.personPatch)
	mux.HandleFunc("POST /api/persons/merge", s.personsMerge)
	mux.HandleFunc("POST /api/persons/hide", s.personsHide)
	mux.HandleFunc("GET /api/person/{id}", s.person)
	mux.HandleFunc("GET /api/person/{id}/files", s.personFiles)
	mux.HandleFunc("POST /api/person/{id}/unlink", s.personUnlink)
	mux.HandleFunc("POST /api/person/{id}/remap", s.personRemap)
	mux.HandleFunc("GET /api/only", s.only)
	mux.HandleFunc("GET /api/faces", s.faces)
	mux.HandleFunc("POST /api/faces/assign", s.facesAssign)
	mux.HandleFunc("GET /api/similar", s.similar)
	mux.HandleFunc("POST /api/notsame", s.notSame)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/stacks", s.stacks)
	mux.HandleFunc("GET /api/copies", s.copies)
	mux.HandleFunc("GET /api/jobs", s.jobs)
	mux.HandleFunc("POST /api/jobs", s.jobs)
	mux.HandleFunc("POST /api/import", s.importMail)
	mux.HandleFunc("GET /thumb/face/{id}", s.faceThumb)
	mux.HandleFunc("GET /preview/{id}", s.preview)
	mux.HandleFunc("GET /original/{id}", s.original)
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") { // file lists are large: tens of thousands of rows
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_ = json.NewEncoder(gz).Encode(v)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error, code int) {
	log.Printf("api: %v", err)
	http.Error(w, err.Error(), code)
}
