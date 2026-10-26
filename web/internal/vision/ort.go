package vision

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

var (
	initOnce sync.Once
	initErr  error
	// Provider actually used for new sessions: "cuda", "directml", "coreml" or "cpu".
	Provider = "cpu"
)

// Init loads the ONNX Runtime shared library (once). lib "" = the platform default name next to the binary.
func Init(lib string) error {
	initOnce.Do(func() {
		if lib != "" {
			preload(lib)
			ort.SetSharedLibraryPath(lib)
		}
		initErr = ort.InitializeEnvironment()
	})
	return initErr
}

// Session: one model with its input/output names in the model's own order.
type Session struct {
	s        *ort.DynamicAdvancedSession
	In, Out  []ort.InputOutputInfo
	path     string
	provider string
	mu       sync.Mutex // runs are serialized: CUDA/DirectML sessions are not happy with concurrent runs
}

// NewSession: GPU first (MS_GPU=0 turns it off), the CPU otherwise.
func NewSession(path string) (*Session, error) {
	in, out, err := ort.GetInputOutputInfo(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s := &Session{In: in, Out: out, path: path}
	for _, p := range providers() {
		if err = s.open(p); err == nil {
			Provider = p
			return s, nil
		}
		if p == "cpu" {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil, fmt.Errorf("%s: no execution provider", path)
}

func names(l []ort.InputOutputInfo) []string {
	s := make([]string, len(l))
	for i, x := range l {
		s[i] = x.Name
	}
	return s
}

func (s *Session) open(p string) error {
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return err
	}
	defer opts.Destroy()
	if p == "directml" { // DirectML can't do memory patterns or parallel execution
		if err := opts.SetMemPattern(false); err != nil {
			return err
		}
		if err := opts.SetExecutionMode(ort.ExecutionModeSequential); err != nil {
			return err
		}
	}
	if err := appendProvider(opts, p); err != nil {
		return err
	}
	ds, err := ort.NewDynamicAdvancedSession(s.path, names(s.In), names(s.Out), opts)
	if err != nil {
		return err
	}
	s.s, s.provider = ds, p
	return nil
}

func providers() []string {
	if os.Getenv("MS_GPU") == "0" {
		return []string{"cpu"}
	}
	switch runtime.GOOS {
	case "windows":
		return []string{"cuda", "directml", "cpu"}
	case "darwin":
		return []string{"coreml", "cpu"}
	}
	return []string{"cuda", "cpu"}
}

func appendProvider(o *ort.SessionOptions, p string) error {
	switch p {
	case "cuda":
		c, err := ort.NewCUDAProviderOptions()
		if err != nil {
			return err
		}
		defer c.Destroy()
		// the GPU may be shared: grow the arena only as needed
		opt := map[string]string{"arena_extend_strategy": "kSameAsRequested"}
		if mb := os.Getenv("PS_GPU_MEM_MB"); mb != "" {
			opt["gpu_mem_limit"] = mb + "000000"
		}
		if err := c.Update(opt); err != nil {
			return err
		}
		return o.AppendExecutionProviderCUDA(c)
	case "directml":
		return o.AppendExecutionProviderDirectML(0)
	case "coreml":
		return o.AppendExecutionProviderCoreML(0)
	}
	return nil
}

// Run: float32 or int inputs in model order; returns the outputs as float32 slices with their shapes.
func (s *Session) Run(inputs ...ort.Value) ([][]float32, [][]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	outs := make([]ort.Value, len(s.Out))
	if err := s.s.Run(inputs, outs); err != nil {
		if s.provider == "cpu" {
			return nil, nil, err
		}
		// a GPU driver that can't run this model: the CPU from now on (slower, but it works)
		log.Printf("%s on %s: %v; using the CPU for it", filepath.Base(s.path), s.provider, err)
		s.s.Destroy()
		if err := s.open("cpu"); err != nil {
			return nil, nil, err
		}
		outs = make([]ort.Value, len(s.Out))
		if err := s.s.Run(inputs, outs); err != nil {
			return nil, nil, err
		}
	}
	data := make([][]float32, len(outs))
	shapes := make([][]int64, len(outs))
	for i, v := range outs {
		t, ok := v.(*ort.Tensor[float32])
		if !ok {
			return nil, nil, fmt.Errorf("output %d is not float32", i)
		}
		data[i] = append([]float32(nil), t.GetData()...)
		shapes[i] = t.GetShape()
		v.Destroy()
	}
	return data, shapes, nil
}

// InputIsInt64: the element type of input i is int64 (else int32 for token ids).
func (s *Session) InputIsInt64(i int) bool {
	return s.In[i].DataType == ort.TensorElementDataTypeInt64
}

func (s *Session) Close() {
	if s != nil && s.s != nil {
		s.s.Destroy()
	}
}
