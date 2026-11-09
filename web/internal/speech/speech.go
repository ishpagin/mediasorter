// Package speech turns the speech in videos into text with times: GigaAM v3 in this process (vision.Speech)
// or a shared service on the home server (MS_ASR_URL). The text goes to the speech table: read under the
// video, found by the search.
package speech

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/ishpagin/mediasorter/web/internal/media"
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

// Segment: a phrase and when it was said, seconds from the start of the video.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Recognizer: mono 16 kHz audio in, phrases out.
type Recognizer func(ctx context.Context, pcm []float32) ([]Segment, error)

// errService: the remote service failed; the job stops instead of marking every video as failed.
var errService = errors.New("сервис распознавания речи недоступен")

// Audio: the first audio track of a file as mono 16 kHz float32; nil when the file has no sound.
// shortcut: the whole track in memory (~230 MB per hour), stream it if hours-long videos show up.
func Audio(ctx context.Context, path string) ([]float32, error) {
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, media.FFmpeg, "-v", "error", "-nostdin", "-i", path, "-map", "0:a:0", "-vn",
		"-ac", "1", "-ar", fmt.Sprint(vision.SpeechRate), "-f", "f32le", "-")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "matches no streams") {
			return nil, nil
		}
		return nil, fmt.Errorf("ffmpeg: %s", strings.TrimSpace(stderr.String()))
	}
	b := out.Bytes()
	pcm := make([]float32, len(b)/4)
	for i := range pcm {
		pcm[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return pcm, nil
}

const (
	chunkMax = 30 * vision.SpeechRate // the model is good up to ~30 s at once
	chunkMin = 20 * vision.SpeechRate
	quiet    = vision.SpeechRate / 10 // 100 ms windows when looking for a pause to cut at
)

// cuts: where to split the audio into pieces of chunkMin..chunkMax samples, each cut at the quietest
// 100 ms in that range (a pause, so words stay whole). The last cut is len(pcm).
func cuts(pcm []float32) []int {
	var out []int
	for at := 0; at < len(pcm); {
		if len(pcm)-at <= chunkMax {
			return append(out, len(pcm))
		}
		best, bestE := at+chunkMax, math.Inf(1)
		for w := at + chunkMin; w+quiet <= at+chunkMax; w += quiet / 2 {
			e := 0.0
			for _, x := range pcm[w : w+quiet] {
				e += float64(x) * float64(x)
			}
			if e < bestE {
				best, bestE = w+quiet/2, e
			}
		}
		out = append(out, best)
		at = best
	}
	return out
}

// Local: recognition with the model in this process.
func Local(m *vision.Speech) Recognizer {
	return func(ctx context.Context, pcm []float32) ([]Segment, error) {
		var toks []vision.Token
		from := 0
		for _, to := range cuts(pcm) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			t, err := m.Recognize(pcm[from:to])
			if err != nil {
				return nil, err
			}
			for _, x := range t {
				toks = append(toks, vision.Token{Text: x.Text, At: x.At + float64(from)/vision.SpeechRate})
			}
			from = to
		}
		return Segments(toks), nil
	}
}

// Segments: tokens into phrases, split after . ! ? … and at pauses over 1.5 s.
func Segments(toks []vision.Token) []Segment {
	var out []Segment
	var b strings.Builder
	start, last := 0.0, 0.0
	flush := func() {
		if t := join(b.String()); strings.IndexFunc(t, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			out = append(out, Segment{start, last + 0.3, t})
		}
		b.Reset()
	}
	for _, t := range toks {
		if b.Len() > 0 && t.At-last > 1.5 {
			flush()
		}
		if b.Len() == 0 {
			start = t.At
		}
		b.WriteString(t.Text)
		last = t.At
		if s := strings.TrimSpace(t.Text); s != "" && strings.ContainsAny(s[len(s)-1:], ".!?") || strings.HasSuffix(s, "…") {
			flush()
		}
	}
	flush()
	for i := 1; i < len(out); i++ {
		out[i-1].End = min(out[i-1].End, out[i].Start)
	}
	return out
}

