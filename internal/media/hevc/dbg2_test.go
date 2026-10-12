package hevc

import (
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
			sh, err := d.parseSliceHeader(r, typ, 0, d.lastIndep)
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
