package media

// cenc.go: Common Encryption (ISO/IEC 23001-7) for fragmented MP4: reading the protection boxes (sinf/schm/tenc,
// senc, saiz/saio, pssh) and decrypting samples with a key the page supplied through EME (Clear Key). All four
// protection schemes are handled:
//
//	cenc  AES-128 CTR over the protected bytes of the sample (the key stream runs on across subsamples)
//	cens  AES-128 CTR, pattern encryption (crypt:skip blocks of 16 bytes in every protected range)
//	cbc1  AES-128 CBC, no pattern
//	cbcs  AES-128 CBC, pattern encryption, the chain restarts at the IV for every subsample
//
// Samples stay encrypted in the TrackStore; Sample.Bytes decrypts on every read, so a seek or a second decode never
// sees plaintext that was kept around.

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrNoKey is returned by Sample.Bytes for an encrypted sample whose key has not been supplied yet.
var ErrNoKey = errors.New("media: the decryption key is not available")

// Protection is the track-level protection scheme information (the sinf box and its tenc defaults).
type Protection struct {
	Scheme      string // "cenc", "cens", "cbc1", "cbcs"
	IVSize      int    // per-sample IV size in bytes; 0 means the constant IV is used
	KID         [16]byte
	ConstIV     []byte
	CryptBlocks int // pattern: encrypted blocks ...
	SkipBlocks  int // ... then clear blocks (both 0: every block is encrypted)
	Protected   bool
}

// Subsample is one clear run followed by one encrypted run of a sample.
type Subsample struct {
	Clear uint16
	Enc   uint32
}

// SampleEnc is the per-sample encryption information (senc, or saiz/saio auxiliary data).
type SampleEnc struct {
	IV   []byte
	Subs []Subsample
}

// KeyStore holds content keys by key ID. It is filled by the Clear Key license handling of the page.
type KeyStore struct {
	mu sync.RWMutex
	m  map[[16]byte][]byte
}

// NewKeyStore creates an empty store.
func NewKeyStore() *KeyStore { return &KeyStore{m: map[[16]byte][]byte{}} }

// Add stores a key (16 bytes) for a key ID.
func (k *KeyStore) Add(kid [16]byte, key []byte) {
	k.mu.Lock()
	k.m[kid] = append([]byte(nil), key...)
	k.mu.Unlock()
}

// Get returns the key for a key ID, or nil.
func (k *KeyStore) Get(kid [16]byte) []byte {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.m[kid]
}

// KeyHolder points at the KeyStore of the MediaKeys currently attached to the media element that plays a buffer
// (nil while none is).
type KeyHolder struct{ p atomic.Pointer[KeyStore] }

// Set attaches or detaches (nil) a key store.
func (h *KeyHolder) Set(k *KeyStore) { h.p.Store(k) }

// Get returns the attached key store.
func (h *KeyHolder) Get() *KeyStore { return h.p.Load() }

// Encrypted reports whether the sample's payload is encrypted.
func (s Sample) Encrypted() bool { return s.enc != nil && s.prot != nil && s.prot.Protected }

// KID is the key ID the sample is encrypted with.
func (s Sample) KID() [16]byte {
	if s.prot != nil {
		return s.prot.KID
	}
	return [16]byte{}
}

// ---- decryption -----------------------------------------------------------------------------------------