// join: the tokens' text with spaces fixed: no double spaces, none before closing punctuation.
func join(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	r := []rune(s)
	for i, c := range r {
		if c == ' ' && i+1 < len(r) && strings.ContainsRune(".,!?:;)…", r[i+1]) {
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Remote: recognition by the shared service: POST {url}/transcribe with a WAV,
// answer {"text": "...", "segments": [{"start", "end", "text"}]}.
func Remote(url string) Recognizer {
	client := &http.Client{Timeout: 30 * time.Minute}
	return func(ctx context.Context, pcm []float32) ([]Segment, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(url, "/")+"/transcribe",
			bytes.NewReader(wav(pcm)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "audio/wav")
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: %v", errService, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%w: HTTP %d", errService, resp.StatusCode)
		}
		var r struct{ Segments []Segment }
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, fmt.Errorf("%w: %v", errService, err)
		}
		out := r.Segments[:0]
		for _, s := range r.Segments {
			if s.Text = strings.TrimSpace(s.Text); s.Text != "" {
				out = append(out, s)
			}
		}
		return out, nil
	}
}

// wav: 16-bit PCM WAV of mono 16 kHz audio.
func wav(pcm []float32) []byte {
	var b bytes.Buffer
	n := uint32(2 * len(pcm))
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, 36+n)
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, []any{uint32(16), uint16(1), uint16(1), uint32(vision.SpeechRate),
		uint32(2 * vision.SpeechRate), uint16(2), uint16(16)})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, n)
	for _, x := range pcm {
		binary.Write(&b, binary.LittleEndian, int16(max(-1, min(1, x))*32767))
	}
	return b.Bytes()
}

// Run recognizes every video not done yet (or changed since), one by one; returns how many had speech.
// A video that fails is marked with its error and not retried until it changes.
func Run(ctx context.Context, d *sql.DB, rec Recognizer, mapPath func(string) string, progress func(done, total int, msg string)) (int, error) {
	type todo struct {
		id    int64
		path  string
		mtime int64
	}
	rows, err := d.Query(`SELECT f.id, f.path, COALESCE(f.mtime, 0) FROM files f
		LEFT JOIN speech_done s ON s.file_id = f.id
		WHERE f.kind = 'video' AND f.error IS NULL AND (s.file_id IS NULL OR s.mtime IS NOT COALESCE(f.mtime, 0))
		ORDER BY f.id`)
	if err != nil {
		return 0, err
	}
	var list []todo
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.id, &t.path, &t.mtime); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	spoken := 0
	for i, t := range list {
		if progress != nil {
			progress(i, len(list), "речь: "+filepath.Base(t.path))
		}
		local := mapPath(t.path)
		if _, err := os.Stat(local); err != nil {
			continue // an unmounted NAS: try again next time, not a failure of the video
		}
		var segs []Segment
		pcm, err := Audio(ctx, local)
		if err == nil && len(pcm) > 0 {
			segs, err = rec(ctx, pcm)
		}
		if ctx.Err() != nil {
			return spoken, ctx.Err()
		}
		if errors.Is(err, errService) {
			return spoken, err
		}
		if err := save(d, t.id, t.mtime, segs, err); err != nil {
			return spoken, err
		}
		if len(segs) > 0 {
			spoken++
		}
	}
	return spoken, nil
}

func save(d *sql.DB, id, mtime int64, segs []Segment, failed error) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM speech WHERE file_id = ?", id); err != nil {
		return err
	}
	for _, s := range segs {
		if _, err := tx.Exec("INSERT INTO speech (file_id, t_start, t_end, text, low) VALUES (?, ?, ?, ?, ?)",
			id, math.Round(s.Start*100)/100, math.Round(s.End*100)/100, s.Text, Lower(s.Text)); err != nil {
			return err
		}
	}
	var msg any
	if failed != nil {
		msg = failed.Error()
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO speech_done (file_id, mtime, error) VALUES (?, ?, ?)", id, mtime, msg); err != nil {
		return err
	}
	return tx.Commit()
}

// Lower: the form text is searched in (SQLite's LIKE folds only ASCII case): lower case, ё as е.
func Lower(s string) string {
	return strings.Map(func(r rune) rune {
		r = unicode.ToLower(r)
		if r == 'ё' {
			return 'е'
		}
		return r
	}, s)
}

// Of: the phrases of a file, in order.
func Of(d *sql.DB, id int64) ([]Segment, error) {
	rows, err := d.Query("SELECT t_start, t_end, text FROM speech WHERE file_id = ? ORDER BY t_start", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Segment{}
	for rows.Next() {
		var s Segment
		if err := rows.Scan(&s.Start, &s.End, &s.Text); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
