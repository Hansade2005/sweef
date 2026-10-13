package vp8enc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/media/vp8"
)

// ---- synthetic sequences -------------------------------------------------------------------

type seqKind int

const (
	seqGradient seqKind = iota // smooth moving gradient + moving box (easy)
	seqNatural                 // multi-octave texture panning with sub-pixel motion, a moving object and sensor noise
	seqStatic                  // a textured still scene with sensor noise (a video call with a person not moving)
	seqSceneCut                // two unrelated textures alternating every 20 frames
)

func (k seqKind) String() string {
	return [...]string{"gradient", "natural", "static", "scenecut"}[k]
}

// texture is a deterministic multi-octave value noise field.
type texture struct{ g [64 * 64]float32 }

func newTexture(seed int64) *texture {
	r := rand.New(rand.NewSource(seed))
	t := &texture{}
	for i := range t.g {
		t.g[i] = r.Float32()
	}
	return t
}

func (t *texture) at(x, y float64) float64 {
	v, amp, f := 0.0, 0.5, 1.0/48
	for o := 0; o < 5; o++ {
		fx, fy := x*f, y*f
		ix, iy := int(math.Floor(fx)), int(math.Floor(fy))
		ax, ay := fx-float64(ix), fy-float64(iy)
		ax, ay = ax*ax*(3-2*ax), ay*ay*(3-2*ay)
		g := func(a, b int) float64 { return float64(t.g[((b&63)<<6)|(a&63)]) }
		top := g(ix, iy)*(1-ax) + g(ix+1, iy)*ax
		bot := g(ix, iy+1)*(1-ax) + g(ix+1, iy+1)*ax
		v += amp * (top*(1-ay) + bot*ay)
		amp *= 0.55
		f *= 2
	}
	return v
}

type seqGen struct {
	w, h int
	kind seqKind
	tex  [2]*texture
	rnd  *rand.Rand
}

func newSeq(w, h int, kind seqKind) *seqGen {
	return &seqGen{w: w, h: h, kind: kind, tex: [2]*texture{newTexture(1), newTexture(2)}, rnd: rand.New(rand.NewSource(99))}
}

func (s *seqGen) frame(n int) *Image {
	w, h := s.w, s.h
	img := &Image{W: w, H: h, Y: make([]byte, w*h), U: make([]byte, ((w+1)/2)*((h+1)/2)), V: make([]byte, ((w+1)/2)*((h+1)/2)), YStride: w, UVStride: (w + 1) / 2}
	cw, ch := (w+1)/2, (h+1)/2
	switch s.kind {
	case seqGradient:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := 40 + 120*float64(x+3*n)/float64(w) + 60*float64(y)/float64(h)
				img.Y[y*w+x] = byte(math.Min(255, v))
			}
		}
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				img.U[y*cw+x] = byte(100 + 40*x/cw)
				img.V[y*cw+x] = byte(160 - 40*y/ch)
			}
		}
		bx, by := (n*5)%(w-60), (n*3)%(h-40)
		for y := by; y < by+40; y++ {
			for x := bx; x < bx+60; x++ {
				img.Y[y*w+x] = 235
				if y/2 < ch && x/2 < cw {
					img.U[(y/2)*cw+x/2] = 90
					img.V[(y/2)*cw+x/2] = 200
				}
			}
		}
	case seqNatural, seqStatic, seqSceneCut:
		tex := s.tex[0]
		ox, oy := 0.7*float64(n), 0.35*float64(n)
		if s.kind == seqStatic {
			ox, oy = 0, 0
		}
		if s.kind == seqSceneCut && (n/20)%2 == 1 {
			tex = s.tex[1]
			ox, oy = 100+0.5*float64(n), 20
		}
		sigma := 1.6
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := 30 + 190*tex.at(float64(x)+ox, float64(y)+oy)
				v += s.rnd.NormFloat64() * sigma
				img.Y[y*w+x] = clamp8(v)
			}
		}
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				u := 118 + 50*tex.at(float64(2*x)+ox+300, float64(2*y)+oy)
				v := 138 - 40*tex.at(float64(2*x)+ox+700, float64(2*y)+oy)
				img.U[y*cw+x] = clamp8(u + s.rnd.NormFloat64())
				img.V[y*cw+x] = clamp8(v + s.rnd.NormFloat64())
			}
		}
		if s.kind != seqSceneCut {
			// a moving "face": an ellipse with its own texture
			cx, cy := float64(w)*0.5+float64(w)*0.2*math.Sin(float64(n)/17), float64(h)*0.5+float64(h)*0.1*math.Cos(float64(n)/23)
			rx, ry := float64(w)/7, float64(h)/5
			for y := int(cy - ry); y <= int(cy+ry); y++ {
				for x := int(cx - rx); x <= int(cx+rx); x++ {
					if x < 0 || y < 0 || x >= w || y >= h {
						continue
					}
					dx, dy := (float64(x)-cx)/rx, (float64(y)-cy)/ry
					if dx*dx+dy*dy <= 1 {
						img.Y[y*w+x] = clamp8(150 + 60*tex.at(float64(x)-cx+500, float64(y)-cy+500) + s.rnd.NormFloat64()*sigma)
						if x%2 == 0 && y%2 == 0 {
							img.U[(y/2)*cw+x/2] = 110
							img.V[(y/2)*cw+x/2] = 165
						}
					}
				}
			}
		}
	}
	return img
}

