package webpush

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// MaxVAPIDLifetime is how far in the future a VAPID token may expire (RFC 8292 section 2: 24 hours).
const MaxVAPIDLifetime = 24 * time.Hour

// VAPIDKey is an application server's signing key (RFC 8292).
type VAPIDKey struct{ Private *ecdsa.PrivateKey }

// NewVAPIDKey makes a fresh P-256 signing key.
func NewVAPIDKey() (*VAPIDKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &VAPIDKey{k}, nil
}

// Public is the uncompressed public key: the applicationServerKey that goes to PushManager.subscribe().
func (k *VAPIDKey) Public() []byte { return pubBytes(&k.Private.PublicKey) }

func pubBytes(p *ecdsa.PublicKey) []byte {
	b := make([]byte, 65)
	b[0] = 4
	p.X.FillBytes(b[1:33])
	p.Y.FillBytes(b[33:])
	return b
}

// Authorization builds the value of the Authorization header for a request to a push service whose origin
// is audience ("https://push.example.net"): "vapid t=<jwt>, k=<key>".
func (k *VAPIDKey) Authorization(audience, subject string, exp time.Time) (string, error) {
	claims := map[string]any{"aud": audience, "exp": exp.Unix()}
	if subject != "" {
		claims["sub"] = subject
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := B64([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + B64(cb)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.Private, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + B64(sig) + ", k=" + B64(k.Public()), nil
}

// ParsePublicKey checks that b is an uncompressed P-256 point (an applicationServerKey).
func ParsePublicKey(b []byte) (*ecdsa.PublicKey, error) {
	if len(b) != 65 || b[0] != 4 {
		return nil, errors.New("webpush: a VAPID key is a 65 octet uncompressed P-256 point")
	}
	if _, err := ecdh.P256().NewPublicKey(b); err != nil {
		return nil, fmt.Errorf("webpush: VAPID key is not on the curve: %w", err)
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(b[1:33]), Y: new(big.Int).SetBytes(b[33:])}, nil
}

// VAPIDClaims are the verified contents of a token.
type VAPIDClaims struct {
	Audience string
	Subject  string
	Expires  time.Time
	Key      []byte // the public key that signed it
}

// VerifyVAPID checks an Authorization header (and, for the draft-era "WebPush" scheme, the Crypto-Key
// header) the way a push service does: signature, algorithm, audience and expiry (not more than 24 hours
// ahead). It returns the claims and the signing key. wantAudience may be empty to skip the audience check.
func VerifyVAPID(authorization, cryptoKey, wantAudience string, now time.Time) (*VAPIDClaims, error) {
	var token, key string
	a := strings.TrimSpace(authorization)
	switch {
	case len(a) > 6 && strings.EqualFold(a[:6], "vapid "):
		for _, part := range strings.Split(a[6:], ",") {
			part = strings.TrimSpace(part)
			if v, ok := strings.CutPrefix(part, "t="); ok {
				token = v
			} else if v, ok := strings.CutPrefix(part, "k="); ok {
				key = v
			}
		}
	case len(a) > 7 && (strings.EqualFold(a[:7], "webpush") || strings.EqualFold(a[:7], "bearer ")):
		token = strings.TrimSpace(a[7:])
		for _, part := range strings.FieldsFunc(cryptoKey, func(r rune) bool { return r == ';' || r == ',' }) {
			if v, ok := strings.CutPrefix(strings.TrimSpace(part), "p256ecdsa="); ok {
				key = v
			}
		}
	default:
		return nil, errors.New("vapid: unsupported authorization scheme")
	}
	if token == "" || key == "" {
		return nil, errors.New("vapid: the token or the key is missing")
	}
	kb, err := DecodeB64(key)
	if err != nil {
		return nil, errors.New("vapid: the key is not base64url")
	}
	pub, err := ParsePublicKey(kb)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("vapid: the token is not a JWT")
	}
	hb, err1 := DecodeB64(parts[0])
	cb, err2 := DecodeB64(parts[1])
	sig, err3 := DecodeB64(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, errors.New("vapid: the token is not base64url")
	}
	var hdr struct{ Alg, Typ string }
	if json.Unmarshal(hb, &hdr) != nil || hdr.Alg != "ES256" {
		return nil, errors.New("vapid: the token must use ES256")
	}
	if len(sig) != 64 {
		return nil, errors.New("vapid: bad signature length")
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("vapid: the signature does not verify")
	}
	var c struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(cb, &c) != nil {
		return nil, errors.New("vapid: the claims are not JSON")
	}
	exp := time.Unix(c.Exp, 0)
	if c.Exp == 0 || !exp.After(now) {
		return nil, errors.New("vapid: the token has expired")
	}
	if exp.After(now.Add(MaxVAPIDLifetime)) {
		return nil, errors.New("vapid: the token expires more than 24 hours ahead")
	}
	if wantAudience != "" && c.Aud != wantAudience {
		return nil, errors.New("vapid: the audience is not this push service")
	}
	return &VAPIDClaims{Audience: c.Aud, Subject: c.Sub, Expires: exp, Key: kb}, nil
}
