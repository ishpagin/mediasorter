package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/menu"
	wrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/db"
	"github.com/ishpagin/mediasorter/web/internal/engine"
	"github.com/ishpagin/mediasorter/web/internal/media"
	"github.com/ishpagin/mediasorter/web/internal/scan"
	"github.com/ishpagin/mediasorter/web/internal/server"
	"github.com/ishpagin/mediasorter/web/internal/setup"
)

func isMac() bool { return runtime.GOOS == "darwin" }

// dataDir: where the app keeps its database, models and settings (Windows: %LOCALAPPDATA%\Mediasorter).
func dataDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	name := "Mediasorter"
	if runtime.GOOS == "linux" {
		name = "mediasorter"
	}
	dir := filepath.Join(base, name)
	return dir, os.MkdirAll(dir, 0o755)
}

// Settings of the app itself (the folders to scan live in the database).
type Settings struct {
	Mode   string   `json:"mode"`   // "" (not chosen yet) | local | remote
	Server string   `json:"server"` // remote: the server's address
	Recent []string `json:"recent"` // servers used before
}

// App: the state behind the window.
type App struct {
	dir  string
	base string // the local server, http://127.0.0.1:port
	ctx  context.Context
	menu *menu.Menu

	mu       sync.Mutex
	settings Settings

	d   *sql.DB
	cfg *core.Config
	eng *engine.Engine
	mux http.Handler

	systemFF bool // ffmpeg is installed in the system: nothing to download

	setupMu  sync.Mutex
	setupRun bool
	setupMsg string
	setupF   float64
	setupErr string
}

func newApp(dir string) (*App, error) {
	a := &App{dir: dir}
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
		json.Unmarshal(b, &a.settings)
	}
	dbPath := filepath.Join(dir, "mediasorter.db")
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(d); err != nil {
		return nil, err
	}
	if err := core.InitUndo(d); err != nil {
		return nil, err
	}
	a.d = d
	// the archive is the user's own folders: nothing is protected by default
	cfg := &core.Config{DBPath: dbPath, Protected: filepath.Join(dir, "protected.txt")}
	a.cfg = cfg
	a.syncRoots()
	media.FFmpeg = filepath.Join(a.ffmpegDir(), exeName("ffmpeg"))
	media.FFprobe = filepath.Join(a.ffmpegDir(), exeName("ffprobe"))
	if !setup.FFmpegReady(a.ffmpegDir()) {
		ff, err1 := exec.LookPath("ffmpeg")
		fp, err2 := exec.LookPath("ffprobe")
		if err1 == nil && err2 == nil {
			media.FFmpeg, media.FFprobe, a.systemFF = ff, fp, true
		}
	}
	opt := scan.DefaultOptions()
	opt.Workers = max(1, min(4, runtime.NumCPU()/2))
	a.eng = engine.New(engine.Config{
		DB:      d,
		Models:  a.modelsDir(),
		ORT:     a.ortPath,
		Sources: func() []string { return engine.Sources(d, nil) },
		MapPath: func(p string) string { return p },
		Scan:    opt,
	})
	mux, err := server.Mux(d, cfg, a.eng)
	if err != nil {
		return nil, err
	}
	a.mux = mux
	if a.settings.Mode == "local" { // finish an interrupted download, rescan a stale archive
		a.prepare(false)
	}
	return a, nil
}

// syncRoots: each photo folder is its own "share": deleted files go to <folder>/_photosorter_deleted
// (a quick move on the same disk or NAS) instead of next to the database.
func (a *App) syncRoots() {
	var m []core.PathMapping
	for _, s := range engine.Sources(a.d, nil) {
		m = append(m, core.PathMapping{Win: s, Local: s})
	}
	a.cfg.PathMap = m
}

