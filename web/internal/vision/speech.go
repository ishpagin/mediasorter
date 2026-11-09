package vision

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	ort "github.com/yalue/onnxruntime_go"
)

// The log-mel features GigaAM v3 expects, as an ONNX graph: from onnx-asr 0.12.0 (github.com/istupakov/onnx-asr,
// MIT, see gigaam_v3_prep.LICENSE).
//
//go:embed gigaam_v3_prep.onnx
var gigaamPrep []byte

// SpeechRate: the sample rate Speech takes (mono float32 in -1..1).
const SpeechRate = 16000

const speechFrame = 0.04 // seconds per output frame: 10 ms hop x subsampling 4

// Speech: GigaAM v3 e2e CTC, Russian speech to text with punctuation (models in one folder, see setup.Speech).
type Speech struct {
	prep, model *Session
	vocab       []string
	blank       int
}

// Token: a piece of recognized text ("▁" already turned into a space) and when it was said, in seconds.
type Token struct {
	Text    string
	At      float64
	LogProb float32 // the model's log-probability of this token (0 = sure)
}

// LoadSpeech opens the model in dir (v3_e2e_ctc.int8.onnx, v3_e2e_ctc_vocab.txt). The CPU only: the model is int8.
func LoadSpeech(dir string) (*Speech, error) {
	prep := filepath.Join(dir, "prep.onnx")
	if b, err := os.ReadFile(prep); err != nil || !bytes.Equal(b, gigaamPrep) {
		if err := os.WriteFile(prep, gigaamPrep, 0o644); err != nil {
			return nil, err
		}
	}
	s := &Speech{blank: -1}
	f, err := os.Open(filepath.Join(dir, "v3_e2e_ctc_vocab.txt"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() { // "token id" per line, ids in order
		tok, id, ok := strings.Cut(sc.Text(), " ")
		if n, err := strconv.Atoi(id); !ok || err != nil || n != len(s.vocab) {
			return nil, fmt.Errorf("vocab line %q", sc.Text())
		}
		if tok == "<blk>" {
			s.blank = len(s.vocab)
		}
		s.vocab = append(s.vocab, strings.ReplaceAll(tok, "▁", " "))
	}
	if s.blank < 0 {
		return nil, fmt.Errorf("vocab: no <blk>")
	}
	if s.prep, err = OpenCPU(prep); err != nil {
		return nil, err
	}
	if s.model, err = OpenCPU(filepath.Join(dir, "v3_e2e_ctc.int8.onnx")); err != nil {
		s.prep.Close()
		return nil, err
	}
	return s, nil
}

func (s *Speech) Close() {
	if s != nil {
		s.prep.Close()
		s.model.Close()
	}
}

// Recognize: the tokens of one piece of audio (keep it under ~30 s), times from its start. Greedy CTC.
func (s *Speech) Recognize(pcm []float32) ([]Token, error) {
	if len(pcm) < SpeechRate/10 {
		return nil, nil
	}
	w, err := ort.NewTensor(ort.NewShape(1, int64(len(pcm))), pcm)
	if err != nil {
		return nil, err
	}
	defer w.Destroy()
	wl, err := ort.NewTensor(ort.NewShape(1), []int64{int64(len(pcm))})
	if err != nil {
		return nil, err
	}
	defer wl.Destroy()
	out, shapes, err := s.prep.Run(w, wl)
	if err != nil {
		return nil, err
	}
	flen := int64(out[1][0])
	f, err := ort.NewTensor(ort.NewShape(shapes[0]...), out[0])
	if err != nil {
		return nil, err
	}
	defer f.Destroy()
	fl, err := ort.NewTensor(ort.NewShape(1), []int64{flen})
	if err != nil {
		return nil, err
	}
	defer fl.Destroy()
	lp, sh, err := s.model.Run(f, fl)
	if err != nil {
		return nil, err
	}
	frames, classes := int(min(sh[0][1], (flen-1)/4+1)), int(sh[0][2])
	var toks []Token
	prev := s.blank
	for t := 0; t < frames; t++ {
		row := lp[0][t*classes : (t+1)*classes]
		k := 0
		for c := range row {
			if row[c] > row[k] {
				k = c
			}
		}
		if k != s.blank && k != prev && k < len(s.vocab) {
			toks = append(toks, Token{s.vocab[k], float64(t) * speechFrame, row[k]})
		}
		prev = k
	}
	return toks, nil
}
