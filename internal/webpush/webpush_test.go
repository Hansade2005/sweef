package webpush

import (
	"bytes"
	"crypto/ecdh"
	"errors"
	"strings"
	"testing"
	"time"
)

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := DecodeB64(strings.NewReplacer(" ", "", "\n", "").Replace(s))
	if err != nil {
		t.Fatalf("base64 %q: %v", s, err)
	}
	return b
}

// RFC 8291 section 5 and Appendix A.
const (
	rfcPlaintext = "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"
	rfcASPriv    = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPriv    = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPub     = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcBody      = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func TestRFC8291DecryptVector(t *testing.T) {
	priv, err := ecdh.P256().NewPrivateKey(unb64(t, rfcUAPriv))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(priv.PublicKey().Bytes(), unb64(t, rfcUAPub)) {
		t.Fatal("the receiver private key does not give the public key of the RFC")
	}
	got, err := Decrypt(priv, unb64(t, rfcAuth), unb64(t, rfcBody))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "When I grow up, I want to be a watermelon" {
		t.Fatalf("plaintext = %q", got)
	}
	if want := unb64(t, rfcPlaintext); !bytes.Equal(got, want) {
		t.Fatal("plaintext differs from the RFC's base64 form")
	}
}

func TestRFC8291IntermediateValuesAndEncrypt(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(unb64(t, rfcASPriv))
	if err != nil {
		t.Fatal(err)
	}
	uaPub := unb64(t, rfcUAPub)
	pub, _ := ecdh.P256().NewPublicKey(uaPub)
	shared, err := asPriv.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shared, unb64(t, "kyrL1jIIOHEzg3sM2ZWRHDRB62YACZhhSlknJ672kSs")) {
		t.Fatal("ecdh_secret differs from Appendix A")
	}
	cek, nonce, err := deriveKeys(shared, unb64(t, rfcAuth), uaPub, asPriv.PublicKey().Bytes(), unb64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cek, unb64(t, "oIhVW04MRdy2XN9CiKLxTg")) {
		t.Fatalf("cek = %s", B64(cek))
	}
	if !bytes.Equal(nonce, unb64(t, "4h_95klXJ5E_qnoN")) {
		t.Fatalf("nonce = %s", B64(nonce))
	}
	body, err := Encrypt(uaPub, unb64(t, rfcAuth), unb64(t, rfcPlaintext), EncryptOptions{Salt: unb64(t, rfcSalt), SenderPrivate: asPriv})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, unb64(t, rfcBody)) {
		t.Fatalf("Encrypt does not reproduce the RFC message:\n got %s\nwant %s", B64(body), strings.ReplaceAll(rfcBody, " ", ""))
	}
}

func TestEncryptDecryptRoundTrips(t *testing.T) {
	k, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		n    int
		o    EncryptOptions
	}{
		{"empty", 0, EncryptOptions{}},
		{"short", 5, EncryptOptions{}},
		{"max single record", MaxPlaintext, EncryptOptions{}},
		{"several records", 1000, EncryptOptions{RecordSize: 128}},
		{"exact record boundary", 2 * (128 - 17), EncryptOptions{RecordSize: 128}},
		{"padded", 10, EncryptOptions{Padding: 100}},
	} {
		msg := bytes.Repeat([]byte{'x'}, tc.n)
		body, err := Encrypt(k.P256dh(), k.Auth, msg, tc.o)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := k.Decrypt(body)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("%s: round trip changed the message", tc.name)
		}
	}
}

func TestDecryptRejectsDamage(t *testing.T) {
	k, _ := NewKeys()
	body, err := Encrypt(k.P256dh(), k.Auth, bytes.Repeat([]byte{'a'}, 300), EncryptOptions{RecordSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":        nil,
		"short header": body[:20],
		"no records":   body[:86],
		"flipped bit":  append(append([]byte(nil), body[:100]...), append([]byte{body[100] ^ 1}, body[101:]...)...),
		"truncated":    body[:len(body)-1],
		"first record only (last delimiter missing)": body[:86+128],
		"wrong auth": nil,
	}
	for name, b := range cases {
		if name == "wrong auth" {
			other, _ := NewKeys()
			if _, err := Decrypt(k.Private, other.Auth, body); !errors.Is(err, ErrDecrypt) {
				t.Errorf("%s: err = %v", name, err)
			}
			continue
		}
		if _, err := k.Decrypt(b); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: err = %v, want ErrDecrypt", name, err)
		}
	}
	// a record size below 18 is invalid
	bad := append([]byte(nil), body...)
	bad[16], bad[17], bad[18], bad[19] = 0, 0, 0, 17
	if _, err := k.Decrypt(bad); !errors.Is(err, ErrDecrypt) {
		t.Errorf("rs=17: %v", err)
	}
	if _, err := Encrypt(k.P256dh(), []byte("short"), nil, EncryptOptions{}); err == nil {
		t.Error("a short auth secret must be refused")
	}
}

