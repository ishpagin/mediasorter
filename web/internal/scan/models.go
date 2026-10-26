package scan

import (
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

// Models: the networks a scan uses. Clip may be nil (scene vectors are then computed by Embed later).
type Models struct {
	Faces *vision.Faces
	Clip  *vision.Clip // image tower
}

// LoadModels: face detector + recognizer and SigLIP2's image tower from dir (models/).
func LoadModels(dir string, opt Options) (*Models, error) {
	f, err := vision.NewFaces(dir, opt.DetSize, opt.MinFace)
	if err != nil {
		return nil, err
	}
	c, err := vision.NewClip(dir, true, false)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Models{Faces: f, Clip: c}, nil
}

func (m *Models) Close() {
	if m.Faces != nil {
		m.Faces.Close()
	}
	if m.Clip != nil {
		m.Clip.Close()
	}
}