func clamp8(v float64) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v + 0.5)
}

// ---- metrics ------------------------------------------------------------------------------------

func psnrPlane(a []byte, as int, b []byte, bs int, w, h int) float64 {
	var se float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := float64(a[y*as+x]) - float64(b[y*bs+x])
			se += d * d
		}
	}
	if se == 0 {
		return 99
	}
	return 10 * math.Log10(255*255*float64(w*h)/se)
}

func psnrImage(src *Image, dec *image.YCbCr) float64 {
	return psnrPlane(src.Y, src.YStride, dec.Y, dec.YStride, src.W, src.H)
}

// ---- tests ---------------------------------------------------------------------------------------

// encodeSeq encodes n frames and returns the packets and the per-frame luma PSNR against the source after
// decoding with the repository's own decoder. It also checks that the decoder's output is bit-identical
// to the encoder's reconstruction.
func encodeSeq(t testing.TB, cfg Config, kind seqKind, n int, checkRecon bool) (pkts [][]byte, psnr []float64, enc *Encoder) {
	enc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gen := newSeq(cfg.Width, cfg.Height, kind)
	dec := vp8.NewDecoder()
	for i := 0; i < n; i++ {
		src := gen.frame(i)
		p, err := enc.Encode(src)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			continue
		}
		pkts = append(pkts, p.Data)
		out, err := dec.Decode(p.Data)
		if err != nil {
			t.Fatalf("frame %d: decode: %v", i, err)
		}
		if out == nil {
			t.Fatalf("frame %d: not shown", i)
		}
		if out.Bounds().Dx() != cfg.Width || out.Bounds().Dy() != cfg.Height {
			t.Fatalf("decoded size %v", out.Bounds())
		}
		psnr = append(psnr, psnrImage(src, out))
		if checkRecon {
			rec := enc.last
			for y := 0; y < cfg.Height; y++ {
				a := rec.y.pix[rec.y.idx(0, y) : rec.y.idx(0, y)+cfg.Width]
				b := out.Y[y*out.YStride : y*out.YStride+cfg.Width]
				if !bytes.Equal(a, b) {
					for x := range a {
						if a[x] != b[x] {
							t.Fatalf("frame %d (key=%v): luma mismatch at (%d,%d): encoder %d decoder %d", i, p.Key, x, y, a[x], b[x])
						}
					}
				}
			}
			for y := 0; y < (cfg.Height+1)/2; y++ {
				w := (cfg.Width + 1) / 2
				if !bytes.Equal(rec.u.pix[rec.u.idx(0, y):rec.u.idx(0, y)+w], out.Cb[y*out.CStride:y*out.CStride+w]) {
					t.Fatalf("frame %d: U mismatch row %d", i, y)
				}
				if !bytes.Equal(rec.v.pix[rec.v.idx(0, y):rec.v.idx(0, y)+w], out.Cr[y*out.CStride:y*out.CStride+w]) {
					t.Fatalf("frame %d: V mismatch row %d", i, y)
				}
			}
		}
	}
	return
}

func TestReconstructionMatchesDecoder(t *testing.T) {
	for _, kind := range []seqKind{seqGradient, seqNatural, seqStatic, seqSceneCut} {
		for _, size := range [][2]int{{320, 180}, {176, 144}, {203, 117}} {
			cfg := Config{Width: size[0], Height: size[1], Bitrate: 300000, FrameRate: 30, KeyInterval: 40, Speed: 2}
			t.Run(fmt.Sprintf("%s_%dx%d", kind, size[0], size[1]), func(t *testing.T) {
				_, psnr, _ := encodeSeq(t, cfg, kind, 45, true)
				if len(psnr) == 0 {
					t.Fatal("no frames")
				}
				avg := 0.0
				for _, p := range psnr {
					avg += p
				}
				avg /= float64(len(psnr))
				t.Logf("avg PSNR-Y %.2f dB over %d frames", avg, len(psnr))
				if avg < 28 {
					t.Errorf("PSNR too low: %.2f", avg)
				}
			})
		}
	}
}

