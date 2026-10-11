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
