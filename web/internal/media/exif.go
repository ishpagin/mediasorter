// Package media reads photos and videos: decoding (JPEG, PNG, WebP, GIF, BMP, TIFF, HEIC, RAW previews,
// video frames via ffmpeg), metadata (EXIF, video tags) and the small derived bits stored in the DB.
package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Meta: what the DB keeps about a file (nil = unknown).
type Meta struct {
	TakenAt     *string // ISO 8601, local time (+offset when known)
	DateSource  *string // exif | video | filename | mtime
	Lat, Lon    *float64
	Alt         *float64
	CameraMake  *string
	CameraModel *string
	Width       *int
	Height      *int
	Duration    *float64
	FPS         *float64
	Codec       *string
	Phash       *int64
	Orientation int // EXIF orientation, not stored
}

type exifTag struct {
	typ   uint16
	count uint32
	val   []byte // raw value bytes
}

// tiff: a parsed TIFF/EXIF block: IFD0, the Exif sub-IFD and the GPS IFD.
type tiff struct {
	bo              binary.ByteOrder
	ifd0, exif, gps map[uint16]exifTag
}

func parseTIFF(b []byte) (*tiff, error) {
	if len(b) < 8 {
		return nil, fmt.Errorf("short tiff")
	}
	t := &tiff{}
	switch string(b[:2]) {
	case "II":
		t.bo = binary.LittleEndian
	case "MM":
		t.bo = binary.BigEndian
	default:
		return nil, fmt.Errorf("not tiff")
	}
	var err error
	if t.ifd0, err = t.ifd(b, t.bo.Uint32(b[4:])); err != nil {
		return nil, err
	}
	if o, ok := t.ifd0[0x8769]; ok {
		t.exif, _ = t.ifd(b, t.u32(o))
	}
	if o, ok := t.ifd0[0x8825]; ok {
		t.gps, _ = t.ifd(b, t.u32(o))
	}
	return t, nil
}

var typeSize = map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 7: 1, 9: 4, 10: 8, 11: 4, 12: 8}

func (t *tiff) ifd(b []byte, off uint32) (map[uint16]exifTag, error) {
	if int(off)+2 > len(b) {
		return nil, fmt.Errorf("bad ifd offset")
	}
	n := int(t.bo.Uint16(b[off:]))
	m := map[uint16]exifTag{}
	for i := 0; i < n; i++ {
		e := int(off) + 2 + i*12
		if e+12 > len(b) {
			break
		}
		tag, typ, cnt := t.bo.Uint16(b[e:]), t.bo.Uint16(b[e+2:]), t.bo.Uint32(b[e+4:])
		size := typeSize[typ] * int(cnt)
		if size == 0 || size > 1<<20 {
			continue
		}
		v := b[e+8 : e+12]
		if size > 4 {
			p := int(t.bo.Uint32(b[e+8:]))
			if p < 0 || p+size > len(b) {
				continue
			}
			v = b[p : p+size]
		} else {
			v = v[:size]
		}
		m[tag] = exifTag{typ, cnt, v}
	}
	return m, nil
}

func (t *tiff) u32(e exifTag) uint32 {
	switch e.typ {
	case 3:
		return uint32(t.bo.Uint16(e.val))
	case 4, 9:
		return t.bo.Uint32(e.val)
	case 1, 7:
		return uint32(e.val[0])
	}
	return 0
}

func (t *tiff) str(m map[uint16]exifTag, tag uint16) string {
	e, ok := m[tag]
	if !ok || e.typ != 2 && e.typ != 7 {
		return ""
	}
	return string(e.val)
}

func (t *tiff) rationals(m map[uint16]exifTag, tag uint16) []float64 {
	e, ok := m[tag]
	if !ok || e.typ != 5 && e.typ != 10 {
		return nil
	}
	out := make([]float64, e.count)
	for i := range out {
		n, d := t.bo.Uint32(e.val[i*8:]), t.bo.Uint32(e.val[i*8+4:])
		if e.typ == 10 {
			out[i] = float64(int32(n)) / float64(int32(d))
		} else {
			out[i] = float64(n) / float64(d)
		}
	}
	return out
}

// text: like Python's _text: NULs and spaces trimmed, "" -> nil.
func text(s string) *string {
	s = strings.TrimSpace(strings.Trim(s, "\x00 "))
	if s == "" {
		return nil
	}
	return &s
}

