package media

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/media/h264"
	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// h264Streams returns every H.264 stream the repository ships as a list of access units with the decoder
// configuration: the MP4 fixtures (length-prefixed units, avcC) and the conformance elementary streams
// (Annex B; interlaced, MBAFF, PAFF, multi-reference), keyed by name.
type h264Stream struct {
	name string
	avcc []byte // nil for Annex B
	aus  [][]byte
}

func h264Streams(t testing.TB) []h264Stream {
	t.Helper()
	var out []h264Stream
	var mp4s []string
	for _, pat := range []string{"testdata/h264_*.mp4", filepath.Join(testMediaDir, "h264_*.mp4")} {
		m, _ := filepath.Glob(pat)
		mp4s = append(mp4s, m...)
	}
	for _, f := range mp4s {
		if strings.Contains(f, "422") {
			continue // 4:2:2 (decoded through the generic path; covered by its own test)
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		info, err := ProbeBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := OpenTracks(BytesSource(b), info)
		if err != nil || v == nil {
			t.Fatalf("%s: open: %v", f, err)
		}
		s := h264Stream{name: filepath.Base(f), avcc: v.Config().Cfg}
		for _, sm := range v.Snapshot() {
			data, _ := sm.Bytes()
			s.aus = append(s.aus, data)
		}
		out = append(out, s)
	}
	es, _ := filepath.Glob("testdata/fmt/es_*.264")
	for _, f := range es {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h264Stream{name: filepath.Base(f), aus: h264.SplitAccessUnits(b)})
	}
	if len(out) < 8 {
		t.Fatalf("only %d H.264 streams found", len(out))
	}
	return out
}

// decodeH264Raw decodes a stream with a fresh decoder (threads frame threads, 0 for the plain decoder) and
// returns the raw planes of every returned picture in decode order.
func decodeH264Raw(t testing.TB, s h264Stream, threads int) [][]byte {
	t.Helper()
	d := h264.NewDecoder()
	d.SetThreads(threads)
	if s.avcc != nil {
		if err := d.SetAVCC(s.avcc); err != nil {
			t.Fatal(err)
		}
	}
	var out [][]byte
	for _, au := range s.aus {
		fr, err := d.DecodeFrame(au)
		if err != nil && fr == nil {
			// a stream that is damaged on purpose: the plain decoder fails the same way
			out = append(out, []byte("error"))
			continue
		}
		if fr != nil {
			fr.Wait()
			if img, ok := fr.ToYCbCr(); ok {
				out = append(out, toYUV(img).b)
			} else {
				out = append(out, vpicRaw(vpic{frm: fr}))
			}
		}
	}
	d.Flush()
	return out
}

// The fast deblocking path must give exactly the pictures of the reference filter.
func TestH264FastDeblockMatchesReference(t *testing.T) {
	for _, s := range h264Streams(t) {
		h264.SetFastDeblock(false)
		ref := decodeH264Raw(t, s, 0)
		h264.SetFastDeblock(true)
		got := decodeH264Raw(t, s, 0)
		if len(ref) != len(got) || len(ref) == 0 {
			t.Fatalf("%s: %d pictures with the reference filter, %d with the fast one", s.name, len(ref), len(got))
		}
		for i := range ref {
			if !bytes.Equal(ref[i], got[i]) {
				t.Fatalf("%s: picture %d differs between the reference and the fast deblocking filter", s.name, i)
			}
		}
	}
	h264.SetFastDeblock(true)
}

// Frame threading must give exactly the pictures of the plain decoder, whatever the number of threads, on
// every stream: the ones it threads (progressive 4:2:0 frames) and the ones it falls back for (interlaced,
// MBAFF, multi-slice with other tools, 4:2:2).
func TestH264FrameThreadsMatchSerial(t *testing.T) {
	for _, s := range h264Streams(t) {
		ref := decodeH264Raw(t, s, 0)
		if len(ref) == 0 {
			t.Fatalf("%s: no pictures", s.name)
		}
		for _, threads := range []int{2, 3, 4} {
			got := decodeH264Raw(t, s, threads)
			if len(got) != len(ref) {
				t.Fatalf("%s, %d threads: %d pictures, plain decoder %d", s.name, threads, len(got), len(ref))
			}
			for i := range ref {
				if !bytes.Equal(ref[i], got[i]) {
					t.Fatalf("%s, %d threads: picture %d differs from the plain decoder", s.name, threads, i)
				}
			}
		}
	}
}

