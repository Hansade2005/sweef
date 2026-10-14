package engine

// platform_crypto.go: the primitives behind crypto.subtle (platformjs/crypto.js).
//
// The JavaScript side owns the WebCrypto contract (algorithm normalisation,
// usages, extractability, CryptoKey objects, DOMException names). This file is
// a stateless toolbox over the Go standard library: every call receives the key
// material it needs (raw bytes for secret keys, PKCS#8 / SPKI DER for
// asymmetric keys) and returns bytes. Nothing is cached, so a VM that goes
// away leaves nothing behind. All binary values travel as standard base64 in a
// JSON object because host functions only carry JSON-like values.

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
)

type cm = map[string]any

func cryptoErr(name, f string, a ...any) error {
	return fmt.Errorf("%s: %s", name, fmt.Sprintf(f, a...))
}

func cbytes(m cm, k string) []byte {
	s, _ := m[k].(string)
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

func cstr(m cm, k string) string { s, _ := m[k].(string); return s }

func cint(m cm, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

func cbool(m cm, k string) bool { b, _ := m[k].(bool); return b }

func cb64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func b64urlEnc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64uDec(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func cryptoHash(name string) (func() hash.Hash, crypto.Hash, error) {
	switch strings.ToUpper(name) {
	case "SHA-1":
		return sha1.New, crypto.SHA1, nil
	case "SHA-256":
		return sha256.New, crypto.SHA256, nil
	case "SHA-384":
		return sha512.New384, crypto.SHA384, nil
	case "SHA-512":
		return sha512.New, crypto.SHA512, nil
	}
	return nil, 0, cryptoErr("NotSupportedError", "unrecognized hash %q", name)
}

func ecCurve(name string) (ecdh.Curve, elliptic.Curve, error) {
	switch name {
	case "P-256":
		return ecdh.P256(), elliptic.P256(), nil
	case "P-384":
		return ecdh.P384(), elliptic.P384(), nil
	case "P-521":
		return ecdh.P521(), elliptic.P521(), nil
	}
	return nil, nil, cryptoErr("NotSupportedError", "unsupported named curve %q", name)
}

func curveName(c elliptic.Curve) string {
	switch c {
	case elliptic.P256():
		return "P-256"
	case elliptic.P384():
		return "P-384"
	case elliptic.P521():
		return "P-521"
	}
	return ""
}

func parsePriv(der []byte) (any, error) {
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, cryptoErr("DataError", "invalid private key: %v", err)
	}
	return k, nil
}

func parsePub(der []byte) (any, error) {
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, cryptoErr("DataError", "invalid public key: %v", err)
	}
	return k, nil
}

// webcrypto dispatches one operation. in is the decoded JSON argument object.
func webcrypto(op string, in cm) (cm, error) {
	switch op {
	case "digest":
		nf, _, err := cryptoHash(cstr(in, "hash"))
		if err != nil {
			return nil, err
		}
		h := nf()
		h.Write(cbytes(in, "data"))
		return cm{"d": cb64(h.Sum(nil))}, nil
	case "hmac":
		nf, _, err := cryptoHash(cstr(in, "hash"))
		if err != nil {
			return nil, err
		}
		m := hmac.New(nf, cbytes(in, "key"))
		m.Write(cbytes(in, "data"))
		return cm{"d": cb64(m.Sum(nil))}, nil
	case "hmacVerify":
		nf, _, err := cryptoHash(cstr(in, "hash"))
		if err != nil {
			return nil, err
		}
		m := hmac.New(nf, cbytes(in, "key"))
		m.Write(cbytes(in, "data"))
		return cm{"ok": subtle.ConstantTimeCompare(m.Sum(nil), cbytes(in, "sig")) == 1}, nil
	case "aes":
		return cryptoAES(in)
	case "hkdf":
		nf, _, err := cryptoHash(cstr(in, "hash"))
		if err != nil {
			return nil, err
		}
		n := cint(in, "len")
		out, err := hkdf.Key(nf, cbytes(in, "ikm"), cbytes(in, "salt"), string(cbytes(in, "info")), n)
		if err != nil {
			return nil, cryptoErr("OperationError", "%v", err)
		}
		return cm{"d": cb64(out)}, nil
	case "pbkdf2":
		nf, _, err := cryptoHash(cstr(in, "hash"))
		if err != nil {
			return nil, err
		}
		out, err := pbkdf2.Key(nf, string(cbytes(in, "pw")), cbytes(in, "salt"), cint(in, "iter"), cint(in, "len"))
		if err != nil {
			return nil, cryptoErr("OperationError", "%v", err)
		}
		return cm{"d": cb64(out)}, nil
	case "generate":
		return cryptoGenerate(in)
	case "derive":
		return cryptoDerive(in)
	case "sign", "verify":
		return cryptoSign(op == "sign", in)
	case "rsaoaep":
		return cryptoOAEP(in)
	case "export":
		return cryptoExport(in)
	case "import":
		return cryptoImport(in)
	case "publicOf":
		return cryptoPublicOf(in)
	}
	return nil, cryptoErr("NotSupportedError", "unknown operation %q", op)
}

func (j *jsVM) cryptoHost(f map[string]hostFn) {
	f["cryptoRandom"] = func(a []any) (any, error) {
		n := int(argI(a, 0))
		if n < 0 || n > 1<<20 {
			return nil, cryptoErr("QuotaExceededError", "too many random bytes requested")
		}
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			return nil, cryptoErr("OperationError", "no random source: %v", err)
		}
		return cb64(b), nil
	}
	f["webcrypto"] = func(a []any) (any, error) {
		var in cm
		if s := argS(a, 1); s != "" {
			if err := json.Unmarshal([]byte(s), &in); err != nil {
				return nil, cryptoErr("TypeError", "bad arguments")
			}
		}
		if in == nil {
			in = cm{}
		}
		out, err := webcrypto(argS(a, 0), in)
		if err != nil {
			return nil, err
		}
		s, _ := json.Marshal(out)
		return string(s), nil
	}
}

