package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ishpagin/mediasorter/web/internal/vision"
)

// FFmpeg / FFprobe: the tools' paths (MS_FFMPEG = folder with both; else next to this binary; else PATH).
var FFmpeg, FFprobe = tool("ffmpeg"), tool("ffprobe")

func tool(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dirs := []string{os.Getenv("MS_FFMPEG")}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if p := filepath.Join(d, name); fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Video: what ffprobe tells about the first video stream.
type Video struct {
	Path     string
	W, H     int // as displayed (rotation applied)
	Duration float64
	Meta     Meta
}

type probe struct {
	Streams []struct {
		CodecType    string            `json:"codec_type"`
		CodecName    string            `json:"codec_name"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		AvgFrameRate string            `json:"avg_frame_rate"`
		Duration     string            `json:"duration"`
		Tags         map[string]string `json:"tags"`
		SideData     []struct {
			Rotation *float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

var locRe = regexp.MustCompile(`^([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)?`)

// OpenVideo: stream info and metadata (date, GPS, camera, codec, fps), like Python's video_meta.
func OpenVideo(ctx context.Context, path string) (*Video, error) {
	out, err := exec.CommandContext(ctx, FFprobe, "-v", "error", "-print_format", "json", "-show_format",
		"-show_streams", "-select_streams", "v:0", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", exitErr(err))
	}
	var p probe
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	if len(p.Streams) == 0 {
		return nil, fmt.Errorf("no video stream")
	}
	s := p.Streams[0]
	v := &Video{Path: path, W: s.Width, H: s.Height}
	for _, sd := range s.SideData {
		if sd.Rotation != nil && int(math.Round(math.Abs(*sd.Rotation)/90))%2 == 1 {
			v.W, v.H = v.H, v.W
		}
	}
	md := map[string]string{}
	for k, x := range p.Format.Tags {
		md[strings.ToLower(k)] = x
	}
	for k, x := range s.Tags {
		md[strings.ToLower(k)] = x
	}
	m := &v.Meta
	for _, key := range []string{"com.apple.quicktime.creationdate", "creation_time"} {
		if d, ok := isoDate(md[key]); ok {
			src := "video"
			m.TakenAt, m.DateSource = &d, &src
			break
		}
	}
	loc := md["com.apple.quicktime.location.iso6709"]
	if loc == "" {
		loc = md["location"]
	}
	if loc == "" {
		loc = md["location-eng"]
	}
	if g := locRe.FindStringSubmatch(loc); g != nil {
		lat, _ := strconv.ParseFloat(g[1], 64)
		lon, _ := strconv.ParseFloat(g[2], 64)
		m.Lat, m.Lon = &lat, &lon
		if g[3] != "" {
			alt, _ := strconv.ParseFloat(g[3], 64)
			m.Alt = &alt
		}
	}
	mk := md["com.apple.quicktime.make"]
	if mk == "" {
		mk = md["make"]
	}
	mo := md["com.apple.quicktime.model"]
	if mo == "" {
		mo = md["model"]
	}
	m.CameraMake, m.CameraModel = text(mk), text(mo)
	codec := s.CodecName
	m.Codec = &codec
	if n, d, ok := strings.Cut(s.AvgFrameRate, "/"); ok {
		a, _ := strconv.ParseFloat(n, 64)
		b, _ := strconv.ParseFloat(d, 64)
		if a > 0 && b > 0 {
			f := a / b
			m.FPS = &f
		}
	}
	v.Duration, _ = strconv.ParseFloat(s.Duration, 64)
	if v.Duration == 0 {
		v.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	}
	if v.Duration > 0 {
		m.Duration = &v.Duration
	}
	return v, nil
}

// isoDate: Apple's local time with offset or UTC creation_time -> ISO 8601 to the second (years > 1971).
func isoDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	for _, f := range []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		t, err := time.Parse(f, s)
		if err != nil || t.Year() <= 1971 {
			continue
		}
		if !strings.ContainsAny(s[10:], "Z+-") {
			return t.Format("2006-01-02T15:04:05"), true
		}
		return t.Format("2006-01-02T15:04:05-07:00"), true
	}
	return "", false
}

// Frame: an upright video frame at T seconds.
type Frame struct {
	T   float64
	Img *vision.RGB
}

// Frames: roughly one keyframe per step seconds. Long videos seek to each point (reads a small part of
// the file: matters on a NAS), short ones are decoded through (keyframes only).
func (v *Video) Frames(ctx context.Context, step float64, each func(Frame) error) error {
	if v.W <= 0 || v.H <= 0 {
		return fmt.Errorf("bad video size")
	}
	if v.Duration > 2*step {
		last := math.NaN()
		for t := 0.0; t < v.Duration; t += step {
			fr, err := v.frameAt(ctx, t)
			if err != nil {
				return err
			}
			if fr == nil {
				break
			}
			if fr.T != last {
				if err := each(*fr); err != nil {
					return err
				}
			}
			last = fr.T
		}
		return nil
	}
	frames, err := v.decode(ctx, nil, 0)
	if err != nil {
		return err
	}
	last := -1e9
	for _, fr := range frames {
		if fr.T-last >= step {
			last = fr.T
			if err := each(fr); err != nil {
				return err
			}
		}
	}
	return nil
}

// frameAt: the keyframe at or before t (like a seek + one decoded frame), nil after the end.
func (v *Video) frameAt(ctx context.Context, t float64) (*Frame, error) {
	frames, err := v.decode(ctx, []string{"-ss", strconv.FormatFloat(t, 'f', 3, 64), "-noaccurate_seek"}, 1)
	if err != nil || len(frames) == 0 {
		return nil, err
	}
	return &frames[0], nil
}

var frameRe = regexp.MustCompile(`pts_time:\s*(-?[\d.]+).*? s:(\d+)x(\d+)`)

// decode: keyframes as RGB (n = 0: all), with their timestamps and sizes from ffmpeg's showinfo.
// Only the codec's own cropping is applied, not the container's ("clean aperture" of iPhone videos): sizes and face boxes
// stay those of the full frame, as the earlier scans stored them.
func (v *Video) decode(ctx context.Context, seek []string, n int) ([]Frame, error) {
	args := []string{"-v", "info", "-nostats", "-hide_banner", "-apply_cropping", "codec", "-skip_frame", "nokey"}
	args = append(args, seek...)
	args = append(args, "-copyts", "-i", v.Path, "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", "showinfo",
		"-fps_mode", "passthrough", "-pix_fmt", "rgb24", "-f", "rawvideo")
	if n > 0 {
		args = append(args, "-frames:v", strconv.Itoa(n))
	}
	args = append(args, "pipe:1")
	cmd := exec.CommandContext(ctx, FFmpeg, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, lastLine(errb.String()))
	}
	var frames []Frame
	raw := out.Bytes()
	for _, m := range frameRe.FindAllStringSubmatch(errb.String(), -1) {
		t, _ := strconv.ParseFloat(m[1], 64)
		w, _ := strconv.Atoi(m[2])
		h, _ := strconv.Atoi(m[3])
		if w*h*3 > len(raw) || w <= 0 || h <= 0 {
			break
		}
		img := vision.NewRGB(w, h)
		copy(img.Pix, raw[:w*h*3])
		raw = raw[w*h*3:]
		frames = append(frames, Frame{T: t, Img: img})
	}
	return frames, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func exitErr(err error) error {
	if e, ok := err.(*exec.ExitError); ok && len(e.Stderr) > 0 {
		return fmt.Errorf("%v: %s", err, lastLine(string(e.Stderr)))
	}
	return err
}
