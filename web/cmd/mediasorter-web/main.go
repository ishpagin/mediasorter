// mediasorter-web: the web UI (Vue app + JSON API) and the analysis (scan, people, scenes, stacks).
//
//	PS_DB=/data/faces.db PS_SCAN_SRC="/photos /nas" PS_PATH_MAP=... MS_PROTECTED=protected.txt
//	MS_MODELS=models MS_ORT=libonnxruntime.so MS_WEB_ADDR=:8511 mediasorter-web [command]
//
// Commands (default: serve): serve | nightly | scan | cluster | tag | stacks | score | models (download them).
// MS_ML_URL=http://...:8503 hands the analysis to the old Python service instead.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ishpagin/mediasorter/web/internal/api"
	"github.com/ishpagin/mediasorter/web/internal/core"
	"github.com/ishpagin/mediasorter/web/internal/db"
	"github.com/ishpagin/mediasorter/web/internal/engine"
	"github.com/ishpagin/mediasorter/web/internal/ml"
	"github.com/ishpagin/mediasorter/web/internal/scan"
	"github.com/ishpagin/mediasorter/web/internal/server"
	"github.com/ishpagin/mediasorter/web/internal/setup"
)

func flagCmd() string {
	if len(os.Args) > 1 {
		return os.Args[1]
	}
	return "serve"
}

// runCommand: one analysis step from the command line (Ctrl+C stops it cleanly).
func runCommand(cmd string, eng *engine.Engine, d interface{}) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	name := map[string]string{"nightly": "scan", "scan": "scan", "cluster": "cluster", "stacks": "stacks_build",
		"score": "stacks_score", "tag": "tag"}[cmd]
	if name == "" {
		fmt.Fprintf(os.Stderr, "unknown command %q: serve | nightly | cluster | tag | stacks | score\n", cmd)
		return 2
	}
	if _, err := eng.StartJob(name); err != nil {
		log.Print(err)
		return 1
	}
	last := ""
	for {
		select {
		case <-ctx.Done():
			eng.Stop()
		default:
		}
		j, _ := eng.StartJob("")
		if line := fmt.Sprintf("%3.0f%% %s", j.Progress*100, j.Message); line != last && j.Running {
			log.Print(line)
			last = line
		}
		if !j.Running {
			if j.Error != "" {
				log.Printf("error: %s", j.Error)
				return 1
			}
			log.Print(j.Message)
			return 0
		}
		sleep()
	}
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// exeDir: the folder of this binary (models and libraries may sit next to it).
func exeDir() string {
	if p, err := os.Executable(); err == nil {
		return filepath.Dir(p)
	}
	return "."
}

func main() {
	if flagCmd() == "models" { // download the models (Docker build): no database needed
		err := setup.Models(context.Background(), env("MS_MODELS", filepath.Join(exeDir(), "models")), func(msg string, f float64) {
			log.Printf("%3.0f%% %s", f*100, msg)
		})
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	dbPath := env("PS_DB", "/data/faces.db")
	d, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if err := db.Migrate(d); err != nil {
		log.Fatalf("db schema: %v", err)
	}
	cfg := &core.Config{DBPath: dbPath, PathMap: core.ParsePathMap(os.Getenv("PS_PATH_MAP")),
		Protected: env("MS_PROTECTED", "/ps/protected.txt")}
	if err := core.InitUndo(d); err != nil {
		log.Fatalf("undo table: %v", err)
	}
	opt := scan.DefaultOptions()
	opt.Workers = max(1, min(8, runtime.NumCPU()/2))
	if n, err := strconv.Atoi(os.Getenv("MS_SCAN_WORKERS")); err == nil && n > 0 {
		opt.Workers = n
	}
	eng := engine.New(engine.Config{
		DB:      d,
		Models:  env("MS_MODELS", filepath.Join(exeDir(), "models")),
		ORT:     func() string { return os.Getenv("MS_ORT") },
		Sources: func() []string { return engine.Sources(d, strings.Fields(os.Getenv("PS_SCAN_SRC"))) },
		MapPath: cfg.MapToLocal,
		Scan:    opt,
	})

	if cmd := flagCmd(); cmd != "serve" {
		os.Exit(runCommand(cmd, eng, d))
	}

	var mlSide api.ML = eng
	if u := os.Getenv("MS_ML_URL"); u != "" {
		mlSide = ml.New(u, os.Getenv("PS_QUEUE_TOKEN"), filepath.Join(filepath.Dir(dbPath), "queue_token"))
	} else if os.Getenv("MS_SCAN") != "0" {
		hour, _ := strconv.Atoi(env("MS_SCAN_HOUR", "3"))
		eng.Schedule(hour)
	}
	mux, err := server.Mux(d, cfg, mlSide)
	if err != nil {
		log.Fatal(err)
	}

	addr := env("MS_WEB_ADDR", ":8511")
	log.Printf("mediasorter-web on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func sleep() { <-time.After(2 * time.Second) }
