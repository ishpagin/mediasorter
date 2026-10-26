package vision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	ort "github.com/yalue/onnxruntime_go"
)

// SigLIP2 ViT-B/16 (multilingual), ONNX export by Immich: 768-d vectors for pictures and texts.
const (
	ClipDim     = 768
	clipSize    = 224
	clipContext = 64
)

type Clip struct {
	visual, textual *Session
	tok             *Tokenizer
}

// NewClip loads the image tower and/or the text tower from models/siglip2.
func NewClip(models string, image, text bool) (*Clip, error) {
	c := &Clip{}
	var err error
	dir := filepath.Join(models, "siglip2")
	if image {
		if c.visual, err = NewSession(filepath.Join(dir, "visual", "model.onnx")); err != nil {
			return nil, err
		}
	}
	if text {
		if c.textual, err = NewSession(filepath.Join(dir, "textual", "model.onnx")); err != nil {
			c.Close()
			return nil, err
		}
		if c.tok, err = LoadTokenizer(filepath.Join(dir, "textual", "tokenizer.json")); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

func (c *Clip) Close() { c.visual.Close(); c.textual.Close() }

// ImageJPEG: vector of a stored preview (JPEG bytes).
func (c *Clip) ImageJPEG(jpg []byte) ([]float32, error) {
	m, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil, err
	}
	return c.Image(FromImage(m))
}

// Image: 'squash' resize to 224x224 (no crop), like the model's preprocessing.
func (c *Clip) Image(img *RGB) ([]float32, error) {
	blob := make([]float32, 3*clipSize*clipSize)
	Blob(ResizeCubic(img, clipSize, clipSize), 127.5, 1/127.5, blob)
	in, err := ort.NewTensor(ort.NewShape(1, 3, clipSize, clipSize), blob)
	if err != nil {
		return nil, err
	}
	defer in.Destroy()
	out, _, err := c.visual.Run(in)
	if err != nil {
		return nil, err
	}
	return unit(out[0][:ClipDim]), nil
}

// Text: vector of a query (any language).
func (c *Clip) Text(s string) ([]float32, error) {
	ids := c.tok.Encode(Canonicalize(s), clipContext)
	var in ort.Value
	var err error
	if c.textual.InputIsInt64(0) {
		v := make([]int64, len(ids))
		for i, x := range ids {
			v[i] = int64(x)
		}
		in, err = ort.NewTensor(ort.NewShape(1, clipContext), v)
	} else {
		in, err = ort.NewTensor(ort.NewShape(1, clipContext), ids)
	}
	if err != nil {
		return nil, err
	}
	defer in.Destroy()
	out, _, err := c.textual.Run(in)
	if err != nil {
		return nil, err
	}
	return unit(out[0][:ClipDim]), nil
}

// TextMean: normalized mean of several prompts' vectors (a category).
func (c *Clip) TextMean(texts []string) ([]float32, error) {
	sum := make([]float32, ClipDim)
	for _, t := range texts {
		v, err := c.Text(t)
		if err != nil {
			return nil, err
		}
		for i := range sum {
			sum[i] += v[i]
		}
	}
	return unit(sum), nil
}

func unit(v []float32) []float32 {
	n := norm(v)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

var (
	punct  = regexp.MustCompile(`[^\p{L}\p{N}_\s]`)
	spaces = regexp.MustCompile(`\s+`)
)

// Canonicalize: open_clip's text cleaning used by this model (lower case, punctuation -> spaces).
func Canonicalize(s string) string {
	s = punct.ReplaceAllString(strings.ToLower(s), " ")
	return strings.TrimFunc(spaces.ReplaceAllString(s, " "), unicode.IsSpace)
}

// Tokenizer: the Gemma BPE of tokenizer.json (spaces -> "▁", byte fallback, <eos> at the end, padded).
type Tokenizer struct {
	vocab         map[string]int32
	ranks         map[[2]string]int
	pad, eos, unk int32
}

func LoadTokenizer(path string) (*Tokenizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Model struct {
			Vocab  map[string]int32  `json:"vocab"`
			Merges []json.RawMessage `json:"merges"`
		} `json:"model"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	t := &Tokenizer{vocab: f.Model.Vocab, ranks: make(map[[2]string]int, len(f.Model.Merges))}
	for i, m := range f.Model.Merges {
		var pair [2]string
		var s string
		if json.Unmarshal(m, &s) == nil { // "a b"
			a, b, _ := strings.Cut(s, " ")
			pair = [2]string{a, b}
		} else if err := json.Unmarshal(m, &pair); err != nil { // ["a", "b"]
			return nil, fmt.Errorf("%s: merge %d: %w", path, i, err)
		}
		if _, dup := t.ranks[pair]; !dup {
			t.ranks[pair] = i
		}
	}
	var ok [3]bool
	t.pad, ok[0] = t.vocab["<pad>"]
	t.eos, ok[1] = t.vocab["<eos>"]
	t.unk, ok[2] = t.vocab["<unk>"]
	if !ok[0] || !ok[1] || !ok[2] {
		return nil, fmt.Errorf("%s: no <pad>/<eos>/<unk>", path)
	}
	return t, nil
}

// Encode: token ids, truncated to n-1 + <eos>, padded with <pad> to n.
func (t *Tokenizer) Encode(s string, n int) []int32 {
	ids := t.bpe(strings.ReplaceAll(s, " ", "▁"))
	if len(ids) > n-1 {
		ids = ids[:n-1]
	}
	ids = append(ids, t.eos)
	for len(ids) < n {
		ids = append(ids, t.pad)
	}
	return ids
}

func (t *Tokenizer) bpe(s string) []int32 {
	if s == "" {
		return nil
	}
	syms := make([]string, 0, len(s))
	for _, r := range s {
		syms = append(syms, string(r))
	}
	for { // merge the best-ranked adjacent pair (leftmost on ties) until none is left
		best, at := -1, -1
		for i := 0; i+1 < len(syms); i++ {
			if r, ok := t.ranks[[2]string{syms[i], syms[i+1]}]; ok && (best < 0 || r < best) {
				best, at = r, i
			}
		}
		if at < 0 {
			break
		}
		syms[at] += syms[at+1]
		syms = append(syms[:at+1], syms[at+2:]...)
	}
	var ids []int32
	for _, x := range syms {
		if id, ok := t.vocab[x]; ok {
			ids = append(ids, id)
			continue
		}
		fell := true // byte fallback: <0xNN> per UTF-8 byte
		var bs []int32
		for _, b := range []byte(x) {
			id, ok := t.vocab[fmt.Sprintf("<0x%02X>", b)]
			if !ok {
				fell = false
				break
			}
			bs = append(bs, id)
		}
		if fell {
			ids = append(ids, bs...)
		} else if len(ids) == 0 || ids[len(ids)-1] != t.unk { // fuse_unk
			ids = append(ids, t.unk)
		}
	}
	return ids
}
