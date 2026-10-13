package capture

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"testing"
)

// refPlanes builds random-but-reproducible 4:2:0 planes for a w x h frame.
func refPlanes(w, h int, seed int64) (y, u, v []byte) {
	r := rand.New(rand.NewSource(seed))
	cw, ch := (w+1)/2, (h+1)/2
	y, u, v = make([]byte, w*h), make([]byte, cw*ch), make([]byte, cw*ch)
	for i := range y {
		y[i] = byte(16 + r.Intn(220))
	}
	for i := range u {
		u[i], v[i] = byte(16+r.Intn(225)), byte(16+r.Intn(225))
	}
	return
}

func frameEq(t *testing.T, name string, got *Frame, y, u, v []byte) {
	t.Helper()
	for i := 0; i < got.H; i++ {
		if !bytes.Equal(got.Y[i*got.YStride:i*got.YStride+got.W], y[i*got.W:(i+1)*got.W]) {
			t.Fatalf("%s: luma row %d differs", name, i)
		}
	}
	cw := (got.W + 1) / 2
	for i := 0; i < (got.H+1)/2; i++ {
		if !bytes.Equal(got.U[i*got.UVStride:i*got.UVStride+cw], u[i*cw:(i+1)*cw]) {
			t.Fatalf("%s: U row %d differs", name, i)
		}
		if !bytes.Equal(got.V[i*got.UVStride:i*got.UVStride+cw], v[i*cw:(i+1)*cw]) {
			t.Fatalf("%s: V row %d differs", name, i)
		}
	}
}

func TestConvertPlanarAndSemiPlanarExact(t *testing.T) {
	for _, sz := range [][2]int{{16, 8}, {64, 48}, {30, 20}} {
		w, h := sz[0], sz[1]
		y, u, v := refPlanes(w, h, 1)
		cw, ch := (w+1)/2, (h+1)/2
		// I420 / YV12
		i420 := append(append(append([]byte{}, y...), u...), v...)
		yv12 := append(append(append([]byte{}, y...), v...), u...)
		d := NewFrame(w, h)
		if err := Convert(d, PixI420, i420, w); err != nil {
			t.Fatal(err)
		}
		frameEq(t, "I420", d, y, u, v)
		d = NewFrame(w, h)
		if err := Convert(d, PixYV12, yv12, w); err != nil {
			t.Fatal(err)
		}
		frameEq(t, "YV12", d, y, u, v)
		// NV12 / NV21, with a padded stride to exercise bytesperline
		for _, swap := range []bool{false, true} {
			stride := w + 6
			buf := make([]byte, stride*h+stride*ch)
			for r := 0; r < h; r++ {
				copy(buf[r*stride:], y[r*w:(r+1)*w])
			}
			for r := 0; r < ch; r++ {
				for c := 0; c < cw; c++ {
					a, b := u[r*cw+c], v[r*cw+c]
					if swap {
						a, b = b, a
					}
					buf[stride*h+r*stride+c*2], buf[stride*h+r*stride+c*2+1] = a, b
				}
			}
			d = NewFrame(w, h)
			f := PixNV12
			if swap {
				f = PixNV21
			}
			if err := Convert(d, f, buf, stride); err != nil {
				t.Fatal(err)
			}
			frameEq(t, string(f), d, y, u, v)
		}
	}
}

