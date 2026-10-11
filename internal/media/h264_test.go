package media

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/h264"
)

// decodeAllNative decodes every video sample of an MP4/WebM natively and
// returns the I420 bytes of all frames in presentation order.
func decodeAllNative(t testing.TB, data []byte) ([]byte, int, int, int) {
	frames, w, h := decodeFramesNative(t, data)
	return bytes.Join(frames, nil), len(frames), w, h
}

func decodeFramesNative(t testing.TB, data []byte) ([][]byte, int, int) {
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
	pics := make([]*frameYUV, len(vs.samples))
	var w, h int
	for i := range vs.samples {
		b, err := vs.payload(src, i)
		if err != nil {
			t.Fatal(err)
		}
		img, err := dec.Decode(b)
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		if img != nil {
			pics[i] = toYUV(img)
			w, h = img.Bounds().Dx(), img.Bounds().Dy()
		}
	}
	var out [][]byte
	for _, i := range vs.order() {
		if pics[i] != nil {
			out = append(out, pics[i].b)
		}
	}
	return out, w, h
}

type frameYUV struct{ b []byte }

func ffmpegRaw(t testing.TB, path string) []byte {
	cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", path, "-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-f", "rawvideo", "-")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, se.String())
	}
	return so.Bytes()
}

func genClip(t testing.TB, name string, args ...string) string {
	dir := t.TempDir()
	out := filepath.Join(dir, name)
	a := append([]string{"-v", "error", "-nostdin", "-y"}, args...)
	a = append(a, out)
	if b, err := exec.Command("ffmpeg", a...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v %s", a, err, b)
	}
	return out
}

func compareNative(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ref := ffmpegRaw(t, path)
	got, n, w, h := decodeAllNative(t, data)
	fs := w*h + 2*((w+1)/2)*((h+1)/2)
	if len(ref) != len(got) {
		t.Fatalf("frame data: ffmpeg %d bytes (%d frames) vs native %d bytes (%d frames)", len(ref), len(ref)/fs, len(got), n)
	}
	bad := 0
	for i := 0; i < len(ref)/fs; i++ {
		a, b := ref[i*fs:(i+1)*fs], got[i*fs:(i+1)*fs]
		if !bytes.Equal(a, b) {
			if bad < 3 {
				j := 0
				for a[j] == b[j] {
					j++
				}
				plane, x, y := "Y", j%w, j/w
				if j >= w*h {
					plane, x, y = "C", (j-w*h)%((w+1)/2), (j-w*h)/((w+1)/2)
				}
				t.Errorf("frame %d differs first at %s (%d,%d): ffmpeg %d native %d", i, plane, x, y, a[j], b[j])
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%d/%d frames differ", bad, len(ref)/fs)
	}
}

func skipNoFF(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip("no ffmpeg")
	}
}

var _ = fmt.Sprint
var _ = h264.ErrUnsupported

func toYUV(img *image.YCbCr) *frameYUV {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 0, w*h*3/2+w)
	for y := 0; y < h; y++ {
		o := img.YOffset(b.Min.X, b.Min.Y+y)
		out = append(out, img.Y[o:o+w]...)
	}
	cw, ch := (w+1)/2, (h+1)/2
	for _, pl := range [][]byte{img.Cb, img.Cr} {
		for y := 0; y < ch; y++ {
			o := img.COffset(b.Min.X, b.Min.Y+2*y)
			out = append(out, pl[o:o+cw]...)
		}
	}
	return &frameYUV{out}
}

func TestH264IntraBaseline(t *testing.T) {
	h264.DebugCorrupt = func(s string) { t.Log("corrupt at", s) }
	skipNoFF(t)
	p := genClip(t, "a.mp4", "-f", "lavfi", "-i", "testsrc2=rate=15:size=192x108", "-t", "1", "-c:v", "libx264", "-profile:v", "baseline", "-x264-params", "keyint=1:cabac=0", "-pix_fmt", "yuv420p")
	compareNative(t, p)
}

func TestH264IntraNoDeblock(t *testing.T) {
	skipNoFF(t)
	p := genClip(t, "a.mp4", "-f", "lavfi", "-i", "testsrc2=rate=15:size=192x108", "-t", "1", "-c:v", "libx264", "-profile:v", "baseline", "-x264-params", "keyint=1:cabac=0:no-deblock=1", "-pix_fmt", "yuv420p")
	compareNative(t, p)
}

type h264Case struct {
	name   string
	src    string
	size   string
	params string
	prof   string
	dur    string
}

