// Package vision runs the neural networks (faces: SCRFD + ArcFace, scenes: SigLIP2) with ONNX Runtime,
// plus the few OpenCV-style image operations they need. Images are 8-bit RGB.
package vision

import (
	"bytes"
	"image"
	"image/draw"
	"math"

	"github.com/ishpagin/mediasorter/web/internal/vision/ljpeg"
)

// RGB: 8-bit, 3 bytes per pixel, rows without padding.
type RGB struct {
	W, H int
	Pix  []byte
}

func NewRGB(w, h int) *RGB { return &RGB{w, h, make([]byte, w*h*3)} }

// FromImage converts any decoded image to RGB (alpha dropped as is, like OpenCV IMREAD_COLOR).
func FromImage(m image.Image) *RGB {
	b := m.Bounds()
	out := NewRGB(b.Dx(), b.Dy())
	switch s := m.(type) {
	case *image.YCbCr:
		if s.SubsampleRatio == image.YCbCrSubsampleRatio420 || s.SubsampleRatio == image.YCbCrSubsampleRatio422 {
			fancyYCbCr(s, out)
			return out
		}
		for y := 0; y < out.H; y++ {
			for x := 0; x < out.W; x++ {
				yi, ci := s.YOffset(b.Min.X+x, b.Min.Y+y), s.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bb := ycbcr(s.Y[yi], s.Cb[ci], s.Cr[ci])
				o := (y*out.W + x) * 3
				out.Pix[o], out.Pix[o+1], out.Pix[o+2] = r, g, bb
			}
		}
		return out
	case *image.Gray:
		for y := 0; y < out.H; y++ {
			for x := 0; x < out.W; x++ {
				v := s.Pix[(y)*s.Stride+x]
				o := (y*out.W + x) * 3
				out.Pix[o], out.Pix[o+1], out.Pix[o+2] = v, v, v
			}
		}
		return out
	}
	n := image.NewNRGBA(image.Rect(0, 0, out.W, out.H))
	draw.Draw(n, n.Rect, m, b.Min, draw.Src)
	for i, j := 0, 0; i < len(n.Pix); i, j = i+4, j+3 {
		out.Pix[j], out.Pix[j+1], out.Pix[j+2] = n.Pix[i], n.Pix[i+1], n.Pix[i+2]
	}
	return out
}

// ycbcr: libjpeg's fixed-point JFIF conversion (jdcolor.c), so pixels match what OpenCV decodes.
func ycbcr(y, cb, cr uint8) (uint8, uint8, uint8) {
	const half = 1 << 15
	Y, Cb, Cr := int(y), int(cb)-128, int(cr)-128
	r := Y + (91881*Cr+half)>>16
	g := Y + (-22554*Cb-46802*Cr+half)>>16
	b := Y + (116130*Cb+half)>>16
	return sat(r), sat(g), sat(b)
}