// The fused conversion of the decoder's sample planes to RGBA equals the narrowing copy followed by the
// YCbCr conversion.
func TestFrameToRGBAMatchesTwoStep(t *testing.T) {
	n := 0
	for _, s := range h264Streams(t) {
		d := h264.NewDecoder()
		if s.avcc != nil {
			if err := d.SetAVCC(s.avcc); err != nil {
				t.Fatal(err)
			}
		}
		for _, au := range s.aus {
			fr, err := d.DecodeFrame(au)
			if err != nil || fr == nil || fr.Depth != 8 || fr.Format != yuv.F420 || fr.Width%2 != 0 || fr.Height%2 != 0 {
				continue
			}
			img, ok := fr.ToYCbCr()
			if !ok {
				continue
			}
			for _, bt709 := range []bool{false, true} {
				a, b := yuvToRGBA(img, bt709), frameToRGBA(fr, bt709)
				if !bytes.Equal(a.Pix, b.Pix) {
					t.Fatalf("%s: converted pictures differ (bt709 %v)", s.name, bt709)
				}
			}
			n++
		}
	}
	if n < 20 {
		t.Fatalf("only %d pictures compared", n)
	}
}

// Damaged streams must neither hang nor crash a decoder with frame threads (a picture that fails ends with its
// rows published, so the pictures that refer to it never wait for ever), and Reset must leave it usable.
func TestH264FrameThreadsDamagedStreams(t *testing.T) {
	defer func(f func(any, []byte)) { h264.DebugPanic = f }(h264.DebugPanic)
	h264.DebugPanic = nil // other tests print every recovered panic; damaging streams on purpose makes many
	done := make(chan struct{})
	go func() {
		defer close(done)
		seed := int64(11)
		if v, err := strconv.ParseInt(os.Getenv("SWEEF_FUZZ_SEED"), 10, 64); err == nil {
			seed = v
		}
		r := rand.New(rand.NewSource(seed))
		for _, s := range h264Streams(t) {
			for round := 0; round < 3; round++ {
				d := h264.NewDecoder()
				d.SetThreads(3)
				if s.avcc != nil {
					d.SetAVCC(s.avcc)
				}
				var frames []*yuv.Frame
				for i, au := range s.aus {
					au = append([]byte(nil), au...)
					if i > 0 && r.Intn(4) == 0 && len(au) > 40 {
						for k := 0; k < 1+r.Intn(8); k++ {
							au[8+r.Intn(len(au)-8)] ^= byte(1 << uint(r.Intn(8)))
						}
					}
					if fr, _ := d.DecodeFrame(au); fr != nil {
						frames = append(frames, fr)
					}
					if round == 2 && i == len(s.aus)/2 {
						d.Reset()
					}
				}
				for _, fr := range frames {
					fr.Wait()
				}
				d.Flush()
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("a decoder with frame threads hung on a damaged stream")
	}
}

// After Reset a decoder with frame threads decodes the same pictures again.
func TestH264FrameThreadsReset(t *testing.T) {
	for _, s := range h264Streams(t) {
		if s.name != "h264_1080p_high.mp4" && s.name != "h264_main_b_cabac.mp4" {
			continue
		}
		d := h264.NewDecoder()
		d.SetThreads(3)
		if s.avcc != nil {
			d.SetAVCC(s.avcc)
		}
		run := func() [][]byte {
			var out [][]byte
			for _, au := range s.aus {
				if fr, _ := d.DecodeFrame(au); fr != nil {
					fr.Wait()
					img, _ := fr.ToYCbCr()
					out = append(out, toYUV(img).b)
				}
			}
			return out
		}
		a := run()
		d.Reset()
		b := run()
		if len(a) != len(b) || len(a) == 0 {
			t.Fatalf("%s: %d pictures, then %d after Reset", s.name, len(a), len(b))
		}
		for i := range a {
			if !bytes.Equal(a[i], b[i]) {
				t.Fatalf("%s: picture %d differs after Reset", s.name, i)
			}
		}
	}
}
