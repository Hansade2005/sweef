package media

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"os"
	"testing"
)

// ---- a small fragmented MP4 muxer and Common Encryption encryptor, test only ------------------------------

func mbox(typ string, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

func mfullbox(typ string, ver byte, flags uint32, parts ...[]byte) []byte {
	h := []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return mbox(typ, append([][]byte{h}, parts...)...)
}

func mu32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func mu16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

type testProt struct {
	scheme       string
	kid          [16]byte
	key          []byte
	ivSize       int // 0: constant IV
	constIV      []byte
	crypt, skip  int
	subsamples   bool
	wantPSSH     bool
	useSaioSaiz  bool
	protectedTrk bool
}

func (tp *testProt) sinf() []byte {
	tenc := []byte{0, 0, 0, 0, 0, 0}
	if tp.crypt > 0 {
		tenc[1] = 0 // version stays 0 unless a pattern is in use
	}
	ver := byte(0)
	pattern := byte(0)
	if tp.crypt > 0 || tp.skip > 0 {
		ver, pattern = 1, byte(tp.crypt<<4|tp.skip)
	}
	body := []byte{0, pattern, 1, byte(tp.ivSize)}
	body = append(body, tp.kid[:]...)
	if tp.ivSize == 0 {
		body = append(body, byte(len(tp.constIV)))
		body = append(body, tp.constIV...)
	}
	return mbox("sinf", mbox("frma", []byte("avc1")), mfullbox("schm", 0, 0, []byte(tp.scheme), mu32(0x10000)),
		mbox("schi", mfullbox("tenc", ver, 0, body)))
}

func muxInit(avcC []byte, w, h int, tp *testProt) []byte {
	avc := make([]byte, 78)
	binary.BigEndian.PutUint16(avc[6:], 1)
	binary.BigEndian.PutUint16(avc[24:], uint16(w))
	binary.BigEndian.PutUint16(avc[26:], uint16(h))
	binary.BigEndian.PutUint16(avc[40+32-2:], 0x18) // depth (offset 74)
	avc[74], avc[75], avc[76], avc[77] = 0, 0x18, 0xff, 0xff
	entry := append(avc, mbox("avcC", avcC)...)
	typ := "avc1"
	if tp != nil {
		entry = append(entry, tp.sinf()...)
		typ = "encv"
	}
	stsd := mfullbox("stsd", 0, 0, mu32(1), mbox(typ, entry))
	stbl := mbox("stbl", stsd, mfullbox("stts", 0, 0, mu32(0)), mfullbox("stsc", 0, 0, mu32(0)), mfullbox("stsz", 0, 0, mu32(0), mu32(0)), mfullbox("stco", 0, 0, mu32(0)))
	minf := mbox("minf", mfullbox("vmhd", 0, 1, make([]byte, 8)), mbox("dinf", mfullbox("dref", 0, 0, mu32(1), mfullbox("url ", 0, 1))), stbl)
	mdhd := mfullbox("mdhd", 0, 0, mu32(0), mu32(0), mu32(90000), mu32(0), []byte{0x55, 0xc4, 0, 0})
	hdlr := mfullbox("hdlr", 0, 0, mu32(0), []byte("vide"), make([]byte, 12), []byte{0})
	matrix := []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0, 0, 0}
	tkhd := mfullbox("tkhd", 0, 3, mu32(0), mu32(0), mu32(1), mu32(0), mu32(0), make([]byte, 8), make([]byte, 8), matrix, mu32(uint32(w)<<16), mu32(uint32(h)<<16))
	mvhd := mfullbox("mvhd", 0, 0, mu32(0), mu32(0), mu32(1000), mu32(0), []byte{0, 1, 0, 0, 1, 0}, make([]byte, 10), matrix, make([]byte, 24), mu32(2))
	trex := mfullbox("trex", 0, 0, mu32(1), mu32(1), mu32(0), mu32(0), mu32(0))
	moovParts := [][]byte{mvhd, mbox("trak", tkhd, mbox("mdia", mdhd, hdlr, minf)), mbox("mvex", trex)}
	if tp != nil && tp.wantPSSH {
		// a Common-system pssh (version 1) naming the key ID
		sys := []byte{0x10, 0x77, 0xef, 0xec, 0xc0, 0xb2, 0x4d, 0x02, 0xac, 0xe3, 0x3c, 0x1e, 0x52, 0xe2, 0xfb, 0x4b}
		moovParts = append(moovParts, mfullbox("pssh", 1, 0, sys, mu32(1), tp.kid[:], mu32(0)))
	}
	return append(mbox("ftyp", []byte("isom"), mu32(1), []byte("isomiso6cenc")), mbox("moov", moovParts...)...)
}

