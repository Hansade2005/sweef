package vp9

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic streams: ffmpeg's libvpx wrapper cannot emit intra-only frames
// or scaled references, so existing streams are rewritten at the bit level.

type bitW struct {
	b []byte
	n int
}

func (w *bitW) put(v, n int) {
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

type bitR struct {
	b []byte
	n int
}

func (r *bitR) get(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		v = v<<1 | int(r.b[r.n>>3]>>(7-uint(r.n&7))&1)
		r.n++
	}
	return v
}

// copyBits copies n bits from r to w.
func copyBits(w *bitW, r *bitR, n int) {
	for ; n > 0; n-- {
		w.put(r.get(1), 1)
	}
}

func ivfWrite(frames [][]byte, w, h int) []byte {
	hdr := make([]byte, 32)
	copy(hdr, "DKIF")
	binary.LittleEndian.PutUint16(hdr[6:], 32)
	copy(hdr[8:], "VP90")
	binary.LittleEndian.PutUint16(hdr[12:], uint16(w))
	binary.LittleEndian.PutUint16(hdr[14:], uint16(h))
	binary.LittleEndian.PutUint32(hdr[16:], 30)
	binary.LittleEndian.PutUint32(hdr[20:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], uint32(len(frames)))
	out := hdr
	for i, f := range frames {
		fh := make([]byte, 12)
		binary.LittleEndian.PutUint32(fh, uint32(len(f)))
		binary.LittleEndian.PutUint64(fh[4:], uint64(i))
		out = append(out, fh...)
		out = append(out, f...)
	}
	return out
}

// hdrInfo locates parts of an uncompressed header (profile 0..2 streams
// narrower than 512 samples, where tile info carries no column bits).
type hdrInfo struct {
	profile  int
	key      bool
	showFrm  bool
	errRes   bool
	colorEnd int // bit offset after the colour config (key frames)
	sizeBit  int // bit offset of frame_size() (key frames)
	tailBit  int // bit offset after render_size()
	endBit   int // bit offset after header_size
}

func colorBits(profile int) int {
	return [4]int{4, 7, 5, 8}[profile]
}

func parseTail(r *bitR, errRes bool, sb64 int) {
	if !errRes {
		r.get(2)
	}
	r.get(2)
	r.get(6)
	r.get(3)
	if r.get(1) == 1 {
		if r.get(1) == 1 {
			for i := 0; i < 6; i++ {
				if r.get(1) == 1 {
					r.get(7)
				}
			}
		}
	}
	r.get(8)
	for i := 0; i < 3; i++ {
		if r.get(1) == 1 {
			r.get(5)
		}
	}
	if r.get(1) == 1 { // segmentation
		if r.get(1) == 1 {
			for i := 0; i < 7; i++ {
				if r.get(1) == 1 {
					r.get(8)
				}
			}
			temporal := r.get(1)
			for i := 0; i < 3; i++ {
				if temporal == 1 && r.get(1) == 1 {
					r.get(8)
				}
			}
		}
		if r.get(1) == 1 {
			r.get(1)
			bits := [4]int{8, 6, 2, 0}
			signed := [4]bool{true, true, false, false}
			for i := 0; i < 8; i++ {
				for j := 0; j < 4; j++ {
					if r.get(1) == 1 {
						r.get(bits[j])
						if signed[j] {
							r.get(1)
						}
					}
				}
			}
		}
	}
	minLog2 := 0
	for (64 << uint(minLog2)) < sb64 {
		minLog2++
	}
	maxLog2 := 1
	for (sb64 >> uint(maxLog2)) >= 4 {
		maxLog2++
	}
	maxLog2--
	for l := minLog2; l < maxLog2; l++ {
		if r.get(1) == 0 {
			break
		}
	}
	if r.get(1) == 1 {
		r.get(1)
	}
	r.get(16)
}

