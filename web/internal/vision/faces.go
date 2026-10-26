package vision

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	ort "github.com/yalue/onnxruntime_go"
)

// ArcFace 5-point alignment template for 112x112 crops.
var arcfaceDst = [5][2]float64{{38.2946, 51.6963}, {73.5318, 51.5014}, {56.0252, 71.7366}, {41.5493, 92.3655}, {70.7299, 92.2041}}

// Face: one detected face in image pixels.
type Face struct {
	Box   [4]float32 // x1, y1, x2, y2
	Score float32
	Kps   [5][2]float32
	Size  float32   // shorter side of the box
	Emb   []float32 // 512, L2-normalized
	Norm  float32   // raw embedding norm: low = poor face
	Thumb []byte    // JPEG
	T     *float64  // position in a video
}

// Faces: SCRFD detector (det_10g) + ArcFace recognizer (w600k_r50).
type Faces struct {
	det, rec *Session
	fixed    map[int]*Session // DirectML: detectors with a fixed square input, by side
	batch1   bool             // DirectML: the recognizer takes one face at a time
	DetSize  int              // longest side the detector sees (1280)
	MinFace  float32          // px, smaller faces are dropped (20)
	scoreThr float32
	nmsThr   float32
}

func NewFaces(models string, detSize int, minFace float32) (*Faces, error) {
	det, err := NewSession(filepath.Join(models, "det_10g.onnx"))
	if err != nil {
		return nil, err
	}
	rec, err := NewSession(filepath.Join(models, "w600k_r50.onnx"))
	if err != nil {
		det.Close()
		return nil, err
	}
	f := &Faces{det: det, rec: rec, DetSize: detSize, MinFace: minFace, scoreThr: 0.5, nmsThr: 0.4}
	if err := f.forDirectML(models); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// forDirectML: DirectML can't run these models with free input sizes: fixed-size copies of them
// (cached next to the originals). The detector then sees a square canvas instead of one padded to x32.
func (f *Faces) forDirectML(models string) error {
	if f.det.provider == "directml" {
		f.fixed = map[int]*Session{}
		for _, side := range []int{f.DetSize, f.DetSize / 2} {
			p := filepath.Join(models, fmt.Sprintf("det_10g_%d.onnx", side))
			if _, err := os.Stat(p); err != nil {
				if err := fixInputDims(filepath.Join(models, "det_10g.onnx"), p, []int64{1, 3, int64(side), int64(side)}); err != nil {
					return err
				}
			}
			s, err := NewSession(p)
			if err != nil {
				return err
			}
			f.fixed[side] = s
		}
	}
	if f.rec.provider == "directml" {
		p := filepath.Join(models, "w600k_r50_b1.onnx")
		if _, err := os.Stat(p); err != nil {
			if err := fixInputDims(filepath.Join(models, "w600k_r50.onnx"), p, []int64{1, 3, 112, 112}); err != nil {
				return err
			}
		}
		s, err := NewSession(p)
		if err != nil {
			return err
		}
		f.rec.Close()
		f.rec, f.batch1 = s, true
	}
	return nil
}

func (f *Faces) Close() {
	f.det.Close()
	f.rec.Close()
	for _, s := range f.fixed {
		s.Close()
	}
}

// Find: faces of at least MinFace px with embeddings and thumbnails.
func (f *Faces) Find(img *RGB) ([]Face, error) {
	found, err := f.Detect(img)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	var out []Face
	for _, x := range found {
		x.Size = min(x.Box[2]-x.Box[0], x.Box[3]-x.Box[1])
		if x.Size >= f.MinFace {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	crops := make([]float32, len(out)*3*112*112)
	for i := range out {
		Blob(WarpAffine(img, similarity(out[i].Kps), 112, 112), 127.5, 1/127.5, crops[i*3*112*112:])
	}
	embs, err := f.embed(crops, len(out))
	if err != nil {
		return nil, err
	}
	for i := range out {
		e := embs[i*512 : (i+1)*512]
		n := norm(e)
		out[i].Emb = make([]float32, 512)
		for k, v := range e {
			out[i].Emb[k] = v / n
		}
		out[i].Norm = n
		out[i].Thumb = Thumb(img, out[i].Box)
	}
	return out, nil
}

// embed: raw ArcFace vectors of n aligned crops (one run, or one per face for DirectML).
func (f *Faces) embed(crops []float32, n int) ([]float32, error) {
	batch := n
	if f.batch1 {
		batch = 1
	}
	var out []float32
	for i := 0; i < n; i += batch {
		in, err := ort.NewTensor(ort.NewShape(int64(batch), 3, 112, 112), crops[i*3*112*112:(i+batch)*3*112*112])
		if err != nil {
			return nil, err
		}
		res, _, err := f.rec.Run(in)
		in.Destroy()
		if err != nil {
			return nil, err
		}
		out = append(out, res[0][:batch*512]...)
	}
	return out, nil
}

// Detect runs at two scales: the full size finds small faces, the half size the ones that fill the frame
// (selfies): at the full size those exceed the model's largest anchors and are missed.
func (f *Faces) Detect(img *RGB) ([]Face, error) {
	var all []Face
	for _, s := range []int{f.DetSize, f.DetSize / 2} {
		r, err := f.detectAt(img, s)
		if err != nil {
			return nil, err
		}
		all = append(all, r...)
	}
	if len(all) < 2 {
		return all, nil
	}
	return nms(all, f.scoreThr, f.nmsThr), nil
}

func (f *Faces) detectAt(img *RGB, size int) ([]Face, error) {
	scale := math.Min(1, float64(size)/float64(max(img.W, img.H))) // never upscale: big faces get missed
	nh, nw := int(math.RoundToEven(float64(img.H)*scale)), int(math.RoundToEven(float64(img.W)*scale))
	ch, cw := (nh+31)/32*32, (nw+31)/32*32 // canvas padded to a multiple of the largest stride
	det := f.det
	if s, ok := f.fixed[size]; ok {
		ch, cw, det = size, size, s
	}
	small := img
	if scale != 1 {
		small = ResizeArea(img, nw, nh)
	}
	blob := make([]float32, 3*ch*cw)
	for i := range blob {
		blob[i] = -127.5 / 128 // padding: black pixels
	}
	plane := ch * cw
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			p := small.Pix[(y*nw+x)*3:]
			o := y*cw + x
			blob[o] = (float32(p[0]) - 127.5) / 128
			blob[plane+o] = (float32(p[1]) - 127.5) / 128
			blob[2*plane+o] = (float32(p[2]) - 127.5) / 128
		}
	}
	in, err := ort.NewTensor(ort.NewShape(1, 3, int64(ch), int64(cw)), blob)
	if err != nil {
		return nil, err
	}
	defer in.Destroy()
	outs, _, err := det.Run(in)
	if err != nil {
		return nil, err
	}
	var found []Face
	for i, stride := range []int{8, 16, 32} {
		sc, bb, kp := outs[i], outs[i+3], outs[i+6]
		gw := cw / stride
		for a, s := range sc {
			if s < f.scoreThr {
				continue
			}
			cell := a / 2 // 2 anchors per cell
			cx, cy := float32((cell%gw)*stride), float32((cell/gw)*stride)
			st := float32(stride)
			d := bb[a*4 : a*4+4]
			x := Face{Score: s, Box: [4]float32{cx - d[0]*st, cy - d[1]*st, cx + d[2]*st, cy + d[3]*st}}
			for k := 0; k < 5; k++ {
				x.Kps[k] = [2]float32{kp[a*10+2*k]*st + cx, kp[a*10+2*k+1]*st + cy}
			}
			inv := float32(1 / scale)
			for k := range x.Box {
				x.Box[k] *= inv
			}
			for k := range x.Kps {
				x.Kps[k][0] *= inv
				x.Kps[k][1] *= inv
			}
			found = append(found, x)
		}
	}
	return nms(found, f.scoreThr, f.nmsThr), nil
}

// nms: OpenCV dnn.NMSBoxes (score order, stable; IoU of x,y,w,h rectangles).
func nms(in []Face, scoreThr, iouThr float32) []Face {
	idx := make([]int, 0, len(in))
	for i, f := range in {
		if f.Score >= scoreThr {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return in[idx[a]].Score > in[idx[b]].Score })
	var keep []Face
	for _, i := range idx {
		ok := true
		for _, k := range keep {
			if iou(in[i].Box, k.Box) > iouThr {
				ok = false
				break
			}
		}
		if ok {
			keep = append(keep, in[i])
		}
	}
	return keep
}

func iou(a, b [4]float32) float32 {
	w := min(a[2], b[2]) - max(a[0], b[0])
	h := min(a[3], b[3]) - max(a[1], b[1])
	if w <= 0 || h <= 0 {
		return 0
	}
	in := w * h
	return in / ((a[2]-a[0])*(a[3]-a[1]) + (b[2]-b[0])*(b[3]-b[1]) - in)
}

// similarity: rotation+scale+shift mapping the 5 landmarks onto the ArcFace template, robust like
// OpenCV's estimateAffinePartial2D(method=LMEDS): the 2-point model with the least median error picks
// the inliers, then a least-squares fit on them (what its Levenberg-Marquardt refinement converges to).
// All 10 point pairs are tried, so it is deterministic where OpenCV samples them at random.
func similarity(kps [5][2]float32) [6]float64 {
	src := make([][2]float64, 5)
	for i := range src {
		src[i] = [2]float64{float64(kps[i][0]), float64(kps[i][1])}
	}
	sqErr := func(m [6]float64, i int) float64 {
		x := m[0]*src[i][0] + m[1]*src[i][1] + m[2] - arcfaceDst[i][0]
		y := m[3]*src[i][0] + m[4]*src[i][1] + m[5] - arcfaceDst[i][1]
		return x*x + y*y
	}
	best, bestMed := [6]float64{}, math.Inf(1)
	for i := 0; i < 5; i++ {
		for j := i + 1; j < 5; j++ {
			m, ok := fitSimilarity(src, []int{i, j})
			if !ok {
				continue
			}
			e := make([]float64, 5)
			for k := range e {
				e[k] = sqErr(m, k)
			}
			sort.Float64s(e)
			if e[2] < bestMed {
				best, bestMed = m, e[2]
			}
		}
	}
	if math.IsInf(bestMed, 1) {
		m, _ := fitSimilarity(src, []int{0, 1, 2, 3, 4})
		return m
	}
	sigma := math.Max(2.5*1.4826*(1+5.0/3)*math.Sqrt(bestMed), 0.001)
	var in []int
	for k := 0; k < 5; k++ {
		if sqErr(best, k) <= sigma*sigma {
			in = append(in, k)
		}
	}
	if len(in) >= 2 {
		if m, ok := fitSimilarity(src, in); ok {
			return m
		}
	}
	return best
}

// fitSimilarity: least-squares [a -b tx; b a ty] mapping src[idx] onto the template points.
func fitSimilarity(src [][2]float64, idx []int) ([6]float64, bool) {
	n := float64(len(idx))
	var mx, my, dx, dy float64
	for _, i := range idx {
		mx += src[i][0] / n
		my += src[i][1] / n
		dx += arcfaceDst[i][0] / n
		dy += arcfaceDst[i][1] / n
	}
	var a, b, ss float64
	for _, i := range idx {
		sx, sy := src[i][0]-mx, src[i][1]-my
		tx, ty := arcfaceDst[i][0]-dx, arcfaceDst[i][1]-dy
		a += sx*tx + sy*ty
		b += sx*ty - sy*tx
		ss += sx*sx + sy*sy
	}
	if ss < 1e-12 {
		return [6]float64{}, false
	}
	a, b = a/ss, b/ss
	return [6]float64{a, -b, dx - (a*mx - b*my), b, a, dy - (b*mx + a*my)}, true
}

// Thumb: square-ish crop around the face with a margin, 128 px on the long side, JPEG 80.
func Thumb(img *RGB, box [4]float32) []byte {
	const out, margin = 128, 0.35
	w, h := float64(box[2]-box[0]), float64(box[3]-box[1])
	side := math.Max(w, h) * (1 + 2*margin)
	cx, cy := float64(box[0]+box[2])/2, float64(box[1]+box[3])/2
	a, b := int(math.Max(0, cx-side/2)), int(math.Max(0, cy-side/2))
	c, d := int(math.Min(float64(img.W), cx+side/2)), int(math.Min(float64(img.H), cy+side/2))
	crop := img.Crop(a, b, c, d)
	if crop.W == 0 || crop.H == 0 {
		return nil
	}
	s := float64(out) / float64(max(crop.W, crop.H))
	nw, nh := int(math.RoundToEven(float64(crop.W)*s)), int(math.RoundToEven(float64(crop.H)*s))
	if s < 1 {
		crop = ResizeAreaBy(crop, s)
	} else {
		crop = ResizeLinear(crop, max(1, nw), max(1, nh))
	}
	return crop.JPEG(80)
}

func norm(v []float32) float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return float32(math.Sqrt(s))
}

// Dot of two equal-length vectors.
func Dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
