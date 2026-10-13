// Package webpush holds the client-side building blocks of Web Push that do not depend on any push
// service: message encryption (RFC 8188 aes128gcm content coding as profiled by RFC 8291) and VAPID
// application server identification (RFC 8292). The transports that talk to a push service live in
// subpackages (autopush) and the self-hostable relay in internal/pushd.
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Limits. RFC 8030 section 7.2 requires a push service to accept bodies of at least 4096 octets. With the
// 86 octet header (16 salt + 4 record size + 1 id length + 65 key), one delimiter octet and the 16 octet tag
// that leaves 3993 octets of plaintext in the default single record.
const (
	DefaultRecordSize = 4096
	MaxBodySize       = 4096
	MaxPlaintext      = MaxBodySize - 86 - 16 - 1
	maxDecryptInput   = 1 << 20
	tagLen            = 16
)

// ErrDecrypt is wrapped by every failure of Decrypt.
var ErrDecrypt = errors.New("webpush: cannot decrypt the message")

var b64 = base64.RawURLEncoding

// B64 encodes with the unpadded URL-safe alphabet used throughout Web Push.
func B64(b []byte) string { return b64.EncodeToString(b) }

// DecodeB64 decodes unpadded (or padded) URL-safe base64.
func DecodeB64(s string) ([]byte, error) {
	for len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}
	return b64.DecodeString(s)
}

// Keys is a subscription's receiving key material: the P-256 key pair (p256dh is the public half) and
// the 16 octet authentication secret that the application server also receives.
type Keys struct {
	Private *ecdh.PrivateKey
	Auth    []byte
}

// NewKeys makes fresh receiver keys.
func NewKeys() (*Keys, error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	auth := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, auth); err != nil {
		return nil, err
	}
	return &Keys{Private: k, Auth: auth}, nil
}

// P256dh is the uncompressed public key (65 octets).
func (k *Keys) P256dh() []byte { return k.Private.PublicKey().Bytes() }

// Decrypt decodes a push message body (the aes128gcm coded body, header included).
func (k *Keys) Decrypt(body []byte) ([]byte, error) { return Decrypt(k.Private, k.Auth, body) }

func hkdfExpand(prk, info []byte, n int) ([]byte, error) {
	return hkdf.Expand(sha256.New, prk, string(info), n)
}

// deriveKeys implements RFC 8291 section 3.4 and RFC 8188 sections 2.2 and 2.3.
func deriveKeys(shared, auth, uaPub, asPub, salt []byte) (cek, nonce []byte, err error) {
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, nil, err
	}
	info := append([]byte("WebPush: info\x00"), uaPub...)
	info = append(info, asPub...)
	ikm, err := hkdfExpand(prkKey, info, 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16); err != nil {
		return nil, nil, err
	}
	if nonce, err = hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12); err != nil {
		return nil, nil, err
	}
	return cek, nonce, nil
}

func recordNonce(base []byte, seq uint64) []byte {
	n := make([]byte, 12)
	copy(n, base)
	var s [8]byte
	binary.BigEndian.PutUint64(s[:], seq)
	for i := 0; i < 8; i++ {
		n[4+i] ^= s[i]
	}
	return n
}

