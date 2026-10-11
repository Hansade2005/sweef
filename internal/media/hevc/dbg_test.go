package hevc

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

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
	ref := ffmpegRaw(t, p, "yuv420p")
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
					t.Logf("frame %d ctb (%d,%d): %d bad luma samples, first at (%d,%d)", fi, cx, cy, bad, fx, fy)
					cnt++
				}
			}
		}
		break
	}
}

func TestDumpParams(t *testing.T) {
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
			t.Logf("SPS err=%v %+v", err, s)
		case 34:
			s, err := parsePPS(unescape(n[2:]))
			if s != nil {
				s.colBd, s.rowBd = nil, nil
			}
			t.Logf("PPS err=%v %+v", err, s)
		}
	}
}



func TestSaoPoint(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	type rec struct {
		pic *Picture
		pre uint16
		sp  saoParams
		nb  [4]uint16
	}
	var recs []rec
	dbgSAOHook = func(d *Decoder, pic *Picture) {
		st := pic.stride[1]
		t.Logf("poc %d left %+v up %+v cur %+v saoflags %v %v", pic.poc, d.sao[10*d.sp.wCtb+15], d.sao[9*d.sp.wCtb+16], d.sao[10*d.sp.wCtb+16], d.slices[0].saoLuma, d.slices[0].saoChroma)
		recs = append(recs, rec{pic, pic.planes[1][85*st+135], d.sao[10*d.sp.wCtb+16],
			[4]uint16{pic.planes[1][84*st+136], pic.planes[1][86*st+134], pic.planes[1][85*st+134], pic.planes[1][85*st+136]}})
	}
	frames, _ := decodeAnnexB(t, data)
	f := frames[1]
	for _, r := range recs {
		if &r.pic.planes[1][0] == &f.Planes[1][0] {
			t.Logf("poc %d pre %d post %d sao %+v nb(a=(x+1,y-1),b=(x-1,y+1),l,r) %v", r.pic.poc, r.pre, f.Planes[1][85*f.Stride[1]+135], r.sp, r.nb)
		}
	}
}

func TestDeblockPoint(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	dbgDeblock = func(pic *Picture, ci int, vertical bool, cx, cy, bs, qpL, tc int, noP, noQ bool) {
		if pic.poc == 5 && ci == 1 && vertical && cx == 136 && cy >= 84 && cy <= 94 {
			st := pic.stride[1]
			t.Logf("edge cx=%d cy=%d bs=%d qp=%d tc=%d noP=%v noQ=%v row0: %v", cx, cy, bs, qpL, tc, noP, noQ, pic.planes[1][cy*st+cx-3:cy*st+cx+3])
		}
	}
	decodeAnnexB(t, data)
}

func TestEdgePoint(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	dbgEdge = func(d *Decoder, pic *Picture, vertical bool, xq, yq int, e uint8) {
		if pic.poc == 5 && vertical && xq == 272 && yq >= 168 && yq <= 184 {
			s := d.sp
			cq := d.cuAt(xq, yq)
			cp := d.cuAt(xq-1, yq)
			t.Logf("luma edge x=%d y=%d e=%d cq=%+v cp=%+v mvfq=%+v mvfp=%+v", xq, yq, e, *cq, *cp, pic.mvf[(yq>>2)*pic.w4+xq>>2], pic.mvf[(yq>>2)*pic.w4+(xq-1)>>2])
			_ = s
		}
	}
	decodeAnnexB(t, data)
}

func TestListBad(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	ref := ffmpegRaw(t, p, "yuv420p")
	frames, _ := decodeAnnexB(t, data)
	fi := 5
	f := frames[fi]
	fb := frameBytes(f)
	r := ref[fi*len(fb) : (fi+1)*len(fb)]
	w, h := f.Width, f.Height
	cnt := 0
	for i := range fb {
		if fb[i] != r[i] {
			pl, x, y := "Y", i%w, i/w
			if i >= w*h {
				j := i - w*h
				pl = "U"
				if j >= f.CWidth*f.CHeight {
					j -= f.CWidth * f.CHeight
					pl = "V"
				}
				x, y = j%f.CWidth, j/f.CWidth
			}
			if cnt < 40 {
				t.Logf("%s (%d,%d) ff=%d mine=%d", pl, x, y, r[i], fb[i])
			}
			cnt++
		}
	}
	t.Log("total", cnt)
}

func TestSaoRow(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	dbgSAOHook = func(d *Decoder, pic *Picture) {
		if pic.poc != 5 {
			return
		}
		for x := 15; x <= 17; x++ {
			t.Logf("ctb(%d,11) %+v", x, d.sao[11*d.sp.wCtb+x])
		}
		t.Logf("slices %d: %+v", len(d.slices), d.slices[0].entry)
	}
	decodeAnnexB(t, data)
}

func TestPerFrame(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	ref := ffmpegRaw(t, p, "yuv420p")
	var order []int
	dbgSAOHook = func(d *Decoder, pic *Picture) { order = append(order, pic.poc) }
	frames, _ := decodeAnnexB(t, data)
	for fi, f := range frames {
		fb := frameBytes(f)
		r := ref[fi*len(fb) : (fi+1)*len(fb)]
		w, h := f.Width, f.Height
		cnt, cl := 0, 0
		for i := range fb {
			if fb[i] != r[i] {
				cnt++
				if i < w*h {
					cl++
				}
			}
		}
		t.Logf("frame %d: %d bad samples (luma %d)", fi, cnt, cl)
	}
	t.Log("decode order (poc):", order)
}

func TestSaoSample(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	dbgSAOHook = func(d *Decoder, pic *Picture) {
		if pic.poc != 5 {
			return
		}
		st := pic.stride[1]
		for y := 87; y <= 91; y++ {
			t.Logf("row %d: %v", y, pic.planes[1][y*st+130:y*st+140])
		}
	}
	decodeAnnexB(t, data)
}
