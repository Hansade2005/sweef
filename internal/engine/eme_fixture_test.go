package engine

// A small fragmented-MP4 muxer plus a Common Encryption encryptor (an independent implementation of the encrypting
// side, cenc and cbcs only), used to build ClearKey-protected MSE content for the EME tests.

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"os"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media"
)

func fxBox(typ string, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

func fxFull(typ string, ver byte, flags uint32, parts ...[]byte) []byte {
	h := []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return fxBox(typ, append([][]byte{h}, parts...)...)
}

func fxU32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func fxU16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

type fxSub struct {
	clear uint16
	enc   uint32
}

type fxProt struct {
	scheme string // "cenc" or "cbcs"
	kid    [16]byte
	key    []byte
	pssh   bool
}

func (p *fxProt) cbcs() bool { return p.scheme == "cbcs" }

func (p *fxProt) sinf() []byte {
	ver, pattern := byte(0), byte(0)
	ivSize := byte(8)
	var tail []byte
	if p.cbcs() {
		ver, pattern, ivSize = 1, 1<<4|9, 0
		tail = append([]byte{16}, []byte("0123456789abcdef")...) // constant IV
	}
	body := []byte{0, pattern, 1, ivSize}
	body = append(body, p.kid[:]...)
	body = append(body, tail...)
	return fxBox("sinf", fxBox("frma", []byte("avc1")), fxFull("schm", 0, 0, []byte(p.scheme), fxU32(0x10000)),
		fxBox("schi", fxFull("tenc", ver, 0, body)))
}

func fxInit(avcC []byte, w, h int, p *fxProt) []byte {
	avc := make([]byte, 78)
	binary.BigEndian.PutUint16(avc[6:], 1)
	binary.BigEndian.PutUint16(avc[24:], uint16(w))
	binary.BigEndian.PutUint16(avc[26:], uint16(h))
	avc[74], avc[75], avc[76], avc[77] = 0, 0x18, 0xff, 0xff
	entry := append(avc, fxBox("avcC", avcC)...)
	typ := "avc1"
	if p != nil {
		entry = append(entry, p.sinf()...)
		typ = "encv"
	}
	stsd := fxFull("stsd", 0, 0, fxU32(1), fxBox(typ, entry))
	stbl := fxBox("stbl", stsd, fxFull("stts", 0, 0, fxU32(0)), fxFull("stsc", 0, 0, fxU32(0)), fxFull("stsz", 0, 0, fxU32(0), fxU32(0)), fxFull("stco", 0, 0, fxU32(0)))
	minf := fxBox("minf", fxFull("vmhd", 0, 1, make([]byte, 8)), fxBox("dinf", fxFull("dref", 0, 0, fxU32(1), fxFull("url ", 0, 1))), stbl)
	mdhd := fxFull("mdhd", 0, 0, fxU32(0), fxU32(0), fxU32(90000), fxU32(0), []byte{0x55, 0xc4, 0, 0})
	hdlr := fxFull("hdlr", 0, 0, fxU32(0), []byte("vide"), make([]byte, 12), []byte{0})
	matrix := []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0}
	tkhd := fxFull("tkhd", 0, 3, fxU32(0), fxU32(0), fxU32(1), fxU32(0), fxU32(0), make([]byte, 8), make([]byte, 8), matrix, fxU32(uint32(w)<<16), fxU32(uint32(h)<<16))
	mvhd := fxFull("mvhd", 0, 0, fxU32(0), fxU32(0), fxU32(1000), fxU32(0), []byte{0, 1, 0, 0, 1, 0}, make([]byte, 10), matrix, make([]byte, 24), fxU32(2))
	trex := fxFull("trex", 0, 0, fxU32(1), fxU32(1), fxU32(0), fxU32(0), fxU32(0))
	moov := [][]byte{mvhd, fxBox("trak", tkhd, fxBox("mdia", mdhd, hdlr, minf)), fxBox("mvex", trex)}
	if p != nil && p.pssh {
		sys := []byte{0x10, 0x77, 0xef, 0xec, 0xc0, 0xb2, 0x4d, 0x02, 0xac, 0xe3, 0x3c, 0x1e, 0x52, 0xe2, 0xfb, 0x4b}
		moov = append(moov, fxFull("pssh", 1, 0, sys, fxU32(1), p.kid[:], fxU32(0)))
	}
	return append(fxBox("ftyp", []byte("isom"), fxU32(1), []byte("isomiso6cenc")), fxBox("moov", moov...)...)
}

// fxSubsamples keeps each NAL's length prefix and first 5 bytes in the clear.
func fxSubsamples(sample []byte) []fxSub {
	var subs []fxSub
	for p := 0; p+4 <= len(sample); {
		n := int(binary.BigEndian.Uint32(sample[p:]))
		hdr := min(5, n)
		subs = append(subs, fxSub{clear: uint16(4 + hdr), enc: uint32(n - hdr)})
		p += 4 + n
	}
	return subs
}