func sat(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// fancyYCbCr: libjpeg's default "fancy" (triangle filter) chroma upsampling for 4:2:0 and 4:2:2
// (jdsample.c h2v2/h2v1_fancy_upsample); Go's decoder leaves chroma subsampled, and nearest-pixel
// upsampling shifts colors enough to change the scene vectors noticeably.
func fancyYCbCr(s *image.YCbCr, out *RGB) {
	b := s.Bounds()
	cw := (b.Dx() + 1) / 2
	ch := b.Dy()
	if s.SubsampleRatio == image.YCbCrSubsampleRatio420 {
		ch = (b.Dy() + 1) / 2
	}
	cb, cr := make([]byte, out.W+1), make([]byte, out.W+1)
	rowAt := func(p []byte, y int) []byte {
		y = min(max(y, 0), ch-1)
		o := y * s.CStride
		return p[o : o+cw]
	}
	up := func(dst []byte, plane []byte, y int) {
		if s.SubsampleRatio == image.YCbCrSubsampleRatio422 {
			h2v1(dst, rowAt(plane, y))
			return
		}
		near := rowAt(plane, y/2)
		far := rowAt(plane, y/2-1) // the upper output row of a pair blends with the row above
		if y%2 == 1 {
			far = rowAt(plane, y/2+1)
		}
		h2v2(dst, near, far)
	}
	for y := 0; y < out.H; y++ {
		up(cb, s.Cb, y)
		up(cr, s.Cr, y)
		yr := s.Y[y*s.YStride:]
		for x := 0; x < out.W; x++ {
			r, g, bb := ycbcr(yr[x], cb[x], cr[x])
			o := (y*out.W + x) * 3
			out.Pix[o], out.Pix[o+1], out.Pix[o+2] = r, g, bb
		}
	}
}

func h2v2(dst, near, far []byte) {
	n := len(near)
	col := func(i int) int { return int(near[i])*3 + int(far[i]) }
	if n == 1 {
		v := col(0)
		dst[0], dst[1] = byte((v*4+8)>>4), byte((v*4+7)>>4)
		return
	}
	this, next := col(0), col(1)
	dst[0], dst[1] = byte((this*4+8)>>4), byte((this*3+next+7)>>4)
	last := this
	this = next
	for i := 1; i < n-1; i++ {
		next = col(i + 1)
		dst[2*i], dst[2*i+1] = byte((this*3+last+8)>>4), byte((this*3+next+7)>>4)
		last, this = this, next
	}
	dst[2*n-2], dst[2*n-1] = byte((this*3+last+8)>>4), byte((this*4+7)>>4)
}

func h2v1(dst, in []byte) {
	n := len(in)
	if n == 1 {
		dst[0], dst[1] = in[0], in[0]
		return
	}
	dst[0], dst[1] = in[0], byte((int(in[0])*3+int(in[1])+2)>>2)
	for i := 1; i < n-1; i++ {
		v := int(in[i]) * 3
		dst[2*i], dst[2*i+1] = byte((v+int(in[i-1])+1)>>2), byte((v+int(in[i+1])+2)>>2)
	}
	dst[2*n-2], dst[2*n-1] = byte((int(in[n-1])*3+int(in[n-2])+1)>>2), in[n-1]
}

func clamp8(v float64) uint8 {
	v = math.Round(v)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func (m *RGB) Image() *image.NRGBA {
	n := image.NewNRGBA(image.Rect(0, 0, m.W, m.H))
	for i, j := 0, 0; j < len(m.Pix); i, j = i+4, j+3 {
		n.Pix[i], n.Pix[i+1], n.Pix[i+2], n.Pix[i+3] = m.Pix[j], m.Pix[j+1], m.Pix[j+2], 255
	}
	return n
}

// JPEG: encoded like libjpeg (OpenCV's imencode) would, 4:2:0.
func (m *RGB) JPEG(quality int) []byte {
	var b bytes.Buffer
	r := image.NewRGBA(image.Rect(0, 0, m.W, m.H))
	for i, j := 0, 0; j < len(m.Pix); i, j = i+4, j+3 {
		r.Pix[i], r.Pix[i+1], r.Pix[i+2], r.Pix[i+3] = m.Pix[j], m.Pix[j+1], m.Pix[j+2], 255
	}
	_ = ljpeg.Encode(&b, r, &ljpeg.Options{Quality: quality})
	return b.Bytes()
}

// Crop: [x1,x2) x [y1,y2), clipped to the image.
func (m *RGB) Crop(x1, y1, x2, y2 int) *RGB {
	x1, y1 = max(0, x1), max(0, y1)
	x2, y2 = min(m.W, x2), min(m.H, y2)
	if x2 <= x1 || y2 <= y1 {
		return NewRGB(0, 0)
	}
	out := NewRGB(x2-x1, y2-y1)
	for y := y1; y < y2; y++ {
		copy(out.Pix[(y-y1)*out.W*3:(y-y1+1)*out.W*3], m.Pix[(y*m.W+x1)*3:(y*m.W+x2)*3])
	}
	return out
}

// Gray: OpenCV's BGR2GRAY weights (fixed point, rounded).
func (m *RGB) Gray() []byte {
	g := make([]byte, m.W*m.H)
	for i := range g {
		r, gg, b := int(m.Pix[i*3]), int(m.Pix[i*3+1]), int(m.Pix[i*3+2])
		g[i] = byte((r*4899 + gg*9617 + b*1868 + 8192) >> 14)
	}
	return g
}

// Rot90: k quarter turns counterclockwise (numpy rot90).
func (m *RGB) Rot90(k int) *RGB {
	k = ((k % 4) + 4) % 4
	if k == 0 {
		return m
	}
	w, h := m.W, m.H
	if k != 2 {
		w, h = h, w
	}
	out := NewRGB(w, h)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			var nx, ny int
			switch k {
			case 1: // counterclockwise
				nx, ny = y, m.W-1-x
			case 2:
				nx, ny = m.W-1-x, m.H-1-y
			case 3:
				nx, ny = m.H-1-y, x
			}
			copy(out.Pix[(ny*w+nx)*3:(ny*w+nx)*3+3], m.Pix[(y*m.W+x)*3:(y*m.W+x)*3+3])
		}
	}
	return out
}

