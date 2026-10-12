package hevc

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// Debug helpers driven by environment variables (skipped otherwise).

func TestFile(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	pf := os.Getenv("HEVC_PIXFMT")
	if pf == "" {
		pf = "yuv420p"
	}
	compareFile(t, p, pf)
}

func TestFirstBad(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	ctb := 32
	if v := os.Getenv("HEVC_CTB"); v != "" {
		fmt.Sscan(v, &ctb)
	}
	data, _ := os.ReadFile(p)
	pf := os.Getenv("HEVC_PIXFMT")
	if pf == "" {
		pf = "yuv420p"
	}
	ref := ffmpegRaw(t, p, pf)
	frames, err := decodeAnnexB(t, data)
	t.Log("err", err)
	for fi, f := range frames {
		fb := frameBytes(f)
		r := ref[fi*len(fb) : (fi+1)*len(fb)]
		if bytes.Equal(fb, r) {
			continue
		}
		w, h := f.Width, f.Height
		if os.Getenv("HEVC_PLANE") == "U" {
			off := w * h
			fb, r = fb[off:], r[off:]
			w, h = f.CWidth, f.CHeight
		}
		cnt := 0
		for cy := 0; cy*ctb < h && cnt < 6; cy++ {
			for cx := 0; cx*ctb < w && cnt < 6; cx++ {
				bad := 0
				fx, fy := -1, -1
				for y := cy * ctb; y < (cy+1)*ctb && y < h; y++ {
					for x := cx * ctb; x < (cx+1)*ctb && x < w; x++ {
						if fb[y*w+x] != r[y*w+x] {
							bad++
							if fx < 0 {
								fx, fy = x, y
							}
						}
					}
				}
				if bad > 0 {
					t.Logf("frame %d ctb (%d,%d): %d bad samples, first at (%d,%d)", fi, cx, cy, bad, fx, fy)
					cnt++
				}
			}
		}
		break
	}
}

func TestTUAt(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	var x, y, r int
	fmt.Sscan(os.Getenv("HEVC_X"), &x)
	fmt.Sscan(os.Getenv("HEVC_Y"), &y)
	fmt.Sscan(os.Getenv("HEVC_R"), &r)
	data, _ := os.ReadFile(p)
	n := 0
	dbgTU = func(d *Decoder, x0, y0, log2, blkIdx int, cbfL bool, cb, cr [2]bool) {
		if n == 0 && x0+(1<<uint(log2)) > x-r && x0 < x+r && y0+(1<<uint(log2)) > y-r && y0 < y+r {
			cu := &d.cu
			t.Logf("TU luma (%d,%d) log2=%d blk=%d cbfL=%v cb=%v cr=%v | CU (%d,%d) log2=%d intra=%v part=%d chromaMode=%v syn=%v lumaMode=%d", x0, y0, log2, blkIdx, cbfL, cb, cr, cu.x0, cu.y0, cu.log2, cu.intra, cu.partMode, cu.chromaMode, cu.chromaModeSyn, d.ipm[(y0>>2)*d.sp.wTb4+x0>>2])
		}
	}
	decodeAnnexB(t, data)
}

func TestDump(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	var x, y, plane, w, h int
	fmt.Sscan(os.Getenv("HEVC_X"), &x)
	fmt.Sscan(os.Getenv("HEVC_Y"), &y)
	fmt.Sscan(os.Getenv("HEVC_PLANE_N"), &plane)
	fmt.Sscan(os.Getenv("HEVC_W"), &w)
	fmt.Sscan(os.Getenv("HEVC_H"), &h)
	pf := os.Getenv("HEVC_PIXFMT")
	if pf == "" {
		pf = "yuv420p"
	}
	data, _ := os.ReadFile(p)
	ref := ffmpegRaw(t, p, pf)
	frames, _ := decodeAnnexB(t, data)
	fi := 0
	fmt.Sscan(os.Getenv("HEVC_FRAME"), &fi)
	f := frames[fi]
	fb := frameBytes(f)
	r := ref[fi*len(fb) : (fi+1)*len(fb)]
	off := 0
	pw := f.Width
	if plane >= 1 {
		off = f.Width * f.Height
		pw = f.CWidth
	}
	if plane == 2 {
		off += f.CWidth * f.CHeight
	}
	for j := 0; j < h; j++ {
		var a, b []int
		for i := 0; i < w; i++ {
			a = append(a, int(r[off+(y+j)*pw+x+i]))
			b = append(b, int(fb[off+(y+j)*pw+x+i]))
		}
		t.Logf("row %d ffmpeg %v mine %v", y+j, a, b)
	}
}