var offsetRe = regexp.MustCompile(`^[+-]\d\d:\d\d$`)

// exifDate: '2018:08:13 13:50:40' (+ '+03:00') -> ISO 8601, or "".
func exifDate(v, offset string) string {
	v = strings.TrimSpace(strings.Trim(v, "\x00"))
	if len(v) > 19 {
		v = v[:19]
	}
	d, err := time.Parse("2006:01:02 15:04:05", v)
	if err != nil {
		return ""
	}
	s := d.Format("2006-01-02T15:04:05")
	if o := strings.TrimSpace(strings.Trim(offset, "\x00")); offsetRe.MatchString(o) {
		s += o
	}
	return s
}

// meta: date taken, GPS and camera from a TIFF/EXIF block (like Python's image_meta).
func (t *tiff) meta() Meta {
	var m Meta
	dt := t.str(t.exif, 0x9003)
	if dt == "" {
		dt = t.str(t.exif, 0x9004)
	}
	if dt == "" {
		dt = t.str(t.ifd0, 0x0132)
	}
	if dt != "" {
		off := t.str(t.exif, 0x9011)
		if off == "" {
			off = t.str(t.exif, 0x9010)
		}
		if s := exifDate(dt, off); s != "" {
			src := "exif"
			m.TakenAt, m.DateSource = &s, &src
		}
	}
	m.CameraMake, m.CameraModel = text(t.str(t.ifd0, 0x010F)), text(t.str(t.ifd0, 0x0110))
	if t.gps != nil {
		lat, lon := dms(t.rationals(t.gps, 2), t.str(t.gps, 1)), dms(t.rationals(t.gps, 4), t.str(t.gps, 3))
		if lat != nil && lon != nil && (math.Abs(*lat) > 1e-6 || math.Abs(*lon) > 1e-6) {
			m.Lat, m.Lon = lat, lon
			if a := t.rationals(t.gps, 6); len(a) == 1 && !math.IsNaN(a[0]) && !math.IsInf(a[0], 0) {
				m.Alt = &a[0]
			}
		}
	}
	if o, ok := t.ifd0[0x0112]; ok {
		m.Orientation = int(t.u32(o))
	}
	return m
}

func dms(v []float64, ref string) *float64 {
	if len(v) < 3 {
		return nil
	}
	d := v[0] + v[1]/60 + v[2]/3600
	if math.IsNaN(d) || math.IsInf(d, 0) {
		return nil
	}
	if r := strings.Trim(ref, "\x00 "); r == "S" || r == "W" {
		d = -d
	}
	return &d
}

// findEXIF: the TIFF block inside a JPEG (APP1), PNG (eXIf), WebP (EXIF chunk) or a TIFF-based file.
func findEXIF(b []byte) []byte {
	switch {
	case len(b) > 4 && b[0] == 0xFF && b[1] == 0xD8:
		for i := 2; i+4 < len(b); {
			if b[i] != 0xFF {
				return nil
			}
			marker := b[i+1]
			if marker == 0xD8 || marker >= 0xD0 && marker <= 0xD7 || marker == 0x01 {
				i += 2
				continue
			}
			if marker == 0xDA || marker == 0xD9 {
				return nil // image data: no EXIF before it
			}
			n := int(binary.BigEndian.Uint16(b[i+2:]))
			seg := b[i+4 : min(len(b), i+2+n)]
			if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
				return seg[6:]
			}
			i += 2 + n
		}
	case bytes.HasPrefix(b, []byte("\x89PNG")):
		for i := 8; i+8 <= len(b); {
			n := int(binary.BigEndian.Uint32(b[i:]))
			typ := string(b[i+4 : i+8])
			if typ == "eXIf" && i+8+n <= len(b) {
				return bytes.TrimPrefix(b[i+8:i+8+n], []byte("Exif\x00\x00"))
			}
			if typ == "IDAT" || typ == "IEND" {
				return nil
			}
			i += 12 + n
		}
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		for i := 12; i+8 <= len(b); {
			n := int(binary.LittleEndian.Uint32(b[i+4:]))
			if string(b[i:i+4]) == "EXIF" && i+8+n <= len(b) {
				return bytes.TrimPrefix(b[i+8:i+8+n], []byte("Exif\x00\x00"))
			}
			i += 8 + n + n%2
		}
	case bytes.HasPrefix(b, []byte("II*\x00")) || bytes.HasPrefix(b, []byte("MM\x00*")):
		return b
	}
	return nil
}