func TestConvertPacked422(t *testing.T) {
	w, h := 32, 16
	r := rand.New(rand.NewSource(7))
	y := make([]byte, w*h)
	u422, v422 := make([]byte, (w/2)*h), make([]byte, (w/2)*h)
	for i := range y {
		y[i] = byte(r.Intn(256))
	}
	for i := range u422 {
		u422[i], v422[i] = byte(r.Intn(256)), byte(r.Intn(256))
	}
	// reference 4:2:0 chroma: average of the two vertically adjacent 4:2:2 samples, rounding half up
	ru, rv := make([]byte, (w/2)*(h/2)), make([]byte, (w/2)*(h/2))
	for cy := 0; cy < h/2; cy++ {
		for cx := 0; cx < w/2; cx++ {
			ru[cy*(w/2)+cx] = byte((int(u422[2*cy*(w/2)+cx]) + int(u422[(2*cy+1)*(w/2)+cx]) + 1) / 2)
			rv[cy*(w/2)+cx] = byte((int(v422[2*cy*(w/2)+cx]) + int(v422[(2*cy+1)*(w/2)+cx]) + 1) / 2)
		}
	}
	for _, f := range []PixelFormat{PixYUYV, PixUYVY} {
		stride := w*2 + 8
		buf := make([]byte, stride*h)
		for row := 0; row < h; row++ {
			for x := 0; x < w; x += 2 {
				o := row*stride + x*2
				uu, vv := u422[row*(w/2)+x/2], v422[row*(w/2)+x/2]
				if f == PixYUYV {
					buf[o], buf[o+1], buf[o+2], buf[o+3] = y[row*w+x], uu, y[row*w+x+1], vv
				} else {
					buf[o], buf[o+1], buf[o+2], buf[o+3] = uu, y[row*w+x], vv, y[row*w+x+1]
				}
			}
		}
		d := NewFrame(w, h)
		if err := Convert(d, f, buf, stride); err != nil {
			t.Fatal(err)
		}
		frameEq(t, string(f), d, y, ru, rv)
	}
	if err := Convert(NewFrame(31, 8), PixYUYV, make([]byte, 31*2*8), 62); err == nil {
		t.Error("odd width YUYV should be refused")
	}
}

// refRGBToYUV is the BT.601 limited-range transform in floating point, written independently of RGBToY & co.
func refYUV(r, g, b float64) (y, u, v float64) {
	y = 16 + (65.481*r+128.553*g+24.966*b)/255
	u = 128 + (-37.797*r-74.203*g+112.0*b)/255
	v = 128 + (112.0*r-93.786*g-18.214*b)/255
	return
}

func TestConvertRGB(t *testing.T) {
	w, h := 40, 24
	r := rand.New(rand.NewSource(3))
	rgb := make([]byte, w*h*3)
	for i := range rgb {
		rgb[i] = byte(r.Intn(256))
	}
	for _, f := range []PixelFormat{PixRGB24, PixBGR24} {
		buf := append([]byte(nil), rgb...)
		if f == PixBGR24 {
			for i := 0; i < len(buf); i += 3 {
				buf[i], buf[i+2] = buf[i+2], buf[i]
			}
		}
		d := NewFrame(w, h)
		if err := Convert(d, f, buf, w*3); err != nil {
			t.Fatal(err)
		}
		maxd := 0.0
		for yy := 0; yy < h; yy++ {
			for x := 0; x < w; x++ {
				p := rgb[(yy*w+x)*3:]
				ey, _, _ := refYUV(float64(p[0]), float64(p[1]), float64(p[2]))
				maxd = math.Max(maxd, math.Abs(ey-float64(d.Y[yy*d.YStride+x])))
			}
		}
		for cy := 0; cy < h/2; cy++ {
			for cx := 0; cx < w/2; cx++ {
				var sr, sg, sb float64
				for dy := 0; dy < 2; dy++ {
					for dx := 0; dx < 2; dx++ {
						p := rgb[((2*cy+dy)*w+2*cx+dx)*3:]
						sr, sg, sb = sr+float64(p[0]), sg+float64(p[1]), sb+float64(p[2])
					}
				}
				_, eu, ev := refYUV(sr/4, sg/4, sb/4)
				maxd = math.Max(maxd, math.Abs(eu-float64(d.U[cy*d.UVStride+cx])))
				maxd = math.Max(maxd, math.Abs(ev-float64(d.V[cy*d.UVStride+cx])))
			}
		}
		if maxd > 2 { // integer arithmetic and the 2x2 average round at most a couple of levels away
			t.Errorf("%s: max deviation from the float reference is %.2f levels", f, maxd)
		}
	}
	// known colours
	white := []byte{255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255}
	d := NewFrame(2, 2)
	_ = Convert(d, PixRGB24, white, 6)
	if d.Y[0] != 235 || d.U[0] != 128 || d.V[0] != 128 {
		t.Errorf("white -> Y=%d U=%d V=%d", d.Y[0], d.U[0], d.V[0])
	}
	black := make([]byte, 12)
	_ = Convert(d, PixBGR24, black, 6)
	if d.Y[0] != 16 || d.U[0] != 128 || d.V[0] != 128 {
		t.Errorf("black -> Y=%d U=%d V=%d", d.Y[0], d.U[0], d.V[0])
	}
}

