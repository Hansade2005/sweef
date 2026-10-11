package vp9

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func readIVF(t testing.TB, b []byte) [][]byte {
	if len(b) < 32 || string(b[:4]) != "DKIF" {
		t.Fatal("not ivf")
	}
	hl := int(binary.LittleEndian.Uint16(b[6:]))
	var out [][]byte
	p := hl
	for p+12 <= len(b) {
		n := int(binary.LittleEndian.Uint32(b[p:]))
		p += 12
		if p+n > len(b) {
			break
		}
		out = append(out, b[p:p+n])
		p += n
	}
	return out
}

func ffmpeg(t testing.TB, args ...string) []byte {
	cmd := exec.Command("ffmpeg", append([]string{"-v", "error", "-nostdin"}, args...)...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg %v: %v %s", args, err, se.String())
	}
	return so.Bytes()
}

// decodeAll returns the concatenated I420 of every shown frame.
func decodeAll(t testing.TB, frames [][]byte) ([]byte, int) {
	d := NewDecoder()
	var out bytes.Buffer
	n := 0
	for i, f := range frames {
		img, err := d.Decode(f)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if img == nil {
			continue
		}
		n++
		b := img.Bounds()
		for y := 0; y < b.Dy(); y++ {
			out.Write(img.Y[y*img.YStride : y*img.YStride+b.Dx()])
		}
		for y := 0; y < (b.Dy()+1)/2; y++ {
			out.Write(img.Cb[y*img.CStride : y*img.CStride+(b.Dx()+1)/2])
		}
		for y := 0; y < (b.Dy()+1)/2; y++ {
			out.Write(img.Cr[y*img.CStride : y*img.CStride+(b.Dx()+1)/2])
		}
	}
	return out.Bytes(), n
}

func compareFF(t *testing.T, name string, ivf []byte, w, h int) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name+".ivf")
	os.WriteFile(p, ivf, 0o644)
	ref := ffmpeg(t, "-i", p, "-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-f", "rawvideo", "-")
	got, n := decodeAll(t, readIVF(t, ivf))
	fs := w * h * 3 / 2
	if w%2 == 1 || h%2 == 1 {
		fs = w*h + 2*((w+1)/2)*((h+1)/2)
	}
	if len(ref) != len(got) {
		t.Fatalf("%s: ffmpeg %d bytes (%d frames), ours %d bytes (%d frames)", name, len(ref), len(ref)/fs, len(got), n)
	}
	bad := 0
	for i := 0; i < len(ref)/fs; i++ {
		if !bytes.Equal(ref[i*fs:(i+1)*fs], got[i*fs:(i+1)*fs]) {
			if bad < 3 {
				a, b := ref[i*fs:(i+1)*fs], got[i*fs:(i+1)*fs]
				j := 0
				for a[j] == b[j] {
					j++
				}
				t.Errorf("%s: frame %d differs first at byte %d (x=%d y=%d) ref=%d got=%d", name, i, j, j%w, j/w, a[j], b[j])
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%s: %d/%d frames differ", name, bad, len(ref)/fs)
	}
}

func genVP8(t *testing.T, src string, size string, extra ...string) []byte {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.ivf")
	args := []string{"-f", "lavfi", "-i", src, "-s", size, "-pix_fmt", "yuv420p", "-c:v", "libvpx-vp9"}
	args = append(args, extra...)
	args = append(args, "-y", out)
	ffmpeg(t, args...)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVP9FFmpegMatrix(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip("no ffmpeg")
	}
	cases := []struct {
		name string
		src  string
		size string
		w, h int
		args []string
	}{
		{"intra", "testsrc2=rate=15:duration=1", "192x108", 192, 108, []string{"-b:v", "300k", "-g", "1", "-cpu-used", "4"}},
		{"base", "testsrc2=rate=15:duration=2", "192x108", 192, 108, []string{"-b:v", "300k", "-g", "30", "-cpu-used", "4", "-auto-alt-ref", "0"}},
		{"slow_altref", "mandelbrot=rate=15:size=256x144", "256x144", 256, 144, []string{"-t", "3", "-b:v", "500k", "-g", "60", "-cpu-used", "0", "-auto-alt-ref", "1", "-lag-in-frames", "16"}},
		{"odd", "mandelbrot=rate=15:size=211x97", "211x97", 211, 97, []string{"-t", "2", "-b:v", "300k", "-cpu-used", "1"}},
		{"tiles", "mandelbrot=rate=15:size=1024x192", "1024x192", 1024, 192, []string{"-t", "1", "-b:v", "800k", "-tile-columns", "2", "-tile-rows", "1", "-cpu-used", "4"}},
		{"lossless", "mandelbrot=rate=15:size=160x96", "160x96", 160, 96, []string{"-t", "1", "-lossless", "1", "-cpu-used", "4"}},
		{"errres", "mandelbrot=rate=15:size=192x108", "192x108", 192, 108, []string{"-t", "2", "-b:v", "300k", "-error-resilient", "1", "-cpu-used", "2"}},
		{"frame_parallel", "mandelbrot=rate=15:size=192x108", "192x108", 192, 108, []string{"-t", "2", "-b:v", "300k", "-frame-parallel", "1", "-cpu-used", "2"}},
		{"aq", "mandelbrot=rate=15:size=192x108", "192x108", 192, 108, []string{"-t", "2", "-b:v", "250k", "-aq-mode", "3", "-cpu-used", "2"}},
		{"high_q", "mandelbrot=rate=15:size=192x108", "192x108", 192, 108, []string{"-t", "2", "-crf", "10", "-b:v", "0", "-cpu-used", "2"}},
		{"low_q", "mandelbrot=rate=15:size=192x108", "192x108", 192, 108, []string{"-t", "2", "-crf", "55", "-b:v", "0", "-cpu-used", "2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ivf := genVP8(t, c.src, c.size, c.args...)
			compareFF(t, c.name, ivf, c.w, c.h)
		})
	}
}

// genTwoPass encodes with libvpx-vp9 in two passes (needed for alt-ref frames).
func genTwoPass(t testing.TB, src, size string, extra ...string) []byte {
	dir := t.TempDir()
	log := filepath.Join(dir, "p")
	base := []string{"-f", "lavfi", "-i", src, "-s", size, "-pix_fmt", "yuv420p", "-c:v", "libvpx-vp9"}
	base = append(base, extra...)
	base = append(base, "-passlogfile", log)
	ffmpeg(t, append(append([]string{}, base...), "-pass", "1", "-f", "null", "/dev/null")...)
	out := filepath.Join(dir, "o.ivf")
	ffmpeg(t, append(append([]string{}, base...), "-pass", "2", "-y", out)...)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVP9TwoPass(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip("no ffmpeg")
	}
	ivf := genTwoPass(t, "mandelbrot=rate=15:size=256x144", "256x144", "-t", "4", "-b:v", "400k", "-g", "100", "-cpu-used", "0", "-auto-alt-ref", "1", "-lag-in-frames", "16")
	compareFF(t, "twopass", ivf, 256, 144)
	ivf = genTwoPass(t, "testsrc2=rate=15:size=320x180", "320x180", "-t", "4", "-b:v", "300k", "-g", "40", "-cpu-used", "1", "-auto-alt-ref", "1", "-lag-in-frames", "12", "-aq-mode", "2", "-tile-columns", "1")
	compareFF(t, "twopass2", ivf, 320, 180)
}
