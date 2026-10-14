package media

import (
	"bytes"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/vp9"
)

// Tile columns decoded on several goroutines must give the same pictures as decoding them one after
// the other (vp9_tiles.webm has 2 tile columns and 2 tile rows).
func TestVP9ParallelTilesMatchSequential(t *testing.T) {
	b := load(t, "vp9_tiles.webm")
	info, err := ProbeBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := OpenTracks(BytesSource(b), info)
	if err != nil || v == nil {
		t.Fatalf("open: %v", err)
	}
	ss := v.Snapshot()
	decodeAll := func(parallel bool) [][]byte {
		vp9.SetTileParallel(parallel)
		defer vp9.SetTileParallel(true)
		dec, err := NewVideoDecoder(v.Config())
		if err != nil {
			t.Fatal(err)
		}
		var out [][]byte
		for _, s := range ss {
			data, _ := s.Bytes()
			pic, err := dec.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if pic.Valid() {
				out = append(out, append([]byte(nil), pic.RGBA().Pix...))
			}
		}
		return out
	}
	seq, par := decodeAll(false), decodeAll(true)
	if len(seq) < 15 || len(seq) != len(par) {
		t.Fatalf("pictures: sequential %d parallel %d", len(seq), len(par))
	}
	for i := range seq {
		if !bytes.Equal(seq[i], par[i]) {
			t.Fatalf("picture %d differs between sequential and parallel tile decoding", i)
		}
	}
}
