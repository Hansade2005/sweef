package cdm

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

const aesBlock = 16

// DecryptSample decrypts one common-encryption sample (ISO/IEC 23001-7) with a 16-byte AES key. It is the
// reference implementation behind Clear Key; CDMs that hold their keys in their own memory do not use it.
//
//	cenc  AES-CTR over the concatenated cipher bytes of all subsamples (one counter for the sample).
//	cens  AES-CTR with a crypt:skip pattern inside each subsample's cipher bytes; skipped blocks do not advance
//	      the counter.
//	cbc1  AES-CBC over whole blocks of each subsample's cipher bytes; the chain continues across subsamples.
//	cbcs  AES-CBC with a crypt:skip pattern; the IV is reset at the start of each subsample and the chain
//	      continues over the encrypted blocks inside it.
//
// For all CBC modes and for cens, a trailing partial block of a protected range stays in the clear. A sample
// without subsamples is one protected range. The result is a new slice; Data is not modified.
func DecryptSample(key []byte, s *Sample) ([]byte, error) {
	out := append([]byte(nil), s.Data...)
	if s.Scheme == SchemeNone {
		return out, nil
	}
	if len(key) != aesBlock {
		return nil, fmt.Errorf("%w: key is %d bytes, want 16", ErrDecrypt, len(key))
	}
	subs := s.Subsamples
	if len(subs) == 0 {
		subs = []Subsample{{ClearBytes: 0, CipherBytes: uint32(len(out))}}
	}
	var total uint64
	for _, ss := range subs {
		total += uint64(ss.ClearBytes) + uint64(ss.CipherBytes)
	}
	if total != uint64(len(out)) {
		return nil, fmt.Errorf("%w: subsamples cover %d bytes of a %d byte sample", ErrDecrypt, total, len(out))
	}
	iv, err := expandIV(s.IV)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	pat := s.Pattern
	switch s.Scheme {
	case SchemeCenc:
		ctr := cipher.NewCTR(blk, iv)
		pos := 0
		for _, ss := range subs {
			pos += int(ss.ClearBytes)
			n := int(ss.CipherBytes)
			ctr.XORKeyStream(out[pos:pos+n], out[pos:pos+n])
			pos += n
		}
	case SchemeCens:
		ctr := cipher.NewCTR(blk, iv)
		pos := 0
		for _, ss := range subs {
			pos += int(ss.ClearBytes)
			n := int(ss.CipherBytes)
			forEachCryptRun(out[pos:pos+n], pat, func(run []byte) { ctr.XORKeyStream(run, run) })
			pos += n
		}
	case SchemeCbc1:
		prev := iv
		pos := 0
		for _, ss := range subs {
			pos += int(ss.ClearBytes)
			n := int(ss.CipherBytes)
			n -= n % aesBlock
			if n > 0 {
				prev = cbcDecrypt(blk, prev, out[pos:pos+n])
			}
			pos += int(ss.CipherBytes)
		}
	case SchemeCbcs:
		pos := 0
		for _, ss := range subs {
			pos += int(ss.ClearBytes)
			n := int(ss.CipherBytes)
			prev := iv
			forEachCryptRun(out[pos:pos+n], pat, func(run []byte) { prev = cbcDecrypt(blk, prev, run) })
			pos += n
		}
	default:
		return nil, fmt.Errorf("%w: unknown scheme %q", ErrUnsupported, s.Scheme)
	}
	return out, nil
}

// forEachCryptRun calls fn for each encrypted run (whole blocks only) of a protected range under a crypt:skip
// block pattern. With a 0:0 pattern all whole blocks of the range are one run.
func forEachCryptRun(rng []byte, p Pattern, fn func([]byte)) {
	if p.CryptBlocks == 0 && p.SkipBlocks == 0 {
		n := len(rng) - len(rng)%aesBlock
		if n > 0 {
			fn(rng[:n])
		}
		return
	}
	crypt := int(p.CryptBlocks) * aesBlock
	skip := int(p.SkipBlocks) * aesBlock
	for len(rng) > 0 {
		n := min(crypt, len(rng))
		n -= n % aesBlock // a short final run protects whole blocks only
		if n == 0 {
			return
		}
		fn(rng[:n])
		rng = rng[n:]
		s := min(skip, len(rng))
		rng = rng[s:]
	}
}

// cbcDecrypt decrypts whole blocks in place and returns the last ciphertext block (the next chain value).
func cbcDecrypt(blk cipher.Block, iv []byte, buf []byte) []byte {
	last := append([]byte(nil), buf[len(buf)-aesBlock:]...)
	cipher.NewCBCDecrypter(blk, iv).CryptBlocks(buf, buf)
	return last
}

// expandIV turns an 8 byte (cenc) or 16 byte IV into the 16 byte counter block / CBC IV.
func expandIV(iv []byte) ([]byte, error) {
	switch len(iv) {
	case 8:
		out := make([]byte, 16)
		copy(out, iv)
		return out, nil
	case 16:
		return append([]byte(nil), iv...), nil
	}
	return nil, fmt.Errorf("%w: IV is %d bytes, want 8 or 16", ErrDecrypt, len(iv))
}
