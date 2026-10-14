package media

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// vpicRaw serialises a decoded picture like ffmpeg's rawvideo muxer does for
// the decoder's native pixel format: planes in order, 16-bit little endian
// samples above 8 bits, monochrome expanded to 4:2:0 with neutral chroma.
func vpicRaw(p vpic) []byte {
	if p.ycc != nil {
		return toYUV(p.ycc).b
	}
	f := p.frm
	f.Wait() // a frame of a decoder with frame threads may still be being reconstructed
	var out []byte
	put := func(pl []uint16, stride, w, h int) {
		for y := 0; y < h; y++ {
			row := pl[y*stride : y*stride+w]
			if f.Depth > 8 {
				for _, v := range row {
					out = append(out, byte(v), byte(v>>8))
				}
			} else {
				for _, v := range row {
					out = append(out, byte(v))
				}
			}
		}
	}
	put(f.Planes[0], f.Stride[0], f.Width, f.Height)
	if f.Format == yuv.F400 {
		cw, ch := (f.Width+1)/2, (f.Height+1)/2
		neutral := uint16(1) << uint(f.Depth-1)
		row := make([]uint16, cw)
		for i := range row {
			row[i] = neutral
		}
		for c := 0; c < 2; c++ {
			for y := 0; y < ch; y++ {
				put(row, cw, cw, 1)
			}
		}
		return out
	}
	put(f.Planes[1], f.Stride[1], f.CWidth, f.CHeight)
	put(f.Planes[2], f.Stride[2], f.CWidth, f.CHeight)
	return out
}

// decodeRawNative decodes a clip with the pure-Go decoders and returns the
// raw frames in presentation order.
func decodeRawNative(t testing.TB, data []byte) [][]byte {
	info, err := ProbeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	src := BytesSource(data)
	vs, err := loadVideoStream(src, info)
	if err != nil {
		t.Fatal(err)
	}
	dec := newNativeDecoder(vs)
	if dec == nil {
		t.Fatalf("no native decoder for %s", vs.codec)
	}
	pd, ok := dec.(picDecoder)
	if !ok {
		t.Fatalf("decoder has no picDecoder")
	}
	pics := make([][]byte, len(vs.samples))
	for i := range vs.samples {
		b, err := vs.payload(src, i)
		if err != nil {
			t.Fatal(err)
		}
		p, err := pd.DecodePic(b)
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		if p.valid() {
			pics[i] = vpicRaw(p)
		}
	}
	var out [][]byte
	for _, i := range vs.order() {
		if pics[i] != nil {
			out = append(out, pics[i])
		}
	}
	return out
}

// ffmpegRawNative returns ffmpeg's decoded output in the decoder's native
// pixel format (rawvideo, passthrough timestamps) and that format's name.
func ffmpegRawNative(t testing.TB, path string) ([]byte, string) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=pix_fmt", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", path, "-fps_mode", "passthrough", "-f", "rawvideo", "-")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, se.String())
	}
	return so.Bytes(), string(bytes.TrimSpace(out))
}

