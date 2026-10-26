package vision

import (
	"encoding/binary"
	"errors"
	"os"
)

// fixInputDims rewrites the shape of a model's first input to fixed sizes (ONNX protobuf, edited on
// the wire: ModelProto.graph(7) > GraphProto.input(11) > ValueInfoProto.type(2) > TypeProto.tensor_type(1)
// > shape(2) > dim(1) {dim_value(1)}). DirectML fails on these models' free dimensions ("?", "None").
func fixInputDims(src, dst string, dims []int64) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	out, err := patchField(b, []int{7, 11, 2, 1, 2}, func(shape []byte) []byte {
		var s []byte
		for _, d := range dims {
			s = appendLen(s, 1, appendVarint(appendTag(nil, 1, 0), uint64(d)))
		}
		return s
	})
	if err != nil {
		return err
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

type pbField struct {
	num, wire int
	raw       []byte // the whole field (tag + value)
	val       []byte // payload of a length-delimited field
}

func readFields(b []byte) ([]pbField, error) {
	var out []pbField
	for i := 0; i < len(b); {
		start := i
		tag, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return nil, errors.New("bad protobuf tag")
		}
		i += n
		f := pbField{num: int(tag >> 3), wire: int(tag & 7)}
		switch f.wire {
		case 0:
			_, n = binary.Uvarint(b[i:])
			if n <= 0 {
				return nil, errors.New("bad varint")
			}
			i += n
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			l, n := binary.Uvarint(b[i:])
			if n <= 0 || i+n+int(l) > len(b) {
				return nil, errors.New("bad length")
			}
			i += n
			f.val = b[i : i+int(l)]
			i += int(l)
		default:
			return nil, errors.New("unsupported wire type")
		}
		if i > len(b) {
			return nil, errors.New("truncated protobuf")
		}
		f.raw = b[start:i]
		out = append(out, f)
	}
	return out, nil
}

// patchField replaces the payload at the path (field numbers; the first match at each level) by
// f(payload), re-encoding the lengths of every enclosing message.
func patchField(b []byte, path []int, f func([]byte) []byte) ([]byte, error) {
	fields, err := readFields(b)
	if err != nil {
		return nil, err
	}
	var out []byte
	done := false
	for _, x := range fields {
		if !done && x.num == path[0] && x.wire == 2 {
			var v []byte
			if len(path) == 1 {
				v = f(x.val)
			} else if v, err = patchField(x.val, path[1:], f); err != nil {
				return nil, err
			}
			out = appendLen(out, x.num, v)
			done = true
			continue
		}
		out = append(out, x.raw...)
	}
	if !done {
		return nil, errors.New("model input shape not found")
	}
	return out, nil
}

func appendTag(b []byte, num, wire int) []byte { return appendVarint(b, uint64(num<<3|wire)) }

func appendVarint(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

func appendLen(b []byte, num int, v []byte) []byte {
	b = appendTag(b, num, 2)
	b = appendVarint(b, uint64(len(v)))
	return append(b, v...)
}
