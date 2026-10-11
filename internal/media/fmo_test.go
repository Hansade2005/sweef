package media

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/h264"
)

type ppsBits struct {
	b []byte
	n int
}

func (w *ppsBits) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n&7 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>uint(i)&1 != 0 {
			w.b[len(w.b)-1] |= 0x80 >> uint(w.n&7)
		}
		w.n++
	}
}

func (w *ppsBits) ue(v int) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	w.put(0, n)
	w.put(v, n+1)
}

type ppsReader struct {
	b []byte
	n int
}

func (r *ppsReader) bit() int {
	v := int(r.b[r.n>>3] >> (7 - uint(r.n&7)) & 1)
	r.n++
	return v
}

func (r *ppsReader) ue() int {
	z := 0
	for r.bit() == 0 {
		z++
	}
	v := 1
	for i := 0; i < z; i++ {
		v = v<<1 | r.bit()
	}
	return v - 1
}

func unescapeNAL(b []byte) []byte {
	var out []byte
	for i := 0; i < len(b); i++ {
		if i >= 2 && b[i] == 3 && b[i-1] == 0 && b[i-2] == 0 && i+1 < len(b) {
			continue
		}
		out = append(out, b[i])
	}
	return out
}

func escapeNAL(b []byte) []byte {
	var out []byte
	z := 0
	for _, c := range b {
		if z >= 2 && c <= 3 {
			out = append(out, 3)
			z = 0
		}
		out = append(out, c)
		if c == 0 {
			z++
		} else {
			z = 0
		}
	}
	return out
}

// withSliceGroups rewrites every PPS of an Annex B stream to declare two
// slice groups of the given type in which group 0 holds every macroblock, so
// the decoded pictures must not change.
func withSliceGroups(es []byte, kind string, nmb int) []byte {
	var out []byte
	for _, n := range splitNALs(es) {
		if n[0]&31 == 8 {
			rb := unescapeNAL(n[1:])
			r := &ppsReader{b: rb}
			w := &ppsBits{}
			w.ue(r.ue())
			w.ue(r.ue())
			w.put(r.bit(), 1)
			w.put(r.bit(), 1)
			if r.ue() != 0 {
				panic("pps already has slice groups")
			}
			w.ue(1)
			switch kind {
			case "type0":
				w.ue(0)
				w.ue(nmb - 1)
				w.ue(0)
			case "type2":
				w.ue(2)
				w.ue(0)
				w.ue(nmb - 1)
			case "type6":
				w.ue(6)
				w.ue(nmb - 1)
				for i := 0; i < nmb; i++ {
					w.put(0, 1)
				}
			}
			// copy the rest up to and including the stop bit
			last := len(rb)*8 - 1
			for last > r.n && rb[last>>3]>>(7-uint(last&7))&1 == 0 {
				last--
			}
			for r.n <= last {
				w.put(r.bit(), 1)
			}
			n = append([]byte{n[0]}, escapeNAL(w.b)...)
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

func splitNALs(es []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+2 < len(es); i++ {
		if es[i] == 0 && es[i+1] == 0 && es[i+2] == 1 {
			if start >= 0 {
				e := i
				for e > start && es[e-1] == 0 {
					e--
				}
				out = append(out, es[start:e])
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 {
		out = append(out, es[start:])
	}
	return out
}

// TestH264SliceGroupParsing checks the slice group machinery (PPS parsing,
// map generation, macroblock iteration) on a degenerate configuration: group
// 0 covers the whole picture, so the output must equal the plain stream. FMO
// proper cannot be checked against ffmpeg, which does not implement it.
func TestH264SliceGroupParsing(t *testing.T) {
	skipNoFF(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "a.264")
	cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "mandelbrot=rate=15:size=192x112", "-t", "1",
		"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-x264-params", "keyint=30:ref=2:slices=3:bframes=0", src)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	es, _ := os.ReadFile(src)
	want, err := decodeES(t, h264.SplitAccessUnits(es))
	if err != nil || len(want) == 0 {
		t.Fatal(err)
	}
	for _, kind := range []string{"type0", "type2", "type6"} {
		t.Run(kind, func(t *testing.T) {
			mod := withSliceGroups(es, kind, 12*7)
			got, err := decodeES(t, h264.SplitAccessUnits(mod))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("%d frames, want %d", len(got), len(want))
			}
			for i := range got {
				if !bytes.Equal(got[i], want[i]) {
					t.Fatalf("frame %d differs", i)
				}
			}
		})
	}
}
