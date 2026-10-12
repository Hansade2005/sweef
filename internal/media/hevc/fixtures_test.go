package hevc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConformanceFixtures decodes the small committed streams in
// testdata/hevcconf (JCT-VC conformance streams and SCM/HM generated ones for
// range extension 14/16 bit, extended precision, cabac bypass alignment, SCC
// and MV-HEVC) and checks every picture against the decoded picture hash SEI
// the encoder wrote. No reference decoder is needed. Streams whose name starts
// with "mv_" are additionally decoded for every layer.
func TestConformanceFixtures(t *testing.T) {
	files, _ := filepath.Glob("../testdata/hevcconf/*.bit")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		f := f
		t.Run(strings.TrimSuffix(filepath.Base(f), ".bit"), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			layers := []int{0}
			if strings.HasPrefix(filepath.Base(f), "mv_") {
				layers = []int{0, 1}
			}
			for _, l := range layers {
				d := NewDecoder()
				d.CheckHash = true
				d.SetOutputLayer(l)
				frames, err := d.Push(data)
				frames = append(frames, d.Flush()...)
				if err != nil {
					t.Fatalf("layer %d: %v", l, err)
				}
				chk, bad := d.HashStats()
				if len(frames) == 0 || chk == 0 || bad != 0 {
					t.Fatalf("layer %d: frames=%d hashes checked=%d bad=%d", l, len(frames), chk, bad)
				}
			}
		})
	}
}
