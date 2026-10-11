package h264

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Data partitioning evidence. ffmpeg (6.0 and later) has no data
// partitioning support, so the reference is the decoder's own output for the
// unpartitioned stream: an ordinary CAVLC stream is rewritten at the
// bitstream level into slice data partitions A/B/C (the residual bits of every
// macroblock move, unchanged, into partition B for intra and C for inter
// macroblocks) and must decode to identical pictures. This is only an
// identity for streams without constrained_intra_pred (with it the nC
// derivation of partitioned slices differs, 9.2.1).

type bitWriter struct {
	b []byte
	n int
}

func (w *bitWriter) bit(v int) {
	if w.n&7 == 0 {
		w.b = append(w.b, 0)
	}
	if v != 0 {
		w.b[len(w.b)-1] |= 0x80 >> uint(w.n&7)
	}
	w.n++
}

func (w *bitWriter) copyBits(src []byte, from, to int) {
	for i := from; i < to; i++ {
		w.bit(int(src[i>>3] >> (7 - uint(i&7)) & 1))
	}
}

func (w *bitWriter) ue(v int) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	for i := 0; i < n; i++ {
		w.bit(0)
	}
	for i := n; i >= 0; i-- {
		w.bit(v >> uint(i) & 1)
	}
}

func (w *bitWriter) align() {
	for w.n&7 != 0 {
		w.bit(0)
	}
}

func (w *bitWriter) trailing() {
	w.bit(1)
	w.align()
}

func escapeRBSP(b []byte) []byte {
	var out []byte
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

type dpDrop int

const (
	dpKeepAll dpDrop = iota
	dpDropB
	dpDropC
	dpDropA
	dpEmptyParts // emit empty B and C partitions too
)

// partitionStream rewrites the non-IDR slices of a CAVLC stream into data
// partitions. It reports the number of slices converted.
func partitionStream(t testing.TB, es []byte, mode dpDrop) ([]byte, int) {
	d := NewDecoder()
	var out []byte
	conv := 0
	sliceID := 0
	emit := func(hdr byte, rbsp []byte) {
		out = append(out, 0, 0, 0, 1, hdr)
		out = append(out, escapeRBSP(rbsp)...)
	}
	for _, n := range splitAnnexB(es) {
		if len(n) == 0 {
			continue
		}
		typ := int(n[0] & 31)
		if typ != 1 {
			d.handleNAL(n)
			out = append(out, 0, 0, 0, 1)
			out = append(out, n...)
			continue
		}
		rbsp := unescape(n[1:])
		r := newBitReader(rbsp)
		h, err := d.parseSliceHeader(r, 1, int(n[0]>>5)&3)
		if err != nil || h.pps.CABAC {
			t.Fatalf("slice header: %v cabac=%v", err, h != nil && h.pps.CABAC)
		}
		hdrEnd := r.pos
		last := r.lst
		var spans []dpSpan
		dpTrace = func(s dpSpan) { spans = append(spans, s) }
		d.handleNAL(n)
		dpTrace = nil
		a, b, c := &bitWriter{}, &bitWriter{}, &bitWriter{}
		a.copyBits(rbsp, 0, hdrEnd)
		a.ue(sliceID & 7)
		b.ue(sliceID & 7)
		c.ue(sliceID & 7)
		if h.pps.RedundantPicCnt {
			b.ue(0)
			c.ue(0)
		}
		pos := hdrEnd
		nb, nc := 0, 0
		for _, s := range spans {
			cut := s.start
			if s.pcm {
				cut = s.pre
			}
			a.copyBits(rbsp, pos, cut)
			pos = s.end
			w := c
			if s.intra {
				w = b
				nb++
				if s.pcm {
					w.align()
				}
			} else {
				nc++
			}
			w.copyBits(rbsp, s.start, s.end)
		}
		a.copyBits(rbsp, pos, last)
		a.trailing()
		b.trailing()
		c.trailing()
		if mode != dpDropA {
			emit(n[0]&0x60|2, a.b)
		}
		if (nb > 0 || mode == dpEmptyParts) && mode != dpDropB {
			emit(n[0]&0x60|3, b.b)
		}
		if (nc > 0 || mode == dpEmptyParts) && mode != dpDropC {
			emit(n[0]&0x60|4, c.b)
		}
		sliceID++
		conv++
	}
	return out, conv
}

func decodeAll(t testing.TB, es []byte) [][]uint16 {
	d := NewDecoder()
	var frames [][]uint16
	for _, au := range SplitAccessUnits(es) {
		fr, _ := d.DecodeFrame(au)
		if fr == nil {
			continue
		}
		var f []uint16
		for p := 0; p < 3; p++ {
			if fr.Planes[p] == nil {
				continue
			}
			w, h := fr.Width, fr.Height
			if p > 0 {
				w, h = fr.CWidth, fr.CHeight
			}
			for y := 0; y < h; y++ {
				f = append(f, fr.Planes[p][y*fr.Stride[p]:y*fr.Stride[p]+w]...)
			}
		}
		frames = append(frames, f)
	}
	return frames
}

func equalFrames(a, b [][]uint16) (int, bool) {
	if len(a) != len(b) {
		return -1, false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return i, false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return i, false
			}
		}
	}
	return 0, true
}

