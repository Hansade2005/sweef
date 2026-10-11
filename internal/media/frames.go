package media

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"golang.org/x/image/vp8"
)

// Source is a random-access media resource.
type Source interface {
	io.ReaderAt
	Size() int64
}

// BytesSource wraps an in-memory buffer.
type BytesSource []byte

func (b BytesSource) ReadAt(p []byte, off int64) (int, error) { return bytesReaderAt(b).ReadAt(p, off) }
func (b BytesSource) Size() int64                             { return int64(len(b)) }

// FrameSource produces the video frame to show at a presentation time.
type FrameSource interface {
	// Mode names the tier: "native" (pure-Go decoder), "ffmpeg", "mjpeg", "anim".
	Mode() string
	// Frame returns the frame displayed at t seconds. It may block (ffmpeg).
	Frame(t float64) (image.Image, error)
	Close()
}

var ffSem = make(chan struct{}, 2)

// OpenFrames chooses the best available frame provider for the resource.
// path, when non-empty, is a local file holding the full resource.
func OpenFrames(src Source, info *Info, path string) (FrameSource, error) {
	v := info.Video()
	if v == nil {
		return nil, fmt.Errorf("media: no video track")
	}
	switch info.Container {
	case "gif":
		return newAnimGIF(src)
	}
	var native FrameSource
	if HasNativeVideo(v.CodecName) && os.Getenv("SWEEF_NO_NATIVE_VIDEO") == "" {
		if nf, err := newNativeFrames(src, info); err == nil {
			native = nf
		}
	}
	if native == nil && (info.Container == "avi" || info.Container == "mp4") && v.CodecName == "mjpeg" {
		if mf, err := newMJPEG(src, info); err == nil {
			native = mf
		}
	}
	if native != nil && native.Mode() == "native" {
		if FFmpegPath() != "" {
			return &chainFrames{first: native, second: &ffmpegFrames{src: src, info: info, path: path, cache: map[int]image.Image{}}}, nil
		}
		return native, nil
	}
	if FFmpegPath() != "" {
		ff := &ffmpegFrames{src: src, info: info, path: path, native: native, cache: map[int]image.Image{}}
		return ff, nil
	}
	if native != nil {
		return native, nil
	}
	return nil, ErrUnsupported
}

// chainFrames asks first and falls back to second when it fails.
type chainFrames struct{ first, second FrameSource }

func (c *chainFrames) Mode() string { return c.first.Mode() }
func (c *chainFrames) Frame(t float64) (image.Image, error) {
	img, err := c.first.Frame(t)
	if err == nil && img != nil {
		return img, nil
	}
	if img2, err2 := c.second.Frame(t); err2 == nil {
		return img2, nil
	}
	return img, err
}
func (c *chainFrames) Close() { c.first.Close(); c.second.Close() }

// ---- ffmpeg tier ---------------------------------------------------------------

type ffmpegFrames struct {
	src    Source
	info   *Info
	path   string
	native FrameSource
	mu     sync.Mutex
	cache  map[int]image.Image
	order  []int
	tmp    string
}

func (f *ffmpegFrames) Mode() string { return "ffmpeg" }

func (f *ffmpegFrames) file() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.path != "" {
		return f.path, nil
	}
	if f.tmp != "" {
		return f.tmp, nil
	}
	tf, err := os.CreateTemp("", "sweef-video-*")
	if err != nil {
		return "", err
	}
	defer tf.Close()
	if _, err := io.Copy(tf, io.NewSectionReader(f.src, 0, f.src.Size())); err != nil {
		os.Remove(tf.Name())
		return "", err
	}
	f.tmp = tf.Name()
	return f.tmp, nil
}

func (f *ffmpegFrames) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tmp != "" {
		os.Remove(f.tmp)
		f.tmp = ""
	}
	if f.native != nil {
		f.native.Close()
	}
}