// ---- AES

func cryptoAES(in cm) (cm, error) {
	key, data := cbytes(in, "key"), cbytes(in, "data")
	mode, enc := cstr(in, "mode"), cbool(in, "enc")
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, cryptoErr("OperationError", "bad AES key length %d", len(key))
	}
	switch mode {
	case "GCM":
		iv, aad := cbytes(in, "iv"), cbytes(in, "aad")
		tag := cint(in, "tagLen") / 8
		if tag == 0 {
			tag = 16
		}
		var g cipher.AEAD
		switch {
		case len(iv) == 12 && tag >= 12 && tag <= 16:
			g, err = cipher.NewGCMWithTagSize(blk, tag)
		case len(iv) > 0 && tag == 16:
			g, err = cipher.NewGCMWithNonceSize(blk, len(iv))
		default:
			return nil, cryptoErr("OperationError", "unsupported AES-GCM iv/tag length combination")
		}
		if err != nil {
			return nil, cryptoErr("OperationError", "%v", err)
		}
		if enc {
			return cm{"d": cb64(g.Seal(nil, iv, data, aad))}, nil
		}
		out, err := g.Open(nil, iv, data, aad)
		if err != nil {
			return nil, cryptoErr("OperationError", "The operation failed for an operation-specific reason")
		}
		return cm{"d": cb64(out)}, nil
	case "CBC":
		iv := cbytes(in, "iv")
		if len(iv) != 16 {
			return nil, cryptoErr("OperationError", "AES-CBC iv must be 16 bytes")
		}
		if enc {
			pad := 16 - len(data)%16
			p := append(append([]byte{}, data...), make([]byte, pad)...)
			for i := len(data); i < len(p); i++ {
				p[i] = byte(pad)
			}
			out := make([]byte, len(p))
			cipher.NewCBCEncrypter(blk, iv).CryptBlocks(out, p)
			return cm{"d": cb64(out)}, nil
		}
		if len(data) == 0 || len(data)%16 != 0 {
			return nil, cryptoErr("OperationError", "The operation failed for an operation-specific reason")
		}
		out := make([]byte, len(data))
		cipher.NewCBCDecrypter(blk, iv).CryptBlocks(out, data)
		pad := int(out[len(out)-1])
		if pad == 0 || pad > 16 || pad > len(out) {
			return nil, cryptoErr("OperationError", "The operation failed for an operation-specific reason")
		}
		bad := 0
		for _, c := range out[len(out)-pad:] {
			bad |= int(c) ^ pad
		}
		if bad != 0 {
			return nil, cryptoErr("OperationError", "The operation failed for an operation-specific reason")
		}
		return cm{"d": cb64(out[:len(out)-pad])}, nil
	case "CTR":
		ctr := cbytes(in, "counter")
		length := cint(in, "length")
		if len(ctr) != 16 || length < 1 || length > 128 {
			return nil, cryptoErr("OperationError", "bad AES-CTR counter")
		}
		out := make([]byte, len(data))
		c := append([]byte{}, ctr...)
		ks := make([]byte, 16)
		for off := 0; off < len(data); off += 16 {
			blk.Encrypt(ks, c)
			n := min(16, len(data)-off)
			for i := 0; i < n; i++ {
				out[off+i] = data[off+i] ^ ks[i]
			}
			ctrInc(c, length)
		}
		return cm{"d": cb64(out)}, nil
	case "KW":
		if enc {
			out, err := aesKeyWrap(blk, data)
			if err != nil {
				return nil, err
			}
			return cm{"d": cb64(out)}, nil
		}
		out, err := aesKeyUnwrap(blk, data)
		if err != nil {
			return nil, err
		}
		return cm{"d": cb64(out)}, nil
	}
	return nil, cryptoErr("NotSupportedError", "unsupported AES mode %q", mode)
}

