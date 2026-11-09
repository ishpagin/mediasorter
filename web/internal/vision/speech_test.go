package vision

// Parity with onnx-asr (the Python reference) on the same model and whole files:
//   MS_SPEECH_REF=/ref (a.wav + a.json {tokens, timestamps} from onnx_asr ... .with_timestamps()) \
//   MS_SPEECH_MODELS=/models/gigaam MS_ORT=/ort/libonnxruntime.dylib go test -run Speech -v ./internal/vision
import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readWav: 16-bit mono PCM as float32.
func readWav(t *testing.T, path string) []float32 {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 12; i+8 <= len(b); {
		id, n := string(b[i:i+4]), int(binary.LittleEndian.Uint32(b[i+4:]))
		if id == "data" {
			d := b[i+8 : min(len(b), i+8+n)]
			pcm := make([]float32, len(d)/2)
			for k := range pcm {
				pcm[k] = float32(int16(binary.LittleEndian.Uint16(d[2*k:]))) / 32768
			}
			return pcm
		}
		i += 8 + n + n%2
	}
	t.Fatalf("%s: no data chunk", path)
	return nil
}

func TestSpeechParity(t *testing.T) {
	ref, models := os.Getenv("MS_SPEECH_REF"), os.Getenv("MS_SPEECH_MODELS")
	if ref == "" || models == "" {
		t.Skip("MS_SPEECH_REF / MS_SPEECH_MODELS not set")
	}
	if err := Init(os.Getenv("MS_ORT")); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSpeech(models)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wavs, _ := filepath.Glob(filepath.Join(ref, "*.wav"))
	if len(wavs) == 0 {
		t.Fatal("no wavs in MS_SPEECH_REF")
	}
	for _, w := range wavs {
		var want struct {
			Tokens     []string
			Timestamps []float64
		}
		b, err := os.ReadFile(strings.TrimSuffix(w, ".wav") + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatal(err)
		}
		got, err := s.Recognize(readWav(t, w))
		if err != nil {
			t.Fatal(err)
		}
		g := make([]string, len(got))
		for i, x := range got {
			g[i] = x.Text
		}
		diff := editDistance(g, want.Tokens)
		t.Logf("%s: %d tokens, %d differ", filepath.Base(w), len(got), diff)
		if diff > len(want.Tokens)*3/100 { // int8 on another ORT build flips a rare near-tie, no more
			t.Errorf("%s: %d of %d tokens differ from onnx-asr", filepath.Base(w), diff, len(want.Tokens))
		}
	}
}

// editDistance: insertions, deletions and substitutions between two token lists.
func editDistance(a, b []string) int {
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(a); i++ {
		diag := row[0]
		row[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			diag, row[j] = row[j], min(row[j]+1, row[j-1]+1, diag+cost)
		}
	}
	return row[len(b)]
}