func (f *ffmpegFrames) Frame(t float64) (image.Image, error) {
	fps := 30.0
	if v := f.info.Video(); v != nil && v.FrameRate > 0 {
		fps = v.FrameRate
	}
	if t < 0 {
		t = 0
	}
	if f.info.Duration > 0 && t >= f.info.Duration {
		t = f.info.Duration - 0.5/fps
		if t < 0 {
			t = 0
		}
	}
	key := int(t*fps + 1e-6)
	f.mu.Lock()
	if img, ok := f.cache[key]; ok {
		f.mu.Unlock()
		return img, nil
	}
	f.mu.Unlock()
	// A native key frame decode is instant and exact at the very start.
	if f.native != nil && key == 0 {
		if img, err := f.native.Frame(0); err == nil && img != nil {
			f.put(key, img)
			return img, nil
		}
	}
	path, err := f.file()
	if err != nil {
		return nil, err
	}
	ff := FFmpegPath()
	ffSem <- struct{}{}
	defer func() { <-ffSem }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ts := float64(key) / fps
	cmd := exec.CommandContext(ctx, ff, "-v", "error", "-nostdin", "-ss", strconv.FormatFloat(ts+0.0005, 'f', 4, 64), "-i", path,
		"-frames:v", "1", "-an", "-sn", "-f", "image2pipe", "-vcodec", "png", "-")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("media: ffmpeg frame at %.3fs failed: %v", t, err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	f.put(key, img)
	return img, nil
}

func (f *ffmpegFrames) put(key int, img image.Image) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache[key] = img
	f.order = append(f.order, key)
	for len(f.order) > 24 {
		delete(f.cache, f.order[0])
		f.order = f.order[1:]
	}
}

// ---- VP8 key frames ------------------------------------------------------------------

type vp8Frames struct {
	src  Source
	info *Info
	mu   sync.Mutex
	last map[int]image.Image
}

func (v *vp8Frames) Mode() string { return "keyframes" }
func (v *vp8Frames) Close()       {}

func (v *vp8Frames) Frame(t float64) (image.Image, error) {
	trk := v.info.Video()
	// Start from the seek point at or before t, but back up one more cluster
	// when needed so a keyframe is found.
	off := v.info.ByteForTime(t)
	if off < v.info.DataStart {
		off = v.info.DataStart
	}
	n := int64(6 << 20)
	if off+n > v.src.Size() {
		n = v.src.Size() - off
	}
	buf, err := readFull(v.src, off, int(n))
	if len(buf) == 0 {
		return nil, err
	}
	scale := uint64(1000000)
	blocks := ParseWebMBlocksScale(buf, scale)
	var key *WebMBlock
	for i := range blocks {
		b := &blocks[i]
		if b.Track != trk.ID || !b.Key {
			continue
		}
		if b.Time <= t+1e-6 || key == nil {
			key = b
		}
		if b.Time > t {
			break
		}
	}
	if key == nil {
		return nil, fmt.Errorf("media: no VP8 key frame found")
	}
	return decodeVP8Key(key.Data)
}

func decodeVP8Key(data []byte) (image.Image, error) {
	d := vp8.NewDecoder()
	d.Init(bytes.NewReader(data), len(data))
	fh, err := d.DecodeFrameHeader()
	if err != nil {
		return nil, err
	}
	if !fh.KeyFrame {
		return nil, fmt.Errorf("media: not a VP8 key frame")
	}
	img, err := d.DecodeFrame()
	if err != nil {
		return nil, err
	}
	return img, nil
}

// ---- MJPEG --------------------------------------------------------------------------------

type mjpegFrames struct {
	src  Source
	refs []SampleRef
}

func (m *mjpegFrames) Mode() string { return "mjpeg" }
func (m *mjpegFrames) Close()       {}

func newMJPEG(src Source, info *Info) (*mjpegFrames, error) {
	m := &mjpegFrames{src: src}
	switch info.Container {
	case "mp4":
		_, refs, err := MP4Samples(src, src.Size(), 0)
		if err != nil {
			return nil, err
		}
		m.refs = refs
	case "avi":
		refs, err := aviFrames(src, info)
		if err != nil {
			return nil, err
		}
		m.refs = refs
	}
	if len(m.refs) == 0 {
		return nil, fmt.Errorf("media: no mjpeg samples")
	}
	return m, nil
}