func TestConvertGreyAndErrors(t *testing.T) {
	d := NewFrame(4, 2)
	if err := Convert(d, PixGrey, []byte{0, 255, 0, 255, 255, 0, 255, 0}, 4); err != nil {
		t.Fatal(err)
	}
	if d.Y[0] != 16 || d.Y[1] != 235 || d.U[0] != 128 {
		t.Errorf("grey: %v %v", d.Y, d.U)
	}
	if err := Convert(d, PixI420, make([]byte, 3), 4); err == nil {
		t.Error("short buffer accepted")
	}
	if err := Convert(d, "XXXX", make([]byte, 100), 4); err == nil {
		t.Error("unknown format accepted")
	}
	if err := Convert(d, PixGrey, make([]byte, 100), 0); err == nil {
		t.Error("zero stride accepted")
	}
}

func testJPEG(t *testing.T, w, h int, gray bool) (data []byte, rgba *image.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) * 255 / (w + h)), 255})
		}
	}
	var buf bytes.Buffer
	var src image.Image = img
	if gray {
		g := image.NewGray(img.Bounds())
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				g.Set(x, y, img.At(x, y))
			}
		}
		src = g
	}
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), img
}

// stripDHT removes every DHT segment, producing the abbreviated JPEG many UVC cameras send.
func stripDHT(t *testing.T, data []byte) []byte {
	t.Helper()
	out := append([]byte(nil), data[:2]...)
	i := 2
	removed := 0
	for i+4 <= len(data) {
		m := data[i+1]
		if data[i] != 0xFF {
			t.Fatalf("marker expected at %d", i)
		}
		n := int(data[i+2])<<8 | int(data[i+3])
		if m == 0xDA { // from SOS on, copy everything
			out = append(out, data[i:]...)
			break
		}
		if m == 0xC4 {
			removed++
		} else {
			out = append(out, data[i:i+2+n]...)
		}
		i += 2 + n
	}
	if removed == 0 {
		t.Fatal("test JPEG had no DHT to strip")
	}
	return out
}