func parseKeyHdr(f []byte, w int) hdrInfo {
	r := &bitR{b: f}
	var h hdrInfo
	r.get(2)
	lo, hi := r.get(1), r.get(1)
	h.profile = hi<<1 | lo
	if h.profile == 3 {
		r.get(1)
	}
	r.get(1) // show_existing_frame
	h.key = r.get(1) == 0
	h.showFrm = r.get(1) == 1
	h.errRes = r.get(1) == 1
	if !h.key {
		return h
	}
	r.get(24)
	r.get(colorBits(h.profile))
	h.colorEnd = r.n
	h.sizeBit = r.n
	r.get(32)
	if r.get(1) == 1 {
		r.get(32)
	}
	h.tailBit = r.n
	parseTail(r, h.errRes, (w+63)/64)
	h.endBit = r.n
	return h
}

// finish pads the header written to w, appends the compressed header and
// tile data that followed the original header, and returns the frame.
func finishFrame(w *bitW, orig []byte, origEndBit int) []byte {
	return append(w.b, orig[(origEndBit+7)>>3:]...)
}

func showExisting(profile, idx int) []byte {
	w := &bitW{}
	w.put(2, 2)
	w.put(profile&1, 1)
	w.put(profile>>1, 1)
	if profile == 3 {
		w.put(0, 1)
	}
	w.put(1, 1)
	w.put(idx, 3)
	return w.b
}

// toIntraOnly rewrites key frame f as a hidden intra-only frame with the
// given refresh flags and reset_frame_context.
func toIntraOnly(f []byte, w int, refresh, reset int) []byte {
	h := parseKeyHdr(f, w)
	r := &bitR{b: f}
	o := &bitW{}
	o.put(2, 2)
	o.put(h.profile&1, 1)
	o.put(h.profile>>1, 1)
	if h.profile == 3 {
		o.put(0, 1)
	}
	o.put(0, 1) // show_existing_frame
	o.put(1, 1) // non-key
	o.put(0, 1) // show_frame
	o.put(btoi(h.errRes), 1)
	o.put(1, 1) // intra_only
	if !h.errRes {
		o.put(reset, 2)
	}
	o.put(0x498342, 24)
	if h.profile > 0 {
		r.n = h.colorEnd - colorBits(h.profile)
		copyBits(o, r, colorBits(h.profile))
	}
	o.put(refresh, 8)
	r.n = h.sizeBit
	copyBits(o, r, h.tailBit-h.sizeBit)
	copyBits(o, r, h.endBit-h.tailBit)
	return finishFrame(o, f, h.endBit)
}

func TestVP9IntraOnly(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip("no ffmpeg")
	}
	for _, c := range []struct {
		name, pf, prof string
	}{{"p0", "yuv420p", "0"}, {"p1_444", "yuv444p", "1"}, {"p2_10", "yuv420p10le", "2"}} {
		for _, v := range []struct{ refresh, reset int }{{0xff, 3}, {0x01, 3}, {0x04, 2}, {0x82, 3}, {0x10, 3}} {
			t.Run(c.name, func(t *testing.T) {
				ivf := genVP9(t, "testsrc2=rate=15:duration=3", "208x120", c.pf, "-profile:v", c.prof, "-cpu-used", "4", "-g", "12")
				pk := readIVF(t, ivf)
				slot := 0
				for s := 0; s < 8; s++ {
					if v.refresh>>uint(s)&1 != 0 {
						slot = s
						break
					}
				}
				var out [][]byte
				n := 0
				for i, f := range pk {
					h := parseKeyHdr(f, 208)
					if h.key && i > 0 && h.showFrm {
						out = append(out, toIntraOnly(f, 208, v.refresh, v.reset), showExisting(h.profile, slot))
						n++
						continue
					}
					out = append(out, f)
				}
				if n == 0 {
					t.Fatal("no second key frame")
				}
				compareNative(t, c.name, ivfWrite(out, 208, 120))
			})
		}
	}
}

// superframe packs frames into one packet with a superframe index.
func superframe(frames ...[]byte) []byte {
	var out []byte
	for _, f := range frames {
		out = append(out, f...)
	}
	marker := byte(0xc0 | 3<<3 | (len(frames) - 1))
	out = append(out, marker)
	for _, f := range frames {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(f)))
	}
	return append(out, marker)
}