func aviFrames(src Source, info *Info) ([]SampleRef, error) {
	// Walk the movi list for "00dc"/"00db" chunks.
	size := src.Size()
	fps := 25.0
	if v := info.Video(); v != nil && v.FrameRate > 0 {
		fps = v.FrameRate
	}
	var refs []SampleRef
	off := info.DataStart
	n := 0
	for off+8 <= size {
		h, err := readFull(src, off, 8)
		if err != nil {
			break
		}
		id := string(h[:4])
		sz := int64(le32(h[4:]))
		if id == "LIST" {
			off += 12
			continue
		}
		if id == "idx1" {
			break
		}
		if len(id) == 4 && (id[2:] == "dc" || id[2:] == "db") && id[:2] == "00" {
			refs = append(refs, SampleRef{Offset: off + 8, Size: int(sz), Time: float64(n) / fps, Sync: true})
			n++
		}
		off += 8 + sz + sz&1
	}
	return refs, nil
}

func (m *mjpegFrames) Frame(t float64) (image.Image, error) {
	idx := 0
	for i, r := range m.refs {
		if r.Time <= t+1e-6 {
			idx = i
		} else {
			break
		}
	}
	r := m.refs[idx]
	b, err := readFull(m.src, r.Offset, r.Size)
	if err != nil {
		return nil, err
	}
	return jpeg.Decode(bytes.NewReader(b))
}

// ---- animated images ----------------------------------------------------------------------

type animFrames struct {
	frames []image.Image
	ends   []float64 // cumulative end time of each frame
	loop   bool
}

func (a *animFrames) Mode() string { return "anim" }
func (a *animFrames) Close()       {}

func (a *animFrames) Frame(t float64) (image.Image, error) {
	if len(a.frames) == 0 {
		return nil, fmt.Errorf("media: no frames")
	}
	total := a.ends[len(a.ends)-1]
	if a.loop && total > 0 {
		for t >= total {
			t -= total
		}
	}
	for i, e := range a.ends {
		if t < e {
			return a.frames[i], nil
		}
	}
	return a.frames[len(a.frames)-1], nil
}

func newAnimGIF(src Source) (FrameSource, error) {
	b, err := readFull(src, 0, int(src.Size()))
	if err != nil {
		return nil, err
	}
	g, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	w, h := g.Config.Width, g.Config.Height
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	a := &animFrames{loop: true}
	var tcum float64
	for i, fr := range g.Image {
		var prev *image.RGBA
		if i < len(g.Disposal) && g.Disposal[i] == gif.DisposalPrevious {
			prev = image.NewRGBA(canvas.Rect)
			copy(prev.Pix, canvas.Pix)
		}
		draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		snap := image.NewRGBA(canvas.Rect)
		copy(snap.Pix, canvas.Pix)
		a.frames = append(a.frames, snap)
		d := float64(g.Delay[i]) / 100
		if d <= 0.01 {
			d = 0.1
		}
		tcum += d
		a.ends = append(a.ends, tcum)
		if i < len(g.Disposal) {
			switch g.Disposal[i] {
			case gif.DisposalBackground:
				draw.Draw(canvas, fr.Bounds(), image.NewUniform(color.Transparent), image.Point{}, draw.Src)
			case gif.DisposalPrevious:
				if prev != nil {
					copy(canvas.Pix, prev.Pix)
				}
			}
		}
	}
	return a, nil
}

// OpenAnimatedImage opens a GIF or APNG as a time-driven frame source. It is
// used for animated images (not <video>); ok is false for still images.
func OpenAnimatedImage(data []byte) (FrameSource, bool) {
	if len(data) > 3 && string(data[:3]) == "GIF" {
		fs, err := newAnimGIF(BytesSource(data))
		if err != nil {
			return nil, false
		}
		af := fs.(*animFrames)
		return fs, len(af.frames) > 1
	}
	if fs := newAPNG(data); fs != nil {
		return fs, true
	}
	return nil, false
}
