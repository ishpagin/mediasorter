// Package setup downloads what the analysis needs on first start: the models (InsightFace buffalo_l,
// SigLIP2) and ffmpeg for this platform. Downloads resume after a dropped connection; files appear only
// when complete.
package setup

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	buffalo = "https://github.com/deepinsight/insightface/releases/download/v0.7/buffalo_l.zip"
	siglip  = "https://huggingface.co/immich-app/ViT-B-16-SigLIP2__webli/resolve/main/"
)

var siglipFiles = []string{"config.json", "visual/model.onnx", "visual/preprocess_cfg.json", "textual/model.onnx",
	"textual/tokenizer.json", "textual/tokenizer_config.json", "textual/special_tokens_map.json"}

// ffmpeg builds per platform: archives with ffmpeg and ffprobe inside (first source that works wins).
var ffmpegSources = map[string][][]string{
	"windows/amd64": {{"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-n8.1-latest-win64-lgpl-8.1.zip"}},
	"darwin/arm64": {
		{"https://www.osxexperts.net/ffmpeg81arm.zip", "https://www.osxexperts.net/ffprobe81arm.zip"},
		{"https://ffmpeg.martin-riedl.de/redirect/latest/macos/arm64/release/ffmpeg.zip",
			"https://ffmpeg.martin-riedl.de/redirect/latest/macos/arm64/release/ffprobe.zip"},
		{"https://ffmpeg.martin-riedl.de/redirect/latest/macos/arm64/snapshot/ffmpeg.zip",
			"https://ffmpeg.martin-riedl.de/redirect/latest/macos/arm64/snapshot/ffprobe.zip"},
	},
	"darwin/amd64": {{"https://ffmpeg.martin-riedl.de/redirect/latest/macos/amd64/release/ffmpeg.zip",
		"https://ffmpeg.martin-riedl.de/redirect/latest/macos/amd64/release/ffprobe.zip"}},
	"linux/amd64": {{"https://ffmpeg.martin-riedl.de/redirect/latest/linux/amd64/release/ffmpeg.zip",
		"https://ffmpeg.martin-riedl.de/redirect/latest/linux/amd64/release/ffprobe.zip"}},
	"linux/arm64": {{"https://ffmpeg.martin-riedl.de/redirect/latest/linux/arm64/release/ffmpeg.zip",
		"https://ffmpeg.martin-riedl.de/redirect/latest/linux/arm64/release/ffprobe.zip"}},
}

// Progress: a line for the user and the share done (0..1).
type Progress func(msg string, f float64)

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ModelsReady: every model file is there.
func ModelsReady(dir string) bool {
	for _, f := range append([]string{"det_10g.onnx", "w600k_r50.onnx"}, prefixed("siglip2/", siglipFiles)...) {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.Size() == 0 {
			return false
		}
	}
	return true
}

// FFmpegReady: both tools are in dir.
func FFmpegReady(dir string) bool {
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if _, err := os.Stat(filepath.Join(dir, exe(n))); err != nil {
			return false
		}
	}
	return true
}

func prefixed(p string, l []string) []string {
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = p + x
	}
	return out
}

// Models downloads the missing models into dir (~1.7 GB in all).
func Models(ctx context.Context, dir string, progress Progress) error {
	face := []string{"det_10g.onnx", "w600k_r50.onnx"}
	if !exists(filepath.Join(dir, face[0])) || !exists(filepath.Join(dir, face[1])) {
		zipPath := filepath.Join(dir, "buffalo_l.zip")
		if err := fetch(ctx, buffalo, zipPath, scaled(progress, "Лица: InsightFace", 0, 0.15)); err != nil {
			return err
		}
		if err := unzip(zipPath, dir, func(name string) string {
			for _, f := range face {
				if filepath.Base(name) == f {
					return f
				}
			}
			return ""
		}); err != nil {
			return err
		}
		os.Remove(zipPath)
	}
	for i, f := range siglipFiles {
		p := filepath.Join(dir, "siglip2", filepath.FromSlash(f))
		if exists(p) {
			continue
		}
		lo := 0.15 + 0.85*float64(i)/float64(len(siglipFiles))
		if err := fetch(ctx, siglip+f, p, scaled(progress, "Сюжеты: SigLIP2", lo, lo+0.85/float64(len(siglipFiles)))); err != nil {
			return err
		}
	}
	return nil
}