// avccNALRanges returns the subsample map of an AVCC sample: each NAL keeps its length prefix and a header
// (clearHdr bytes of the NAL) in the clear.
func avccSubsamples(sample []byte, clearHdr int) []Subsample {
	var subs []Subsample
	for p := 0; p+4 <= len(sample); {
		n := int(binary.BigEndian.Uint32(sample[p:]))
		clear := 4 + min(clearHdr, n)
		subs = append(subs, Subsample{Clear: uint16(clear), Enc: uint32(n - min(clearHdr, n))})
		p += 4 + n
	}
	return subs
}

// encryptSample is an independent implementation of the encryption side.
func encryptSample(tp *testProt, key []byte, iv []byte, subs []Subsample, in []byte) []byte {
	blk, _ := aes.NewCipher(key)
	out := append([]byte(nil), in...)
	iv16 := make([]byte, 16)
	copy(iv16, iv)
	var ranges [][2]int
	if len(subs) == 0 {
		ranges = [][2]int{{0, len(in)}}
	} else {
		pos := 0
		for _, s := range subs {
			pos += int(s.Clear)
			ranges = append(ranges, [2]int{pos, pos + int(s.Enc)})
			pos += int(s.Enc)
		}
	}
	switch tp.scheme {
	case "cenc":
		st := cipher.NewCTR(blk, iv16)
		for _, r := range ranges {
			st.XORKeyStream(out[r[0]:r[1]], out[r[0]:r[1]])
		}
	case "cbcs":
		for _, r := range ranges {
			prev := append([]byte(nil), iv16...)
			n := (r[1] - r[0]) / 16
			for b := 0; b < n; {
				for c := 0; c < tp.crypt && b+c < n; c++ {
					off := r[0] + (b+c)*16
					for i := 0; i < 16; i++ {
						out[off+i] ^= prev[i]
					}
					blk.Encrypt(out[off:off+16], out[off:off+16])
					copy(prev, out[off:off+16])
				}
				b += tp.crypt + tp.skip
			}
		}
	case "cens":
		st := cipher.NewCTR(blk, iv16)
		for _, r := range ranges {
			n := (r[1] - r[0]) / 16
			for b := 0; b < n; {
				for c := 0; c < tp.crypt && b+c < n; c++ {
					off := r[0] + (b+c)*16
					st.XORKeyStream(out[off:off+16], out[off:off+16])
				}
				b += tp.crypt + tp.skip
			}
		}
	case "cbc1":
		prev := append([]byte(nil), iv16...)
		for _, r := range ranges {
			for off := r[0]; off+16 <= r[1]; off += 16 {
				for i := 0; i < 16; i++ {
					out[off+i] ^= prev[i]
				}
				blk.Encrypt(out[off:off+16], out[off:off+16])
				copy(prev, out[off:off+16])
			}
		}
	}
	return out
}

func muxFragment(seq int, baseDTS int64, samples [][]byte, dur uint32, tp *testProt, encs []*SampleEnc, keyflags []bool) []byte {
	// trun: data offset, duration, size, flags
	n := len(samples)
	var trun []byte
	trun = append(trun, mu32(uint32(n))...)
	trun = append(trun, mu32(0)...) // data offset patched below
	for i, s := range samples {
		fl := uint32(0x10000) // not a sync sample
		if keyflags[i] {
			fl = 0
		}
		trun = append(trun, mu32(dur)...)
		trun = append(trun, mu32(uint32(len(s)))...)
		trun = append(trun, mu32(fl)...)
	}
	var senc []byte
	if tp != nil {
		sb := mu32(uint32(n))
		hasSubs := false
		for _, e := range encs {
			if len(e.Subs) > 0 {
				hasSubs = true
			}
		}
		for _, e := range encs {
			if tp.ivSize > 0 {
				sb = append(sb, e.IV...)
			}
			if hasSubs {
				sb = append(sb, mu16(uint16(len(e.Subs)))...)
				for _, s := range e.Subs {
					sb = append(sb, mu16(s.Clear)...)
					sb = append(sb, mu32(s.Enc)...)
				}
			}
		}
		fl := uint32(0)
		if hasSubs {
			fl = 2
		}
		senc = mfullbox("senc", 0, fl, sb)
	}
	build := func(off uint32) []byte {
		t := append([]byte(nil), trun...)
		binary.BigEndian.PutUint32(t[4:], off)
		parts := [][]byte{
			mfullbox("tfhd", 0, 0x20000, mu32(1)),
			mfullbox("tfdt", 1, 0, binary.BigEndian.AppendUint64(nil, uint64(baseDTS))),
			mfullbox("trun", 0, 0x701, t),
		}
		if senc != nil {
			parts = append(parts, senc)
		}
		return mbox("moof", mfullbox("mfhd", 0, 0, mu32(uint32(seq))), mbox("traf", parts...))
	}
	moof := build(0)
	moof = build(uint32(len(moof) + 8))
	var data []byte
	for _, s := range samples {
		data = append(data, s...)
	}
	return append(moof, mbox("mdat", data)...)
}

