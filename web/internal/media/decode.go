package media

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/gen2brain/heic"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/ishpagin/mediasorter/web/internal/vision"
)

var (
	RawExt   = set(".cr3", ".cr2", ".nef", ".arw", ".dng", ".raf", ".orf", ".rw2")
	ImageExt = set(".jpg", ".jpeg", ".png", ".bmp", ".webp", ".tif", ".tiff", ".heic", ".heif", ".gif",
		".cr3", ".cr2", ".nef", ".arw", ".dng", ".raf", ".orf", ".rw2")
	VideoExt = set(".mp4", ".mov", ".m4v", ".avi", ".mkv", ".3gp", ".mts", ".m2ts", ".wmv", ".webm")
)

func set(s ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range s {
		m[x] = true
	}
	return m
}

// Kind: "image", "video" or "" by extension.
func Kind(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case ImageExt[ext]:
		return "image"
	case VideoExt[ext]:
		return "video"
	}
	return ""
}

// ReadImage: the upright picture and its EXIF metadata.
func ReadImage(path string) (*vision.RGB, Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, Meta{}, err
	}
	return DecodeImage(data, strings.ToLower(filepath.Ext(path)))
}

// DecodeImage: ext picks RAW / HEIC handling ("" = sniff the format).
func DecodeImage(data []byte, ext string) (*vision.RGB, Meta, error) {
	switch {
	case RawExt[ext]:
		jpg := largestJPEG(data)
		if jpg == nil {
			return nil, Meta{}, errors.New("no embedded preview in RAW")
		}
		img, m, err := decodeJPEG(jpg)
		if err == nil && m.TakenAt == nil { // the preview often has no EXIF: the RAW's own (TIFF-based) one
			if t, e := parseTIFF(findEXIF(data)); e == nil {
				rm := t.meta()
				rm.Orientation = m.Orientation
				m = rm
			}
		}
		return img, m, err
	case ext == ".heic" || ext == ".heif" || bytes.Contains(data[:min(len(data), 32)], []byte("ftypheic")):
		m, err := heic.Decode(bytes.NewReader(data)) // applies the rotation itself
		if err != nil {
			return nil, Meta{}, fmt.Errorf("heic: %w", err)
		}
		var meta Meta
		if t, e := parseTIFF(heicEXIF(data)); e == nil {
			meta = t.meta()
		}
		return vision.FromImage(m), meta, nil
	case len(data) > 2 && data[0] == 0xFF && data[1] == 0xD8:
		return decodeJPEG(data)
	}
	m, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, Meta{}, err
	}
	var meta Meta
	if t, e := parseTIFF(findEXIF(data)); e == nil {
		meta = t.meta()
	}
	return vision.FromImage(m), meta, nil
}

// decodeJPEG applies the EXIF orientation, like OpenCV's imdecode.
func decodeJPEG(data []byte) (*vision.RGB, Meta, error) {
	m, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, Meta{}, err
	}
	var meta Meta
	if t, e := parseTIFF(findEXIF(data)); e == nil {
		meta = t.meta()
	}
	return vision.FromImage(m).Orient(meta.Orientation), meta, nil
}

// largestJPEG: the biggest embedded JPEG of a RAW file (the camera's full-size preview).
func largestJPEG(b []byte) []byte {
	var best []byte
	bestArea := 0
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0xFF || b[i+1] != 0xD8 || b[i+2] != 0xFF {
			continue
		}
		c, err := jpeg.DecodeConfig(bytes.NewReader(b[i:]))
		if err != nil || c.Width*c.Height <= bestArea {
			continue
		}
		best, bestArea = b[i:], c.Width*c.Height
	}
	return best
}

// DHash: 64-bit difference hash, signed (fits SQLite INTEGER); close values = near-identical pictures.
func DHash(img *vision.RGB) int64 {
	g := grayRGB(img)
	s := vision.ResizeArea(g, 9, 8)
	var v uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			v <<= 1
			if s.Pix[(y*9+x+1)*3] > s.Pix[(y*9+x)*3] {
				v |= 1
			}
		}
	}
	return int64(v)
}

func grayRGB(img *vision.RGB) *vision.RGB {
	g := img.Gray()
	out := vision.NewRGB(img.W, img.H)
	for i, v := range g {
		out.Pix[i*3], out.Pix[i*3+1], out.Pix[i*3+2] = v, v, v
	}
	return out
}

// Preview: at most 320 px on the long side, JPEG 75 (what scenes and the UI use later).
func Preview(img *vision.RGB) []byte {
	return Shrink(img, 320).JPEG(75)
}

// Shrink to at most size px on the long side (area resampling; OpenCV rounding of the new size).
func Shrink(img *vision.RGB, size int) *vision.RGB {
	s := float64(size) / float64(max(img.W, img.H))
	if s >= 1 {
		return img
	}
	return vision.ResizeAreaBy(img, s)
}
