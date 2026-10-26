// Package scan: the archive scan (faces, metadata, previews, scene vectors) and what follows it:
// clustering people, scene tags, stacks of similar shots, their quality, places from GPS.
package scan

import (
	"context"
	"sort"

	"github.com/ishpagin/mediasorter/web/internal/media"
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

const (
	videoMinFace  = 40  // px; smaller faces in videos are strangers far away
	videoMaxFaces = 100 // per video, the largest/sharpest are kept
	videoPreviews = 6
)

// Preview: a stored small JPEG (one per photo, a few per video).
type Preview struct {
	T    *float64
	JPEG []byte
}

// Result of analyzing one file.
type Result struct {
	Meta     media.Meta
	Faces    []vision.Face
	Previews []Preview
	clips    [][]float32 // scene vectors of the previews, when computed during the scan
}

// Engine: the models a scan needs.
type Engine struct {
	Faces     *vision.Faces
	VideoStep float64 // seconds between sampled video frames (2)
}

// Analyze one file: like Python's analyze(). Faces in videos are de-duplicated per file.
func (e *Engine) Analyze(ctx context.Context, path, kind string) (*Result, error) {
	if kind == "image" {
		img, meta, err := media.ReadImage(path)
		if err != nil {
			return nil, err
		}
		w, h, ph := img.W, img.H, media.DHash(img)
		meta.Width, meta.Height, meta.Phash = &w, &h, &ph
		faces, err := e.Faces.Find(img)
		if err != nil {
			return nil, err
		}
		return &Result{Meta: meta, Faces: faces, Previews: []Preview{{JPEG: media.Preview(img)}}}, nil
	}

	v, err := media.OpenVideo(ctx, path)
	if err != nil {
		return nil, err
	}
	r := &Result{Meta: v.Meta}
	var kept [][]float32
	nextPreview := 0.0
	err = v.Frames(ctx, e.VideoStep, func(fr media.Frame) error {
		if r.Meta.Width == nil {
			w, h, ph := fr.Img.W, fr.Img.H, media.DHash(fr.Img)
			r.Meta.Width, r.Meta.Height, r.Meta.Phash = &w, &h, &ph
		}
		if fr.T >= nextPreview && len(r.Previews) < videoPreviews {
			t := fr.T
			r.Previews = append(r.Previews, Preview{T: &t, JPEG: media.Preview(fr.Img)})
			nextPreview = t + v.Duration/videoPreviews
		}
		faces, err := e.Faces.Find(fr.Img)
		if err != nil {
			return err
		}
	next:
		for _, f := range faces {
			if f.Size < videoMinFace {
				continue
			}
			for _, k := range kept { // a near-identical face from this video is already kept
				if vision.Dot(f.Emb, k) >= 0.6 {
					continue next
				}
			}
			t := fr.T
			f.T = &t
			kept = append(kept, f.Emb)
			r.Faces = append(r.Faces, f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(r.Faces, func(a, b int) bool {
		return r.Faces[a].Size*r.Faces[a].Score > r.Faces[b].Size*r.Faces[b].Score
	})
	if len(r.Faces) > videoMaxFaces {
		r.Faces = r.Faces[:videoMaxFaces]
	}
	return r, nil
}