type clip struct {
	vs   *VideoSamples
	init []byte
	frag []byte
}

func loadClip(t *testing.T, tp *testProt, perFrag int) *clip {
	t.Helper()
	data, err := os.ReadFile("testdata/h264_baseline_cavlc.mp4")
	if err != nil {
		t.Skip(err)
	}
	vs, err := ExtractVideoSamples(data)
	if err != nil {
		t.Fatal(err)
	}
	c := &clip{vs: vs, init: muxInit(vs.Description, vs.Width, vs.Height, tp)}
	for i := 0; i < len(vs.Chunks); i += perFrag {
		end := min(i+perFrag, len(vs.Chunks))
		var samples [][]byte
		var encs []*SampleEnc
		var keys []bool
		for k := i; k < end; k++ {
			s := vs.Chunks[k].Data
			keys = append(keys, vs.Chunks[k].Key)
			if tp != nil {
				iv := make([]byte, 16)
				for j := range iv {
					iv[j] = byte(k*7 + j)
				}
				if tp.ivSize == 8 {
					iv = iv[:8]
				}
				e := &SampleEnc{IV: iv}
				if tp.ivSize == 0 {
					e.IV = nil
				}
				if tp.subsamples {
					e.Subs = avccSubsamples(s, 5)
				}
				use := iv
				if tp.ivSize == 0 {
					use = tp.constIV
				}
				s = encryptSample(tp, tp.key, use, e.Subs, s)
				encs = append(encs, e)
			}
			samples = append(samples, s)
		}
		c.frag = append(c.frag, muxFragment(i/perFrag+1, int64(i)*3000, samples, 3000, tp, encs, keys)...)
	}
	return c
}

func drain(st *TrackStore) []Sample {
	var out []Sample
	cur := -1.0
	for {
		s, ok, _ := st.Next(cur)
		if !ok {
			return out
		}
		out = append(out, s)
		cur = s.DTS
	}
}

func TestCommonEncryptionSchemes(t *testing.T) {
	key := []byte("0123456789abcdef")
	kid := [16]byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	cases := []struct {
		name string
		tp   testProt
	}{
		{"cenc 8-byte IV, subsamples", testProt{scheme: "cenc", ivSize: 8, subsamples: true}},
		{"cenc 16-byte IV, whole sample", testProt{scheme: "cenc", ivSize: 16}},
		{"cens pattern 1:9, subsamples", testProt{scheme: "cens", ivSize: 16, crypt: 1, skip: 9, subsamples: true}},
		{"cbc1 whole sample", testProt{scheme: "cbc1", ivSize: 16}},
		{"cbcs pattern 1:9, constant IV, subsamples", testProt{scheme: "cbcs", ivSize: 0, constIV: []byte("fedcba9876543210"), crypt: 1, skip: 9, subsamples: true}},
		{"cbcs pattern 5:5, per-sample IV", testProt{scheme: "cbcs", ivSize: 16, crypt: 5, skip: 5, subsamples: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tp := c.tp
			tp.kid, tp.key, tp.wantPSSH = kid, key, true
			cl := loadClip(t, &tp, 6)
			b := NewMSEBuffer(`video/mp4; codecs="avc1.42c00c"`)
			res, err := b.Append(cl.init, 0)
			if err != nil {
				t.Fatal(err)
			}
			if res.Init == nil || len(res.Init.InitData) != 1 || res.Init.InitData[0].Type != "cenc" || !bytes.Contains(res.Init.InitData[0].Data, kid[:]) {
				t.Fatalf("init data %+v", res.Init)
			}
			if _, err := b.Append(cl.frag, 0); err != nil {
				t.Fatal(err)
			}
			st := b.FirstStore("video")
			cfg := st.Config()
			if cfg.Prot == nil || cfg.Prot.Scheme != tp.scheme || cfg.Prot.KID != kid || cfg.CodecName != "h264" {
				t.Fatalf("track config %+v", cfg)
			}
			samples := drain(st)
			if len(samples) != len(cl.vs.Chunks) {
				t.Fatalf("%d samples, want %d", len(samples), len(cl.vs.Chunks))
			}
			// without a key the samples cannot be read
			if _, err := samples[0].Bytes(); err != ErrNoKey {
				t.Fatalf("no key: %v", err)
			}
			ks := NewKeyStore()
			b.SetKeyStore(ks)
			if _, err := samples[0].Bytes(); err != ErrNoKey {
				t.Fatalf("empty key store: %v", err)
			}
			ks.Add(kid, key)
			for i, s := range samples {
				if !s.Encrypted() {
					t.Fatalf("sample %d is not marked encrypted", i)
				}
				got, err := s.Bytes()
				if err != nil || !bytes.Equal(got, cl.vs.Chunks[i].Data) {
					t.Fatalf("sample %d: decrypted differently from the original (%v)", i, err)
				}
			}
			// a wrong key gives garbage, not the original
			ks.Add(kid, []byte("wrong-key-000000"))
			if got, _ := samples[1].Bytes(); bytes.Equal(got, cl.vs.Chunks[1].Data) {
				t.Fatal("a wrong key decrypted the sample")
			}
			// the decrypted stream is a decodable H.264 stream
			ks.Add(kid, key)
			dec, err := NewVideoDecoder(cfg)
			if err != nil {
				t.Fatal(err)
			}
			pics := 0
			for _, s := range samples {
				d, _ := s.Bytes()
				if p, err := dec.Decode(d); err == nil && p.Valid() {
					pics++
				}
			}
			if pics != len(samples) {
				t.Fatalf("decoded %d of %d pictures", pics, len(samples))
			}
		})
	}
}