// Orient applies an EXIF orientation (1..8) so the picture is upright.
func (m *RGB) Orient(o int) *RGB {
	switch o {
	case 2:
		return m.flipX()
	case 3:
		return m.Rot90(2)
	case 4:
		return m.Rot90(2).flipX()
	case 5:
		return m.Rot90(3).flipX()
	case 6:
		return m.Rot90(3)
	case 7:
		return m.Rot90(1).flipX()
	case 8:
		return m.Rot90(1)
	}
	return m
}

func (m *RGB) flipX() *RGB {
	out := NewRGB(m.W, m.H)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			copy(out.Pix[(y*m.W+x)*3:(y*m.W+x)*3+3], m.Pix[(y*m.W+m.W-1-x)*3:(y*m.W+m.W-1-x)*3+3])
		}
	}
	return out
}

// ResizeArea: OpenCV INTER_AREA for shrinking to a given size (each output pixel = area-weighted mean).
func ResizeArea(m *RGB, w, h int) *RGB {
	return resizeArea(m, w, h, float64(m.W)/float64(w), float64(m.H)/float64(h))
}

// ResizeAreaBy: cv2.resize(fx=f, fy=f) with INTER_AREA: the size is rounded, the scale stays exactly 1/f.
func ResizeAreaBy(m *RGB, f float64) *RGB {
	w, h := max(1, int(math.RoundToEven(float64(m.W)*f))), max(1, int(math.RoundToEven(float64(m.H)*f)))
	return resizeArea(m, w, h, 1/f, 1/f)
}

func resizeArea(m *RGB, w, h int, sx, sy float64) *RGB {
	if w == m.W && h == m.H {
		return m
	}
	if w > m.W || h > m.H {
		return ResizeLinear(m, w, h)
	}
	return &RGB{w, h, areaResize(m.Pix, m.W, m.H, 3, w, h, sx, sy)}
}

// ResizeAreaGray: INTER_AREA for an 8-bit gray picture to a given size.
func ResizeAreaGray(g []byte, W, H, w, h int) []byte {
	if w == W && h == H {
		return g
	}
	return areaResize(g, W, H, 1, w, h, float64(W)/float64(w), float64(H)/float64(h))
}

// areaResize: separable area-weighted mean, ch interleaved channels.
func areaResize(pix []byte, W, H, ch, w, h int, sx, sy float64) []byte {
	xs, ys := areaTaps(W, w, sx), areaTaps(H, h, sy)
	tmp := make([]float32, H*w*ch) // horizontal pass
	for y := 0; y < H; y++ {
		row := pix[y*W*ch:]
		for x, taps := range xs {
			o := (y*w + x) * ch
			for _, t := range taps {
				p := row[t.i*ch:]
				for c := 0; c < ch; c++ {
					tmp[o+c] += float32(p[c]) * t.w
				}
			}
		}
	}
	out := make([]byte, w*h*ch)
	for y, taps := range ys {
		for x := 0; x < w*ch; x++ {
			var v float32
			for _, t := range taps {
				v += tmp[t.i*w*ch+x] * t.w
			}
			out[y*w*ch+x] = clamp8(float64(v))
		}
	}
	return out
}

type tap struct {
	i int
	w float32
}

func areaTaps(src, dst int, scale float64) [][]tap {
	out := make([][]tap, dst)
	for d := range out {
		a, b := float64(d)*scale, float64(d+1)*scale
		for s := int(a); s < int(math.Ceil(b)) && s < src; s++ {
			lo, hi := math.Max(a, float64(s)), math.Min(b, float64(s+1))
			if hi > lo { // weights over the clipped interval (the last output pixel may run past the edge)
				out[d] = append(out[d], tap{s, float32((hi - lo) / (math.Min(b, float64(src)) - a))})
			}
		}
	}
	return out
}

// ResizeLinear: OpenCV INTER_LINEAR (half-pixel centers, edge pixels replicated).
func ResizeLinear(m *RGB, w, h int) *RGB {
	return resizeKernel(m, w, h, 1, func(t float64) [4]float64 { return [4]float64{0, 1 - t, t, 0} })
}