func TestH264Matrix(t *testing.T) {
	skipNoFF(t)
	h264.DebugCorrupt = func(s string) { t.Log("corrupt at", s) }
	cases := []h264Case{
		{"base_p", "testsrc2=rate=15", "192x108", "keyint=30:cabac=0:bframes=0", "baseline", "2"},
		{"base_ref4", "mandelbrot=rate=15", "192x112", "keyint=30:cabac=0:bframes=0:ref=4:subme=9:partitions=all:me=umh", "baseline", "2"},
		{"main_cabac_i", "testsrc2=rate=15", "192x108", "keyint=1:cabac=1:bframes=0", "main", "1"},
		{"main_cabac_p", "testsrc2=rate=15", "192x108", "keyint=30:cabac=1:bframes=0", "main", "2"},
		{"main_cabac_p_ref", "mandelbrot=rate=15", "192x112", "keyint=30:cabac=1:bframes=0:ref=4:subme=9:partitions=all:me=umh", "main", "2"},
		{"main_b_cavlc", "testsrc2=rate=15", "192x108", "keyint=30:cabac=0:bframes=3:weightb=0:direct=spatial", "main", "2"},
		{"main_b_cabac_spatial", "mandelbrot=rate=15", "192x112", "keyint=30:cabac=1:bframes=3:direct=spatial:b-adapt=0", "main", "2"},
		{"main_b_cabac_temporal", "mandelbrot=rate=15", "192x112", "keyint=30:cabac=1:bframes=3:direct=temporal:b-adapt=0", "main", "2"},
		{"main_bpyramid_weightb", "mandelbrot=rate=15", "192x112", "keyint=30:cabac=1:bframes=4:b-pyramid=normal:weightb=1:ref=4", "main", "2"},
		{"high_8x8", "mandelbrot=rate=15", "192x112", "keyint=30:cabac=1:bframes=2:8x8dct=1:ref=3", "high", "2"},
		{"high_8x8_cavlc", "mandelbrot=rate=15", "192x112", "keyint=30:cabac=0:bframes=2:8x8dct=1:ref=3", "high", "2"},
		{"high_weightp", "testsrc2=rate=15", "192x108", "keyint=30:cabac=1:bframes=0:weightp=2", "high", "2"},
		{"odd_crop", "testsrc2=rate=15", "202x98", "keyint=30:cabac=1:bframes=2", "high", "1"},
		{"multislice", "mandelbrot=rate=15", "192x112", "keyint=30:cabac=1:bframes=2:slices=4", "main", "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := genClip(t, "a.mp4", "-f", "lavfi", "-i", c.src, "-s", c.size, "-t", c.dur, "-c:v", "libx264", "-profile:v", c.prof, "-x264-params", c.params, "-pix_fmt", "yuv420p")
			compareNative(t, p)
		})
	}
}

func TestH264Stress(t *testing.T) {
	skipNoFF(t)
	h264.DebugCorrupt = func(s string) { t.Log("corrupt at", s) }
	cases := []struct{ name, vf, params, prof, extra string }{
		{"noise_hi_cabac", "noise=alls=40:allf=t", "keyint=30:cabac=1:bframes=3:ref=5:8x8dct=1:subme=10:trellis=2", "high", "-crf 8"},
		{"noise_hi_cavlc", "noise=alls=40:allf=t", "keyint=30:cabac=0:bframes=0:ref=3", "baseline", "-crf 6"},
		{"noise_lo", "noise=alls=30:allf=t", "keyint=30:cabac=1:bframes=2", "main", "-crf 40"},
		{"cqm_jvt", "noise=alls=10:allf=t", "keyint=30:cabac=1:bframes=2:cqm=jvt:8x8dct=1", "high", "-crf 20"},
		{"constrained_intra", "noise=alls=10:allf=t", "keyint=30:cabac=0:bframes=0:constrained-intra=1", "baseline", "-crf 22"},
		{"weightp1_openGOP", "noise=alls=5:allf=t", "keyint=20:cabac=1:bframes=3:weightp=1:open-gop=1", "high", "-crf 24"},
		{"deblock_offsets", "noise=alls=5:allf=t", "keyint=30:cabac=1:bframes=2:deblock=3,-2", "high", "-crf 26"},
		{"mbtree_aq", "noise=alls=5:allf=t", "keyint=60:cabac=1:bframes=5:b-adapt=2:aq-mode=2:rc-lookahead=20", "high", "-crf 24"},
		{"intra_only_hi", "noise=alls=8:allf=t", "keyint=1:cabac=1:8x8dct=1", "high", "-crf 18"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := []string{"-f", "lavfi", "-i", "mandelbrot=rate=15:size=256x144", "-vf", c.vf, "-t", "3", "-c:v", "libx264", "-profile:v", c.prof, "-x264-params", c.params, "-pix_fmt", "yuv420p"}
			args = append(args, splitFields(c.extra)...)
			p := genClip(t, "a.mp4", args...)
			compareNative(t, p)
		})
	}
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func benchH264(b *testing.B, params, prof string) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		b.Skip("no ffmpeg")
	}
	p := genClip(b, "a.mp4", "-f", "lavfi", "-i", "mandelbrot=rate=30:size=640x360", "-vf", "noise=alls=6:allf=t", "-t", "4", "-c:v", "libx264", "-profile:v", prof, "-x264-params", params, "-pix_fmt", "yuv420p", "-b:v", "1500k")
	data, _ := os.ReadFile(p)
	info, _ := ProbeBytes(data)
	src := BytesSource(data)
	vs, err := loadVideoStream(src, info)
	if err != nil {
		b.Fatal(err)
	}
	payloads := make([][]byte, len(vs.samples))
	for i := range payloads {
		payloads[i], _ = vs.payload(src, i)
	}
	b.ResetTimer()
	n := 0
	for i := 0; i < b.N; i++ {
		dec := newNativeDecoder(vs)
		for _, pl := range payloads {
			if _, err := dec.Decode(pl); err != nil {
				b.Fatal(err)
			}
			n++
		}
	}
	b.ReportMetric(float64(n)/b.Elapsed().Seconds(), "fps")
}