// toExplicitSize rewrites an inter frame so that it signals an explicit
// frame size (no size taken from a reference), forcing scaled prediction.
func toExplicitSize(f []byte, origW, nw, nh int) []byte {
	r := &bitR{b: f}
	o := &bitW{}
	copyBits(o, r, 2)
	lo, hi := r.get(1), r.get(1)
	o.put(lo, 1)
	o.put(hi, 1)
	if hi == 1 && lo == 1 {
		copyBits(o, r, 1)
	}
	copyBits(o, r, 1) // show_existing_frame (0)
	copyBits(o, r, 1) // frame_type
	show := r.get(1)
	o.put(show, 1)
	errRes := r.get(1)
	o.put(errRes, 1)
	if show == 0 {
		copyBits(o, r, 1)
	}
	if errRes == 0 {
		copyBits(o, r, 2)
	}
	copyBits(o, r, 8+3*4)
	for i := 0; i < 3; i++ {
		if r.get(1) == 1 {
			break
		}
	}
	o.put(0, 3)
	o.put(nw-1, 16)
	o.put(nh-1, 16)
	o.put(0, 1) // render_and_frame_size_different
	if r.get(1) == 1 {
		r.get(32)
	}
	start := r.n
	copyBits(o, r, 1) // allow_hp
	if r.get(1) == 1 {
		o.put(1, 1)
	} else {
		o.put(0, 1)
		copyBits(o, r, 2)
	}
	_ = start
	t := &bitR{b: f, n: r.n}
	parseTail(t, errRes == 1, (origW+63)/64)
	copyBits(o, r, t.n-r.n)
	return finishFrame(o, f, t.n)
}

func TestVP9ScaledRefs(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip("no ffmpeg")
	}
	// Stream A supplies a hidden key frame at one size; stream B, encoded at
	// another size from the same source, supplies the inter frames. B's data is
	// consistent with its own frame size, so its references (A's key frame
	// instead of B's) are scaled legitimately.
	for _, c := range []struct {
		name, pf, prof string
		aw, ah, bw, bh int
	}{
		{"same", "yuv420p", "0", 160, 96, 160, 96},
		{"down", "yuv420p", "0", 208, 120, 160, 96},
		{"up", "yuv420p", "0", 128, 72, 208, 120},
		{"odd", "yuv420p", "0", 213, 117, 171, 99},
		{"p1_444", "yuv444p", "1", 208, 120, 160, 96},
		{"p1_422", "yuv422p", "1", 160, 96, 208, 120},
		{"p1_440", "yuv440p", "1", 208, 120, 152, 88},
		{"p2_10", "yuv420p10le", "2", 208, 120, 160, 96},
		{"p2_12_up", "yuv420p12le", "2", 112, 64, 208, 120},
		{"p3_444_10", "yuv444p10le", "3", 208, 120, 176, 104},
	} {
		t.Run(c.name, func(t *testing.T) {
			sz := func(w, h int) string { return fmt.Sprintf("%dx%d", w, h) }
			opts := []string{"-profile:v", c.prof, "-cpu-used", "4", "-g", "100", "-auto-alt-ref", "0", "-lag-in-frames", "0", "-error-resilient", "1"}
			a := readIVF(t, genVP9(t, "testsrc2=rate=15:duration=2", sz(c.aw, c.ah), c.pf, opts...))
			b := readIVF(t, genVP9(t, "testsrc2=rate=15:duration=2", sz(c.bw, c.bh), c.pf, opts...))
			key := append([]byte(nil), a[0]...)
			bit := 6 // show_frame
			if c.prof == "3" {
				bit = 7
			}
			key[bit>>3] &^= 0x80 >> uint(bit&7)
			out := [][]byte{key, toExplicitSize(b[1], c.bw, c.bw, c.bh)}
			out = append(out, b[2:]...)
			compareNative(t, c.name, ivfWrite(out, c.bw, c.bh))
		})
	}
}