// DecryptSample returns the plaintext of an encrypted sample payload (data is not modified).
func DecryptSample(p *Protection, e *SampleEnc, key, data []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, errors.New("media: the content key is not 16 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := e.IV
	if len(iv) == 0 {
		iv = p.ConstIV
	}
	if len(iv) != 8 && len(iv) != 16 {
		return nil, fmt.Errorf("media: bad initialization vector (%d bytes)", len(iv))
	}
	iv16 := make([]byte, 16)
	copy(iv16, iv)

	// the protected byte ranges of the sample
	type rng struct{ off, n int }
	var rs []rng
	if len(e.Subs) == 0 {
		rs = []rng{{0, len(data)}}
	} else {
		pos := 0
		for _, s := range e.Subs {
			pos += int(s.Clear)
			n := int(s.Enc)
			if pos+n > len(data) {
				return nil, errors.New("media: subsample map exceeds the sample")
			}
			if n > 0 {
				rs = append(rs, rng{pos, n})
			}
			pos += n
		}
	}
	out := append([]byte(nil), data...)
	pattern := p.CryptBlocks > 0 // pattern 0:0 means every block is encrypted

	switch p.Scheme {
	case "cenc", "cens":
		ctr := cipher.NewCTR(block, iv16)
		for _, r := range rs {
			seg := out[r.off : r.off+r.n]
			if p.Scheme == "cenc" || !pattern {
				ctr.XORKeyStream(seg, seg)
				continue
			}
			forEachPatternBlock(len(seg), p.CryptBlocks, p.SkipBlocks, func(off, n int) {
				ctr.XORKeyStream(seg[off:off+n], seg[off:off+n])
			})
		}
	case "cbc1", "cbcs":
		prev := append([]byte(nil), iv16...)
		for _, r := range rs {
			seg := out[r.off : r.off+r.n]
			if p.Scheme == "cbcs" {
				prev = append(prev[:0], iv16...) // the chain restarts for every subsample
			}
			decBlock := func(off int) {
				var ct [16]byte
				copy(ct[:], seg[off:off+16])
				block.Decrypt(seg[off:off+16], seg[off:off+16])
				for i := 0; i < 16; i++ {
					seg[off+i] ^= prev[i]
				}
				copy(prev, ct[:])
			}
			if p.Scheme == "cbc1" || !pattern {
				for off := 0; off+16 <= len(seg); off += 16 {
					decBlock(off)
				}
				continue
			}
			forEachPatternBlock(len(seg), p.CryptBlocks, p.SkipBlocks, func(off, n int) {
				for o := off; o < off+n; o += 16 {
					decBlock(o)
				}
			})
		}
	default:
		return nil, fmt.Errorf("media: unsupported protection scheme %q", p.Scheme)
	}
	return out, nil
}

// forEachPatternBlock calls f for each run of encrypted whole blocks of a range of size n under the pattern
// crypt:skip (in blocks of 16 bytes); a partial block at the end of the range stays clear.
func forEachPatternBlock(n, crypt, skip int, f func(off, n int)) {
	blocks := n / 16
	for b := 0; b < blocks; {
		c := min(crypt, blocks-b)
		f(b*16, c*16)
		b += crypt + skip
	}
}

// Bytes returns the coded payload, decrypted when the sample is encrypted and its key is known.
func (s Sample) Bytes() ([]byte, error) {
	var data []byte
	if s.data != nil || s.load == nil {
		data = s.data
	} else {
		var err error
		if data, err = s.load(); err != nil {
			return nil, err
		}
	}
	if !s.Encrypted() {
		return data, nil
	}
	var ks *KeyStore
	if s.kh != nil {
		ks = s.kh.Get()
	}
	if ks == nil {
		return nil, ErrNoKey
	}
	key := ks.Get(s.prot.KID)
	if key == nil {
		return nil, ErrNoKey
	}
	return DecryptSample(s.prot, s.enc, key, data)
}

// ---- boxes ------------------------------------------------------------------------------------------------

// parseSinf reads a protection scheme information box: the original sample entry format and the protection.
func parseSinf(sinf mp4box) (orig string, p *Protection) {
	kids := parseBoxes(sinf.data)
	if f := findBox(kids, "frma"); f != nil && len(f.data) >= 4 {
		orig = string(f.data[:4])
	}
	p = &Protection{Scheme: "cenc"}
	if s := findBox(kids, "schm"); s != nil && len(s.data) >= 8 {
		p.Scheme = string(s.data[4:8])
	}
	if schi := findBox(kids, "schi"); schi != nil {
		if t := findBox(parseBoxes(schi.data), "tenc"); t != nil && len(t.data) >= 24 {
			d := t.data
			if d[0] >= 1 {
				p.CryptBlocks, p.SkipBlocks = int(d[5]>>4), int(d[5]&15)
			}
			p.Protected = d[6] == 1
			p.IVSize = int(d[7])
			copy(p.KID[:], d[8:24])
			if p.Protected && p.IVSize == 0 && len(d) >= 25 {
				n := int(d[24])
				if 25+n <= len(d) {
					p.ConstIV = append([]byte(nil), d[25:25+n]...)
				}
			}
		}
	}
	return
}

// parseSenc reads a SampleEncryptionBox: one entry per sample of the fragment.
func parseSenc(b []byte, ivSize int) ([]*SampleEnc, error) {
	if len(b) < 8 {
		return nil, errors.New("media: short senc")
	}
	flags := be32(b) & 0xffffff
	n := int(be32(b[4:]))
	q := 8
	out := make([]*SampleEnc, 0, n)
	for i := 0; i < n; i++ {
		e := &SampleEnc{}
		if q+ivSize > len(b) {
			return out, errors.New("media: truncated senc")
		}
		e.IV = append([]byte(nil), b[q:q+ivSize]...)
		q += ivSize
		if flags&2 != 0 {
			if q+2 > len(b) {
				return out, errors.New("media: truncated senc")
			}
			k := int(be16(b[q:]))
			q += 2
			if q+6*k > len(b) {
				return out, errors.New("media: truncated senc")
			}
			for j := 0; j < k; j++ {
				e.Subs = append(e.Subs, Subsample{Clear: uint16(be16(b[q:])), Enc: be32(b[q+2:])})
				q += 6
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// parseAux reads sample auxiliary information laid out per saiz/saio (the alternative to senc): sizes[i] bytes per
// sample at the offsets of saio, relative to base.
func parseAux(saiz, saio []byte, base int64, area func(off int64, n int) []byte, ivSize int, subs bool) []*SampleEnc {
	if len(saiz) < 9 || len(saio) < 8 {
		return nil
	}
	zf := be32(saiz) & 0xffffff
	q := 4
	if zf&1 != 0 {
		q += 8
	}
	if q+5 > len(saiz) {
		return nil
	}
	def := int(saiz[q])
	cnt := int(be32(saiz[q+1:]))
	q += 5
	of := be32(saio) & 0xffffff
	ver := saio[0]
	p := 4
	if of&1 != 0 {
		p += 8
	}
	if p+4 > len(saio) {
		return nil
	}
	ec := int(be32(saio[p:]))
	p += 4
	if ec < 1 {
		return nil
	}
	var off int64
	if ver == 0 && p+4 <= len(saio) {
		off = int64(be32(saio[p:]))
	} else if ver == 1 && p+8 <= len(saio) {
		off = int64(be64(saio[p:]))
	} else {
		return nil
	}
	var out []*SampleEnc
	pos := base + off
	for i := 0; i < cnt; i++ {
		sz := def
		if def == 0 {
			if q+i >= len(saiz) {
				return out
			}
			sz = int(saiz[q+i])
		}
		d := area(pos, sz)
		if d == nil || len(d) < ivSize {
			return out
		}
		e := &SampleEnc{IV: append([]byte(nil), d[:ivSize]...)}
		if subs && len(d) >= ivSize+2 {
			k := int(be16(d[ivSize:]))
			r := ivSize + 2
			for j := 0; j < k && r+6 <= len(d); j++ {
				e.Subs = append(e.Subs, Subsample{Clear: uint16(be16(d[r:])), Enc: be32(d[r+2:])})
				r += 6
			}
		}
		out = append(out, e)
		pos += int64(sz)
	}
	return out
}

// psshBoxes returns the raw pssh boxes (header included) among kids.
func psshBoxes(kids []mp4box) [][]byte {
	var out [][]byte
	for _, k := range kids {
		if k.typ != "pssh" {
			continue
		}
		b := make([]byte, 8+len(k.data))
		b[0], b[1], b[2], b[3] = byte(len(b)>>24), byte(len(b)>>16), byte(len(b)>>8), byte(len(b))
		copy(b[4:], "pssh")
		copy(b[8:], k.data)
		out = append(out, b)
	}
	return out
}

// b64url encodes b the way EME key IDs are written (base64url, no padding).
func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// B64URL decodes the base64url strings of Clear Key licenses and key ID lists (padding is optional).
func B64URL(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	return base64.RawURLEncoding.DecodeString(s)
}