func BenchmarkH264HighCABAC640(b *testing.B) {
	benchH264(b, "keyint=60:cabac=1:bframes=3:8x8dct=1:ref=3", "high")
}
func BenchmarkH264BaselineCAVLC640(b *testing.B) {
	benchH264(b, "keyint=60:cabac=0:bframes=0:ref=1", "baseline")
}

func psnrRGB(a []byte, img *image.RGBA) float64 {
	var se float64
	n := 0
	for i := 0; i < len(img.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			d := float64(a[n]) - float64(img.Pix[i+c])
			se += d * d
			n++
		}
	}
	if se == 0 {
		return 99
	}
	mse := se / float64(n)
	return 10 * math.Log10(255*255/mse)
}

// TestNativeFramesVsFFmpeg drives the public FrameSource API without ffmpeg
// and compares RGB output with ffmpeg's conversion.
func TestNativeFramesVsFFmpeg(t *testing.T) {
	skipNoFF(t)
	cases := []struct {
		name string
		args []string
	}{
		{"h264_bframes.mp4", []string{"-f", "lavfi", "-i", "mandelbrot=rate=15:size=192x112", "-t", "2", "-c:v", "libx264", "-profile:v", "high", "-x264-params", "keyint=30:bframes=3:ref=3", "-pix_fmt", "yuv420p"}},
		{"vp9.webm", []string{"-f", "lavfi", "-i", "mandelbrot=rate=15:size=192x112", "-t", "2", "-c:v", "libvpx-vp9", "-b:v", "400k", "-g", "20", "-cpu-used", "4", "-pix_fmt", "yuv420p"}},
		{"vp9.mp4", []string{"-f", "lavfi", "-i", "mandelbrot=rate=15:size=192x112", "-t", "2", "-c:v", "libvpx-vp9", "-b:v", "400k", "-g", "20", "-cpu-used", "4", "-pix_fmt", "yuv420p"}},
		{"vp8.webm", []string{"-f", "lavfi", "-i", "mandelbrot=rate=15:size=192x112", "-t", "2", "-c:v", "libvpx", "-b:v", "400k", "-g", "20", "-pix_fmt", "yuv420p"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := genClip(t, c.name, c.args...)
			data, _ := os.ReadFile(path)
			info, err := ProbeBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", path, "-fps_mode", "passthrough", "-vf", "scale=flags=neighbor+accurate_rnd+full_chroma_int,format=rgb24", "-f", "rawvideo", "-")
			ref, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			v := info.Video()
			fs := v.Width * v.Height * 3
			SetFFmpegEnabled(false)
			defer SetFFmpegEnabled(true)
			fsrc, err := OpenFrames(BytesSource(data), info, "")
			if err != nil {
				t.Fatal(err)
			}
			if fsrc.Mode() != "native" {
				t.Fatalf("mode %s", fsrc.Mode())
			}
			nframes := len(ref) / fs
			worst := 99.0
			// out of order access exercises seeking
			for _, k := range []int{0, 1, 2, 3, 10, 5, 29, 28, 12, 13, 14} {
				if k >= nframes {
					continue
				}
				img, err := fsrc.Frame((float64(k) + 0.1) / 15)
				if err != nil {
					t.Fatal(err)
				}
				p := psnrRGB(ref[k*fs:(k+1)*fs], img.(*image.RGBA))
				t.Logf("k=%d psnr %.1f", k, p)
				if p < worst {
					worst = p
				}
			}
			t.Logf("worst PSNR vs ffmpeg RGB: %.1f dB", worst)
			if worst < 45 {
				t.Errorf("PSNR %.1f < 45", worst)
			}
		})
	}
}

// A stream outside the native decoder's scope (4:2:2) must not be played
// natively: without ffmpeg OpenFrames reports it unsupported.
func TestH264UnsupportedProfileNeedsFFmpeg(t *testing.T) {
	data, err := os.ReadFile("testdata/h264_422.mp4")
	if err != nil {
		t.Fatal(err)
	}
	info, err := ProbeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	if fs, err := OpenFrames(BytesSource(data), info, ""); err == nil {
		fs.Close()
		t.Fatal("4:2:2 H.264 opened without ffmpeg")
	}
}
