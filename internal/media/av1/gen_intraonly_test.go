package av1

import (
	"os"
	"testing"
)

// bitW is a minimal MSB-first bit writer for the stream rewriters.
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

func (w *bitW) copyBits(src []byte, from, to int) {
	for i := from; i < to; i++ {
		w.put(int(src[i>>3]>>(7-uint(i&7))&1), 1)
	}
}

// makeIntraOnly rewrites the shown key frames after the first one of a
// low-overhead stream (coded with OBU_FRAME) as intra_only frames
// refreshing slots 0..6: the tile data of both frame types is coded the same
// way, only the frame header differs. No encoder emits intra-only frames
// (libaom has the feature disabled), so the streams exercising them are made
// this way and judged against dav1d.
func makeIntraOnly(t testing.TB, data []byte, errRes bool) []byte {
	d := NewDecoder()
	var out []byte
	nkey := 0
	for pos := 0; pos < len(data); {
		b := &bitReader{buf: data[pos:]}
		b.f(1)
		typ := b.f(4)
		ext := b.flag()
		b.flag()
		b.f(1)
		if ext {
			b.f(8)
		}
		size := b.leb128()
		hdrLen := b.pos / 8
		obu := data[pos : pos+hdrLen+size]
		payload := data[pos+hdrLen : pos+hdrLen+size]
		pos += hdrLen + size
		if typ == obuFrame && d.seq != nil {
			pb := &bitReader{buf: payload}
			tmp := &Decoder{seq: d.seq}
			tmp.refOrderHint = d.refOrderHint
			fh, err := tmp.parseFrameHeader(pb, 0, 0)
			if err == nil && !fh.showExisting && fh.frameType == keyFrame && fh.showFrame {
				nkey++
				if nkey > 1 {
					pb.byteAlign()
					w := &bitW{}
					w.copyBits(payload, 0, 1)
					w.put(intraOnlyFrame, 2)
					w.copyBits(payload, 3, fh.posErrorRes) // show_frame ...
					w.put(b2i(errRes), 1)                  // error_resilient_mode
					w.copyBits(payload, fh.posErrorRes, fh.posRefresh)
					w.put(0x7f, 8) // refresh_frame_flags
					if errRes && d.seq.enableOrderHint {
						for i := 0; i < numRefFrames; i++ {
							w.put(d.refOrderHint[i], d.seq.orderHintBits)
						}
					}
					w.copyBits(payload, fh.posRefresh, fh.hdrBits)
					for w.n&7 != 0 {
						w.put(0, 1)
					}
					np := append(w.b, payload[pb.pos>>3:]...)
					nh := append([]byte(nil), obu[:1]...)
					if ext {
						nh = append(nh, obu[1])
					}
					nh = append(nh, lebBytes(len(np))...)
					obu = append(nh, np...)
				}
			}
		}
		out = append(out, obu...)
		d.Decode(data[pos-hdrLen-size : pos])
	}
	return out
}

// TestGenIntraOnly writes the rewritten stream of $AV1_GEN_IN to
// $AV1_GEN_OUT (development helper).
func TestGenIntraOnly(t *testing.T) {
	in := os.Getenv("AV1_GEN_IN")
	if in == "" {
		t.Skip("AV1_GEN_IN not set")
	}
	data, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("AV1_GEN_OUT"), makeIntraOnly(t, data, os.Getenv("AV1_GEN_ERRRES") == "1"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeShortSignaling rewrites the inter frame headers of a low-overhead
// stream (OBU_FRAME, order hints on, no frame ids) to code the reference
// frame indices with frame_refs_short_signaling from the original LAST and
// GOLDEN slots. The tile data then decodes against references derived by the
// set_frame_refs process, which gives a different but valid stream.
func makeShortSignaling(t testing.TB, data []byte) []byte {
	d := NewDecoder()
	var out []byte
	for pos := 0; pos < len(data); {
		b := &bitReader{buf: data[pos:]}
		b.f(1)
		typ := b.f(4)
		ext := b.flag()
		b.flag()
		b.f(1)
		if ext {
			b.f(8)
		}
		size := b.leb128()
		hdrLen := b.pos / 8
		obu := data[pos : pos+hdrLen+size]
		payload := data[pos+hdrLen : pos+hdrLen+size]
		pos += hdrLen + size
		if typ == obuFrame && d.seq != nil && d.seq.enableOrderHint && !d.seq.frameIDNumbersPresent {
			pb := &bitReader{buf: payload}
			tmp := &Decoder{seq: d.seq}
			tmp.ref, tmp.refValid, tmp.refOrderHint, tmp.refFrameID = d.ref, d.refValid, d.refOrderHint, d.refFrameID
			fh, err := tmp.parseFrameHeader(pb, 0, 0)
			same := false
			if err == nil && !fh.showExisting && !fh.frameIsIntra && fh.posShortSig > 0 {
				chk := *fh
				tmp.setFrameRefs(&chk, fh.refFrameIdx[0], fh.refFrameIdx[3])
				same = chk.refFrameIdx == fh.refFrameIdx
				if os.Getenv("AV1_GEN_ONLYSAME") == "1" && !same {
					err = errBitstream
				}
			}
			if err == nil && !fh.showExisting && !fh.frameIsIntra && fh.posShortSig > 0 {
				pb.byteAlign()
				w := &bitW{}
				w.copyBits(payload, 0, fh.posShortSig)
				w.put(1, 1)
				w.put(fh.refFrameIdx[0], 3)
				w.put(fh.refFrameIdx[3], 3)
				w.copyBits(payload, fh.posRefIdxEnd, fh.hdrBits)
				for w.n&7 != 0 {
					w.put(0, 1)
				}
				np := append(w.b, payload[pb.pos>>3:]...)
				nh := append([]byte(nil), obu[:1]...)
				if ext {
					nh = append(nh, obu[1])
				}
				nh = append(nh, lebBytes(len(np))...)
				obu = append(nh, np...)
			}
		}
		out = append(out, obu...)
		d.Decode(data[pos-hdrLen-size : pos])
	}
	return out
}

// TestGenShortSignaling writes the rewritten stream of $AV1_GEN_IN to
// $AV1_GEN_OUT (development helper).
func TestGenShortSignaling(t *testing.T) {
	in := os.Getenv("AV1_GEN_IN")
	if in == "" {
		t.Skip("AV1_GEN_IN not set")
	}
	data, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("AV1_GEN_OUT"), makeShortSignaling(t, data), 0o644); err != nil {
		t.Fatal(err)
	}
}