func hasConstrainedIntra(es []byte) bool {
	d := NewDecoder()
	for _, n := range splitAnnexB(es) {
		if len(n) > 0 && n[0]&31 == 7 {
			if s, err := parseSPS(unescape(n[1:])); err == nil {
				d.setSPS(s)
			}
		}
		if len(n) > 0 && n[0]&31 == 8 {
			if p, err := parsePPS(unescape(n[1:]), &d.sps); err == nil && p.ConstrainedIntra {
				return true
			}
		}
	}
	return false
}

func checkPartitioned(t *testing.T, es []byte) {
	t.Helper()
	want := decodeAll(t, es)
	if len(want) == 0 {
		t.Fatal("no frames from the original")
	}
	for _, mode := range []dpDrop{dpKeepAll, dpEmptyParts} {
		mod, n := partitionStream(t, es, mode)
		if n == 0 {
			t.Skip("no non-IDR slices")
		}
		got := decodeAll(t, mod)
		if i, ok := equalFrames(want, got); !ok {
			t.Fatalf("mode %d: partitioned stream differs from the original (frame %d; %d vs %d frames)", mode, i, len(want), len(got))
		}
	}
	// Lost partitions must neither crash nor lose pictures.
	for _, mode := range []dpDrop{dpDropB, dpDropC, dpDropA} {
		mod, _ := partitionStream(t, es, mode)
		got := decodeAll(t, mod)
		if mode != dpDropA && len(got) != len(want) {
			t.Errorf("mode %d: %d frames, want %d", mode, len(got), len(want))
		}
		if _, same := equalFrames(want, got); same && mode != dpDropA {
			t.Logf("mode %d: dropping the partitions changed nothing (no such residual?)", mode)
		}
	}
}

func TestDataPartitioningX264(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	cases := map[string][]string{
		"p":          {"-profile:v", "baseline", "-x264-params", "keyint=30:ref=2:slices=3:bframes=0"},
		"b":          {"-profile:v", "main", "-x264-params", "cabac=0:keyint=30:ref=3:bframes=3:weightp=2:b-pyramid=normal"},
		"interlaced": {"-profile:v", "main", "-flags", "+ildct+ilme", "-x264-params", "cabac=0:keyint=30:ref=2:bframes=2:interlaced=1"},
		"8x8":        {"-profile:v", "high", "-x264-params", "cabac=0:keyint=30:ref=2:bframes=2:8x8dct=1"},
	}
	for name, args := range cases {
		args := args
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "a.264")
			full := append([]string{"-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "mandelbrot=rate=15:size=192x112", "-t", "2", "-c:v", "libx264", "-pix_fmt", "yuv420p"}, args...)
			full = append(full, src)
			if b, err := exec.Command("ffmpeg", full...).CombinedOutput(); err != nil {
				t.Skipf("ffmpeg: %v %s", err, b)
			}
			es, _ := os.ReadFile(src)
			checkPartitioned(t, es)
		})
	}
}

// TestDataPartitioningConformance runs the rewrite over the CAVLC streams of
// a conformance directory (SWEEF_H264_CONF).
func TestDataPartitioningConformance(t *testing.T) {
	root := os.Getenv("SWEEF_H264_CONF")
	if root == "" {
		t.Skip("SWEEF_H264_CONF not set")
	}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".264", ".jsv", ".avc", ".26l", ".jvt", ".h264":
		default:
			return nil
		}
		t.Run(filepath.Base(p), func(t *testing.T) {
			es, _ := os.ReadFile(p)
			aus := SplitAccessUnits(es)
			if len(aus) > 12 {
				aus = aus[:12]
			}
			es = bytes.Join(aus, nil)
			if hasConstrainedIntra(es) {
				t.Skip("constrained_intra_pred")
			}
			d := NewDecoder()
			for _, n := range splitAnnexB(es) {
				if len(n) > 0 && n[0]&31 == 8 {
					if pp, err := parsePPS(unescape(n[1:]), &d.sps); err == nil && pp.CABAC {
						t.Skip("CABAC")
					}
				}
				if len(n) > 0 && n[0]&31 == 7 {
					if s, err := parseSPS(unescape(n[1:])); err == nil {
						d.setSPS(s)
					}
				}
			}
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic %v", r)
				}
			}()
			checkPartitioned(t, es)
		})
		return nil
	})
}