// ResizeCubic: OpenCV INTER_CUBIC (a = -0.75, half-pixel centers, no antialiasing, edges replicated).
func ResizeCubic(m *RGB, w, h int) *RGB {
	return resizeKernel(m, w, h, 2, func(t float64) [4]float64 {
		const a = -0.75
		c0 := ((a*(t+1)-5*a)*(t+1)+8*a)*(t+1) - 4*a
		c1 := ((a+2)*t-(a+3))*t*t + 1
		c2 := ((a+2)*(1-t)-(a+3))*(1-t)*(1-t) + 1
		return [4]float64{c0, c1, c2, 1 - c0 - c1 - c2}
	})
}

// resizeKernel: separable 4-tap resampling, taps at floor(sx)-1 .. floor(sx)+2.
func resizeKernel(m *RGB, w, h, _ int, coef func(float64) [4]float64) *RGB {
	type taps struct {
		i [4]int
		c [4]float32
	}
	mk := func(src, dst int) []taps {
		scale := float64(src) / float64(dst)
		out := make([]taps, dst)
		for d := range out {
			s := (float64(d)+0.5)*scale - 0.5
			f := math.Floor(s)
			c := coef(s - f)
			for k := 0; k < 4; k++ {
				out[d].i[k] = min(max(int(f)-1+k, 0), src-1)
				out[d].c[k] = float32(c[k])
			}
		}
		return out
	}
	xs, ys := mk(m.W, w), mk(m.H, h)
	tmp := make([]float32, m.H*w*3)
	for y := 0; y < m.H; y++ {
		row := m.Pix[y*m.W*3:]
		for x, t := range xs {
			for ch := 0; ch < 3; ch++ {
				var v float32
				for k := 0; k < 4; k++ {
					v += float32(row[t.i[k]*3+ch]) * t.c[k]
				}
				tmp[(y*w+x)*3+ch] = v
			}
		}
	}
	out := NewRGB(w, h)
	for y, t := range ys {
		for x := 0; x < w*3; x++ {
			var v float32
			for k := 0; k < 4; k++ {
				v += tmp[t.i[k]*w*3+x] * t.c[k]
			}
			out.Pix[y*w*3+x] = clamp8(float64(v))
		}
	}
	return out
}

// WarpAffine: dst(x,y) = src(M·(x,y,1)) inverted like OpenCV (M maps src -> dst), bilinear, border = 0.
func WarpAffine(m *RGB, M [6]float64, w, h int) *RGB {
	// invert the 2x3 matrix
	a, b, c, d, e, f := M[0], M[1], M[2], M[3], M[4], M[5]
	det := a*e - b*d
	if det == 0 {
		return NewRGB(w, h)
	}
	ia, ib, id, ie := e/det, -b/det, -d/det, a/det
	ic, iff := -(ia*c + ib*f), -(id*c + ie*f)
	out := NewRGB(w, h)
	px := func(x, y, ch int) float64 {
		if x < 0 || y < 0 || x >= m.W || y >= m.H {
			return 0
		}
		return float64(m.Pix[(y*m.W+x)*3+ch])
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := ia*float64(x) + ib*float64(y) + ic
			sy := id*float64(x) + ie*float64(y) + iff
			x0, y0 := int(math.Floor(sx)), int(math.Floor(sy))
			fx, fy := sx-float64(x0), sy-float64(y0)
			for ch := 0; ch < 3; ch++ {
				v := px(x0, y0, ch)*(1-fx)*(1-fy) + px(x0+1, y0, ch)*fx*(1-fy) +
					px(x0, y0+1, ch)*(1-fx)*fy + px(x0+1, y0+1, ch)*fx*fy
				out.Pix[(y*w+x)*3+ch] = clamp8(v)
			}
		}
	}
	return out
}

// Blob: NCHW float32 of RGB with (p - mean) * scale.
func Blob(m *RGB, mean, scale float32, dst []float32) {
	n := m.W * m.H
	for i := 0; i < n; i++ {
		dst[i] = (float32(m.Pix[i*3]) - mean) * scale
		dst[n+i] = (float32(m.Pix[i*3+1]) - mean) * scale
		dst[2*n+i] = (float32(m.Pix[i*3+2]) - mean) * scale
	}
}
