// Package engine runs the analysis inside the web server: the scan (on a schedule or on request), people,
// scenes, stacks, text queries and picture conversion. One long job at a time.
package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ishpagin/mediasorter/web/internal/media"
	"github.com/ishpagin/mediasorter/web/internal/ml"
	"github.com/ishpagin/mediasorter/web/internal/scan"
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

// Config of the engine.
type Config struct {
	DB      *sql.DB
	Models  string              // folder with det_10g.onnx, w600k_r50.onnx, siglip2/
	ORT     func() string       // ONNX Runtime library ("" = default name), asked on first use
	Sources func() []string     // folders to scan (read each time: the user may change them)
	MapPath func(string) string // a stored path -> a path openable here
	Scan    scan.Options
}

// Engine: see the package comment.
type Engine struct {
	cfg Config

	mu      sync.Mutex
	job     ml.Job
	cancel  context.CancelFunc
	text    *vision.Clip // the text tower (1 GB): loaded on demand, dropped after a while unused
	textUse time.Time
	textMu  sync.Mutex
}

func New(cfg Config) *Engine {
	e := &Engine{cfg: cfg}
	go e.idle()
	return e
}

// ErrNoModels: the models are not downloaded yet.
var ErrNoModels = errors.New("нейросети ещё не скачаны")

func (e *Engine) init() error {
	if _, err := os.Stat(e.cfg.Models); err != nil {
		return ErrNoModels
	}
	return vision.Init(e.cfg.ORT())
}

func (e *Engine) textModel() (*vision.Clip, error) {
	e.textMu.Lock()
	defer e.textMu.Unlock()
	e.textUse = time.Now()
	if e.text != nil {
		return e.text, nil
	}
	if err := e.init(); err != nil {
		return nil, err
	}
	c, err := vision.NewClip(e.cfg.Models, false, true)
	if err != nil {
		return nil, err
	}
	e.text = c
	return c, nil
}

func (e *Engine) idle() {
	for range time.Tick(time.Minute) {
		e.textMu.Lock()
		if e.text != nil && time.Since(e.textUse) > 15*time.Minute {
			e.text.Close()
			e.text = nil
		}
		e.textMu.Unlock()
	}
}

// EmbedText: the scene-space vector of a text query.
func (e *Engine) EmbedText(text string) ([]float32, error) {
	c, err := e.textModel()
	if err != nil {
		return nil, err
	}
	return c.Text(strings.TrimSpace(text))
}

// Convert: a JPEG (at most 3000 px) of a picture the browser cannot show (HEIC, RAW, TIFF).
func (e *Engine) Convert(path string) ([]byte, error) {
	var ok bool
	if err := e.cfg.DB.QueryRow("SELECT 1 FROM files WHERE path = ?", path).Scan(&ok); err != nil {
		return nil, fmt.Errorf("unknown file")
	}
	img, _, err := media.ReadImage(e.cfg.MapPath(path))
	if err != nil {
		return nil, err
	}
	return media.Shrink(img, 3000).JPEG(90), nil
}

// Jobs the UI can start.
var jobNames = map[string]string{
	"scan":         "Сканирование",
	"cluster":      "Пересчёт людей",
	"stacks_build": "Стопки",
	"stacks_score": "Оценка качества",
	"tag":          "Сюжеты",
}

// StartJob starts a job ("" = just the status).
func (e *Engine) StartJob(name string) (ml.Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if name == "" {
		return e.job, nil
	}
	if _, ok := jobNames[name]; !ok {
		return e.job, fmt.Errorf("unknown job")
	}
	if e.job.Running {
		return e.job, fmt.Errorf("уже идёт: %s", *e.job.Name)
	}
	n := name
	e.job = ml.Job{Name: &n, Running: true}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go func() {
		defer cancel()
		msg, err := e.run(ctx, name)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.job.Running, e.job.Progress, e.job.Message = false, 1, msg
		if err != nil {
			e.job.Error = err.Error()
			log.Printf("job %s: %v", name, err)
		} else {
			log.Printf("job %s: %s", name, msg)
		}
	}()
	return e.job, nil
}

// Stop cancels the running job (it stops between files; nothing half-written).
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
}

// Running: a job is going on.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.job.Running
}

func (e *Engine) progress(lo, hi float64) scan.Progress {
	return func(done, total int, msg string) {
		e.mu.Lock()
		defer e.mu.Unlock()
		f := 0.0
		if total > 0 {
			f = float64(done) / float64(total)
		}
		e.job.Progress = lo + (hi-lo)*min(1, f)
		e.job.Message = msg
	}
}

