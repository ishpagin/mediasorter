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
	"github.com/ishpagin/mediasorter/web/internal/scan"
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

// Segment: a phrase and when it was said, seconds from the start of the video.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	// Speaker is asr's label (s1, s2) for this phrase; empty when asr did not say.
	Speaker string `json:"speaker,omitempty"`
	// Voice is the id in voices; set by Of only.
	Voice int64 `json:"voice,omitempty"`
	// Confidence only filters phrases in Remote; it is not stored.
	Confidence *float64 `json:"confidence,omitempty"`
}

// Voice: one speaker asr heard in a file. Embedding is empty for a voice with under 3 s of speech.
type Voice struct {
	Label     string    `json:"label"`
	Seconds   float64   `json:"seconds"`
	Embedding []float32 `json:"embedding"`
}

// Result: what a recognizer returns. Model is asr's embedding_model, so vectors of different models are not mixed.
type Result struct {
	Segments []Segment
	Voices   []Voice
	Model    string
}

// Recognizer: mono 16 kHz audio in, phrases and voices out.
type Recognizer func(ctx context.Context, pcm []float32) (Result, error)

// retryPause: waits before the retries of a failed service call (tests set it to zeros).
var retryPause = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute}

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
	return func(ctx context.Context, pcm []float32) (Result, error) {
		var toks []vision.Token
		from := 0
		for _, to := range cuts(pcm) {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			t, err := m.Recognize(pcm[from:to])
			if err != nil {
				return Result{}, err
			}
			for _, x := range t {
				x.At += float64(from) / vision.SpeechRate
				toks = append(toks, x)
			}
			from = to
		}
		return Result{Segments: Segments(toks)}, nil
	}
}

// minConfidence: phrases the model is less sure of are dropped (mean token probability). On real speech
// misheard phrases sit at 0.5-0.6, noise and music give no tokens at all; asr's VAD will do the rest.
const minConfidence = 0.45

// worth: a phrase with at least two letters or digits (one-letter scraps come out of noise).
func worth(t string) bool {
	n := 0
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n >= 2
}