// RFC 8292 section 2.4.
func TestRFC8292ExampleVerifies(t *testing.T) {
	const hdr = "vapid t=eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9.eyJhdWQiOiJodHRwczovL3" +
		"B1c2guZXhhbXBsZS5uZXQiLCJleHAiOjE0NTM1MjM3NjgsInN1YiI6Im1ha" +
		"Wx0bzpwdXNoQGV4YW1wbGUuY29tIn0.i3CYb7t4xfxCDquptFOepC9GAu_H" +
		"LGkMlMuCGSK2rpiUfnK9ojFwDXb1JrErtmysazNjjvW2L9OkSSHzvoD1oA," +
		" k=BA1Hxzyi1RUM1b5wjxsn7nGxAszw2u61m164i3MrAIxHF6YK5h4SDYic-dR" +
		"uU_RCPCfA5aq9ojSwk5Y2EmClBPs"
	valid := time.Unix(1453523768-3600, 0) // the token is valid until 2016-01-23T04:36:08Z
	c, err := VerifyVAPID(hdr, "", "https://push.example.net", valid)
	if err != nil {
		t.Fatal(err)
	}
	if c.Audience != "https://push.example.net" || c.Subject != "mailto:push@example.com" || c.Expires.Unix() != 1453523768 {
		t.Fatalf("claims = %+v", c)
	}
	// the JWK of Figure 2 is the same key as the k parameter
	x, y := unb64(t, "DUfHPKLVFQzVvnCPGyfucbECzPDa7rWbXriLcysAjEc"), unb64(t, "F6YK5h4SDYic-dRuU_RCPCfA5aq9ojSwk5Y2EmClBPs")
	if !bytes.Equal(c.Key, append(append([]byte{4}, x...), y...)) {
		t.Fatal("k is not the JWK of the RFC")
	}
	if _, err := VerifyVAPID(hdr, "", "", time.Unix(1453523768+1, 0)); err == nil {
		t.Error("an expired token must be refused")
	}
	if _, err := VerifyVAPID(hdr, "", "https://other.example", valid); err == nil {
		t.Error("a token for another audience must be refused")
	}
	// the exp of the RFC is one hour ahead of "valid"; a token whose exp is more than 24 hours ahead is refused
	if _, err := VerifyVAPID(hdr, "", "", time.Unix(1453523768-25*3600, 0)); err == nil {
		t.Error("a token that expires more than 24 hours ahead must be refused")
	}
	// a changed claim breaks the signature
	tampered := strings.Replace(hdr, "eyJhdWQiOiJodHRwczovL3", "eyJhdWQiOiJodHRwczovL2", 1)
	if _, err := VerifyVAPID(tampered, "", "", valid); err == nil {
		t.Error("a tampered token must be refused")
	}
}

func TestVAPIDSignAndVerify(t *testing.T) {
	k, err := NewVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	h, err := k.Authorization("https://push.example", "mailto:ops@example.com", now.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	c, err := VerifyVAPID(h, "", "https://push.example", now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.Key, k.Public()) || c.Subject != "mailto:ops@example.com" {
		t.Fatalf("claims = %+v", c)
	}
	// the older "WebPush <jwt>" + Crypto-Key form is accepted too
	tok := strings.TrimSuffix(strings.SplitN(strings.TrimPrefix(h, "vapid t="), ",", 2)[0], "")
	if _, err := VerifyVAPID("WebPush "+tok, "p256ecdsa="+B64(k.Public()), "https://push.example", now); err != nil {
		t.Errorf("legacy scheme: %v", err)
	}
	for _, bad := range []string{"", "vapid", "vapid t=a.b.c", "Basic abc", "vapid t=a.b.c, k=AAAA"} {
		if _, err := VerifyVAPID(bad, "", "", now); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if _, err := ParsePublicKey(make([]byte, 65)); err == nil {
		t.Error("a point off the curve must be refused")
	}
}