func TestDecodeMJPEG(t *testing.T) {
	w, h := 64, 48
	data, _ := testJPEG(t, w, h, false)
	// reference: image/jpeg decode -> float BT.601 per pixel, chroma averaged over each 2x2 block
	dec, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	rgb := func(x, y int) (float64, float64, float64) {
		r, g, b, _ := dec.At(x, y).RGBA()
		return float64(r >> 8), float64(g >> 8), float64(b >> 8)
	}
	check := func(name string, in []byte) {
		d := NewFrame(w, h)
		if err := DecodeMJPEG(d, in); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var sum, maxd float64
		var n int
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, b := rgb(x, y)
				ey, _, _ := refYUV(r, g, b)
				dd := math.Abs(ey - float64(d.Y[y*d.YStride+x]))
				sum += dd
				n++
				maxd = math.Max(maxd, dd)
			}
		}
		for cy := 0; cy < h/2; cy++ {
			for cx := 0; cx < w/2; cx++ {
				var sr, sg, sb float64
				for dy := 0; dy < 2; dy++ {
					for dx := 0; dx < 2; dx++ {
						r, g, b := rgb(2*cx+dx, 2*cy+dy)
						sr, sg, sb = sr+r, sg+g, sb+b
					}
				}
				_, eu, ev := refYUV(sr/4, sg/4, sb/4)
				for _, p := range [][2]float64{{eu, float64(d.U[cy*d.UVStride+cx])}, {ev, float64(d.V[cy*d.UVStride+cx])}} {
					dd := math.Abs(p[0] - p[1])
					sum += dd
					n++
					maxd = math.Max(maxd, dd)
				}
			}
		}
		if mean := sum / float64(n); mean > 1.0 || maxd > 5 {
			t.Errorf("%s: mean deviation %.2f, max %.1f levels from the reference", name, mean, maxd)
		}
	}
	check("with tables", data)
	check("abbreviated (no DHT)", stripDHT(t, data))

	// grey JPEG: luma only, neutral chroma
	gdata, _ := testJPEG(t, w, h, true)
	d := NewFrame(w, h)
	if err := DecodeMJPEG(d, stripDHT(t, gdata)); err != nil {
		t.Fatal(err)
	}
	if d.U[0] != 128 || d.V[5] != 128 {
		t.Errorf("grey chroma %d %d", d.U[0], d.V[5])
	}
	// size mismatch and garbage are errors, not panics
	if err := DecodeMJPEG(NewFrame(32, 32), data); err == nil {
		t.Error("size mismatch accepted")
	}
	if err := DecodeMJPEG(d, []byte("not a jpeg")); err == nil {
		t.Error("garbage accepted")
	}
	if err := DecodeMJPEG(d, data[:len(data)/2]); err == nil {
		t.Error("truncated frame accepted")
	}
}

func TestStdDHTMatchesEncoder(t *testing.T) {
	// The Go encoder writes the Annex K tables. Our inserted copy must be byte-identical to its DHT payloads.
	data, _ := testJPEG(t, 16, 16, false)
	var enc []byte
	i := 2
	for i+4 <= len(data) {
		n := int(data[i+2])<<8 | int(data[i+3])
		if data[i+1] == 0xC4 {
			enc = append(enc, data[i+4:i+2+n]...)
		}
		if data[i+1] == 0xDA {
			break
		}
		i += 2 + n
	}
	if !bytes.Equal(enc, stdDHT[4:]) {
		t.Fatalf("inserted tables differ from the encoder's (%d vs %d bytes)", len(enc), len(stdDHT)-4)
	}
}

func TestFrameHelpers(t *testing.T) {
	// RGBA round trip stays close
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 200, 100, 50, 255
	}
	f := FromImage(img)
	back := f.ToRGBA()
	for c := 0; c < 3; c++ {
		if d := int(back.Pix[c]) - int(img.Pix[c]); d < -3 || d > 3 {
			t.Errorf("round trip channel %d: %d vs %d", c, back.Pix[c], img.Pix[c])
		}
	}
	// scaling a flat frame keeps it flat, scaling a 2x2 checker down averages it
	flat := NewBlack(64, 48)
	out := NewFrame(20, 10)
	Scale(out, flat)
	for _, p := range out.Y {
		if p != 16 {
			t.Fatalf("flat scale produced %d", p)
		}
	}
	chk := NewFrame(4, 4)
	for i := range chk.Y {
		chk.Y[i] = []byte{0, 200}[(i+i/4)%2]
	}
	one := NewFrame(2, 2)
	Scale(one, chk)
	for _, p := range one.Y {
		if p != 100 {
			t.Fatalf("checker average %d, want 100", p)
		}
	}
	// scale up replicates
	up := NewFrame(4, 4)
	Scale(up, one)
	if up.Y[15] != 100 {
		t.Fatal("upscale")
	}
	c := f.Clone()
	c.Y[0] = 1
	if f.Y[0] == 1 {
		t.Error("clone shares memory")
	}
}

func BenchmarkDecodeMJPEG640x480(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 640; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	d := NewFrame(640, 480)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := DecodeMJPEG(d, buf.Bytes()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConvertYUYV1080p(b *testing.B) {
	d := NewFrame(1920, 1080)
	src := make([]byte, 1920*2*1080)
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		_ = Convert(d, PixYUYV, src, 1920*2)
	}
}