// compareRaw decodes the clip natively and requires every frame to match
// ffmpeg's output byte for byte. It returns the number of frames.
func compareRaw(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ref, pf := ffmpegRawNative(t, path)
	frames := decodeRawNative(t, data)
	if len(frames) == 0 {
		t.Fatal("no frames decoded")
	}
	fs := len(frames[0])
	if len(ref) != fs*len(frames) {
		t.Fatalf("%s: ffmpeg %d bytes (%d frames of %d) vs native %d frames", pf, len(ref), len(ref)/fs, fs, len(frames))
	}
	bad := 0
	for i, f := range frames {
		r := ref[i*fs : (i+1)*fs]
		if !bytes.Equal(r, f) {
			if bad < 3 {
				j := 0
				for r[j] == f[j] {
					j++
				}
				t.Errorf("%s frame %d first differs at byte %d (of %d): ffmpeg %d native %d", pf, i, j, fs, r[j], f[j])
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%s: %d/%d frames differ", pf, bad, len(frames))
	}
	return len(frames)
}

// remux copies an elementary stream into an MP4 so both decoders see the
// same access units.
func remux(t testing.TB, in, out string) {
	if b, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-y", "-i", in, "-c:v", "copy", "-an", out).CombinedOutput(); err != nil {
		t.Fatalf("remux %s: %v %s", in, err, b)
	}
}

type fmtCase struct {
	name string
	args []string // ffmpeg encoder arguments (after the lavfi input)
}

func TestH264Formats(t *testing.T) {
	skipNoFF(t)
	src := []string{"-f", "lavfi", "-i", "mandelbrot=rate=15:size=192x112", "-t", "1"}
	vf := []string{"-vf", "noise=alls=6:allf=t"}
	cases := []fmtCase{
		{"hi422_8_cabac", []string{"-c:v", "libx264", "-profile:v", "high422", "-pix_fmt", "yuv422p", "-x264-params", "keyint=30:bframes=2:ref=2"}},
		{"hi422_8_cavlc", []string{"-c:v", "libx264", "-profile:v", "high422", "-pix_fmt", "yuv422p", "-x264-params", "keyint=30:cabac=0:bframes=2:8x8dct=1"}},
		{"hi422_10", []string{"-c:v", "libx264", "-profile:v", "high422", "-pix_fmt", "yuv422p10le", "-x264-params", "keyint=30:bframes=2:ref=2"}},
		{"hi10", []string{"-c:v", "libx264", "-profile:v", "high10", "-pix_fmt", "yuv420p10le", "-x264-params", "keyint=30:bframes=3:ref=3:weightb=1:8x8dct=1"}},
		{"hi10_cavlc", []string{"-c:v", "libx264", "-profile:v", "high10", "-pix_fmt", "yuv420p10le", "-x264-params", "keyint=30:cabac=0:bframes=2"}},
		{"hi444_8", []string{"-c:v", "libx264", "-profile:v", "high444", "-pix_fmt", "yuv444p", "-x264-params", "keyint=30:bframes=2:ref=2:8x8dct=1"}},
		{"hi444_8_cavlc", []string{"-c:v", "libx264", "-profile:v", "high444", "-pix_fmt", "yuv444p", "-x264-params", "keyint=30:cabac=0:bframes=2:8x8dct=1"}},
		{"hi444_10", []string{"-c:v", "libx264", "-profile:v", "high444", "-pix_fmt", "yuv444p10le", "-x264-params", "keyint=30:bframes=2:ref=2:8x8dct=1"}},
		{"gray8", []string{"-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "gray", "-x264-params", "keyint=30:bframes=2"}},
		{"gray10", []string{"-c:v", "libx264", "-profile:v", "high10", "-pix_fmt", "gray10le", "-x264-params", "keyint=30:bframes=2"}},
		{"lossless444", []string{"-c:v", "libx264", "-profile:v", "high444", "-pix_fmt", "yuv444p", "-qp", "0", "-x264-params", "keyint=30:bframes=2"}},
		{"lossless420", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-qp", "0", "-x264-params", "keyint=30:bframes=2"}},
		{"mbaff_420", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-x264-params", "interlaced=1:tff=1:keyint=30:bframes=2:ref=3:8x8dct=1"}},
		{"mbaff_420_bff_cavlc", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-x264-params", "interlaced=1:bff=1:cabac=0:keyint=30:bframes=2"}},
		{"mbaff_hi422_10", []string{"-c:v", "libx264", "-profile:v", "high422", "-pix_fmt", "yuv422p10le", "-x264-params", "interlaced=1:tff=1:keyint=30:bframes=2"}},
		{"mbaff_hi10", []string{"-c:v", "libx264", "-profile:v", "high10", "-pix_fmt", "yuv420p10le", "-x264-params", "interlaced=1:tff=1:keyint=30:bframes=3:weightb=1"}},
		{"mbaff_hi444", []string{"-c:v", "libx264", "-profile:v", "high444", "-pix_fmt", "yuv444p", "-x264-params", "interlaced=1:tff=1:keyint=30:bframes=2:8x8dct=1"}},
		{"fake_interlaced", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-x264-params", "fake-interlaced=1:keyint=30:bframes=2"}},
		{"ildct", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-flags", "+ildct", "-x264-params", "keyint=30:bframes=2"}},
		{"slices_wp", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-x264-params", "keyint=30:bframes=0:slices=4:weightp=2:ref=4"}},
		{"intra_refresh_cavlc", []string{"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-x264-params", "intra-refresh=1:keyint=30:ref=3"}},
		{"lossless444_10", []string{"-c:v", "libx264", "-profile:v", "high444", "-pix_fmt", "yuv444p10le", "-qp", "0", "-x264-params", "keyint=30:bframes=2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append(append(append([]string{}, src...), vf...), c.args...)
			p := genClip(t, "a.mp4", args...)
			n := compareRaw(t, p)
			t.Logf("%d frames bit-exact", n)
		})
	}
}

var _ = fmt.Sprint
var _ image.Image
var _ = filepath.Join