// ctrInc adds one to the low `bits` bits of the big-endian counter block, wrapping within them.
func ctrInc(c []byte, bits int) {
	for i := len(c) - 1; i >= 0 && bits > 0; i-- {
		if bits >= 8 {
			c[i]++
			bits -= 8
			if c[i] != 0 {
				return
			}
			continue
		}
		mask := byte(1<<bits - 1)
		c[i] = c[i]&^mask | (c[i]+1)&mask
		return
	}
}

var aesKWIV = []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}

func aesKeyWrap(b cipher.Block, p []byte) ([]byte, error) {
	if len(p) < 16 || len(p)%8 != 0 {
		return nil, cryptoErr("OperationError", "AES-KW input must be a multiple of 8 bytes, at least 16")
	}
	n := len(p) / 8
	r := make([][]byte, n)
	for i := range r {
		r[i] = append([]byte{}, p[i*8:i*8+8]...)
	}
	a := append([]byte{}, aesKWIV...)
	buf := make([]byte, 16)
	for j := 0; j < 6; j++ {
		for i := 0; i < n; i++ {
			copy(buf, a)
			copy(buf[8:], r[i])
			b.Encrypt(buf, buf)
			t := uint64(n*j + i + 1)
			copy(a, buf[:8])
			var tb [8]byte
			binary.BigEndian.PutUint64(tb[:], t)
			for k := 0; k < 8; k++ {
				a[k] ^= tb[k]
			}
			copy(r[i], buf[8:])
		}
	}
	out := append([]byte{}, a...)
	for _, x := range r {
		out = append(out, x...)
	}
	return out, nil
}

func aesKeyUnwrap(b cipher.Block, c []byte) ([]byte, error) {
	if len(c) < 24 || len(c)%8 != 0 {
		return nil, cryptoErr("OperationError", "The operation failed for an operation-specific reason")
	}
	n := len(c)/8 - 1
	a := append([]byte{}, c[:8]...)
	r := make([][]byte, n)
	for i := range r {
		r[i] = append([]byte{}, c[8+i*8:16+i*8]...)
	}
	buf := make([]byte, 16)
	for j := 5; j >= 0; j-- {
		for i := n - 1; i >= 0; i-- {
			var tb [8]byte
			binary.BigEndian.PutUint64(tb[:], uint64(n*j+i+1))
			for k := 0; k < 8; k++ {
				buf[k] = a[k] ^ tb[k]
			}
			copy(buf[8:], r[i])
			b.Decrypt(buf, buf)
			copy(a, buf[:8])
			copy(r[i], buf[8:])
		}
	}
	if subtle.ConstantTimeCompare(a, aesKWIV) != 1 {
		return nil, cryptoErr("OperationError", "The operation failed for an operation-specific reason")
	}
	var out []byte
	for _, x := range r {
		out = append(out, x...)
	}
	return out, nil
}

// ---- asymmetric keys

