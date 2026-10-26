package scan

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ishpagin/mediasorter/web/internal/media"
	"github.com/ishpagin/mediasorter/web/internal/vision"
)

// Progress: done of total, with a short line of what is going on.
type Progress func(done, total int, msg string)

// Options of a scan (the Python defaults).
type Options struct {
	DetSize   int     // 960
	MinFace   float32 // 24
	VideoStep float64 // 2
	Workers   int     // files analyzed at once
	NoVideo   bool
}

func DefaultOptions() Options { return Options{DetSize: 960, MinFace: 24, VideoStep: 2, Workers: 4} }

type todo struct {
	path, kind  string
	size, mtime int64
}

// Scan one folder (recursively, read-only): new and changed files are analyzed, files gone from it are
// forgotten. A missing folder (an unmounted NAS) is an error, never "everything was deleted".
func Scan(ctx context.Context, db *sql.DB, models *Models, src string, opt Options, progress Progress) (int, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return 0, err
	}
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return 0, fmt.Errorf("%s is not a folder (NAS not mounted?): nothing scanned", src)
	}
	known := map[string][2]int64{}
	var inDB []string
	rows, err := db.Query("SELECT path, size, mtime, error IS NULL FROM files")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var p string
		var size, mtime sql.NullInt64
		var ok bool
		if err := rows.Scan(&p, &size, &mtime, &ok); err != nil {
			rows.Close()
			return 0, err
		}
		inDB = append(inDB, p)
		if ok { // files with errors are retried
			known[p] = [2]int64{size.Int64, mtime.Int64}
		}
	}
	rows.Close()

	var list []todo
	seen := map[string]bool{}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folder: skip it, keep going
		}
		name := d.Name()
		if d.IsDir() {
			if p != src && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_review") || name == "_photosorter_deleted") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, "._") { // macOS AppleDouble metadata, not real images
			return nil
		}
		kind := media.Kind(name)
		if kind == "" || opt.NoVideo && kind == "video" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		seen[p] = true
		sz, mt := info.Size(), info.ModTime().Unix()
		if k, ok := known[p]; !ok || k != [2]int64{sz, mt} {
			list = append(list, todo{p, kind, sz, mt})
		}
		return ctx.Err()
	})
	if err != nil {
		return 0, err
	}
	prefix := src + string(filepath.Separator)
	var gone []string
	for _, p := range inDB {
		if strings.HasPrefix(p, prefix) && !seen[p] {
			gone = append(gone, p)
		}
	}
	for _, p := range gone {
		if _, err := db.Exec("DELETE FROM files WHERE path = ?", p); err != nil {
			return 0, err
		}
	}
	sort.SliceStable(list, func(a, b int) bool { // photos first: usable while videos still run
		if (list[a].kind == "image") != (list[b].kind == "image") {
			return list[a].kind == "image"
		}
		return list[a].path < list[b].path
	})
	logf(progress, 0, len(list), "%d files in %s, %d new or changed, %d gone", len(seen), src, len(list), len(gone))
	if len(list) == 0 {
		return 0, Geocode(db)
	}

	engine := &Engine{Faces: models.Faces, VideoStep: opt.VideoStep}
	type done struct {
		t   todo
		r   *Result
		err error
	}
	jobs, results := make(chan todo), make(chan done)
	var wg sync.WaitGroup
	for w := 0; w < max(1, opt.Workers); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				r, err := engine.Analyze(ctx, t.path, t.kind)
				if err == nil && models.Clip != nil {
					err = r.embedPreviews(models.Clip)
				}
				results <- done{t, r, err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, t := range list {
			select {
			case jobs <- t:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	n, errs := 0, 0
	for d := range results {
		if ctx.Err() != nil {
			continue // drain: a cancelled scan writes nothing half-done
		}
		r, msg := d.r, ""
		if d.err != nil { // ONNX Runtime on Windows may answer in the local code page
			r, msg = &Result{}, strings.ToValidUTF8(d.err.Error(), "?")
			errs++
		}
		if err := store(db, d.t, r, msg); err != nil {
			return n, err
		}
		n++
		if n%20 == 0 || n == len(list) {
			logf(progress, n, len(list), "%d of %d files (%d unreadable)", n, len(list), errs)
		}
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	if err := refreshPersonStats(db); err != nil {
		return n, err
	}
	return n, Geocode(db)
}

func logf(p Progress, done, total int, f string, a ...any) {
	if p != nil {
		p(done, total, fmt.Sprintf(f, a...))
	}
}

// embedPreviews: scene vectors of the previews right away (otherwise the "embed" step does it later).
func (r *Result) embedPreviews(c *vision.Clip) error {
	r.clips = make([][]float32, len(r.Previews))
	for i, p := range r.Previews {
		v, err := c.ImageJPEG(p.JPEG)
		if err != nil {
			return err
		}
		r.clips[i] = v
	}
	return nil
}

var nameDate = regexp.MustCompile(`(?:^|[^\d])((?:19|20)\d\d)[-_.]?(0[1-9]|1[0-2])[-_.]?([0-2]\d|3[01])(?:[-_. T]?([01]\d|2[0-3])[-_.]?([0-5]\d)[-_.]?([0-5]\d))?(?:[^\d]|$)`)

// DateFromName: IMG_20180813_135040.jpg, VID-20190101-WA0001.mp4, 2019-01-01 12.30.45.jpg ...
func DateFromName(name string) string {
	if m := nameDate.FindStringSubmatchIndex(name); m != nil { // only the first match, like Python's re.search
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return name[m[2*i]:m[2*i+1]]
		}
		y, _ := strconv.Atoi(g(1))
		mo, _ := strconv.Atoi(g(2))
		d, _ := strconv.Atoi(g(3))
		t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
		if t.Day() != d || t.Month() != time.Month(mo) {
			return ""
		}
		if g(4) == "" {
			return t.Format("2006-01-02T15:04:05")
		}
		h, _ := strconv.Atoi(g(4))
		mi, _ := strconv.Atoi(g(5))
		s, _ := strconv.Atoi(g(6))
		return time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC).Format("2006-01-02T15:04:05")
	}
	return ""
}

// store: one analyzed file, replacing its old rows. Like Python's store_file.
func store(db *sql.DB, t todo, r *Result, errMsg string) error {
	m := r.Meta
	if m.TakenAt == nil {
		d, src := DateFromName(filepath.Base(t.path)), "filename"
		if d == "" {
			d, src = time.Unix(t.mtime, 0).Format("2006-01-02T15:04:05"), "mtime"
		}
		m.TakenAt, m.DateSource = &d, &src
	}
	var e *string
	if errMsg != "" {
		e = &errMsg
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM files WHERE path = ?", t.path); err != nil {
		return err
	}
	res, err := tx.Exec(`INSERT INTO files (path, kind, size, mtime, n_faces, error, scanned_at, width, height, duration,
		fps, codec, taken_at, date_source, lat, lon, alt, camera_make, camera_model, phash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.path, t.kind, t.size, t.mtime, len(r.Faces), e, time.Now().Format("2006-01-02T15:04:05"),
		m.Width, m.Height, m.Duration, m.FPS, m.Codec, m.TakenAt, m.DateSource, m.Lat, m.Lon, m.Alt,
		m.CameraMake, m.CameraModel, m.Phash)
	if err != nil {
		return err
	}
	fid, _ := res.LastInsertId()
	for i, p := range r.Previews {
		if _, err := tx.Exec("INSERT INTO previews (file_id, t_sec, jpeg) VALUES (?,?,?)", fid, p.T, p.JPEG); err != nil {
			return err
		}
		if r.clips != nil {
			if _, err := tx.Exec("INSERT INTO clip (file_id, t_sec, emb) VALUES (?,?,?)", fid, p.T, F16Blob(r.clips[i])); err != nil {
				return err
			}
		}
	}
	for _, f := range r.Faces {
		if _, err := tx.Exec(`INSERT INTO faces (file_id, t_sec, x1, y1, x2, y2, det_score, size, emb_norm, embedding, thumb)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, fid, f.T, f.Box[0], f.Box[1], f.Box[2], f.Box[3], f.Score, f.Size, f.Norm,
			F16Blob(f.Emb), f.Thumb); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func refreshPersonStats(db *sql.DB) error {
	_, err := db.Exec(`UPDATE persons SET
		n_faces = (SELECT COUNT(*) FROM faces WHERE person_id = persons.id),
		n_files = (SELECT COUNT(DISTINCT file_id) FROM faces WHERE person_id = persons.id)`)
	if err == nil {
		_, err = db.Exec("DELETE FROM persons WHERE n_faces = 0 AND name IS NULL")
	}
	return err
}

// F16Blob: little-endian float16 (round to nearest even), what numpy's astype(float16).tobytes() gives.
func F16Blob(v []float32) []byte {
	b := make([]byte, 2*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint16(b[2*i:], toF16(x))
	}
	return b
}

func toF16(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int((b>>23)&0xff) - 127 + 15
	man := b & 0x7fffff
	switch {
	case (b>>23)&0xff == 0xff: // inf / nan
		if man != 0 {
			return sign | 0x7e00
		}
		return sign | 0x7c00
	case exp >= 31:
		return sign | 0x7c00
	case exp <= 0: // subnormal or zero
		if exp < -10 {
			return sign
		}
		man |= 0x800000
		shift := uint(14 - exp)
		h := man >> shift
		rem := man & (1<<shift - 1)
		half := uint32(1) << (shift - 1)
		if rem > half || rem == half && h&1 == 1 {
			h++
		}
		return sign | uint16(h)
	}
	h := uint32(exp)<<10 | man>>13
	rem := man & 0x1fff
	if rem > 0x1000 || rem == 0x1000 && h&1 == 1 {
		h++ // may carry into the exponent: still correct
	}
	return sign | uint16(h)
}

// F16 decodes one float16.
func F16(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1f
	man := uint32(h & 0x3ff)
	switch {
	case exp == 0:
		if man == 0 {
			return math.Float32frombits(sign)
		}
		e := uint32(127 - 15 + 1)
		for man&0x400 == 0 {
			man <<= 1
			e--
		}
		return math.Float32frombits(sign | e<<23 | (man&0x3ff)<<13)
	case exp == 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | man<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | man<<13)
}

// Vectors decodes float16 blobs into rows of dim floats.
func Vectors(blob []byte, dim int) [][]float32 {
	n := len(blob) / 2 / dim
	out := make([][]float32, n)
	for i := range out {
		out[i] = make([]float32, dim)
		for k := 0; k < dim; k++ {
			out[i][k] = F16(binary.LittleEndian.Uint16(blob[(i*dim+k)*2:]))
		}
	}
	return out
}