func (e *Engine) run(ctx context.Context, name string) (string, error) {
	if err := e.init(); err != nil {
		return "", err
	}
	switch name {
	case "scan":
		return e.Nightly(ctx)
	case "cluster":
		n, err := scan.Cluster(e.cfg.DB, scan.DefaultClusterOptions(), e.progress(0, 1))
		return fmt.Sprintf("Людей: %d", n), err
	case "stacks_build":
		k, n, err := scan.BuildStacks(e.cfg.DB, e.progress(0, 1))
		return fmt.Sprintf("Стопок: %d, фото в них: %d", k, n), err
	case "tag":
		t, err := e.textModel()
		if err == nil {
			err = scan.Tag(e.cfg.DB, t)
		}
		return "Сюжеты разложены", err
	case "stacks_score":
		t, err := e.textModel()
		if err != nil {
			return "", err
		}
		_, err = scan.Score(ctx, e.cfg.DB, t, e.cfg.MapPath, e.cfg.Scan.Workers*2, e.progress(0, 1))
		return "Качество оценено", err
	}
	return "", fmt.Errorf("unknown job")
}

// Nightly: scan every source, then people, scene vectors and tags, stacks and their quality.
// A missing source (an unmounted NAS) is skipped, never emptied; nothing scanned = stop.
func (e *Engine) Nightly(ctx context.Context) (string, error) {
	srcs := e.cfg.Sources()
	if len(srcs) == 0 {
		return "", fmt.Errorf("не выбрана папка с фото")
	}
	m, err := scan.LoadModels(e.cfg.Models, e.cfg.Scan)
	if err != nil {
		return "", err
	}
	log.Printf("scan: models on %s, %d files at once", vision.Provider, e.cfg.Scan.Workers)
	total, scanned := 0, 0
	for i, src := range srcs {
		lo := 0.7 * float64(i) / float64(len(srcs))
		n, err := scan.Scan(ctx, e.cfg.DB, m, src, e.cfg.Scan, e.progress(lo, lo+0.7/float64(len(srcs))))
		if ctx.Err() != nil {
			m.Close()
			return "", ctx.Err()
		}
		if err != nil {
			log.Printf("scan %s: %v", src, err)
			continue
		}
		total += n
		scanned++
	}
	if scanned == 0 {
		m.Close()
		return "", fmt.Errorf("ни одна папка не найдена (NAS не подключён?)")
	}
	if _, err := scan.Embed(e.cfg.DB, m.Clip, e.progress(0.7, 0.75)); err != nil {
		m.Close()
		return "", err
	}
	m.Close()
	steps := []struct {
		at  float64
		msg string
		f   func() error
	}{
		{0.75, "люди", func() error { _, err := scan.Cluster(e.cfg.DB, scan.DefaultClusterOptions(), nil); return err }},
		{0.8, "сюжеты", func() error {
			t, err := e.textModel()
			if err == nil {
				err = scan.Tag(e.cfg.DB, t)
			}
			return err
		}},
		{0.85, "стопки", func() error { _, _, err := scan.BuildStacks(e.cfg.DB, nil); return err }},
		{0.9, "качество в стопках", func() error {
			t, err := e.textModel()
			if err == nil {
				_, err = scan.Score(ctx, e.cfg.DB, t, e.cfg.MapPath, e.cfg.Scan.Workers*2, nil)
			}
			return err
		}},
	}
	for _, s := range steps {
		e.progress(s.at, s.at)(0, 1, s.msg)
		if err := s.f(); err != nil {
			return "", fmt.Errorf("%s: %w", s.msg, err)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	e.cfg.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('last_scan', ?)", time.Now().Format(time.RFC3339))
	b, _ := json.Marshal(srcs)
	e.cfg.DB.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('last_scan_sources', ?)", string(b))
	return fmt.Sprintf("Готово: новых или изменённых файлов %d", total), nil
}

// LastScan: when the last full scan finished (zero = never).
func (e *Engine) LastScan() time.Time {
	var v string
	e.cfg.DB.QueryRow("SELECT value FROM settings WHERE key = 'last_scan'").Scan(&v)
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

// Sources: the folders to scan from the "scan_sources" setting (a JSON list; set from the app),
// else fallback (the server's PS_SCAN_SRC).
func Sources(d *sql.DB, fallback []string) []string {
	var v string
	if d.QueryRow("SELECT value FROM settings WHERE key = 'scan_sources'").Scan(&v) == nil {
		var l []string
		if json.Unmarshal([]byte(v), &l) == nil && len(l) > 0 {
			return l
		}
	}
	return fallback
}

// SetSources stores the folders to scan.
func SetSources(d *sql.DB, l []string) error {
	b, err := json.Marshal(l)
	if err == nil {
		_, err = d.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('scan_sources', ?)", string(b))
	}
	return err
}

// Stale: a scan is due: never done, other folders since, or more than 12 hours ago.
func (e *Engine) Stale() bool {
	srcs := e.cfg.Sources()
	if len(srcs) == 0 {
		return false
	}
	var last string
	e.cfg.DB.QueryRow("SELECT value FROM settings WHERE key = 'last_scan_sources'").Scan(&last)
	b, _ := json.Marshal(srcs)
	return last != string(b) || time.Since(e.LastScan()) > 12*time.Hour
}

// Schedule: a scan right away if one is due (see Stale), then every day at hour:00.
func (e *Engine) Schedule(hour int) {
	if e.Stale() {
		e.StartJob("scan")
	}
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.Local)
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(time.Until(next))
			if _, err := e.StartJob("scan"); err != nil {
				log.Printf("nightly scan: %v", err)
			}
		}
	}()
}
