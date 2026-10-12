package hevc

import (
	"fmt"
	"os"
	"testing"
)

func TestFirstErr(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	d := NewDecoder()
	d.CheckHash = true
	for _, n := range splitAnnexB(data) {
		err := d.handleNAL(n)
		typ := int(n[0]>>1) & 63
		if typ < 32 {
			t.Logf("nal %d err=%v ctbs=%d size=%d", typ, err, d.decodedCtbs, len(n))
		}
	}
	d.finishPicture()
	t.Log(d.HashStats())
}

func TestSliceTypes(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	d := NewDecoder()
	cnt := map[int]int{}
	for _, n := range splitAnnexB(data) {
		typ := int(n[0]>>1) & 63
		if typ < 32 && int(n[0]&1)<<5|int(n[1]>>3) == 0 {
			r := &bitReader{b: unescape(n[2:])}
			sh, err := d.parseSliceHeader(r, typ, 0, d.lastIndep, 0)
			if err == nil {
				cnt[sh.sliceType]++
				d.lastIndep = sh
			}
		}
		d.handleParamNAL(n)
	}
	t.Logf("slice types B/P/I: %v", cnt)
}

// TestDumpYUV writes the decoded frames (frameBytes layout) to HEVC_OUT.
func TestDumpYUV(t *testing.T) {
	p, o := os.Getenv("HEVC_FILE"), os.Getenv("HEVC_OUT")
	if p == "" || o == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	frames, err := decodeAnnexB(t, data)
	var out []byte
	for _, f := range frames {
		out = append(out, frameBytes(f)...)
	}
	os.WriteFile(o, out, 0o644)
	t.Log(len(frames), err)
}

// TestLayerErr prints the NAL units whose handling failed for HEVC_LAYER.
func TestLayerErr(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	var layer int
	fmt.Sscan(os.Getenv("HEVC_LAYER"), &layer)
	data, _ := os.ReadFile(p)
	d := NewDecoder()
	d.CheckHash = true
	d.SetOutputLayer(layer)
	cnt := 0
	for i, n := range splitAnnexB(data) {
		err := d.handleNAL(n)
		typ := int(n[0]>>1) & 63
		l := int(n[0]&1)<<5 | int(n[1]>>3)
		if err != nil && cnt < 8 {
			cnt++
			t.Logf("NAL #%d type %d layer %d tid %d: %v (ctbs %d)", i, typ, l, int(n[1]&7)-1, err, d.decodedCtbs)
		}
	}
	d.finishPicture()
	t.Log(d.HashStats())
}

func TestDumpVPS(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	for _, n := range splitAnnexB(data) {
		typ := int(n[0]>>1) & 63
		l := int(n[0]&1)<<5 | int(n[1]>>3)
		switch typ {
		case 32:
			v, err := parseVPS(unescape(n[2:]))
			if err != nil {
				t.Log("VPS err", err)
				continue
			}
			t.Logf("VPS id=%d layers=%d ext=%v ids=%v views=%d scal=%v numDirect=%v allRefActive=%v maxOne=%v pocNotPres=%v dep=%v repfmt=%v idx=%v", v.id, v.maxLayers, v.hasExt, v.layerIDInNuh[:v.maxLayers], v.numViews, v.scalMask, v.numDirectRef[:v.maxLayers], v.allRefActive, v.maxOneActive, v.pocLsbNotPres[:v.maxLayers], v.directDep[1][:2], v.repFormats, v.repFormatIdx[:v.maxLayers])
			t.Logf("   viewIDs=%v dirType=%v", v.viewID[:v.maxLayers], v.directDepType[1][:2])
		case 33:
			s, err := parseSPSLayer(unescape(n[2:]), l, func(id int) *VPS { return nil })
			t.Logf("SPS layer %d err=%v multi=%v", l, err, s != nil && s.multiLayer)
		case 34:
			pp, err := parsePPS(unescape(n[2:]))
			if err == nil {
				t.Logf("PPS layer %d id=%d sps=%d pocReset=%v extraBits=%d sliceHdrExt=%v", l, pp.id, pp.spsID, pp.pocResetInfo, pp.numExtraSliceHdrBits, pp.sliceHeaderExt)
			} else {
				t.Logf("PPS layer %d err=%v", l, err)
			}
		}
	}
}

func TestSliceInfo(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	d := NewDecoder()
	for _, n := range splitAnnexB(data) {
		typ := int(n[0]>>1) & 63
		if typ < 32 {
			rbsp := unescape(n[2:])
			sh, err := d.parseSliceHeader(&bitReader{b: rbsp}, typ, int(n[1]&7)-1, d.lastIndep, 0)
			if err != nil {
				t.Log("slice err", err)
				return
			}
			t.Logf("nal %d first=%v type=%d poc=%d numRef=%v totalCurr=%d qp=%d merge=%d intMv=%v dataOff=%d lenrbsp=%d", typ, sh.firstSlice, sh.sliceType, sh.pocLsb, sh.numRefIdx, sh.numPicTotalCurr, sh.sliceQp, sh.maxMergeCand, sh.useIntegerMv, sh.dataOffset, len(rbsp))
			return
		}
		d.handleParamNAL(n)
	}
}

func TestHashTrace(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	data, _ := os.ReadFile(p)
	d := NewDecoder()
	d.CheckHash = true
	last := 0
	for i, n := range splitAnnexB(data) {
		d.handleNAL(n)
		typ := int(n[0]>>1) & 63
		if typ == 40 || typ == 39 {
			t.Logf("NAL #%d SEI type %d curPoc=%d", i, typ, func() int {
				if d.cur != nil {
					return d.cur.poc
				}
				return -1
			}())
		}
		_, b := d.HashStats()
		if b != last {
			t.Logf("bad hash at NAL #%d type %d", i, typ)
			last = b
		}
	}
}

func TestTUTrace(t *testing.T) {
	p := os.Getenv("HEVC_FILE")
	if p == "" {
		t.Skip()
	}
	var poc, px0, px1, py0, py1 int
	fmt.Sscan(os.Getenv("HEVC_POC"), &poc)
	fmt.Sscan(os.Getenv("HEVC_X0"), &px0)
	fmt.Sscan(os.Getenv("HEVC_X1"), &px1)
	fmt.Sscan(os.Getenv("HEVC_Y0"), &py0)
	fmt.Sscan(os.Getenv("HEVC_Y1"), &py1)
	data, _ := os.ReadFile(p)
	dbgTU = func(d *Decoder, x0, y0, log2, blkIdx int, cbfL bool, cb, cr [2]bool) {
		if d.cur.poc == poc && x0 >= px0 && x0 < px1 && y0 >= py0 && y0 < py1 {
			t.Logf("TU (%d,%d) log2=%d cbf=%v %v %v act=%v off=%d/%d qp=%d intra=%v bypass=%v", x0, y0, log2, cbfL, cb, cr, d.tuAct, d.cuQpOffsetCb, d.cuQpOffsetCr, d.qpY, d.cu.intra, d.cu.bypass)
		}
	}
	defer func() { dbgTU = nil }()
	decodeAnnexB(t, data)
}
