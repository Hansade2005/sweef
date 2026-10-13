package media

import (
	"image"
	"math"
	"testing"
)

// the fixtures show one solid colour per second
var fixtureColours = [6][3]int{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 0}, {255, 0, 255}, {0, 255, 255}}

// fixtureTone is the tone frequency of second n (Hz)
func fixtureTone(n int) float64 { return 440 + 220*float64(n) }

func loadMSE(t *testing.T, mime, dir, initName string, segs []string) *MSEBuffer {
	t.Helper()
	b := NewMSEBuffer(mime)
	if _, err := b.Append(fx(t, dir, initName), 0); err != nil {
		t.Fatal(err)
	}
	for _, s := range segs {
		if _, err := b.Append(fx(t, dir, s), 0); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func close3(img *image.RGBA, x, y int, want [3]int, tol int) bool {
	o := img.PixOffset(x, y)
	for i := 0; i < 3; i++ {
		if d := int(img.Pix[o+i]) - want[i]; d < -tol || d > tol {
			return false
		}
	}
	return true
}

// decodeAt decodes the picture shown at time tm the way the player does:
// from the sync sample before it.
func decodeAt(t *testing.T, st *TrackStore, tm float64) *image.RGBA {
	t.Helper()
	dec, err := NewVideoDecoder(st.Config())
	if err != nil {
		t.Fatal(err)
	}
	want, ok := st.ShownAt(tm)
	if !ok {
		t.Fatalf("nothing shown at %v", tm)
	}
	k, ok := st.KeyAtOrBefore(want.DTS)
	if !ok {
		t.Fatalf("no key before %v", want.DTS)
	}
	var pic Picture
	for s := k; ; {
		data, _ := s.Bytes()
		p, err := dec.Decode(data)
		if err != nil {
			t.Fatalf("decode at DTS %.3f: %v", s.DTS, err)
		}
		if s.DTS == want.DTS {
			pic = p
			break
		}
		n, ok, _ := st.Next(s.DTS)
		if !ok {
			t.Fatal("ran out of samples")
		}
		s = n
	}
	if !pic.Valid() {
		t.Fatal("no picture")
	}
	return pic.RGBA()
}

func checkColours(t *testing.T, st *TrackStore, label string) {
	t.Helper()
	for _, sec := range []int{0, 2, 3, 5} {
		img := decodeAt(t, st, float64(sec)+0.6)
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		// bottom-right corner is background, away from the moving box
		if !close3(img, w-4, h-4, fixtureColours[sec], 40) {
			o := img.PixOffset(w-4, h-4)
			t.Errorf("%s second %d: pixel %v, want colour %v", label, sec, img.Pix[o:o+3], fixtureColours[sec])
		}
	}
}

func TestDecodeH264FragmentsBothQualities(t *testing.T) {
	segs := []string{seg(0, 1, "m4s"), seg(0, 2, "m4s"), seg(0, 3, "m4s")}
	b := loadMSE(t, `video/mp4; codecs="avc1.4d400c"`, "h264", "init-0.m4s", segs)
	checkColours(t, b.FirstStore("video"), "h264 320x180")
	lo := loadMSE(t, `video/mp4; codecs="avc1.4d400b"`, "h264", "init-1.m4s", []string{seg(1, 1, "m4s"), seg(1, 2, "m4s"), seg(1, 3, "m4s")})
	checkColours(t, lo.FirstStore("video"), "h264 160x90")
	if img := decodeAt(t, lo.FirstStore("video"), 1); img.Bounds().Dx() != 160 || img.Bounds().Dy() != 90 {
		t.Fatalf("low quality size %v", img.Bounds())
	}
}

func TestDecodeVP9WebM(t *testing.T) {
	b := loadMSE(t, `video/webm; codecs="vp9"`, "vp9", "init-0.webm", []string{seg(0, 1, "webm"), seg(0, 2, "webm"), seg(0, 3, "webm")})
	checkColours(t, b.FirstStore("video"), "vp9")
}

func TestDecodeAV1FragmentedMP4(t *testing.T) {
	b := loadMSE(t, `video/mp4; codecs="av01.0.00M.08"`, "av1", "init-0.m4s", []string{seg(0, 1, "m4s"), seg(0, 2, "m4s"), seg(0, 3, "m4s")})
	checkColours(t, b.FirstStore("video"), "av1")
}

// toneHz estimates the frequency of mono PCM by counting zero crossings.
func toneHz(pcm []float32, rate int) float64 {
	n := 0
	for i := 1; i < len(pcm); i++ {
		if (pcm[i-1] < 0) != (pcm[i] < 0) {
			n++
		}
	}
	return float64(n) / 2 / (float64(len(pcm)) / float64(rate))
}

func decodeAudioSpan(t *testing.T, st *TrackStore, from, to float64) ([]float32, int) {
	t.Helper()
	dec, err := NewAudioDecoder(st.Config())
	if err != nil {
		t.Fatal(err)
	}
	var out []float32
	s, ok := st.FirstAfter(from)
	if !ok {
		t.Fatal("no audio there")
	}
	for s.PTS < to {
		data, _ := s.Bytes()
		pcm, err := dec.Decode(data)
		if err != nil {
			t.Fatalf("audio decode: %v", err)
		}
		if len(pcm) > 0 {
			out = append(out, pcm[0]...)
		}
		n, ok, _ := st.Next(s.DTS)
		if !ok {
			break
		}
		s = n
	}
	return out, dec.Rate()
}

func TestDecodeAudioAACAndOpus(t *testing.T) {
	aac := loadMSE(t, `audio/mp4; codecs="mp4a.40.2"`, "h264", "init-2.m4s", []string{seg(2, 1, "m4s"), seg(2, 2, "m4s"), seg(2, 3, "m4s"), seg(2, 4, "m4s")})
	opus := loadMSE(t, `audio/webm; codecs="opus"`, "vp9", "init-1.webm", []string{seg(1, 1, "webm"), seg(1, 2, "webm"), seg(1, 3, "webm"), seg(1, 4, "webm")})
	for name, st := range map[string]*TrackStore{"aac": aac.FirstStore("audio"), "opus": opus.FirstStore("audio")} {
		for _, sec := range []int{0, 2, 4} {
			// skip 0.3 s into each second: the tone steps there
			pcm, rate := decodeAudioSpan(t, st, float64(sec)+0.2, float64(sec)+0.9)
			// the first packets after a cold start are pre-roll: ignore the first 100 ms
			pcm = pcm[rate/10:]
			got, want := toneHz(pcm, rate), fixtureTone(sec)
			if math.Abs(got-want) > want*0.06 {
				t.Errorf("%s second %d: tone %.0f Hz, want %.0f", name, sec, got, want)
			}
			var pk float32
			for _, v := range pcm {
				if v > pk {
					pk = v
				}
			}
			if pk < 0.35 || pk > 0.7 {
				t.Errorf("%s second %d: peak %v, want ~0.5", name, sec, pk)
			}
		}
	}
}
