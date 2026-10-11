package vp9

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// rawFrame serialises a frame like ffmpeg's rawvideo output of the same
// pixel format (planar, 16-bit little endian above 8 bits).
func rawFrame(f *yuv.Frame) []byte {
	var out []byte
	put := func(p []uint16, stride, w, h int) {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := p[y*stride+x]
				if f.Depth > 8 {
					out = append(out, byte(v), byte(v>>8))
				} else {
					out = append(out, byte(v))
				}
			}
		}
	}
	put(f.Planes[0], f.Stride[0], f.Width, f.Height)
	if f.Format != yuv.F400 {
		put(f.Planes[1], f.Stride[1], f.CWidth, f.CHeight)
		put(f.Planes[2], f.Stride[2], f.CWidth, f.CHeight)
	}
	return out
}

func decodeRaw(t testing.TB, frames [][]byte) [][]byte {
	d := NewDecoder()
	var out [][]byte
	for i, c := range frames {
		fr, err := d.DecodeFrame(c)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if fr != nil {
			out = append(out, rawFrame(fr))
		}
	}
	return out
}

// compareNative decodes an IVF stream and compares every frame with
// ffmpeg's output in the stream's native pixel format.
func compareNative(t *testing.T, name string, ivf []byte) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name+".ivf")
	os.WriteFile(p, ivf, 0o644)
	ref := ffmpeg(t, "-i", p, "-fps_mode", "passthrough", "-f", "rawvideo", "-")
	got := decodeRaw(t, readIVF(t, ivf))
	if len(got) == 0 {
		t.Fatal("no frames")
	}
	fs := len(got[0])
	if len(ref) != fs*len(got) {
		t.Fatalf("%s: ffmpeg %d bytes (%.2f frames of %d), ours %d frames", name, len(ref), float64(len(ref))/float64(fs), fs, len(got))
	}
	bad := 0
	for i, g := range got {
		r := ref[i*fs : (i+1)*fs]
		if !bytes.Equal(r, g) {
			if bad < 3 {
				j := 0
				for r[j] == g[j] {
					j++
				}
				t.Errorf("%s: frame %d first differs at byte %d (of %d): ffmpeg %d ours %d", name, i, j, fs, r[j], g[j])
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%s: %d/%d frames differ", name, bad, len(got))
	}
}

func genVP9(t *testing.T, src, size, pixfmt string, extra ...string) []byte {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.ivf")
	args := []string{"-f", "lavfi", "-i", src, "-s", size, "-pix_fmt", pixfmt, "-c:v", "libvpx-vp9", "-b:v", "0", "-crf", "30", "-g", "10"}
	args = append(args, extra...)
	args = append(args, "-y", out)
	ffmpeg(t, args...)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVP9Profiles(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip("no ffmpeg")
	}
	for _, c := range []struct{ name, pf, prof string }{
		{"p1_444", "yuv444p", "1"},
		{"p1_422", "yuv422p", "1"},
		{"p1_440", "yuv440p", "1"},
		{"p2_420_10", "yuv420p10le", "2"},
		{"p2_420_12", "yuv420p12le", "2"},
		{"p3_444_10", "yuv444p10le", "3"},
		{"p3_444_12", "yuv444p12le", "3"},
		{"p3_422_10", "yuv422p10le", "3"},
		{"p3_440_12", "yuv440p12le", "3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ivf := genVP9(t, "testsrc2=rate=15:duration=2", "208x120", c.pf, "-profile:v", c.prof, "-cpu-used", "4")
			compareNative(t, c.name, ivf)
			// odd dimensions, several tiles, other content
			ivf = genVP9(t, "mandelbrot=rate=15", "213x117", c.pf, "-profile:v", c.prof, "-cpu-used", "2", "-frames:v", "20", "-tile-columns", "1")
			compareNative(t, c.name+"_odd", ivf)
		})
	}
}

func TestVP9Lossless10(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	ivf := genVP9(t, "testsrc2=rate=15:duration=1", "208x120", "yuv420p10le", "-profile:v", "2", "-lossless", "1", "-crf", "0")
	compareNative(t, "lossless10", ivf)
}
