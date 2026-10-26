package vision

// Parity with the Python pipeline on reference outputs (made by a dump script on the server, not in git):
//   MS_PARITY=/ref MS_MODELS=/models MS_ORT=/ort/lib/libonnxruntime.so go test -run Parity -v ./internal/vision
import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	ort "github.com/yalue/onnxruntime_go"
)

type npy struct {
	shape []int
	f32   []float32
	u8    []byte
}

// readNpy: C-order float32 / float64 / uint8 arrays (what numpy.save writes for our data).
func readNpy(path string) (*npy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hl := int(binary.LittleEndian.Uint16(b[8:10]))
	head, data := string(b[10:10+hl]), b[10+hl:]
	if b[6] >= 2 {
		hl = int(binary.LittleEndian.Uint32(b[8:12]))
		head, data = string(b[12:12+hl]), b[12+hl:]
	}
	if strings.Contains(head, "'fortran_order': True") {
		return nil, fmt.Errorf("%s: fortran order", path)
	}
	n := &npy{}
	for _, s := range regexp.MustCompile(`\d+`).FindAllString(regexp.MustCompile(`'shape': \(([^)]*)\)`).FindStringSubmatch(head)[1], -1) {
		v, _ := strconv.Atoi(s)
		n.shape = append(n.shape, v)
	}
	switch {
	case strings.Contains(head, "'<f4'"):
		n.f32 = make([]float32, len(data)/4)
		for i := range n.f32 {
			n.f32[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
		}
	case strings.Contains(head, "'<f8'"):
		n.f32 = make([]float32, len(data)/8)
		for i := range n.f32 {
			n.f32[i] = float32(math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:])))
		}
	case strings.Contains(head, "'|u1'"):
		n.u8 = data
	default:
		return nil, fmt.Errorf("%s: dtype in %s", path, head)
	}
	return n, nil
}