// synthFixtures builds the committed synthetic streams.
func synthFixtures(t *testing.T) map[string][]byte {
	out := map[string][]byte{}
	gen := func(w, h int, pf, prof string, extra ...string) [][]byte {
		args := append([]string{"-profile:v", prof, "-cpu-used", "4", "-g", "100", "-auto-alt-ref", "0", "-lag-in-frames", "0"}, extra...)
		return readIVF(t, genVP9(t, "testsrc2=rate=15:duration=1", fmt.Sprintf("%dx%d", w, h), pf, args...))
	}
	// intra-only frames replacing the second key frame
	for _, c := range []struct{ name, pf, prof string }{
		{"vp9_intraonly_p0", "yuv420p", "0"},
		{"vp9_intraonly_p2_10", "yuv420p10le", "2"},
		{"vp9_intraonly_p1_444", "yuv444p", "1"},
	} {
		pk := gen(144, 80, c.pf, c.prof, "-g", "6")
		var frames [][]byte
		for i, f := range pk {
			h := parseKeyHdr(f, 144)
			if h.key && i > 0 && h.showFrm {
				frames = append(frames, toIntraOnly(f, 144, 0x82, 3), showExisting(h.profile, 1))
				continue
			}
			frames = append(frames, f)
		}
		out[c.name] = ivfWrite(frames, 144, 80)
	}
	// scaled references
	for _, c := range []struct {
		name, pf, prof string
		aw, ah, bw, bh int
	}{
		{"vp9_scaled_down", "yuv420p", "0", 144, 80, 112, 64},
		{"vp9_scaled_up_444", "yuv444p", "1", 80, 48, 144, 80},
		{"vp9_scaled_p2_12", "yuv420p12le", "2", 96, 56, 144, 80},
	} {
		er := []string{"-error-resilient", "1"}
		a := gen(c.aw, c.ah, c.pf, c.prof, er...)
		b := gen(c.bw, c.bh, c.pf, c.prof, er...)
		key := append([]byte(nil), a[0]...)
		key[0] &^= 0x02
		frames := [][]byte{key, toExplicitSize(b[1], c.bw, c.bw, c.bh)}
		frames = append(frames, b[2:]...)
		out[c.name] = ivfWrite(frames, c.bw, c.bh)
	}
	return out
}

// TestGenVP9Fixtures writes the synthetic fixtures and ffmpeg's reference
// MD5s into the directory named by SWEEF_GEN_VP9_FIXTURES.
func TestGenVP9Fixtures(t *testing.T) {
	dir := os.Getenv("SWEEF_GEN_VP9_FIXTURES")
	if dir == "" {
		t.Skip()
	}
	for name, ivf := range synthFixtures(t) {
		p := filepath.Join(dir, name+".ivf")
		os.WriteFile(p, ivf, 0o644)
		ref := ffmpeg(t, "-i", p, "-fps_mode", "passthrough", "-f", "rawvideo", "-")
		got := decodeRaw(t, readIVF(t, ivf))
		fs := len(got[0])
		if len(ref) != fs*len(got) {
			t.Fatalf("%s: frame count mismatch", name)
		}
		var sb strings.Builder
		for i := range got {
			sum := md5.Sum(ref[i*fs : (i+1)*fs])
			sb.WriteString(hex.EncodeToString(sum[:]) + "\n")
		}
		os.WriteFile(filepath.Join(dir, name+".md5"), []byte(sb.String()), 0o644)
	}
}

// TestVP9Fixtures checks the committed streams without ffmpeg.
func TestVP9Fixtures(t *testing.T) {
	files, _ := filepath.Glob("../testdata/fmt/vp9_*.md5")
	n := 0
	for _, f := range files {
		ivf, err := os.ReadFile(strings.TrimSuffix(f, ".md5") + ".ivf")
		if err != nil {
			continue
		}
		n++
		t.Run(filepath.Base(f), func(t *testing.T) {
			want, _ := os.ReadFile(f)
			wl := strings.Fields(string(want))
			got := decodeRaw(t, readIVF(t, ivf))
			if len(got) != len(wl) {
				t.Fatalf("%d frames, want %d", len(got), len(wl))
			}
			for i, g := range got {
				sum := md5.Sum(g)
				if hex.EncodeToString(sum[:]) != wl[i] {
					t.Fatalf("frame %d differs", i)
				}
			}
		})
	}
	if n == 0 {
		t.Fatal("no fixtures")
	}
}