func newGCM(cek []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// Decrypt decodes an RFC 8291 message with the receiver's private key and auth secret. The sender's
// public key is taken from the keyid of the RFC 8188 header, as RFC 8291 section 4 prescribes.
func Decrypt(uaPriv *ecdh.PrivateKey, auth, body []byte) ([]byte, error) {
	if len(body) > maxDecryptInput {
		return nil, fmt.Errorf("%w: body too large", ErrDecrypt)
	}
	if len(body) < 21 {
		return nil, fmt.Errorf("%w: truncated header", ErrDecrypt)
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idlen := int(body[20])
	if rs < 18 {
		return nil, fmt.Errorf("%w: record size %d is too small", ErrDecrypt, rs)
	}
	if idlen != 65 || len(body) < 21+idlen {
		return nil, fmt.Errorf("%w: key id is not an uncompressed P-256 point", ErrDecrypt)
	}
	asPubBytes := body[21 : 21+idlen]
	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: sender key: %v", ErrDecrypt, err)
	}
	shared, err := uaPriv.ECDH(asPub)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	cek, nonce, err := deriveKeys(shared, auth, uaPriv.PublicKey().Bytes(), asPubBytes, salt)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(cek)
	if err != nil {
		return nil, err
	}
	data := body[21+idlen:]
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: no records", ErrDecrypt)
	}
	var out []byte
	for seq := uint64(0); len(data) > 0; seq++ {
		n := len(data)
		if uint64(n) > uint64(rs) {
			n = int(rs)
		}
		rec := data[:n]
		data = data[n:]
		if len(rec) < tagLen+1 {
			return nil, fmt.Errorf("%w: short record", ErrDecrypt)
		}
		pt, err := gcm.Open(nil, recordNonce(nonce, seq), rec, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: authentication failed", ErrDecrypt)
		}
		// The delimiter is the last non-zero octet: 1 for a record that is followed by another, 2 for the last.
		i := len(pt) - 1
		for i >= 0 && pt[i] == 0 {
			i--
		}
		if i < 0 {
			return nil, fmt.Errorf("%w: no padding delimiter", ErrDecrypt)
		}
		last := len(data) == 0
		if !((pt[i] == 2 && last) || (pt[i] == 1 && !last)) {
			return nil, fmt.Errorf("%w: bad padding delimiter (truncated or reordered records)", ErrDecrypt)
		}
		out = append(out, pt[:i]...)
	}
	return out, nil
}

// EncryptOptions are knobs the RFC leaves to the sender; the zero value is the usual choice.
type EncryptOptions struct {
	RecordSize uint32 // default 4096
	Padding    int    // octets of zero padding after the delimiter of the last record (must fit the record)
	// Fixed values for known-answer tests; random when nil.
	Salt          []byte
	SenderPrivate *ecdh.PrivateKey
}

// Encrypt encodes a message for a subscription (RFC 8291 section 3). uaPublic is the p256dh value and
// auth the 16 octet authentication secret of the subscription. It exists for the relay's tests and for
// tools; a browser only decrypts.
func Encrypt(uaPublic, auth, plaintext []byte, o EncryptOptions) ([]byte, error) {
	uaPub, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("webpush: receiver key: %w", err)
	}
	if len(auth) != 16 {
		return nil, errors.New("webpush: the auth secret must be 16 octets")
	}
	rs := o.RecordSize
	if rs == 0 {
		rs = DefaultRecordSize
	}
	if rs < 18 {
		return nil, errors.New("webpush: record size below 18")
	}
	asPriv := o.SenderPrivate
	if asPriv == nil {
		if asPriv, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
			return nil, err
		}
	}
	salt := o.Salt
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, salt); err != nil {
			return nil, err
		}
	}
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	cek, nonce, err := deriveKeys(shared, auth, uaPublic, asPub, salt)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(cek)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 21+len(asPub)+len(plaintext)+o.Padding+tagLen+1)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, rs)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	per := int(rs) - tagLen - 1 // plaintext octets per record
	msg := append([]byte(nil), plaintext...)
	for seq := uint64(0); ; seq++ {
		n := len(msg)
		last := n <= per
		if !last {
			n = per
		}
		rec := append([]byte(nil), msg[:n]...)
		msg = msg[n:]
		if last {
			rec = append(rec, 2)
			if len(rec)+o.Padding > per+1 {
				return nil, errors.New("webpush: padding does not fit the last record")
			}
			rec = append(rec, make([]byte, o.Padding)...)
		} else {
			rec = append(rec, 1)
		}
		out = gcm.Seal(out, recordNonce(nonce, seq), rec, nil)
		if last {
			break
		}
	}
	return out, nil
}