func TestBitrateAndFPS(t *testing.T) {
	type tc struct {
		w, h, kbps int
		kind       seqKind
	}
	cases := []tc{{320, 180, 250, seqNatural}, {640, 360, 800, seqNatural}, {1280, 720, 1500, seqNatural}, {640, 360, 300, seqStatic}}
	if testing.Short() {
		cases = cases[:2]
	}
	for _, c := range cases {
		cfg := Config{Width: c.w, Height: c.h, Bitrate: c.kbps * 1000, FrameRate: 30, Speed: -1}
		enc, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		gen := newSeq(c.w, c.h, c.kind)
		dec := vp8.NewDecoder()
		const n = 90
		frames := make([]*Image, n)
		for i := range frames {
			frames[i] = gen.frame(i)
		}
		var bytesOut int
		var psnr float64
		var encTime time.Duration
		cnt := 0
		for i := 0; i < n; i++ {
			p, err := enc.Encode(frames[i])
			if err != nil {
				t.Fatal(err)
			}
			if p == nil {
				continue
			}
			encTime += p.EncTime
			bytesOut += len(p.Data)
			out, err := dec.Decode(p.Data)
			if err != nil {
				t.Fatal(err)
			}
			psnr += psnrImage(frames[i], out)
			cnt++
		}
		secs := float64(n) / 30
		st := enc.Stats()
		t.Logf("%dx%d %s target %d kbps: actual %.0f kbps, PSNR-Y %.2f dB, %d frames (%d dropped, %d key), encode %.1f ms/frame (%.0f fps), final speed %d, last q %d",
			c.w, c.h, c.kind, c.kbps, float64(bytesOut)*8/secs/1000, psnr/float64(cnt), cnt, st.Dropped, st.KeyFrames,
			float64(encTime.Milliseconds())/float64(cnt), float64(cnt)/encTime.Seconds(), st.Speed, st.LastQ)
	}
}

// The tools the encoder claims must actually be used: intra and inter blocks, all three references, new
// vectors and skipped blocks.
func TestCodingToolsAreUsed(t *testing.T) {
	cfg := Config{Width: 320, Height: 180, Bitrate: 250000, FrameRate: 30, Speed: 2}
	_, _, enc := encodeSeq(t, cfg, seqStatic, 120, false)
	st := enc.Stats()
	t.Logf("static scene with a moving object: %+v", st)
	if st.MBIntra == 0 || st.MBLast == 0 || st.MBSkip == 0 {
		t.Errorf("intra %d last %d skip %d", st.MBIntra, st.MBLast, st.MBSkip)
	}
	if st.MBGolden+st.MBAlt == 0 {
		t.Errorf("no macroblock ever referenced the golden or altref frame")
	}
	_, _, enc = encodeSeq(t, cfg, seqNatural, 60, false)
	st = enc.Stats()
	t.Logf("panning texture: %+v", st)
	if st.MBNewMV == 0 {
		t.Errorf("no new motion vectors on a panning sequence")
	}
}

// Golden refreshes move the old golden frame to the altref slot; with frequent refreshes both references are in
// use and the stream must still decode to the encoder's reconstruction (checked bit by bit).
func TestGoldenAndAltRefStayInSync(t *testing.T) {
	cfg := Config{Width: 192, Height: 112, Bitrate: 200000, FrameRate: 3, Speed: 2} // low nominal rate: golden every 15 frames
	_, _, enc := encodeSeq(t, cfg, seqStatic, 100, true)
	st := enc.Stats()
	t.Logf("golden %d altref %d last %d", st.MBGolden, st.MBAlt, st.MBLast)
	if st.MBGolden == 0 || st.MBAlt == 0 {
		t.Errorf("golden %d altref %d macroblocks: one of the references was never used", st.MBGolden, st.MBAlt)
	}
}

func TestKeyFrameRequestAndResize(t *testing.T) {
	enc, err := New(Config{Width: 320, Height: 180, Bitrate: 400000})
	if err != nil {
		t.Fatal(err)
	}
	gen := newSeq(320, 180, seqNatural)
	dec := vp8.NewDecoder()
	keys := 0
	for i := 0; i < 30; i++ {
		if i == 10 {
			enc.RequestKeyFrame()
		}
		p, _ := enc.Encode(gen.frame(i))
		if p == nil {
			continue
		}
		if p.Key {
			keys++
			if !vp8.IsKeyFrame(p.Data) {
				t.Fatal("packet says key but is not")
			}
		}
		if _, err := dec.Decode(p.Data); err != nil {
			t.Fatal(err)
		}
		if i == 10 && !p.Key {
			t.Fatal("requested key frame was not produced")
		}
	}
	if keys != 2 {
		t.Fatalf("key frames: %d, want 2", keys)
	}
	if err := enc.Resize(256, 144); err != nil {
		t.Fatal(err)
	}
	gen = newSeq(256, 144, seqNatural)
	for i := 0; i < 10; i++ {
		p, err := enc.Encode(gen.frame(i))
		if err != nil || p == nil {
			t.Fatal(err)
		}
		if i == 0 && !p.Key {
			t.Fatal("no key frame after resize")
		}
		img, err := dec.Decode(p.Data)
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 144 {
			t.Fatalf("size after resize %v", img.Bounds())
		}
	}
}

