package media

import (
	"bytes"
	"image"
	"math"
	"os"
	"testing"
)

func i420Gradient(w, h, t int) (y, u, v []byte) {
	y = make([]byte, w*h)
	u = make([]byte, (w+1)/2*((h+1)/2))
	v = make([]byte, len(u))
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			y[j*w+i] = byte(40 + (i*3+j*2+t*4)%180)
		}
	}
	cw := (w + 1) / 2
	for j := 0; j < (h+1)/2; j++ {
		for i := 0; i < cw; i++ {
			u[j*cw+i] = byte(100 + (i*2+t)%60)
			v[j*cw+i] = byte(140 - (j*2+t)%60)
		}
	}
	return
}

func psnr(a, b []byte) float64 {
	var se float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		se += d * d
	}
	if se == 0 {
		return 99
	}
	return 10 * math.Log10(255*255/(se/float64(len(a))))
}

func TestWCVP8RoundTrip(t *testing.T) {
	const w, h = 160, 96
	enc, err := NewWCVP8Encoder(w, h, 3_000_000, 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewWCVideoDecoder("vp8", nil)
	if err != nil {
		t.Fatal(err)
	}
	worst := 99.0
	keys := 0
	for f := 0; f < 8; f++ {
		y, u, v := i420Gradient(w, h, f)
		data, key, err := enc.Encode(w, h, y, u, v, w, (w+1)/2, false)
		if err != nil || data == nil {
			t.Fatalf("frame %d: %v %v", f, err, data == nil)
		}
		if key {
			keys++
		}
		if key != VP8Keyframe(data) {
			t.Fatalf("frame %d: key flag %v disagrees with the bitstream", f, key)
		}
		pics, err := dec.Decode(data)
		if err != nil || len(pics) != 1 {
			t.Fatalf("decode %d: %v", f, err)
		}
		pic := pics[0]
		if pic.Format != "I420" || pic.Width != w || pic.Height != h {
			t.Fatalf("picture %+v", pic)
		}
		if p := psnr(y, pic.Planes[0]); p < worst {
			worst = p
		}
	}
	if keys != 1 || worst < 33 {
		t.Fatalf("keys=%d worst Y PSNR %.1f dB", keys, worst)
	}
	t.Logf("VP8 round trip: worst luma PSNR %.1f dB over 8 frames", worst)
}

func TestWCOpusRoundTrip(t *testing.T) {
	const rate, ch = 48000, 2
	enc, err := NewWCOpusEncoder(rate, ch, 96000, 20000, "audio")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewWCAudioDecoder("opus", nil, rate, ch, false)
	if err != nil {
		t.Fatal(err)
	}
	n := enc.FrameSamples()
	if n != 960 {
		t.Fatalf("frame samples %d", n)
	}
	var in, out []float32
	for f := 0; f < 40; f++ {
		pcm := make([]float32, n*ch)
		for i := 0; i < n; i++ {
			s := float32(0.4 * math.Sin(2*math.Pi*440*float64(f*n+i)/rate))
			pcm[i*ch], pcm[i*ch+1] = s, -s
		}
		pkt, err := enc.Encode(pcm)
		if err != nil || len(pkt) == 0 {
			t.Fatalf("encode %d: %v", f, err)
		}
		planar, r, err := dec.Decode(pkt)
		if err != nil || r != rate || len(planar) != ch {
			t.Fatalf("decode %d: %v rate=%d ch=%d", f, err, r, len(planar))
		}
		for i := 0; i < n; i++ {
			in = append(in, pcm[i*ch])
		}
		out = append(out, planar[0]...)
	}
	// the codec delays the signal; find the lag with the best match, then measure the error there
	best, bestLag := math.Inf(1), 0
	for lag := 0; lag < 1500; lag++ {
		var se float64
		for i := 8000; i < 30000; i++ {
			d := float64(in[i]) - float64(out[i+lag])
			se += d * d
		}
		if se < best {
			best, bestLag = se, lag
		}
	}
	var sig float64
	for i := 8000; i < 30000; i++ {
		sig += float64(in[i]) * float64(in[i])
	}
	snr := 10 * math.Log10(sig/best)
	if snr < 15 {
		t.Fatalf("Opus round trip SNR %.1f dB at lag %d", snr, bestLag)
	}
	t.Logf("Opus round trip: SNR %.1f dB, codec delay %d samples", snr, bestLag)
}

func TestWCRawAudioAndADTS(t *testing.T) {
	d, err := NewWCAudioDecoder("pcm-s16", nil, 8000, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	out, rate, err := d.Decode([]byte{0x00, 0x40, 0x00, 0xc0, 0xff, 0x7f, 0x00, 0x80})
	if err != nil || rate != 8000 || len(out) != 2 || len(out[0]) != 2 {
		t.Fatalf("%v %v %v", out, rate, err)
	}
	if out[0][0] != 0.5 || out[1][0] != -0.5 || out[1][1] != -1 || out[0][1] < 0.999 {
		t.Fatalf("pcm-s16 decoded as %v", out)
	}
	u, _ := NewWCAudioDecoder("ulaw", nil, 8000, 1, false)
	o, _, _ := u.Decode([]byte{0xff, 0x7f, 0x00, 0x80})
	if o[0][0] != 0 || o[0][1] != 0 || o[0][2] > -0.9 || o[0][3] < 0.9 {
		t.Fatalf("ulaw decoded as %v", o[0])
	}
	// ADTS header for AAC-LC, 44.1 kHz, stereo: 0xFFF1 5080 ...
	asc, body, err := splitADTS([]byte{0xff, 0xf1, 0x50, 0x80, 0x01, 0x3f, 0xfc, 0xaa, 0xbb})
	if err != nil || len(asc) != 2 || asc[0] != 0x12 || asc[1] != 0x10 || len(body) != 2 {
		t.Fatalf("asc=%x body=%x err=%v", asc, body, err)
	}
	if WCVideoName("avc1.64001f") != "h264" || WCVideoName("vp09.00.10.08") != "vp9" || WCVideoName("av01.0.04M.08") != "av1" || WCVideoName("hvc1.1.6.L93.B0") != "hevc" || WCVideoName("h264") != "" {
		t.Fatal("codec string mapping")
	}
}

// annexBOf turns AVCC length-prefixed samples into Annex B, with the parameter sets of the avcC in front of the
// first one.
func annexBOf(avcc []byte, sample []byte, first bool) []byte {
	var out []byte
	sc := []byte{0, 0, 0, 1}
	if first {
		p := 5
		n := int(avcc[p] & 31)
		p++
		for i := 0; i < n; i++ {
			l := int(avcc[p])<<8 | int(avcc[p+1])
			out = append(append(out, sc...), avcc[p+2:p+2+l]...)
			p += 2 + l
		}
		m := int(avcc[p])
		p++
		for i := 0; i < m; i++ {
			l := int(avcc[p])<<8 | int(avcc[p+1])
			out = append(append(out, sc...), avcc[p+2:p+2+l]...)
			p += 2 + l
		}
	}
	ls := int(avcc[4]&3) + 1
	for len(sample) > ls {
		n := 0
		for i := 0; i < ls; i++ {
			n = n<<8 | int(sample[i])
		}
		out = append(append(out, sc...), sample[ls:ls+n]...)
		sample = sample[ls+n:]
	}
	return out
}

// Every pure-Go video decoder, driven the way WebCodecs drives it (one chunk at a time, codec string and
// description), produces the pictures the player's own decoder produces for the same clip: in decode order for the
// codecs whose pictures belong to their chunk, in display order (with the decoder's own delay, then Flush) for HEVC.
func TestWCVideoDecodersAgreeWithThePlayer(t *testing.T) {
	cases := []struct{ file, codec string }{
		{"vp8_inter.webm", "vp8"},
		{"vp9_arf.webm", "vp09.00.10.08"},
		{"h264_baseline_cavlc.mp4", "avc1.42001e"},
		{"h264_main_b_cabac.mp4", "avc1.4d401e"},
		{"av1_inter.mp4", "av01.0.04M.08"},
		{"hevc_main_b.mp4", "hvc1.1.6.L93.B0"},
	}
	lumaEq := func(got *WCPicture, want *image.YCbCr) bool {
		if got.Format != "I420" || got.Width != want.Bounds().Dx() || got.Height != want.Bounds().Dy() {
			return false
		}
		for y := 0; y < got.Height; y++ {
			if !bytes.Equal(got.Planes[0][y*got.Strides[0]:y*got.Strides[0]+got.Width], want.Y[y*want.YStride:y*want.YStride+got.Width]) {
				return false
			}
		}
		return true
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + c.file)
			if err != nil {
				t.Skip(err)
			}
			info, err := ProbeBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			src := BytesSource(data)
			vs, err := loadVideoStream(src, info)
			if err != nil {
				t.Fatal(err)
			}
			ref := newNativeDecoder(vs)
			wc, err := NewWCVideoDecoder(c.codec, vs.cfg)
			if err != nil {
				t.Fatal(err)
			}
			var annex *WCVideoDecoder
			if vs.codec == "h264" {
				if annex, err = NewWCVideoDecoder(c.codec, nil); err != nil {
					t.Fatal(err)
				}
			}
			var refPics []*image.YCbCr // decode order
			refAt := map[int]*image.YCbCr{}
			var got []*WCPicture
			for i := range vs.samples {
				b, err := vs.payload(src, i)
				if err != nil {
					t.Fatal(err)
				}
				want, _ := ref.Decode(b)
				pics, _ := wc.Decode(b)
				if want != nil {
					refPics = append(refPics, want)
					refAt[i] = want
				}
				if wc.DecodeOrder() {
					if (want == nil) != (len(pics) == 0) || len(pics) > 1 {
						t.Fatalf("sample %d: reference picture %v, WebCodecs pictures %d", i, want != nil, len(pics))
					}
					if want != nil && !lumaEq(pics[0], want) {
						t.Fatalf("sample %d: luma differs from the player's decoder", i)
					}
				}
				got = append(got, pics...)
				if annex != nil {
					ap, _ := annex.Decode(annexBOf(vs.cfg, b, i == 0))
					if (len(ap) == 0) != (len(pics) == 0) || (len(ap) > 0 && !bytes.Equal(ap[0].Planes[0], pics[0].Planes[0])) {
						t.Fatalf("sample %d: Annex B decode differs from the AVC decode", i)
					}
				}
			}
			got = append(got, wc.Flush()...)
			if !wc.DecodeOrder() {
				// display order: the player orders its decode-order pictures by presentation time
				var pres []*image.YCbCr
				for _, i := range vs.order() {
					if p := refAt[i]; p != nil {
						pres = append(pres, p)
					}
				}
				if len(got) != len(pres) {
					t.Fatalf("%d pictures, the player shows %d", len(got), len(pres))
				}
				for k := range got {
					if !lumaEq(got[k], pres[k]) {
						t.Fatalf("picture %d is not the %dth in presentation order", k, k)
					}
				}
			} else if len(got) != len(refPics) {
				t.Fatalf("%d pictures, reference %d", len(got), len(refPics))
			}
			if len(got) < 5 {
				t.Fatalf("only %d pictures", len(got))
			}
			t.Logf("%s: %d pictures identical to the player's decoder (reorder depth %d)", c.codec, len(got), wc.ReorderDepth())
		})
	}
}
