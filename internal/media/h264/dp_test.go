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
	out, n, _ := partitionStreamCI(t, es, mode)
	return out, n
}

// partitionStreamCI is partitionStream for streams that may use
// constrained_intra_pred_flag: the coeff_token of the blocks of intra
// macroblocks are re-coded for the nC derivation of partitioned slices
// (inter neighbours count as 0), using an independent model of the
// neighbour derivation. It also returns the number of tokens re-coded.
func partitionStreamCI(t testing.TB, es []byte, mode dpDrop) ([]byte, int, int) {
	model := &dpModel{}
	recoded := 0
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
		var spans []spanRec
		var blocks []dpBlock
		dpBlockTrace = func(b dpBlock) { blocks = append(blocks, b) }
		dpTrace = func(s dpSpan) {
			spans = append(spans, spanRec{s, blocks})
			blocks = nil
		}
		d.handleNAL(n)
		dpTrace, dpBlockTrace = nil, nil
		model.beginSlice(h)
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
			if s.intra && !s.pcm && h.pps.ConstrainedIntra {
				recoded += model.recode(t, w, rbsp, &s, h, true)
			} else {
				if !s.pcm {
					model.recode(t, nil, rbsp, &s, h, false)
				} else {
					model.pcm(&s, h)
				}
				w.copyBits(rbsp, s.start, s.end)
			}
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
	return out, conv, recoded
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
		mod, n, rec := partitionStreamCI(t, es, mode)
		if n == 0 {
			t.Skip("no non-IDR slices")
		}
		if hasConstrainedIntra(es) {
			t.Logf("mode %d: %d coeff_tokens re-coded for the partitioned nC derivation", mode, rec)
			if rec == 0 {
				t.Errorf("constrained_intra_pred stream without any token affected by the nC rule: the test checks nothing")
			}
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
		"ci_p":       {"-profile:v", "baseline", "-x264-params", "keyint=30:ref=2:slices=3:bframes=0:constrained-intra=1"},
		"ci_b":       {"-profile:v", "main", "-x264-params", "cabac=0:keyint=40:ref=3:bframes=3:constrained-intra=1:scenecut=60"},
	}
	for name, args := range cases {
		args := args
		name := name
		t.Run(name, func(t *testing.T) {
			src := "mandelbrot=rate=15:size=192x112"
			if strings.HasPrefix(name, "ci_") {
				// busy content: intra macroblocks in P/B pictures next to coded inter ones
				src = "testsrc2=rate=15:size=192x112,noise=alls=30:allf=t"
			}
			dir := t.TempDir()
			out := filepath.Join(dir, "a.264")
			full := append([]string{"-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", src, "-t", "2", "-c:v", "libx264", "-pix_fmt", "yuv420p"}, args...)
			full = append(full, out)
			if b, err := exec.Command("ffmpeg", full...).CombinedOutput(); err != nil {
				t.Skipf("ffmpeg: %v %s", err, b)
			}
			es, _ := os.ReadFile(out)
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

type spanRec struct {
	dpSpan
	blocks []dpBlock
}

// dpModel is an independent model of the CAVLC nC derivation (9.2.1) for
// progressive 4:2:0 frames with slices in raster order.
type dpModel struct {
	mbs        map[int]*[3][16]int
	intra      map[int]bool
	sliceFirst int
}

func (m *dpModel) beginSlice(h *sliceHeader) {
	if m.mbs == nil || h.firstMB == 0 {
		m.mbs = map[int]*[3][16]int{}
		m.intra = map[int]bool{}
	}
	m.sliceFirst = h.firstMB
}

func (m *dpModel) pcm(s *spanRec, h *sliceHeader) {
	addr := s.mby*h.sps.MbW + s.mbx
	m.intra[addr] = true
	var tot [3][16]int
	for c := range tot {
		for i := range tot[c] {
			tot[c][i] = 16
		}
	}
	m.mbs[addr] = &tot
}

// nC derives nC of the block at raster position (bx,by) of component comp
// (4 blocks wide for luma, 2 for chroma) of macroblock addr.
func (m *dpModel) nC(cur *[3][16]int, w *dpModelCtx, comp, bx, by int, intraRule bool) int {
	wb := 4
	if comp > 0 {
		wb = 2
	}
	get := func(mbAddr int, x, y int) (int, bool) {
		if mbAddr < m.sliceFirst || mbAddr < 0 {
			return 0, false
		}
		t, ok := m.mbs[mbAddr]
		if !ok {
			return 0, true // skipped or without residual: no coefficients
		}
		if intraRule && !w.intra[mbAddr] {
			return 0, true // inter neighbour of an intra macroblock in a partitioned slice
		}
		return t[comp][y*wb+x], true
	}
	var na, nb int
	var aok, bok bool
	if bx > 0 {
		na, aok = cur[comp][by*wb+bx-1], true
	} else if w.mbx > 0 {
		na, aok = get(w.addr-1, wb-1, by)
	}
	if by > 0 {
		nb, bok = cur[comp][(by-1)*wb+bx], true
	} else if w.addr-w.mbw >= 0 {
		nb, bok = get(w.addr-w.mbw, bx, wb-1)
	}
	switch {
	case aok && bok:
		return (na + nb + 1) >> 1
	case aok:
		return na
	case bok:
		return nb
	}
	return 0
}

type dpModelCtx struct {
	addr, mbx, mbw int
	intra          map[int]bool
}

// recode walks the blocks of one macroblock: it checks the model against the
// decoder's nC, stores the block totals and, when out is not nil, copies the
// span to out with the coeff_token of every block coded for the nC of a
// partitioned slice. It returns the number of tokens that changed.
func (m *dpModel) recode(t testing.TB, out *bitWriter, rbsp []byte, s *spanRec, h *sliceHeader, intraSpan bool) int {
	mbw := h.sps.MbW
	addr := s.mby*mbw + s.mbx
	ctx := &dpModelCtx{addr: addr, mbx: s.mbx, mbw: mbw, intra: m.intra}
	type id struct {
		comp, idx int
		dc        bool
	}
	var ids []id
	if s.i16 {
		ids = append(ids, id{0, 0, true})
	}
	for b8 := 0; b8 < 4; b8++ {
		if s.cbp&(1<<uint(b8)) != 0 {
			for k := 0; k < 4; k++ {
				ids = append(ids, id{0, b8*4 + k, false})
			}
		}
	}
	if s.cbp>>4 != 0 {
		ids = append(ids, id{1, 0, true}, id{2, 0, true})
	}
	if s.cbp>>4 == 2 {
		for c := 1; c <= 2; c++ {
			for b := 0; b < 4; b++ {
				ids = append(ids, id{c, b, false})
			}
		}
	}
	if len(ids) != len(s.blocks) {
		t.Fatalf("mb %d: %d blocks expected, %d parsed", addr, len(ids), len(s.blocks))
	}
	m.intra[addr] = s.intra
	var cur [3][16]int
	changed := 0
	pos := s.start
	for i, bl := range s.blocks {
		b := ids[i]
		var nOrig, nDP int
		switch {
		case b.comp > 0 && b.dc:
			nOrig, nDP = -1, -1
		default:
			bx, by := 0, 0
			if b.comp == 0 {
				if !b.dc {
					bx, by = blkX[b.idx], blkY[b.idx]
				}
			} else {
				bx, by = b.idx&1, b.idx>>1
			}
			nOrig = m.nC(&cur, ctx, b.comp, bx, by, false)
			nDP = m.nC(&cur, ctx, b.comp, bx, by, true)
			if !b.dc {
				wb := 4
				if b.comp > 0 {
					wb = 2
				}
				cur[b.comp][by*wb+bx] = bl.total
			}
		}
		if nOrig != bl.nC {
			t.Fatalf("mb %d block %d: model nC %d, decoder %d", addr, i, nOrig, bl.nC)
		}
		if out != nil {
			class := func(n int) int {
				switch {
				case n < 0:
					return -1
				case n < 2:
					return 0
				case n < 4:
					return 1
				case n < 8:
					return 2
				}
				return 3
			}
			out.copyBits(rbsp, pos, bl.start)
			pos = bl.tokEnd
			if class(nDP) == class(nOrig) {
				out.copyBits(rbsp, bl.start, bl.tokEnd)
			} else {
				changed++
				k := class(nDP)
				sym := bl.total<<2 | bl.t1
				var l, c int
				if k == 3 {
					l = 6
					if bl.total == 0 {
						c = 3
					} else {
						c = (bl.total-1)<<2 | bl.t1
					}
				} else {
					l, c = int(coeffTokenLen[k][sym]), int(coeffTokenBits[k][sym])
				}
				for j := l - 1; j >= 0; j-- {
					out.bit(c >> uint(j) & 1)
				}
			}
		}
	}
	if out != nil {
		out.copyBits(rbsp, pos, s.end)
	}
	m.mbs[addr] = &cur
	return changed
}