// heicEXIF: the Exif item of a HEIF file (meta/iinf + iloc), its TIFF part.
func heicEXIF(b []byte) []byte {
	meta := box(b, "meta")
	if meta == nil || len(meta) < 4 {
		return nil
	}
	meta = meta[4:] // full box: version + flags
	iinf, iloc := box(meta, "iinf"), box(meta, "iloc")
	if iinf == nil || iloc == nil {
		return nil
	}
	id := exifItemID(iinf)
	if id == 0 {
		return nil
	}
	off, n := ilocExtent(iloc, id)
	if n < 10 || off+n > uint64(len(b)) {
		return nil
	}
	item := b[off : off+n]
	skip := uint64(binary.BigEndian.Uint32(item)) + 4 // offset to the TIFF header
	if skip >= uint64(len(item)) {
		return nil
	}
	return item[skip:]
}

// box: payload of the first child box of that type (ISO BMFF, 32/64-bit sizes).
func box(b []byte, typ string) []byte {
	for i := 0; i+8 <= len(b); {
		size, hdr := uint64(binary.BigEndian.Uint32(b[i:])), 8
		if size == 1 && i+16 <= len(b) {
			size, hdr = binary.BigEndian.Uint64(b[i+8:]), 16
		} else if size == 0 {
			size = uint64(len(b) - i)
		}
		if size < uint64(hdr) || uint64(i)+size > uint64(len(b)) {
			return nil
		}
		if string(b[i+4:i+8]) == typ {
			return b[i+hdr : uint64(i)+size]
		}
		i += int(size)
	}
	return nil
}

func exifItemID(iinf []byte) uint32 {
	if len(iinf) < 6 {
		return 0
	}
	p := 6
	if iinf[0] != 0 { // version 1+: 32-bit entry count
		p = 8
	}
	for p+8 <= len(iinf) {
		size := int(binary.BigEndian.Uint32(iinf[p:]))
		if size < 8 || p+size > len(iinf) {
			return 0
		}
		if string(iinf[p+4:p+8]) == "infe" {
			e := iinf[p+8 : p+size]
			if len(e) >= 4 && e[0] >= 2 {
				var id uint32
				q := 4
				if e[0] == 2 {
					id, q = uint32(binary.BigEndian.Uint16(e[4:])), 6
				} else if len(e) >= 8 {
					id, q = binary.BigEndian.Uint32(e[4:]), 8
				}
				if q+6 <= len(e) && string(e[q+2:q+6]) == "Exif" {
					return id
				}
			}
		}
		p += size
	}
	return 0
}

func ilocExtent(b []byte, want uint32) (uint64, uint64) {
	if len(b) < 8 {
		return 0, 0
	}
	ver := b[0]
	offSize, lenSize := int(b[4]>>4), int(b[4]&15)
	baseSize, idxSize := int(b[5]>>4), 0
	if ver == 1 || ver == 2 {
		idxSize = int(b[5] & 15)
	}
	p := 6
	var count int
	if ver < 2 {
		count, p = int(binary.BigEndian.Uint16(b[p:])), p+2
	} else {
		count, p = int(binary.BigEndian.Uint32(b[p:])), p+4
	}
	read := func(n int) uint64 {
		var v uint64
		for i := 0; i < n && p < len(b); i++ {
			v = v<<8 | uint64(b[p])
			p++
		}
		return v
	}
	for i := 0; i < count && p < len(b); i++ {
		var id uint32
		if ver < 2 {
			id = uint32(read(2))
		} else {
			id = uint32(read(4))
		}
		if ver == 1 || ver == 2 {
			read(2) // construction method
		}
		read(2) // data reference index
		base := read(baseSize)
		ext := int(read(2))
		var off, n uint64
		for k := 0; k < ext; k++ {
			if idxSize > 0 {
				read(idxSize)
			}
			o, l := read(offSize), read(lenSize)
			if k == 0 {
				off, n = o, l
			}
		}
		if id == want {
			return base + off, n
		}
	}
	return 0, 0
}