func fxEncrypt(p *fxProt, key, iv []byte, subs []fxSub, in []byte) []byte {
	blk, _ := aes.NewCipher(key)
	out := append([]byte(nil), in...)
	iv16 := make([]byte, 16)
	copy(iv16, iv)
	pos := 0
	for _, s := range subs {
		pos += int(s.clear)
		lo, hi := pos, pos+int(s.enc)
		pos = hi
		if p.scheme == "cenc" {
			continue
		}
		prev := append([]byte(nil), iv16...) // cbcs: the chain restarts for every subsample
		n := (hi - lo) / 16
		for b := 0; b < n; b += 10 { // 1:9 pattern
			off := lo + b*16
			for i := 0; i < 16; i++ {
				out[off+i] ^= prev[i]
			}
			blk.Encrypt(out[off:off+16], out[off:off+16])
			copy(prev, out[off:off+16])
		}
	}
	if p.scheme == "cenc" { // one CTR stream over all the protected ranges of the sample
		st := cipher.NewCTR(blk, iv16)
		pos = 0
		for _, s := range subs {
			pos += int(s.clear)
			st.XORKeyStream(out[pos:pos+int(s.enc)], out[pos:pos+int(s.enc)])
			pos += int(s.enc)
		}
	}
	return out
}

func fxFragment(seq int, baseDTS int64, samples [][]byte, dur uint32, p *fxProt, ivs [][]byte, subs [][]fxSub, key []bool) []byte {
	n := len(samples)
	trun := fxU32(uint32(n))
	trun = append(trun, fxU32(0)...)
	for i, s := range samples {
		fl := uint32(0x10000)
		if key[i] {
			fl = 0
		}
		trun = append(trun, fxU32(dur)...)
		trun = append(trun, fxU32(uint32(len(s)))...)
		trun = append(trun, fxU32(fl)...)
	}
	var senc []byte
	if p != nil {
		sb := fxU32(uint32(n))
		for i := range samples {
			if !p.cbcs() {
				sb = append(sb, ivs[i]...)
			}
			sb = append(sb, fxU16(uint16(len(subs[i])))...)
			for _, s := range subs[i] {
				sb = append(sb, fxU16(s.clear)...)
				sb = append(sb, fxU32(s.enc)...)
			}
		}
		senc = fxFull("senc", 0, 2, sb)
	}
	build := func(off uint32) []byte {
		t := append([]byte(nil), trun...)
		binary.BigEndian.PutUint32(t[4:], off)
		parts := [][]byte{fxFull("tfhd", 0, 0x20000, fxU32(1)), fxFull("tfdt", 1, 0, binary.BigEndian.AppendUint64(nil, uint64(baseDTS))), fxFull("trun", 0, 0x701, t)}
		if senc != nil {
			parts = append(parts, senc)
		}
		return fxBox("moof", fxFull("mfhd", 0, 0, fxU32(uint32(seq))), fxBox("traf", parts...))
	}
	moof := build(0)
	moof = build(uint32(len(moof) + 8))
	var data []byte
	for _, s := range samples {
		data = append(data, s...)
	}
	return append(moof, fxBox("mdat", data)...)
}

// fxClip builds init + media data of the 320x... H.264 test clip, optionally protected, as one appendable buffer.
func fxClip(t *testing.T, p *fxProt) []byte {
	t.Helper()
	data, err := os.ReadFile("../media/testdata/h264_baseline_cavlc.mp4")
	if err != nil {
		t.Skip(err)
	}
	vs, err := media.ExtractVideoSamples(data)
	if err != nil {
		t.Fatal(err)
	}
	out := fxInit(vs.Description, vs.Width, vs.Height, p)
	const perFrag = 4
	for i := 0; i < len(vs.Chunks); i += perFrag {
		end := min(i+perFrag, len(vs.Chunks))
		var samples, ivs [][]byte
		var subs [][]fxSub
		var keys []bool
		for k := i; k < end; k++ {
			s := vs.Chunks[k].Data
			keys = append(keys, vs.Chunks[k].Key)
			if p != nil {
				iv := make([]byte, 8)
				for j := range iv {
					iv[j] = byte(k*7 + j + 1)
				}
				sub := fxSubsamples(s)
				use := iv
				if p.cbcs() {
					use = []byte("0123456789abcdef")
				}
				s = fxEncrypt(p, p.key, use, sub, s)
				ivs = append(ivs, iv)
				subs = append(subs, sub)
			}
			samples = append(samples, s)
		}
		out = append(out, fxFragment(i/perFrag+1, int64(i)*3000, samples, 3000, p, ivs, subs, keys)...)
	}
	return out
}
