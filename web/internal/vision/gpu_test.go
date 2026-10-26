package vision

import (
	"image/jpeg"
	"os"
	"testing"
	"time"
)

// TestGPU: the same faces and scene vector on the GPU provider of this platform as on the CPU, and how much
// faster it is. Any photo with faces:
//
//	MS_MODELS=/models MS_ORT=libonnxruntime.so MS_TEST_IMAGE=group.jpg go test -run TestGPU -v ./internal/vision
//
// MS_EXPECT_GPU=1 fails the test when no GPU provider ran the models.
func TestGPU(t *testing.T) {
	models, path := os.Getenv("MS_MODELS"), os.Getenv("MS_TEST_IMAGE")
	if models == "" || path == "" {
		t.Skip("MS_MODELS and MS_TEST_IMAGE not set")
	}
	if err := Init(os.Getenv("MS_ORT")); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := jpeg.Decode(fh)
	fh.Close()
	if err != nil {
		t.Fatal(err)
	}
	img := FromImage(m)

	type result struct {
		faces       []Face
		clip        []float32
		providers   string
		face, scene time.Duration
	}
	run := func() result {
		f, err := NewFaces(models, 960, 24)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		c, err := NewClip(models, true, false)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var r result
		for i := 0; i < 4; i++ { // the first run warms up (GPU kernels, CoreML compilation)
			t0 := time.Now()
			if r.faces, err = f.Find(img); err != nil {
				t.Fatal(err)
			}
			t1 := time.Now()
			if r.clip, err = c.Image(img); err != nil {
				t.Fatal(err)
			}
			if i > 0 {
				r.face += t1.Sub(t0) / 3
				r.scene += time.Since(t1) / 3
			}
		}
		r.providers = "faces " + f.det.provider + "/" + f.rec.provider + ", scenes " + c.visual.provider
		return r
	}

	gpu := run()
	t.Setenv("MS_GPU", "0")
	cpu := run()
	t.Logf("GPU (%s): faces %v, scene %v", gpu.providers, gpu.face, gpu.scene)
	t.Logf("CPU (%s): faces %v, scene %v", cpu.providers, cpu.face, cpu.scene)
	if os.Getenv("MS_EXPECT_GPU") != "" && gpu.providers == cpu.providers {
		t.Errorf("no GPU provider ran the models")
	}
	if len(gpu.faces) != len(cpu.faces) || len(cpu.faces) == 0 {
		t.Fatalf("faces: GPU %d, CPU %d", len(gpu.faces), len(cpu.faces))
	}
	for i, cf := range cpu.faces { // order may differ: scores differ in the last digits
		best := float32(0)
		for _, gf := range gpu.faces {
			best = max(best, Dot(gf.Emb, cf.Emb))
		}
		if best < 0.99 {
			t.Errorf("face %d: best cos %.4f", i, best)
		}
	}
	if c := Dot(gpu.clip, cpu.clip); c < 0.995 {
		t.Errorf("scene vector: cos %.4f", c)
	}
	t.Logf("%d faces match", len(cpu.faces))
}