func TestDumpFlags(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	for _, n := range splitAnnexB(data) {
		typ := int(n[0]>>1) & 63
		switch typ {
		case 33:
			s, err := parseSPS(unescape(n[2:]))
			if err != nil {
				t.Log("SPS err", err)
				continue
			}
			t.Logf("SPS cf=%d bd=%d/%d ctb=%d mincb=%d tb=%d..%d amp=%v sao=%v pcm=%v scal=%v strong=%v tsRot=%v tsCtx=%v implRdpcm=%v explRdpcm=%v intraSmoothOff=%v highPrec=%v persRice=%v extprec=%v align=%v", s.chromaFormat, s.bitDepthY, s.bitDepthC, s.log2Ctb, s.log2MinCb, s.log2MinTb, s.log2MaxTb, s.amp, s.sao, s.pcm, s.scalingEnabled, s.strongIntra, s.tsRotation, s.tsContext, s.implicitRdpcm, s.explicitRdpcm, s.intraSmoothingOff, s.highPrecOffsets, s.persistentRice, s.extPrecision, s.cabacBypassAlign)
		case 34:
			s, err := parsePPS(unescape(n[2:]))
			if err != nil {
				t.Log("PPS err", err)
				continue
			}
			t.Logf("PPS tskip=%v log2maxts=%d cuQpDelta=%v depth=%d signhide=%v tqbypass=%v wpp=%v tiles=%v cross=%v cqpoff=%v wp=%v/%v constrained=%v", s.transformSkip, s.log2MaxTsSize, s.cuQpDelta, s.diffCuQpDeltaDepth, s.signHiding, s.transquantBypass, s.wpp, s.tiles, s.crossComponentPred, s.chromaQpOffsetList, s.weightedPred, s.weightedBi, s.constrainedIntra)
		}
	}
}

func BenchmarkDecode(b *testing.B) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		b.Skip()
	}
	data, _ := os.ReadFile(p)
	b.ResetTimer()
	n := 0
	for i := 0; i < b.N; i++ {
		d := NewDecoder()
		fr, _ := d.Push(data)
		fr = append(fr, d.Flush()...)
		n += len(fr)
	}
	b.ReportMetric(float64(n)/b.Elapsed().Seconds(), "fps")
}

// TestGenDebug decodes a saved generated stream and reports transform blocks
// near the first mismatching sample (luma plane of frame 0 in coding order).
func TestGenDebug(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	pf := os.Getenv("HEVC_PIXFMT")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	ref, err := ffmpegRaw1(t, p, pf)
	t.Log("ffmpeg err:", err)
	frames, _ := decodeAnnexB(t, data)
	for fi, f := range frames {
		fb := frameBytes(f)
		if (fi+1)*len(fb) > len(ref) {
			break
		}
		r := ref[fi*len(fb) : (fi+1)*len(fb)]
		if bytes.Equal(fb, r) {
			continue
		}
		bps := 1
		if f.Depth > 8 {
			bps = 2
		}
		// first diff
		for i := range fb {
			if fb[i] != r[i] {
				pos := i / bps
				plane, x, y := 0, pos%f.Width, pos/f.Width
				if pos >= f.Width*f.Height {
					pos -= f.Width * f.Height
					plane = 1
					if f.CWidth > 0 && pos >= f.CWidth*f.CHeight {
						pos -= f.CWidth * f.CHeight
						plane = 2
					}
					x, y = pos%f.CWidth, pos/f.CWidth
				}
				t.Logf("frame %d first diff plane %d (%d,%d) ffmpeg %d native %d", fi, plane, x, y, r[i], fb[i])
				// list the TBs that cover the position (decode order of that frame unknown: print all in first picture)
				break
			}
		}
		break
	}
}

func TestTBList(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	var x, y, r int
	fmt.Sscan(os.Getenv("HEVC_X"), &x)
	fmt.Sscan(os.Getenv("HEVC_Y"), &y)
	fmt.Sscan(os.Getenv("HEVC_R"), &r)
	data, _ := os.ReadFile(p)
	n := 0
	_ = n
	dbgTB = func(d *Decoder, c, xC, yC, log2 int, ti *tbInfo, intra bool, mode int) {
		if c == 0 && (os.Getenv("HEVC_ALL") != "" && ti.ts && d.cur.poc == 0 || xC <= x && x < xC+(1<<uint(log2)) && yC <= y && y < yC+(1<<uint(log2))) {
			t.Logf("poc %d TB c%d (%d,%d) log2=%d ts=%v intra=%v mode=%d bypass=%v nnz=%d max=%d,%d", d.cur.poc, c, xC, yC, log2, ti.ts, intra, mode, d.cu.bypass, ti.nnz, ti.maxX, ti.maxY)
		}
	}
	decodeAnnexB(t, data)
}

func TestPlaneDiffs(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	pf := os.Getenv("HEVC_PIXFMT")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	ref, _ := ffmpegRaw1(t, p, pf)
	frames, _ := decodeAnnexB(t, data)
	f := frames[0]
	fb := frameBytes(f)
	r := ref[:len(fb)]
	bps := 1
	if f.Depth > 8 {
		bps = 2
	}
	off := 0
	for pl := 0; pl < 3; pl++ {
		w, h := f.Width, f.Height
		if pl > 0 {
			w, h = f.CWidth, f.CHeight
		}
		cnt := 0
		first := -1
		for i := 0; i < w*h*bps; i++ {
			if fb[off+i] != r[off+i] {
				if first < 0 {
					first = i / bps
				}
				cnt++
			}
		}
		if cnt > 0 {
			t.Logf("plane %d: %d differing bytes, first at (%d,%d)", pl, cnt, first%w, first/w)
		}
		off += w * h * bps
	}
}