func cryptoGenerate(in cm) (cm, error) {
	var priv any
	var err error
	switch cstr(in, "kind") {
	case "ec":
		_, c, e := ecCurve(cstr(in, "curve"))
		if e != nil {
			return nil, e
		}
		priv, err = ecdsa.GenerateKey(c, rand.Reader)
	case "x25519":
		priv, err = ecdh.X25519().GenerateKey(rand.Reader)
	case "ed25519":
		_, priv, err = ed25519.GenerateKey(rand.Reader)
	case "rsa":
		bits := cint(in, "bits")
		// 2048 is the floor for generated keys (NIST SP 800-57, and what CodeQL's weak-key check requires);
		// importing an existing smaller key still works.
		if bits < 2048 || bits > 8192 {
			return nil, cryptoErr("OperationError", "unsupported RSA modulus length %d (2048 to 8192)", bits)
		}
		if e := cint(in, "e"); e != 65537 {
			return nil, cryptoErr("OperationError", "only the public exponent 65537 is supported")
		}
		priv, err = rsa.GenerateKey(rand.Reader, bits)
	default:
		return nil, cryptoErr("NotSupportedError", "unsupported key kind")
	}
	if err != nil {
		return nil, cryptoErr("OperationError", "%v", err)
	}
	return keyPairDER(priv)
}

func keyPairDER(priv any) (cm, error) {
	p8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, cryptoErr("OperationError", "%v", err)
	}
	var pub any
	switch k := priv.(type) {
	case *ecdsa.PrivateKey:
		pub = &k.PublicKey
	case *ecdh.PrivateKey:
		pub = k.PublicKey()
	case ed25519.PrivateKey:
		pub = k.Public()
	case *rsa.PrivateKey:
		pub = &k.PublicKey
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, cryptoErr("OperationError", "%v", err)
	}
	return cm{"priv": cb64(p8), "pub": cb64(spki)}, nil
}

func cryptoPublicOf(in cm) (cm, error) {
	k, err := parsePriv(cbytes(in, "priv"))
	if err != nil {
		return nil, err
	}
	return keyPairDER(k)
}

func toECDHPriv(k any) (*ecdh.PrivateKey, error) {
	switch v := k.(type) {
	case *ecdh.PrivateKey:
		return v, nil
	case *ecdsa.PrivateKey:
		return v.ECDH()
	}
	return nil, errors.New("not an ECDH key")
}

func toECDHPub(k any) (*ecdh.PublicKey, error) {
	switch v := k.(type) {
	case *ecdh.PublicKey:
		return v, nil
	case *ecdsa.PublicKey:
		return v.ECDH()
	}
	return nil, errors.New("not an ECDH key")
}

func cryptoDerive(in cm) (cm, error) {
	pk, err := parsePriv(cbytes(in, "priv"))
	if err != nil {
		return nil, err
	}
	pbk, err := parsePub(cbytes(in, "pub"))
	if err != nil {
		return nil, err
	}
	a, e1 := toECDHPriv(pk)
	b, e2 := toECDHPub(pbk)
	if e1 != nil || e2 != nil {
		return nil, cryptoErr("InvalidAccessError", "ECDH needs EC or X25519 keys")
	}
	if a.Curve() != b.Curve() {
		return nil, cryptoErr("InvalidAccessError", "The public and private keys use different curves")
	}
	z, err := a.ECDH(b)
	if err != nil {
		return nil, cryptoErr("OperationError", "%v", err)
	}
	return cm{"d": cb64(z)}, nil
}

type ecSig struct{ R, S *big.Int }