// Segments: tokens into phrases, split after . ! ? … and at pauses over 1.5 s; scraps and unsure phrases dropped.
func Segments(toks []vision.Token) []Segment {
	var out []Segment
	var b strings.Builder
	start, last := 0.0, 0.0
	var logp float64
	n := 0
	flush := func() {
		if t := join(b.String()); n > 0 && worth(t) && math.Exp(logp/float64(n)) >= minConfidence {
			out = append(out, Segment{Start: start, End: last + 0.3, Text: t})
		}
		b.Reset()
		logp, n = 0, 0
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
		logp += float64(t.LogProb)
		n++
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

// Remote: recognition by the shared service: POST {url}/transcribe?speakers=1 with a WAV, answer
// {"segments": [{"start", "end", "text", "confidence", "speaker"}], "speakers": [{"label", "seconds", "embedding"}],
// "embedding_model": "..."}. Phrases with confidence under 0.6 are dropped; an old asr sends no such fields.
func Remote(url string) Recognizer {
	client := &http.Client{Timeout: 30 * time.Minute}
	return func(ctx context.Context, pcm []float32) (Result, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(url, "/")+"/transcribe?speakers=1",
			bytes.NewReader(wav(pcm)))
		if err != nil {
			return Result{}, err
		}
		req.Header.Set("Content-Type", "audio/wav")
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return Result{}, fmt.Errorf("%w: %v", errService, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return Result{}, fmt.Errorf("%w: HTTP %d", errService, resp.StatusCode)
		}
		var r struct {
			Segments []Segment
			Speakers []Voice
			Model    string `json:"embedding_model"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return Result{}, fmt.Errorf("%w: %v", errService, err)
		}
		out := Result{Voices: r.Speakers, Model: r.Model}
		for _, s := range r.Segments {
			if s.Text = strings.TrimSpace(s.Text); worth(s.Text) && (s.Confidence == nil || *s.Confidence >= 0.6) {
				out.Segments = append(out.Segments, s)
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
	binary.Write(&b, binary.LittleEndian, struct { // binary.Write skips a []any silently: a struct
		Size             uint32
		Format, Channels uint16
		Rate, ByteRate   uint32
		Align, Bits      uint16
	}{16, 1, 1, vision.SpeechRate, 2 * vision.SpeechRate, 2, 16})
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
		var res Result
		pcm, err := Audio(ctx, local)
		if err == nil && len(pcm) > 0 {
			res, err = rec(ctx, pcm)
			for _, pause := range retryPause {
				if !errors.Is(err, errService) {
					break
				}
				select {
				case <-ctx.Done():
				case <-time.After(pause):
					res, err = rec(ctx, pcm)
				}
			}
		}
		if ctx.Err() != nil {
			return spoken, ctx.Err()
		}
		if errors.Is(err, errService) {
			return spoken, err
		}
		if err := save(d, t.id, t.mtime, res, err); err != nil {
			return spoken, err
		}
		if len(res.Segments) > 0 {
			spoken++
		}
	}
	return spoken, nil
}

// save replaces a file's phrases and voices. A voice heard again takes the person and the manual flag of the
// old voice it overlaps most in time, and the user's "not the same" / "not his voice" marks move with it.
func save(d *sql.DB, id, mtime int64, r Result, failed error) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type old struct {
		person sql.NullInt64
		manual int
		segs   [][2]float64
	}
	olds := map[int64]*old{}
	rows, err := tx.Query("SELECT id, person_id, manual FROM voices WHERE file_id = ?", id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var vid int64
		o := &old{}
		if err := rows.Scan(&vid, &o.person, &o.manual); err != nil {
			rows.Close()
			return err
		}
		olds[vid] = o
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	if len(olds) > 0 {
		if rows, err = tx.Query("SELECT voice_id, t_start, t_end FROM speech WHERE file_id = ? AND voice_id IS NOT NULL", id); err != nil {
			return err
		}
		for rows.Next() {
			var vid int64
			var iv [2]float64
			if err := rows.Scan(&vid, &iv[0], &iv[1]); err != nil {
				rows.Close()
				return err
			}
			olds[vid].segs = append(olds[vid].segs, iv)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
	}
	// marks of the old voices, read before the rows go away
	pairs := func(q string) ([][2]int64, error) {
		rows, err := tx.Query(q, id)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out [][2]int64
		for rows.Next() {
			var p [2]int64
			if err := rows.Scan(&p[0], &p[1]); err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, rows.Err()
	}
	notPerson, err := pairs("SELECT voice_id, person_id FROM voice_not_person WHERE voice_id IN (SELECT id FROM voices WHERE file_id = ?)")
	if err != nil {
		return err
	}
	notSame, err := pairs(`SELECT a, b FROM voice_not_same WHERE a IN (SELECT id FROM voices WHERE file_id = ?1)
		OR b IN (SELECT id FROM voices WHERE file_id = ?1)`)
	if err != nil {
		return err
	}
	for _, q := range []string{"DELETE FROM speech WHERE file_id = ?", "DELETE FROM voices WHERE file_id = ?"} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM voice_not_same WHERE a NOT IN (SELECT id FROM voices) OR b NOT IN (SELECT id FROM voices)`); err != nil {
		return err
	}

	ivs := map[string][][2]float64{} // new voice label -> its phrases
	for _, s := range r.Segments {
		if s.Speaker != "" {
			ivs[s.Speaker] = append(ivs[s.Speaker], [2]float64{s.Start, s.End})
		}
	}
	ids := map[string]int64{}
	inherit := map[int64][]int64{} // old voice id -> new voices that took its marks
	for _, v := range r.Voices {
		if len(ivs[v.Label]) == 0 {
			continue
		}
		var best int64
		bestOv := 0.0
		for oid, o := range olds {
			ov := 0.0
			for _, a := range o.segs {
				for _, b := range ivs[v.Label] {
					ov += max(0, min(a[1], b[1])-max(a[0], b[0]))
				}
			}
			if ov > bestOv || ov == bestOv && ov > 0 && oid < best {
				best, bestOv = oid, ov
			}
		}
		var emb, person any
		if len(v.Embedding) > 0 {
			emb = scan.F16Blob(v.Embedding)
		}
		manual := 0
		if bestOv > 0 {
			if olds[best].person.Valid {
				person = olds[best].person.Int64
			}
			manual = olds[best].manual
		}
		res, err := tx.Exec("INSERT INTO voices (file_id, label, seconds, model, emb, person_id, manual) VALUES (?, ?, ?, ?, ?, ?, ?)",
			id, v.Label, v.Seconds, r.Model, emb, person, manual)
		if err != nil {
			return err
		}
		nid, _ := res.LastInsertId()
		ids[v.Label] = nid
		if bestOv > 0 {
			inherit[best] = append(inherit[best], nid)
		}
	}
	for _, p := range notPerson {
		for _, nid := range inherit[p[0]] {
			if _, err := tx.Exec("INSERT OR IGNORE INTO voice_not_person (voice_id, person_id) VALUES (?, ?)", nid, p[1]); err != nil {
				return err
			}
		}
	}
	mapped := func(v int64) []int64 { // an old voice of this file becomes its inheritors, any other stays
		if _, ok := olds[v]; ok {
			return inherit[v]
		}
		return []int64{v}
	}
	for _, p := range notSame {
		for _, a := range mapped(p[0]) {
			for _, b := range mapped(p[1]) {
				if a != b {
					if _, err := tx.Exec("INSERT OR IGNORE INTO voice_not_same (a, b) VALUES (?, ?)", min(a, b), max(a, b)); err != nil {
						return err
					}
				}
			}
		}
	}

	for _, s := range r.Segments {
		var vid any
		if n, ok := ids[s.Speaker]; ok {
			vid = n
		}
		if _, err := tx.Exec("INSERT INTO speech (file_id, t_start, t_end, text, low, voice_id) VALUES (?, ?, ?, ?, ?, ?)",
			id, math.Round(s.Start*100)/100, math.Round(s.End*100)/100, s.Text, Lower(s.Text), vid); err != nil {
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
	rows, err := d.Query("SELECT t_start, t_end, text, COALESCE(voice_id, 0) FROM speech WHERE file_id = ? ORDER BY t_start", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Segment{}
	for rows.Next() {
		var s Segment
		if err := rows.Scan(&s.Start, &s.End, &s.Text, &s.Voice); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