type refFile struct {
	I     int    `json:"i"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Shape []int  `json:"shape"`
	BGR   bool   `json:"bgr"`
	Faces []struct {
		Box   []float32 `json:"box"`
		Score float32   `json:"score"`
		Norm  float32   `json:"norm"`
	} `json:"faces"`
	Det   [][]float32 `json:"det"`
	Error string      `json:"error"`
}

func parityEnv(t *testing.T) (string, string) {
	ref, models := os.Getenv("MS_PARITY"), os.Getenv("MS_MODELS")
	if ref == "" || models == "" {
		t.Skip("MS_PARITY / MS_MODELS not set")
	}
	if err := Init(os.Getenv("MS_ORT")); err != nil {
		t.Fatal(err)
	}
	return ref, models
}

func TestParityTokens(t *testing.T) {
	ref, models := parityEnv(t)
	var toks []struct {
		Text, Canon string
		IDs         []int32
	}
	b, _ := os.ReadFile(filepath.Join(ref, "tokens.json"))
	if err := json.Unmarshal(b, &toks); err != nil {
		t.Fatal(err)
	}
	tk, err := LoadTokenizer(filepath.Join(models, "siglip2", "textual", "tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, x := range toks {
		c := Canonicalize(x.Text)
		got := tk.Encode(c, clipContext)
		if c != x.Canon || fmt.Sprint(got) != fmt.Sprint(x.IDs) {
			bad++
			t.Errorf("%q: canon %q vs %q\n go %v\n py %v", x.Text, c, x.Canon, got, x.IDs)
		}
	}
	t.Logf("tokens: %d/%d equal", len(toks)-bad, len(toks))

	clip, err := NewClip(models, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	E, err := readNpy(filepath.Join(ref, "text_emb.npy"))
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, 24)
	for _, x := range toks[:12] {
		texts = append(texts, x.Text)
	}
	for _, x := range toks[len(toks)-12:] {
		texts = append(texts, x.Text)
	}
	worst := float32(1)
	for i, s := range texts {
		v, err := clip.Text(s)
		if err != nil {
			t.Fatal(err)
		}
		worst = min(worst, Dot(v, E.f32[i*ClipDim:(i+1)*ClipDim]))
	}
	t.Logf("text vectors: worst cosine %.5f (provider %s)", worst, Provider)
	if worst < 0.999 {
		t.Errorf("text vectors differ")
	}
}

func TestParityModels(t *testing.T) {
	ref, models := parityEnv(t)
	var man []refFile
	b, _ := os.ReadFile(filepath.Join(ref, "manifest.json"))
	if err := json.Unmarshal(b, &man); err != nil {
		t.Fatal(err)
	}
	faces, err := NewFaces(models, 1280, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer faces.Close()
	clip, err := NewClip(models, true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	clipWorst, embWorst, nFaces, nMiss := float32(1), float32(1), 0, 0
	for _, r := range man {
		if r.Kind != "image" || r.Error != "" {
			continue
		}
		// scene vector of the very same preview JPEG
		if jpg, err := os.ReadFile(filepath.Join(ref, fmt.Sprintf("%d_prev.jpg", r.I))); err == nil {
			v, err := clip.ImageJPEG(jpg)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := readNpy(filepath.Join(ref, fmt.Sprintf("%d_clip.npy", r.I)))
			c := Dot(v, want.f32)
			clipWorst = min(clipWorst, c)
			if c < 0.99 {
				t.Errorf("%d clip cos %.4f", r.I, c)
			}
		}
		if !r.BGR {
			continue
		}
		// faces on the very same pixels
		raw, err := readNpy(filepath.Join(ref, fmt.Sprintf("%d_bgr.npy", r.I)))
		if err != nil {
			t.Fatal(err)
		}
		img := NewRGB(raw.shape[1], raw.shape[0])
		for i := 0; i < len(raw.u8); i += 3 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = raw.u8[i+2], raw.u8[i+1], raw.u8[i]
		}
		got, err := faces.Find(img)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := readNpy(filepath.Join(ref, fmt.Sprintf("%d_emb.npy", r.I)))
		used := map[int]bool{}
		for k, wf := range r.Faces {
			nFaces++
			best, bi := float32(0), -1
			for j, g := range got {
				if o := iou(g.Box, [4]float32{wf.Box[0], wf.Box[1], wf.Box[2], wf.Box[3]}); o > best && !used[j] {
					best, bi = o, j
				}
			}
			if best < 0.9 {
				nMiss++
				t.Errorf("%d face %d (score %.2f) not found (best IoU %.2f)", r.I, k, wf.Score, best)
				continue
			}
			used[bi] = true
			c := Dot(got[bi].Emb, want.f32[k*512:(k+1)*512])
			embWorst = min(embWorst, c)
			if c < 0.98 {
				t.Errorf("%d face %d: embedding cos %.4f", r.I, k, c)
			}
		}
		if len(got) != len(r.Faces) {
			t.Errorf("%d: %d faces, python %d", r.I, len(got), len(r.Faces))
		}
	}
	t.Logf("clip worst cos %.5f; faces %d (missed %d), embedding worst cos %.5f; provider %s",
		clipWorst, nFaces, nMiss, embWorst, Provider)
}

// TestFixInputDims: the fixed-size copy of the detector (made for DirectML) gives the same output.
func TestFixInputDims(t *testing.T) {
	_, models := parityEnv(t)
	dst := filepath.Join(t.TempDir(), "det_320.onnx")
	if err := fixInputDims(filepath.Join(models, "det_10g.onnx"), dst, []int64{1, 3, 320, 320}); err != nil {
		t.Fatal(err)
	}
	in, out, err := ort.GetInputOutputInfo(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(in[0].Dimensions) != "[1 3 320 320]" {
		t.Fatalf("dims %v", in[0].Dimensions)
	}
	a, err := NewSession(filepath.Join(models, "det_10g.onnx"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSession(dst)
	if err != nil {
		t.Fatal(err)
	}
	x := make([]float32, 3*320*320)
	for i := range x {
		x[i] = float32(i%251)/125 - 1
	}
	tin, _ := ort.NewTensor(ort.NewShape(1, 3, 320, 320), x)
	ra, _, err1 := a.Run(tin)
	rb, _, err2 := b.Run(tin)
	if err1 != nil || err2 != nil || len(ra) != len(out) {
		t.Fatal(err1, err2)
	}
	for i := range ra {
		for k := range ra[i] {
			if d := ra[i][k] - rb[i][k]; d > 1e-4 || d < -1e-4 { // fixed shapes: other kernels, float noise
				t.Fatalf("output %d differs at %d: %g vs %g", i, k, ra[i][k], rb[i][k])
			}
		}
	}
}