func cryptoSign(sign bool, in cm) (cm, error) {
	alg, data := cstr(in, "alg"), cbytes(in, "data")
	var nf func() hash.Hash
	var ch crypto.Hash
	if h := cstr(in, "hash"); h != "" {
		var err error
		if nf, ch, err = cryptoHash(h); err != nil {
			return nil, err
		}
	}
	digest := func() []byte { h := nf(); h.Write(data); return h.Sum(nil) }
	if sign {
		pk, err := parsePriv(cbytes(in, "key"))
		if err != nil {
			return nil, err
		}
		switch alg {
		case "ECDSA":
			k, ok := pk.(*ecdsa.PrivateKey)
			if !ok {
				return nil, cryptoErr("InvalidAccessError", "not an ECDSA key")
			}
			der, err := ecdsa.SignASN1(rand.Reader, k, digest())
			if err != nil {
				return nil, cryptoErr("OperationError", "%v", err)
			}
			var s ecSig
			if _, err := asn1.Unmarshal(der, &s); err != nil {
				return nil, cryptoErr("OperationError", "%v", err)
			}
			n := (k.Curve.Params().BitSize + 7) / 8
			out := make([]byte, 2*n)
			s.R.FillBytes(out[:n])
			s.S.FillBytes(out[n:])
			return cm{"d": cb64(out)}, nil
		case "Ed25519":
			k, ok := pk.(ed25519.PrivateKey)
			if !ok {
				return nil, cryptoErr("InvalidAccessError", "not an Ed25519 key")
			}
			return cm{"d": cb64(ed25519.Sign(k, data))}, nil
		case "RSASSA-PKCS1-v1_5", "RSA-PSS":
			k, ok := pk.(*rsa.PrivateKey)
			if !ok {
				return nil, cryptoErr("InvalidAccessError", "not an RSA key")
			}
			var sig []byte
			if alg == "RSA-PSS" {
				sig, err = rsa.SignPSS(rand.Reader, k, ch, digest(), &rsa.PSSOptions{SaltLength: cint(in, "saltLen")})
			} else {
				sig, err = rsa.SignPKCS1v15(rand.Reader, k, ch, digest())
			}
			if err != nil {
				return nil, cryptoErr("OperationError", "%v", err)
			}
			return cm{"d": cb64(sig)}, nil
		}
		return nil, cryptoErr("NotSupportedError", "unsupported signature algorithm")
	}
	pb, err := parsePub(cbytes(in, "key"))
	if err != nil {
		return nil, err
	}
	sig := cbytes(in, "sig")
	switch alg {
	case "ECDSA":
		k, ok := pb.(*ecdsa.PublicKey)
		if !ok {
			return nil, cryptoErr("InvalidAccessError", "not an ECDSA key")
		}
		n := (k.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*n {
			return cm{"ok": false}, nil
		}
		der, _ := asn1.Marshal(ecSig{new(big.Int).SetBytes(sig[:n]), new(big.Int).SetBytes(sig[n:])})
		return cm{"ok": ecdsa.VerifyASN1(k, digest(), der)}, nil
	case "Ed25519":
		k, ok := pb.(ed25519.PublicKey)
		if !ok {
			return nil, cryptoErr("InvalidAccessError", "not an Ed25519 key")
		}
		return cm{"ok": len(sig) == ed25519.SignatureSize && ed25519.Verify(k, data, sig)}, nil
	case "RSASSA-PKCS1-v1_5", "RSA-PSS":
		k, ok := pb.(*rsa.PublicKey)
		if !ok {
			return nil, cryptoErr("InvalidAccessError", "not an RSA key")
		}
		if alg == "RSA-PSS" {
			return cm{"ok": rsa.VerifyPSS(k, ch, digest(), sig, &rsa.PSSOptions{SaltLength: cint(in, "saltLen")}) == nil}, nil
		}
		return cm{"ok": rsa.VerifyPKCS1v15(k, ch, digest(), sig) == nil}, nil
	}
	return nil, cryptoErr("NotSupportedError", "unsupported signature algorithm")
}

func cryptoOAEP(in cm) (cm, error) {
	nf, _, err := cryptoHash(cstr(in, "hash"))
	if err != nil {
		return nil, err
	}
	data, label := cbytes(in, "data"), cbytes(in, "label")
	if cbool(in, "enc") {
		pb, err := parsePub(cbytes(in, "key"))
		if err != nil {
			return nil, err
		}
		k, ok := pb.(*rsa.PublicKey)
		if !ok {
			return nil, cryptoErr("InvalidAccessError", "not an RSA key")
		}
		out, err := rsa.EncryptOAEP(nf(), rand.Reader, k, data, label)
		if err != nil {
			return nil, cryptoErr("OperationError", "%v", err)
		}
		return cm{"d": cb64(out)}, nil
	}
	pk, err := parsePriv(cbytes(in, "key"))
	if err != nil {
		return nil, err
	}
	k, ok := pk.(*rsa.PrivateKey)
	if !ok {
		return nil, cryptoErr("InvalidAccessError", "not an RSA key")
	}
	out, err := rsa.DecryptOAEP(nf(), nil, k, data, label)
	if err != nil {
		return nil, cryptoErr("OperationError", "The operation failed for an operation-specific reason")
	}
	return cm{"d": cb64(out)}, nil
}

// ---- import / export: raw and JWK parts (DER is the interchange form for pkcs8 / spki)

func fixed(n *big.Int, size int) string { return b64urlEnc(n.FillBytes(make([]byte, size))) }

func cryptoExport(in cm) (cm, error) {
	format := cstr(in, "format")
	der := cbytes(in, "key")
	private := cbool(in, "private")
	var k any
	var err error
	if private {
		k, err = parsePriv(der)
	} else {
		k, err = parsePub(der)
	}
	if err != nil {
		return nil, err
	}
	if format == "raw" {
		switch v := k.(type) {
		case *ecdsa.PublicKey:
			e, err := v.ECDH()
			if err != nil {
				return nil, cryptoErr("OperationError", "%v", err)
			}
			return cm{"d": cb64(e.Bytes())}, nil
		case *ecdh.PublicKey:
			return cm{"d": cb64(v.Bytes())}, nil
		case ed25519.PublicKey:
			return cm{"d": cb64(v)}, nil
		}
		return nil, cryptoErr("InvalidAccessError", "raw export is not defined for this key")
	}
	jwk := cm{}
	switch v := k.(type) {
	case *ecdsa.PrivateKey:
		n := (v.Curve.Params().BitSize + 7) / 8
		jwk["kty"], jwk["crv"] = "EC", curveName(v.Curve)
		jwk["x"], jwk["y"], jwk["d"] = fixed(v.X, n), fixed(v.Y, n), fixed(v.D, n)
	case *ecdsa.PublicKey:
		n := (v.Curve.Params().BitSize + 7) / 8
		jwk["kty"], jwk["crv"] = "EC", curveName(v.Curve)
		jwk["x"], jwk["y"] = fixed(v.X, n), fixed(v.Y, n)
	case *ecdh.PrivateKey:
		jwk["kty"], jwk["crv"] = "OKP", "X25519"
		jwk["x"], jwk["d"] = b64urlEnc(v.PublicKey().Bytes()), b64urlEnc(v.Bytes())
	case *ecdh.PublicKey:
		jwk["kty"], jwk["crv"] = "OKP", "X25519"
		jwk["x"] = b64urlEnc(v.Bytes())
	case ed25519.PrivateKey:
		jwk["kty"], jwk["crv"] = "OKP", "Ed25519"
		jwk["x"], jwk["d"] = b64urlEnc(v.Public().(ed25519.PublicKey)), b64urlEnc(v.Seed())
	case ed25519.PublicKey:
		jwk["kty"], jwk["crv"] = "OKP", "Ed25519"
		jwk["x"] = b64urlEnc(v)
	case *rsa.PrivateKey:
		v.Precompute()
		jwk["kty"] = "RSA"
		jwk["n"], jwk["e"] = b64urlEnc(v.N.Bytes()), b64urlEnc(big.NewInt(int64(v.E)).Bytes())
		size := (v.N.BitLen() + 7) / 8
		jwk["d"] = fixed(v.D, size)
		half := (size + 1) / 2
		jwk["p"], jwk["q"] = fixed(v.Primes[0], half), fixed(v.Primes[1], half)
		jwk["dp"], jwk["dq"], jwk["qi"] = fixed(v.Precomputed.Dp, half), fixed(v.Precomputed.Dq, half), fixed(v.Precomputed.Qinv, half)
	case *rsa.PublicKey:
		jwk["kty"] = "RSA"
		jwk["n"], jwk["e"] = b64urlEnc(v.N.Bytes()), b64urlEnc(big.NewInt(int64(v.E)).Bytes())
	default:
		return nil, cryptoErr("OperationError", "cannot export this key")
	}
	return cm{"jwk": jwk}, nil
}

func cryptoImport(in cm) (cm, error) {
	format, kind := cstr(in, "format"), cstr(in, "kind")
	if format == "raw" {
		raw := cbytes(in, "data")
		switch kind {
		case "ec":
			c, _, err := ecCurve(cstr(in, "curve"))
			if err != nil {
				return nil, err
			}
			pub, err := c.NewPublicKey(raw)
			if err != nil {
				return nil, cryptoErr("DataError", "invalid EC point")
			}
			d, err := x509.MarshalPKIXPublicKey(pub)
			if err != nil {
				return nil, cryptoErr("DataError", "%v", err)
			}
			return cm{"pub": cb64(d)}, nil
		case "x25519":
			pub, err := ecdh.X25519().NewPublicKey(raw)
			if err != nil {
				return nil, cryptoErr("DataError", "invalid X25519 key")
			}
			d, _ := x509.MarshalPKIXPublicKey(pub)
			return cm{"pub": cb64(d)}, nil
		case "ed25519":
			if len(raw) != ed25519.PublicKeySize {
				return nil, cryptoErr("DataError", "invalid Ed25519 key length")
			}
			d, _ := x509.MarshalPKIXPublicKey(ed25519.PublicKey(raw))
			return cm{"pub": cb64(d)}, nil
		}
		return nil, cryptoErr("NotSupportedError", "raw import not defined")
	}
	if format == "spki" || format == "pkcs8" {
		var k any
		var err error
		if format == "pkcs8" {
			k, err = parsePriv(cbytes(in, "data"))
		} else {
			k, err = parsePub(cbytes(in, "data"))
		}
		if err != nil {
			return nil, err
		}
		info := cm{}
		ok := false
		switch v := k.(type) {
		case *ecdsa.PrivateKey:
			ok, info["kind"], info["curve"] = kind == "ec", "ec", curveName(v.Curve)
		case *ecdsa.PublicKey:
			ok, info["kind"], info["curve"] = kind == "ec", "ec", curveName(v.Curve)
		case *ecdh.PrivateKey, *ecdh.PublicKey:
			ok, info["kind"] = kind == "x25519", "x25519"
		case ed25519.PrivateKey, ed25519.PublicKey:
			ok, info["kind"] = kind == "ed25519", "ed25519"
		case *rsa.PrivateKey:
			ok, info["kind"], info["bits"] = kind == "rsa", "rsa", v.N.BitLen()
		case *rsa.PublicKey:
			ok, info["kind"], info["bits"] = kind == "rsa", "rsa", v.N.BitLen()
		}
		if !ok {
			return nil, cryptoErr("DataError", "the key does not match the algorithm")
		}
		if format == "pkcs8" {
			pair, err := keyPairDER(k)
			if err != nil {
				return nil, err
			}
			info["priv"], info["pub"] = pair["priv"], pair["pub"]
		} else {
			info["pub"] = in["data"]
		}
		return info, nil
	}
	if format != "jwk" {
		return nil, cryptoErr("NotSupportedError", "unsupported key format")
	}
	j, _ := in["jwk"].(map[string]any)
	if j == nil {
		return nil, cryptoErr("DataError", "bad JWK")
	}
	dec := func(k string) ([]byte, error) {
		b, err := b64uDec(cstr(j, k))
		if err != nil || len(b) == 0 {
			return nil, cryptoErr("DataError", "JWK member %q is missing or malformed", k)
		}
		return b, nil
	}
	d := cstr(j, "d")
	switch kind {
	case "ec":
		c, _, err := ecCurve(cstr(j, "crv"))
		if err != nil {
			return nil, cryptoErr("DataError", "unsupported JWK curve")
		}
		if cstr(j, "kty") != "EC" {
			return nil, cryptoErr("DataError", "JWK kty must be EC")
		}
		x, e1 := dec("x")
		y, e2 := dec("y")
		if e1 != nil || e2 != nil {
			return nil, cryptoErr("DataError", "JWK is missing x or y")
		}
		point := append(append([]byte{4}, x...), y...)
		pub, err := c.NewPublicKey(point)
		if err != nil {
			return nil, cryptoErr("DataError", "JWK point is not on the curve")
		}
		if d == "" {
			der, _ := x509.MarshalPKIXPublicKey(pub)
			return cm{"pub": cb64(der), "curve": cstr(j, "crv")}, nil
		}
		db, err := dec("d")
		if err != nil {
			return nil, err
		}
		priv, err := c.NewPrivateKey(db)
		if err != nil || !priv.PublicKey().Equal(pub) {
			return nil, cryptoErr("DataError", "JWK private key does not match the public key")
		}
		ek, err := ecdsaFromECDH(priv, cstr(j, "crv"))
		if err != nil {
			return nil, err
		}
		pair, err := keyPairDER(ek)
		if err != nil {
			return nil, err
		}
		pair["curve"] = cstr(j, "crv")
		return pair, nil
	case "x25519", "ed25519":
		if cstr(j, "kty") != "OKP" {
			return nil, cryptoErr("DataError", "JWK kty must be OKP")
		}
		want := "X25519"
		if kind == "ed25519" {
			want = "Ed25519"
		}
		if cstr(j, "crv") != want {
			return nil, cryptoErr("DataError", "JWK crv must be %s", want)
		}
		x, err := dec("x")
		if err != nil {
			return nil, err
		}
		if d == "" {
			if kind == "x25519" {
				pub, err := ecdh.X25519().NewPublicKey(x)
				if err != nil {
					return nil, cryptoErr("DataError", "invalid key")
				}
				der, _ := x509.MarshalPKIXPublicKey(pub)
				return cm{"pub": cb64(der)}, nil
			}
			if len(x) != ed25519.PublicKeySize {
				return nil, cryptoErr("DataError", "invalid key")
			}
			der, _ := x509.MarshalPKIXPublicKey(ed25519.PublicKey(x))
			return cm{"pub": cb64(der)}, nil
		}
		db, err := dec("d")
		if err != nil {
			return nil, err
		}
		if kind == "x25519" {
			priv, err := ecdh.X25519().NewPrivateKey(db)
			if err != nil || subtle.ConstantTimeCompare(priv.PublicKey().Bytes(), x) != 1 {
				return nil, cryptoErr("DataError", "JWK private key does not match the public key")
			}
			return keyPairDER(priv)
		}
		if len(db) != ed25519.SeedSize {
			return nil, cryptoErr("DataError", "invalid Ed25519 seed")
		}
		priv := ed25519.NewKeyFromSeed(db)
		if subtle.ConstantTimeCompare(priv.Public().(ed25519.PublicKey), x) != 1 {
			return nil, cryptoErr("DataError", "JWK private key does not match the public key")
		}
		return keyPairDER(priv)
	case "rsa":
		if cstr(j, "kty") != "RSA" {
			return nil, cryptoErr("DataError", "JWK kty must be RSA")
		}
		nb, e1 := dec("n")
		eb, e2 := dec("e")
		if e1 != nil || e2 != nil {
			return nil, cryptoErr("DataError", "JWK is missing n or e")
		}
		pub := rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
		if d == "" {
			der, err := x509.MarshalPKIXPublicKey(&pub)
			if err != nil {
				return nil, cryptoErr("DataError", "%v", err)
			}
			return cm{"pub": cb64(der), "bits": pub.N.BitLen()}, nil
		}
		db, e1 := dec("d")
		pb, e2 := dec("p")
		qb, e3 := dec("q")
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, cryptoErr("DataError", "RSA private JWK needs d, p and q")
		}
		priv := &rsa.PrivateKey{PublicKey: pub, D: new(big.Int).SetBytes(db), Primes: []*big.Int{new(big.Int).SetBytes(pb), new(big.Int).SetBytes(qb)}}
		if err := priv.Validate(); err != nil {
			return nil, cryptoErr("DataError", "invalid RSA key: %v", err)
		}
		priv.Precompute()
		pair, err := keyPairDER(priv)
		if err != nil {
			return nil, cryptoErr("DataError", "%v", err)
		}
		pair["bits"] = pub.N.BitLen()
		return pair, nil
	}
	return nil, cryptoErr("NotSupportedError", "unsupported key kind")
}

func ecdsaFromECDH(p *ecdh.PrivateKey, curve string) (*ecdsa.PrivateKey, error) {
	_, c, err := ecCurve(curve)
	if err != nil {
		return nil, err
	}
	pub := p.PublicKey().Bytes()
	n := (c.Params().BitSize + 7) / 8
	k := &ecdsa.PrivateKey{D: new(big.Int).SetBytes(p.Bytes())}
	k.Curve = c
	k.X = new(big.Int).SetBytes(pub[1 : 1+n])
	k.Y = new(big.Int).SetBytes(pub[1+n:])
	return k, nil
}