// Clear tracks and tracks without a protection box are untouched, and key IDs without a pssh box are reported
// as "keyids" init data.
func TestEncryptedWithoutPSSHReportsKeyIDs(t *testing.T) {
	tp := testProt{scheme: "cenc", ivSize: 8, kid: [16]byte{1, 2, 3}, key: []byte("0123456789abcdef"), subsamples: true}
	cl := loadClip(t, &tp, 8)
	b := NewMSEBuffer(`video/mp4; codecs="avc1.42c00c"`)
	res, err := b.Append(cl.init, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Init == nil || len(res.Init.InitData) != 1 || res.Init.InitData[0].Type != "keyids" || string(res.Init.InitData[0].Data) != `{"kids":["AQIDAAAAAAAAAAAAAAAAAA"]}` {
		t.Fatalf("%+v", res.Init)
	}
	clear := loadClip(t, nil, 8)
	b2 := NewMSEBuffer(`video/mp4; codecs="avc1.42c00c"`)
	r2, _ := b2.Append(clear.init, 0)
	if len(r2.Init.InitData) != 0 {
		t.Fatalf("clear stream reports init data: %+v", r2.Init.InitData)
	}
	if _, err := b2.Append(clear.frag, 0); err != nil {
		t.Fatal(err)
	}
	for i, s := range drain(b2.FirstStore("video")) {
		d, err := s.Bytes()
		if err != nil || s.Encrypted() || !bytes.Equal(d, clear.vs.Chunks[i].Data) {
			t.Fatalf("clear sample %d: %v", i, err)
		}
	}
}

// Auxiliary information in saiz/saio (instead of senc) is read as well.
func TestSaizSaioAuxiliaryInfo(t *testing.T) {
	// two samples: IV 8 bytes + 1 subsample each, laid out back to back after the moof
	aux := []byte{}
	for i := 0; i < 2; i++ {
		aux = append(aux, bytes.Repeat([]byte{byte(i + 1)}, 8)...)
		aux = append(aux, mu16(1)...)
		aux = append(aux, mu16(4)...)
		aux = append(aux, mu32(uint32(100+i))...)
	}
	saiz := mfullbox("saiz", 0, 0, []byte{16}, mu32(2))
	saio := mfullbox("saio", 0, 0, mu32(1), mu32(0))
	mdat := aux
	area := func(off int64, n int) []byte {
		if off < 0 || int(off)+n > len(mdat) {
			return nil
		}
		return mdat[off : int(off)+n]
	}
	got := parseAux(saiz[8:], saio[8:], 0, area, 8, true)
	if len(got) != 2 || got[1].IV[0] != 2 || len(got[1].Subs) != 1 || got[1].Subs[0].Enc != 101 || got[0].Subs[0].Clear != 4 {
		t.Fatalf("%+v", got)
	}
}
