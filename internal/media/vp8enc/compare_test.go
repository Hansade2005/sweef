package vp8enc

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/vp8"
)

// TestCompareLibvpx encodes the same synthetic sequences with this encoder and with libvpx (through ffmpeg,
// real-time mode, CBR) at the same bitrate and reports bitrate and PSNR-Y of both. It is a measurement, not a
// pass/fail test: set SWEEF_VP8_COMPARE=1 to run it. The sequences are synthetic (procedural texture, noise,
// a moving ellipse), not camera footage, so the numbers say how close the encoder is to libvpx on this
// content, not how it looks on real video.
func TestCompareLibvpx(t *testing.T) {
	if os.Getenv("SWEEF_VP8_COMPARE") == "" {
		t.Skip("set SWEEF_VP8_COMPARE=1 to compare with libvpx")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	type tc struct {
		w, h, kbps int
		kind       seqKind
	}
	for _, c := range []tc{{320, 180, 200, seqNatural}, {640, 360, 600, seqNatural}, {640, 360, 300, seqStatic}, {1280, 720, 1500, seqNatural}, {1280, 720, 1500, seqGradient}} {
		const n = 90
		gen := newSeq(c.w, c.h, c.kind)
		frames := make([]*Image, n)
		var raw bytes.Buffer
		for i := range frames {
			frames[i] = gen.frame(i)
			raw.Write(frames[i].Y)
			raw.Write(frames[i].U)
			raw.Write(frames[i].V)
		}
		dir := t.TempDir()
		yuv := filepath.Join(dir, "in.yuv")
		if err := os.WriteFile(yuv, raw.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		// ours
		enc, _ := New(Config{Width: c.w, Height: c.h, Bitrate: c.kbps * 1000, FrameRate: 30, Speed: 2})
		dec := vp8.NewDecoder()
		var ourBytes int
		var ourPSNR float64
		for i := 0; i < n; i++ {
			p, err := enc.Encode(frames[i])
			if err != nil || p == nil {
				t.Fatal(err)
			}
			ourBytes += len(p.Data)
			out, err := dec.Decode(p.Data)
			if err != nil {
				t.Fatal(err)
			}
			ourPSNR += psnrImage(frames[i], out)
		}
		// libvpx: real-time mode (what a WebRTC sender uses) and "good" quality mode (slow, for reference)
		type libRes struct {
			kbps, psnr float64
		}
		runLib := func(name string, extra ...string) libRes {
			ivf := filepath.Join(dir, name+".ivf")
			b := c.kbps * 1000
			args := []string{"-v", "error", "-nostdin", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", itoa(c.w) + "x" + itoa(c.h), "-r", "30", "-i", yuv,
				"-c:v", "libvpx", "-b:v", itoa(b), "-minrate", itoa(b), "-maxrate", itoa(b), "-bufsize", itoa(b / 2), "-g", "9999", "-lag-in-frames", "0"}
			args = append(args, extra...)
			args = append(args, "-y", ivf)
			if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
				t.Fatalf("libvpx encode: %v %s", err, out)
			}
			st, _ := os.Stat(ivf)
			dout, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", ivf, "-f", "rawvideo", "-pix_fmt", "yuv420p", "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			fs := c.w*c.h + 2*((c.w+1)/2)*((c.h+1)/2)
			var ps float64
			cnt := len(dout) / fs
			for i := 0; i < cnt && i < n; i++ {
				ps += psnrPlane(frames[i].Y, c.w, dout[i*fs:], c.w, c.w, c.h)
			}
			return libRes{float64(st.Size()) * 8 / (float64(n) / 30) / 1000, ps / float64(cnt)}
		}
		rt := runLib("rt", "-deadline", "realtime", "-cpu-used", "5", "-error-resilient", "1")
		good := runLib("good", "-deadline", "good", "-cpu-used", "2")
		secs := float64(n) / 30
		t.Logf("%dx%d %-8s target %4d kbps | ours: %4.0f kbps %.2f dB | libvpx realtime cpu-used 5: %4.0f kbps %.2f dB | libvpx good cpu-used 2: %4.0f kbps %.2f dB",
			c.w, c.h, c.kind, c.kbps, float64(ourBytes)*8/secs/1000, ourPSNR/n, rt.kbps, rt.psnr, good.kbps, good.psnr)
	}
}

func itoa(v int) string {
	return string(appendInt(nil, v))
}

func appendInt(b []byte, v int) []byte {
	if v == 0 {
		return append(b, '0')
	}
	var tmp [20]byte
	i := len(tmp)
	for v > 0 {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
	}
	return append(b, tmp[i:]...)
}