func TestBitrateChange(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 6 s")
	}
	enc, _ := New(Config{Width: 640, Height: 360, Bitrate: 1000000, Speed: 1})
	gen := newSeq(640, 360, seqNatural)
	measure := func(from, to int) float64 {
		b := 0
		for i := from; i < to; i++ {
			p, _ := enc.Encode(gen.frame(i))
			if p != nil {
				b += len(p.Data)
			}
		}
		return float64(b) * 8 / (float64(to-from) / 30) / 1000
	}
	a := measure(0, 60)
	enc.SetBitrate(250000)
	_ = measure(60, 90) // settle
	b := measure(90, 150)
	t.Logf("1000 kbps target: %.0f kbps; after SetBitrate(250 kbps): %.0f kbps", a, b)
	if b > a*0.6 {
		t.Errorf("bitrate did not follow the new target: %.0f -> %.0f", a, b)
	}
}

// ---- cross-checks with ffmpeg (libvpx / ffvp8) -------------------------------------------------

func writeIVF(path string, w, h int, pkts [][]byte) error {
	var b bytes.Buffer
	b.WriteString("DKIF")
	binary.Write(&b, binary.LittleEndian, uint16(0))
	binary.Write(&b, binary.LittleEndian, uint16(32))
	b.WriteString("VP80")
	binary.Write(&b, binary.LittleEndian, uint16(w))
	binary.Write(&b, binary.LittleEndian, uint16(h))
	binary.Write(&b, binary.LittleEndian, uint32(30))
	binary.Write(&b, binary.LittleEndian, uint32(1))
	binary.Write(&b, binary.LittleEndian, uint32(len(pkts)))
	binary.Write(&b, binary.LittleEndian, uint32(0))
	for i, p := range pkts {
		binary.Write(&b, binary.LittleEndian, uint32(len(p)))
		binary.Write(&b, binary.LittleEndian, uint64(i))
		b.Write(p)
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// TestFFmpegDecodesOurStreams decodes the encoder's streams with ffmpeg's native VP8 decoder and with libvpx
// and compares the pictures with the encoder's own reconstruction.
func TestFFmpegDecodesOurStreams(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip("ffmpeg not available")
	}
	for _, kind := range []seqKind{seqNatural, seqGradient, seqSceneCut} {
		cfg := Config{Width: 320, Height: 180, Bitrate: 300000, FrameRate: 30, KeyInterval: 50, Speed: 2}
		pkts, _, _ := encodeSeq(t, cfg, kind, 60, false)
		dir := t.TempDir()
		ivf := filepath.Join(dir, "a.ivf")
		if err := writeIVF(ivf, cfg.Width, cfg.Height, pkts); err != nil {
			t.Fatal(err)
		}
		// reference: our own decoder
		d := vp8.NewDecoder()
		var want bytes.Buffer
		for _, p := range pkts {
			img, err := d.Decode(p)
			if err != nil {
				t.Fatal(err)
			}
			for y := 0; y < cfg.Height; y++ {
				want.Write(img.Y[y*img.YStride : y*img.YStride+cfg.Width])
			}
			for y := 0; y < cfg.Height/2; y++ {
				want.Write(img.Cb[y*img.CStride : y*img.CStride+cfg.Width/2])
			}
			for y := 0; y < cfg.Height/2; y++ {
				want.Write(img.Cr[y*img.CStride : y*img.CStride+cfg.Width/2])
			}
		}
		for _, dec := range []string{"vp8", "libvpx"} {
			out, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-c:v", dec, "-i", ivf, "-f", "rawvideo", "-pix_fmt", "yuv420p", "-").Output()
			if err != nil {
				var stderr []byte
				if ee, ok := err.(*exec.ExitError); ok {
					stderr = ee.Stderr
				}
				t.Fatalf("%s %s: %v %s", kind, dec, err, stderr)
			}
			if !bytes.Equal(out, want.Bytes()) {
				t.Errorf("%s: ffmpeg %s output differs from our decoder (%d vs %d bytes)", kind, dec, len(out), want.Len())
			} else {
				t.Logf("%s: ffmpeg %s decodes %d frames bit-exactly like the repository decoder", kind, dec, len(pkts))
			}
		}
	}
}
