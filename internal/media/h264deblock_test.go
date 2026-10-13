package media

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/h264"
)

// The deblocking filter run as a wavefront on several goroutines must give exactly the pictures of the
// serial filter (h264_wave_320x192.mp4 is 20x12 macroblocks, High profile, CABAC, B-frames).
func TestH264ParallelDeblockMatchesSerial(t *testing.T) {
	for _, name := range []string{"h264_wave_320x192.mp4", "h264_main_b_cabac.mp4", "h264_high_8x8_b.mp4", "h264_multislice_crop.mp4"} {
		b, err := os.ReadFile(filepath.Join(testMediaDir, name))
		if err != nil {
			b, err = os.ReadFile(filepath.Join("testdata", name))
		}
		if err != nil {
			t.Fatal(err)
		}
		info, err := ProbeBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := OpenTracks(BytesSource(b), info)
		if err != nil || v == nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		ss := v.Snapshot()
		decodeAll := func(parallel bool) [][]byte {
			h264.SetParallelDeblock(parallel)
			defer h264.SetParallelDeblock(true)
			dec, err := NewVideoDecoder(v.Config())
			if err != nil {
				t.Fatal(err)
			}
			var out [][]byte
			for _, s := range ss {
				data, _ := s.Bytes()
				pic, err := dec.Decode(data)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if pic.Valid() {
					out = append(out, append([]byte(nil), pic.RGBA().Pix...))
				}
			}
			return out
		}
		serial, par := decodeAll(false), decodeAll(true)
		if len(serial) < 8 || len(serial) != len(par) {
			t.Fatalf("%s: pictures: serial %d parallel %d", name, len(serial), len(par))
		}
		for i := range serial {
			if !bytes.Equal(serial[i], par[i]) {
				t.Fatalf("%s: picture %d differs between serial and parallel deblocking", name, i)
			}
		}
	}
}
