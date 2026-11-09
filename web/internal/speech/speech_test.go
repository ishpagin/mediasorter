package speech

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ishpagin/mediasorter/web/internal/db"
	"github.com/ishpagin/mediasorter/web/internal/media"
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

func TestCuts(t *testing.T) {
	pcm := make([]float32, 75*vision.SpeechRate)
	for i := range pcm {
		pcm[i] = 0.5
	}
	pause := 24 * vision.SpeechRate
	for i := pause; i < pause+vision.SpeechRate/5; i++ {
		pcm[i] = 0
	}
	c := cuts(pcm)
	if c[0] < pause || c[0] > pause+vision.SpeechRate/5 {
		t.Errorf("first cut at %.2f s, want in the pause at 24 s", float64(c[0])/vision.SpeechRate)
	}
	prev := 0
	for _, x := range c {
		if x-prev > chunkMax || x <= prev {
			t.Errorf("cuts %v: piece %d..%d", c, prev, x)
		}
		prev = x
	}
	if prev != len(pcm) {
		t.Errorf("cuts %v do not end at %d", c, len(pcm))
	}
	if c := cuts(pcm[:10*vision.SpeechRate]); len(c) != 1 {
		t.Errorf("short audio cut: %v", c)
	}
}

func TestSegments(t *testing.T) {
	tok := func(s string, at float64) vision.Token { return vision.Token{Text: s, At: at} }
	got := Segments([]vision.Token{
		tok(" При", 0.1), tok("вет", 0.2), tok(" ,", 0.3), tok(" мир", 0.4), tok(".", 0.5),
		tok(" Как", 0.7), tok(" дела", 1.2), // a pause, no full stop
		tok(" Хо", 5), tok("рошо", 5.1), tok("!", 5.2),
		tok(" .", 9), // a lone mark is not a phrase
	})
	want := []Segment{{0.1, 0.7, "Привет, мир."}, {0.7, 1.5, "Как дела"}, {5, 5.5, "Хорошо!"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Text != w.Text || g.Start != w.Start || g.End-w.End > 1e-9 || w.End-g.End > 1e-9 {
			t.Errorf("segment %d: got %+v, want %+v", i, g, w)
		}
	}
	if Lower("Ёлка ЖДЁТ") != "елка ждет" {
		t.Errorf("Lower: %q", Lower("Ёлка ЖДЁТ"))
	}
}

// ff makes a test file with ffmpeg.
func ff(t *testing.T, args ...string) {
	if out, err := exec.Command(media.FFmpeg, append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v %s", args, err, out)
	}
}

// TestWav: what Remote sends is a WAV ffmpeg reads back sample for sample.
func TestWav(t *testing.T) {
	if _, err := exec.LookPath(media.FFmpeg); err != nil {
		t.Skip("no ffmpeg")
	}
	pcm := make([]float32, 3*vision.SpeechRate)
	for i := range pcm {
		pcm[i] = float32(math.Sin(float64(i) / 10))
	}
	p := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(p, wav(pcm), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Audio(context.Background(), p)
	if err != nil || len(got) != len(pcm) {
		t.Fatalf("read back %d samples of %d: %v", len(got), len(pcm), err)
	}
	for i := range pcm {
		if math.Abs(float64(got[i]-pcm[i])) > 1e-3 {
			t.Fatalf("sample %d: %f, want %f", i, got[i], pcm[i])
		}
	}
}

func TestRunRemote(t *testing.T) {
	if _, err := exec.LookPath(media.FFmpeg); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	sound, silent := filepath.Join(dir, "sound.wav"), filepath.Join(dir, "silent.mkv")
	ff(t, "-f", "lavfi", "-i", "sine=frequency=440:duration=3", sound)
	ff(t, "-f", "lavfi", "-i", "color=c=black:s=64x64:d=1", "-c:v", "ffv1", silent)
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		id   int
		path string
	}{{1, sound}, {2, silent}, {3, filepath.Join(dir, "on-unmounted-nas.mp4")}} {
		if _, err := d.Exec("INSERT INTO files (id, path, kind, mtime) VALUES (?, ?, 'video', 7)", f.id, f.path); err != nil {
			t.Fatal(err)
		}
	}

	calls, down := 0, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if down {
			http.Error(w, "boom", 500)
			return
		}
		if r.URL.Path != "/transcribe" || r.Header.Get("Content-Type") != "audio/wav" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		json.NewEncoder(w).Encode(map[string]any{"text": "Торт со свечами.",
			"segments": []Segment{{0, 1.5, "Торт со свечами."}, {1.5, 2, " "}}})
	}))
	defer srv.Close()
	same := func(p string) string { return p }

	n, err := Run(context.Background(), d, Remote(srv.URL+"/"), same, nil)
	if err != nil || n != 1 || calls != 1 { // the silent video is not sent
		t.Fatalf("run: n=%d calls=%d err=%v", n, calls, err)
	}
	segs, err := Of(d, 1)
	if err != nil || len(segs) != 1 || segs[0].Text != "Торт со свечами." {
		t.Fatalf("speech of 1: %+v %v", segs, err)
	}
	var low string
	d.QueryRow("SELECT low FROM speech WHERE file_id = 1").Scan(&low)
	if low != "торт со свечами." {
		t.Errorf("low = %q", low)
	}
	var done int
	d.QueryRow("SELECT COUNT(*) FROM speech_done").Scan(&done)
	if done != 2 { // the missing file waits for its NAS
		t.Errorf("speech_done rows: %d, want 2", done)
	}

	if n, err = Run(context.Background(), d, Remote(srv.URL), same, nil); err != nil || calls != 1 {
		t.Errorf("second run listened again: calls=%d err=%v", calls, err)
	}

	d.Exec("UPDATE files SET mtime = 8 WHERE id = 1") // the file changed: listen again, but the service is down
	down = true
	if _, err = Run(context.Background(), d, Remote(srv.URL), same, nil); !errors.Is(err, errService) {
		t.Errorf("service down: err=%v", err)
	}
	var mtime int
	d.QueryRow("SELECT mtime FROM speech_done WHERE file_id = 1").Scan(&mtime)
	if mtime != 7 {
		t.Errorf("a failed service marked the video as done (mtime %d)", mtime)
	}
}
