package scan

// End-to-end parity with the Python scan on reference files (dump made on the server, not in git):
//   MS_PARITY=/ref MS_MODELS=/models MS_ORT=... go test -run Parity -v ./internal/scan
import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishpagin/mediasorter/web/internal/media"
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

type refMeta struct {
	TakenAt     *string  `json:"taken_at"`
	DateSource  *string  `json:"date_source"`
	Lat         *float64 `json:"lat"`
	Lon         *float64 `json:"lon"`
	Alt         *float64 `json:"alt"`
	CameraMake  *string  `json:"camera_make"`
	CameraModel *string  `json:"camera_model"`
	Width       *int     `json:"width"`
	Height      *int     `json:"height"`
	Duration    *float64 `json:"duration"`
	FPS         *float64 `json:"fps"`
	Codec       *string  `json:"codec"`
	Phash       *int64   `json:"phash"`
}

type refFile struct {
	I        int       `json:"i"`
	Path     string    `json:"path"`
	Kind     string    `json:"kind"`
	Shape    []int     `json:"shape"`
	Meta     refMeta   `json:"meta"`
	Dhash    *int64    `json:"dhash"`
	Previews []float64 `json:"previews"`
	Faces    []struct {
		Box   []float32 `json:"box"`
		Score float32   `json:"score"`
		T     *float64  `json:"t"`
	} `json:"faces"`
	Error string `json:"error"`
}

func s(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func f(p *float64) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%.5f", *p)
}

func emb(path string) ([]float32, int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0
	}
	hl := int(binary.LittleEndian.Uint16(b[8:10]))
	head, data := string(b[10:10+hl]), b[10+hl:]
	out := []float32{}
	if strings.Contains(head, "<f8") {
		for i := 0; i+8 <= len(data); i += 8 {
			out = append(out, float32(math.Float64frombits(binary.LittleEndian.Uint64(data[i:]))))
		}
	} else {
		for i := 0; i+4 <= len(data); i += 4 {
			out = append(out, math.Float32frombits(binary.LittleEndian.Uint32(data[i:])))
		}
	}
	return out, len(out) / max(1, len(out)) // shape not needed
}

// RAW files are not compared on metadata: Python read the preview's EXIF, Go reads the RAW's own
// (with the time zone and the real camera name).
func TestParityAnalyze(t *testing.T) {
	ref, models := os.Getenv("MS_PARITY"), os.Getenv("MS_MODELS")
	if ref == "" || models == "" {
		t.Skip("MS_PARITY / MS_MODELS not set")
	}
	if err := vision.Init(os.Getenv("MS_ORT")); err != nil {
		t.Fatal(err)
	}
	var man []refFile
	b, _ := os.ReadFile(filepath.Join(ref, "manifest.json"))
	if err := json.Unmarshal(b, &man); err != nil {
		t.Fatal(err)
	}
	faces, err := vision.NewFaces(models, 1280, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer faces.Close()
	clip, err := vision.NewClip(models, true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()
	e := &Engine{Faces: faces, VideoStep: 2}
	only := os.Getenv("MS_ONLY")
	var nFaces, nMiss, nExtra, metaBad int
	worstEmb, worstClip := float32(1), float32(1)
	for _, r := range man {
		if r.Error != "" || only != "" && !strings.Contains(r.Path, only) {
			continue
		}
		got, err := e.Analyze(context.Background(), r.Path, r.Kind)
		tag := fmt.Sprintf("%d %s", r.I, filepath.Ext(r.Path))
		if err != nil {
			t.Errorf("%s: %v", tag, err)
			continue
		}
		m, w := got.Meta, r.Meta
		if r.Kind == "image" {
			if *m.Height != r.Shape[0] || *m.Width != r.Shape[1] {
				t.Errorf("%s: size %dx%d, python %dx%d", tag, *m.Width, *m.Height, r.Shape[1], r.Shape[0])
			}
			if r.Dhash != nil && bits.OnesCount64(uint64(*m.Phash^*r.Dhash)) > 4 {
				t.Errorf("%s: dhash differs by %d bits", tag, bits.OnesCount64(uint64(*m.Phash^*r.Dhash)))
			}
			v, err := clip.ImageJPEG(got.Previews[0].JPEG)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := emb(filepath.Join(ref, fmt.Sprintf("%d_clip.npy", r.I)))
			c := vision.Dot(v, want)
			worstClip = min(worstClip, c)
			if c < 0.985 { // WebP: Go's VP8 decoder vs libwebp, ~0.987
				t.Errorf("%s: scene vector of own preview cos %.4f", tag, c)
			}
		} else {
			if s(m.Codec) != s(w.Codec) || f(m.FPS) != f(w.FPS) || f(m.Duration) != f(w.Duration) ||
				*m.Width != *w.Width || *m.Height != *w.Height {
				t.Errorf("%s: video %s %s %s %dx%d, python %s %s %s %dx%d", tag, s(m.Codec), f(m.FPS), f(m.Duration),
					*m.Width, *m.Height, s(w.Codec), f(w.FPS), f(w.Duration), *w.Width, *w.Height)
			}
			var pt []float64
			for _, p := range got.Previews {
				pt = append(pt, *p.T)
			}
			if fmt.Sprintf("%.2f", pt) != fmt.Sprintf("%.2f", r.Previews) {
				t.Errorf("%s: preview times %.2f, python %.2f", tag, pt, r.Previews)
			}
		}
		raw := media.RawExt[strings.ToLower(filepath.Ext(r.Path))]
		if !raw && s(m.TakenAt) != s(w.TakenAt) || s(m.DateSource) != s(w.DateSource) || f(m.Lat) != f(w.Lat) ||
			f(m.Lon) != f(w.Lon) || f(m.Alt) != f(w.Alt) || s(m.CameraMake) != s(w.CameraMake) ||
			s(m.CameraModel) != s(w.CameraModel) {
			metaBad++
			t.Errorf("%s meta:\n go %s %s %s,%s,%s %q %q\n py %s %s %s,%s,%s %q %q", tag,
				s(m.TakenAt), s(m.DateSource), f(m.Lat), f(m.Lon), f(m.Alt), s(m.CameraMake), s(m.CameraModel),
				s(w.TakenAt), s(w.DateSource), f(w.Lat), f(w.Lon), f(w.Alt), s(w.CameraMake), s(w.CameraModel))
		}
		want, _ := emb(filepath.Join(ref, fmt.Sprintf("%d_emb.npy", r.I)))
		used := map[int]bool{}
		for k, wf := range r.Faces {
			nFaces++
			best, bi := float32(0), -1
			for j, g := range got.Faces {
				if used[j] || (wf.T != nil && g.T != nil && math.Abs(*wf.T-*g.T) > 0.01) {
					continue
				}
				if c := vision.Dot(g.Emb, want[k*512:(k+1)*512]); c > best {
					best, bi = c, j
				}
			}
			if best < 0.95 {
				nMiss++
				t.Errorf("%s: python face %d (score %.2f t %v) not found, best cos %.3f", tag, k, wf.Score, wf.T, best)
				continue
			}
			used[bi] = true
			worstEmb = min(worstEmb, best)
		}
		if extra := len(got.Faces) - len(used); extra > 0 {
			nExtra += extra
			t.Errorf("%s: %d extra faces (go %d, python %d)", tag, extra, len(got.Faces), len(r.Faces))
		}
	}
	t.Logf("faces %d: missed %d, extra %d, worst cos %.4f; own preview scene worst cos %.4f; meta differs in %d files",
		nFaces, nMiss, nExtra, worstEmb, worstClip, metaBad)
}