func exeName(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

func (a *App) modelsDir() string { return filepath.Join(a.dir, "models") }
func (a *App) ffmpegDir() string { return filepath.Join(a.dir, "ffmpeg") }

func (a *App) cudaDir() string { return filepath.Join(a.dir, "cuda") }

// ortPath: the downloaded CUDA build on Linux with NVIDIA, else ONNX Runtime shipped with the app (next to
// the binary; on macOS in the bundle's Frameworks).
func (a *App) ortPath() string {
	if p := os.Getenv("MS_ORT"); p != "" {
		return p
	}
	if setup.CUDAReady(a.cudaDir()) {
		return filepath.Join(a.cudaDir(), "libonnxruntime.so")
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	d := filepath.Dir(exe)
	for _, p := range []string{
		filepath.Join(d, "onnxruntime.dll"),
		filepath.Join(d, "..", "Frameworks", "libonnxruntime.dylib"),
		filepath.Join(d, "libonnxruntime.dylib"),
		filepath.Join(d, "libonnxruntime.so"),
		filepath.Join(d, "lib", "libonnxruntime.so"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (a *App) save() error {
	b, _ := json.MarshalIndent(a.settings, "", "  ")
	return os.WriteFile(filepath.Join(a.dir, "settings.json"), b, 0o644)
}

// startURL: where the window (or a browser) should be now.
func (a *App) startURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settings.Mode == "remote" && a.settings.Server != "" {
		return a.settings.Server
	}
	return a.base + "/"
}

// open sends the window to a page of the local server.
func (a *App) open(path string) {
	wrt.WindowExecJS(a.ctx, fmt.Sprintf("location.href = %q", a.base+path))
}

func (a *App) beforeClose(ctx context.Context) bool {
	if !a.eng.Running() {
		return false
	}
	res, _ := wrt.MessageDialog(ctx, wrt.MessageDialogOptions{
		Type:    wrt.QuestionDialog,
		Title:   "Идёт разбор фото",
		Message: "Закрыть Mediasorter? Разбор продолжится при следующем запуске с того же места.",
	})
	return !(res == "Yes" || res == "Ok" || res == "OK" || res == "Да")
}

func (a *App) shutdown() {
	a.eng.Stop()
	for i := 0; i < 50 && a.eng.Running(); i++ { // let the current file finish writing
		time.Sleep(100 * time.Millisecond)
	}
	a.d.Close()
}

// handler: /api/desktop/* for the connect page, the rest is the regular app.
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/desktop/state", a.state)
	mux.HandleFunc("POST /api/desktop/pick", a.pick)
	mux.HandleFunc("POST /api/desktop/local", a.local)
	mux.HandleFunc("POST /api/desktop/remote", a.remote)
	mux.HandleFunc("POST /api/desktop/scan", func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.eng.StartJob("scan"); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.Handle("/", a.mux)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// GET /api/desktop/state: what the connect page shows.
func (a *App) state(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	s := a.settings
	a.mu.Unlock()
	a.setupMu.Lock()
	st := map[string]any{"running": a.setupRun, "message": a.setupMsg, "progress": a.setupF, "error": a.setupErr,
		"ready": setup.ModelsReady(a.modelsDir()) && (a.systemFF || setup.FFmpegReady(a.ffmpegDir()))}
	a.setupMu.Unlock()
	var files int
	a.d.QueryRow("SELECT COUNT(*) FROM files").Scan(&files)
	last := ""
	if t := a.eng.LastScan(); !t.IsZero() {
		last = t.Format(time.RFC3339)
	}
	writeJSON(w, map[string]any{
		"mode": s.Mode, "server": s.Server, "recent": s.Recent,
		"sources": engine.Sources(a.d, []string{}), "files": files, "lastScan": last,
		"setup": st, "dataDir": a.dir,
	})
}

// POST /api/desktop/pick: the system's folder dialog; {"path": ""} when cancelled.
func (a *App) pick(w http.ResponseWriter, r *http.Request) {
	p, err := wrt.OpenDirectoryDialog(a.ctx, wrt.OpenDialogOptions{Title: "Папка с фото и видео"})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"path": p})
}

// POST /api/desktop/local {"sources": [...]}: work on this computer: save the folders, download what is
// missing, then scan.
func (a *App) local(w http.ResponseWriter, r *http.Request) {
	var req struct{ Sources []string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Sources) == 0 {
		http.Error(w, "выбери хотя бы одну папку", http.StatusBadRequest)
		return
	}
	var clean []string
	for _, s := range req.Sources {
		if st, err := os.Stat(s); err != nil || !st.IsDir() {
			http.Error(w, "папка не найдена: "+s, http.StatusBadRequest)
			return
		}
		clean = append(clean, filepath.Clean(s))
	}
	if err := engine.SetSources(a.d, clean); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.syncRoots()
	a.mu.Lock()
	a.settings.Mode = "local"
	err := a.save()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.prepare(true)
	writeJSON(w, map[string]bool{"ok": true})
}

// prepare downloads the models and ffmpeg if needed, then starts a scan (always when asked, or when the
// last one is older than 12 hours).
func (a *App) prepare(scanNow bool) {
	a.setupMu.Lock()
	if a.setupRun {
		a.setupMu.Unlock()
		return
	}
	a.setupRun, a.setupErr = true, ""
	a.setupMu.Unlock()
	go func() {
		progress := func(lo, hi float64) setup.Progress {
			return func(msg string, f float64) {
				a.setupMu.Lock()
				a.setupMsg, a.setupF = msg, lo+(hi-lo)*f
				a.setupMu.Unlock()
			}
		}
		ctx := context.Background()
		var err error
		lo := 0.0
		if setup.NVIDIA() && os.Getenv("MS_GPU") != "0" {
			lo = 0.5
			if err := setup.CUDA(ctx, a.cudaDir(), progress(0, lo)); err != nil {
				log.Printf("cuda: %v", err) // not fatal: the CPU then, and the download resumes next start
			}
		}
		if !a.systemFF {
			err = setup.FFmpeg(ctx, a.ffmpegDir(), progress(lo, lo+0.1*(1-lo)))
		}
		if err == nil {
			err = setup.Models(ctx, a.modelsDir(), progress(lo+0.1*(1-lo), 1))
		}
		a.setupMu.Lock()
		a.setupRun = false
		if err != nil {
			a.setupErr = err.Error()
			log.Printf("setup: %v", err)
		} else {
			a.setupMsg, a.setupF = "Готово", 1
		}
		a.setupMu.Unlock()
		if err == nil && len(engine.Sources(a.d, nil)) > 0 && (scanNow || a.eng.Stale()) {
			if _, err := a.eng.StartJob("scan"); err != nil {
				log.Printf("scan: %v", err)
			}
		}
	}()
}

// POST /api/desktop/remote {"server": "http://..."}: check the address and remember it.
func (a *App) remote(w http.ResponseWriter, r *http.Request) {
	var req struct{ Server string }
	json.NewDecoder(r.Body).Decode(&req)
	s := strings.TrimRight(strings.TrimSpace(req.Server), "/")
	if s != "" && !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		http.Error(w, "адрес вида http://192.168.1.10:8501", http.StatusBadRequest)
		return
	}
	if u.Port() == "" && u.Scheme == "http" {
		u.Host += ":8501"
	}
	s = u.String()
	c := http.Client{Timeout: 5 * time.Second}
	res, err := c.Get(s + "/api/meta")
	if err != nil {
		log.Printf("remote %s: %v", s, err)
		http.Error(w, "По адресу "+s+" никто не отвечает. Проверь адрес и что сервер включён (и что этот компьютер в той же сети).", http.StatusBadGateway)
		return
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		http.Error(w, "По адресу "+s+" отвечает что-то другое, не Mediasorter (ответ "+res.Status+").", http.StatusBadGateway)
		return
	}
	a.mu.Lock()
	a.settings.Mode, a.settings.Server = "remote", s
	recent := []string{s}
	for _, x := range a.settings.Recent {
		if x != s && len(recent) < 5 {
			recent = append(recent, x)
		}
	}
	a.settings.Recent = recent
	err = a.save()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"url": s})
}