// FFmpeg puts ffmpeg and ffprobe into dir (nothing to do if they are already there).
func FFmpeg(ctx context.Context, dir string, progress Progress) error {
	if FFmpegReady(dir) {
		return nil
	}
	sources := ffmpegSources[runtime.GOOS+"/"+runtime.GOARCH]
	if len(sources) == 0 {
		return fmt.Errorf("нет готовой сборки ffmpeg для %s/%s: установи ffmpeg сам", runtime.GOOS, runtime.GOARCH)
	}
	var last error
	for _, urls := range sources {
		last = nil
		for i, u := range urls {
			arch := filepath.Join(dir, fmt.Sprintf("ffmpeg-%d.zip", i))
			if err := fetch(ctx, u, arch, scaled(progress, "Видео: ffmpeg", float64(i)/float64(len(urls)),
				float64(i+1)/float64(len(urls)))); err != nil {
				last = err
				break
			}
			err := unzip(arch, dir, func(name string) string {
				b := filepath.Base(name)
				if b == exe("ffmpeg") || b == exe("ffprobe") {
					return b
				}
				return ""
			})
			os.Remove(arch)
			if err != nil {
				last = err
				break
			}
		}
		if last == nil && FFmpegReady(dir) {
			for _, n := range []string{"ffmpeg", "ffprobe"} {
				os.Chmod(filepath.Join(dir, exe(n)), 0o755)
			}
			unquarantine(dir)
			return nil
		}
	}
	if last == nil {
		last = fmt.Errorf("в архиве нет ffmpeg/ffprobe")
	}
	return last
}

// unquarantine: downloaded tools on macOS must not be blocked by Gatekeeper's quarantine flag.
func unquarantine(dir string) {
	if runtime.GOOS == "darwin" {
		exec.Command("xattr", "-dr", "com.apple.quarantine", dir).Run()
	}
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

func scaled(p Progress, msg string, lo, hi float64) func(done, total int64) {
	return func(done, total int64) {
		if p == nil {
			return
		}
		f := 0.0
		if total > 0 {
			f = float64(done) / float64(total)
		}
		p(fmt.Sprintf("%s: %d из %d МБ", msg, done>>20, total>>20), lo+(hi-lo)*f)
	}
}

// fetch downloads url to dst via dst.part, resuming after errors (up to 10 tries).
func fetch(ctx context.Context, url, dst string, progress func(done, total int64)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	part := dst + ".part"
	var lastErr error
	for try := 0; try < 10; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
		done, total, err := fetchOnce(ctx, url, part, progress)
		if err == nil {
			return os.Rename(part, dst)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = fmt.Errorf("%s: %w (скачано %d из %d МБ)", url, err, done>>20, total>>20)
	}
	return lastErr
}

func fetchOnce(ctx context.Context, url, part string, progress func(done, total int64)) (int64, int64, error) {
	have := int64(0)
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "mediasorter")
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return have, 0, err
	}
	defer res.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	switch {
	case res.StatusCode == http.StatusPartialContent:
	case res.StatusCode == http.StatusOK:
		have, flags = 0, os.O_CREATE|os.O_WRONLY|os.O_TRUNC // the server ignored Range: start over
	case res.StatusCode == http.StatusRequestedRangeNotSatisfiable && have > 0:
		return have, have, nil // already complete
	default:
		return have, 0, fmt.Errorf("HTTP %s", res.Status)
	}
	total := have + res.ContentLength
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return have, total, err
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	last := time.Time{}
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return have, total, werr
			}
			have += int64(n)
			if progress != nil && time.Since(last) > 300*time.Millisecond {
				progress(have, total)
				last = time.Now()
			}
		}
		if err == io.EOF {
			if progress != nil {
				progress(have, total)
			}
			if res.ContentLength >= 0 && have < total {
				return have, total, io.ErrUnexpectedEOF
			}
			return have, total, nil
		}
		if err != nil {
			return have, total, err
		}
	}
}

// unzip extracts the entries pick names (pick returns the file name to write, "" = skip) into dir.
func unzip(path, dir string, pick func(name string) string) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		name := pick(f.Name)
		if name == "" || f.FileInfo().IsDir() || strings.Contains(name, "..") {
			continue
		}
		if err := extract(f, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func extract(f *zip.File, dst string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	tmp := dst + ".part"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
