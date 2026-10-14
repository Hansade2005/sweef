package cdm

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ---- independent reference encryptors (block by block, different structure from DecryptSample) ----

func inc128(c []byte) {
	for i := 15; i >= 0; i-- {
		c[i]++
		if c[i] != 0 {
			return
		}
	}
}

// encryptRef encrypts plain with the common-encryption rules, written block by block.
func encryptRef(t testing.TB, key []byte, scheme Scheme, iv []byte, pat Pattern, subs []Subsample, plain []byte) []byte {
	t.Helper()
	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := append([]byte(nil), plain...)
	iv16 := make([]byte, 16)
	copy(iv16, iv)
	counter := append([]byte(nil), iv16...)
	chain := append([]byte(nil), iv16...) // cbc1 chain across subsamples
	pos := 0
	var ks []byte // unused keystream bytes: the cenc cipher bytes of all subsamples form one byte stream
	ctrBlock := func(dst []byte) {
		for i := range dst {
			if len(ks) == 0 {
				ks = make([]byte, 16)
				blk.Encrypt(ks, counter)
				inc128(counter)
			}
			dst[i] ^= ks[0]
			ks = ks[1:]
		}
	}
	for _, ss := range subs {
		pos += int(ss.ClearBytes)
		n := int(ss.CipherBytes)
		rng := out[pos : pos+n]
		switch scheme {
		case SchemeCenc:
			for off := 0; off < n; off += 16 {
				end := min(off+16, n)
				ctrBlock(rng[off:end])
			}
		case SchemeCens:
			for b := 0; b < n/16; b++ {
				if pat.CryptBlocks+pat.SkipBlocks > 0 && uint32(b)%(pat.CryptBlocks+pat.SkipBlocks) >= pat.CryptBlocks {
					continue
				}
				ctrBlock(rng[b*16 : b*16+16])
			}
		case SchemeCbc1:
			for b := 0; b < n/16; b++ {
				cur := rng[b*16 : b*16+16]
				for i := range cur {
					cur[i] ^= chain[i]
				}
				blk.Encrypt(cur, cur)
				copy(chain, cur)
			}
		case SchemeCbcs:
			prev := append([]byte(nil), iv16...)
			for b := 0; b < n/16; b++ {
				if pat.CryptBlocks+pat.SkipBlocks > 0 && uint32(b)%(pat.CryptBlocks+pat.SkipBlocks) >= pat.CryptBlocks {
					continue
				}
				cur := rng[b*16 : b*16+16]
				for i := range cur {
					cur[i] ^= prev[i]
				}
				blk.Encrypt(cur, cur)
				copy(prev, cur)
			}
		}
		pos += n
	}
	return out
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// ---- helpers to drive the interface the way EME does ----

func clearKeyLicense(kid, key []byte) []byte {
	b, _ := json.Marshal(map[string]any{
		"keys": []map[string]string{{"kty": "oct", "kid": base64.RawURLEncoding.EncodeToString(kid), "k": base64.RawURLEncoding.EncodeToString(key)}},
		"type": "temporary",
	})
	return b
}

func openClearKey(t *testing.T, kid, key []byte) (Backend, Session) {
	t.Helper()
	ctx := context.Background()
	b, err := NewClearKeyProvider().New(ctx, InstanceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	s, err := b.CreateSession(ctx, SessionTemporary)
	if err != nil {
		t.Fatal(err)
	}
	initData, _ := json.Marshal(map[string]any{"kids": []string{base64.RawURLEncoding.EncodeToString(kid)}})
	msg, err := s.GenerateRequest(ctx, "keyids", initData)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != MessageLicenseRequest {
		t.Fatalf("message type %q", msg.Type)
	}
	var req struct {
		Kids []string `json:"kids"`
		Type string   `json:"type"`
	}
	if err := json.Unmarshal(msg.Data, &req); err != nil || len(req.Kids) != 1 || req.Type != "temporary" {
		t.Fatalf("license request %s (%v)", msg.Data, err)
	}
	if got, _ := base64.RawURLEncoding.DecodeString(req.Kids[0]); !bytes.Equal(got, kid) {
		t.Fatalf("request kid %x", got)
	}
	if err := s.Update(ctx, clearKeyLicense(kid, key)); err != nil {
		t.Fatal(err)
	}
	return b, s
}

func TestClearKeyDecryptSchemesThroughInterface(t *testing.T) {
	kid, key := randBytes(16), randBytes(16)
	b, _ := openClearKey(t, kid, key)
	plain := randBytes(5000)
	subs := []Subsample{{16, 400}, {5, 1003}, {0, 160}, {33, 1000}, {7, 2376}} // totals 5000
	var total uint32
	for _, s := range subs {
		total += s.ClearBytes + s.CipherBytes
	}
	if int(total) != len(plain) {
		t.Fatal("test setup", total)
	}
	cases := []struct {
		name   string
		scheme Scheme
		iv     []byte
		pat    Pattern
		subs   []Subsample
	}{
		{"cenc 8-byte IV subsamples", SchemeCenc, randBytes(8), Pattern{}, subs},
		{"cenc 16-byte IV subsamples", SchemeCenc, randBytes(16), Pattern{}, subs},
		{"cenc whole sample", SchemeCenc, randBytes(8), Pattern{}, nil},
		{"cbcs 1:9 subsamples", SchemeCbcs, randBytes(16), Pattern{1, 9}, subs},
		{"cbcs 1:9 whole sample", SchemeCbcs, randBytes(16), Pattern{1, 9}, nil},
		{"cbcs 5:5 subsamples", SchemeCbcs, randBytes(16), Pattern{5, 5}, subs},
		{"cbcs 0:0 means all blocks", SchemeCbcs, randBytes(16), Pattern{}, subs},
		{"cens 1:9", SchemeCens, randBytes(8), Pattern{1, 9}, subs},
		{"cbc1 across subsamples", SchemeCbc1, randBytes(16), Pattern{}, subs},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			effSubs := c.subs
			if effSubs == nil {
				effSubs = []Subsample{{0, uint32(len(plain))}}
			}
			enc := encryptRef(t, key, c.scheme, c.iv, c.pat, effSubs, plain)
			if bytes.Equal(enc, plain) {
				t.Fatal("encryptor did nothing")
			}
			// clear bytes must be untouched by the encryptor (sanity of the reference)
			got, err := b.Decrypt(context.Background(), &Sample{Data: enc, KeyID: kid, IV: c.iv, Subsamples: c.subs, Scheme: c.scheme, Pattern: c.pat})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				i := 0
				for i < len(got) && got[i] == plain[i] {
					i++
				}
				t.Fatalf("decrypted data differs from the plain text from byte %d", i)
			}
			if bytes.Equal(enc, got) {
				t.Fatal("Decrypt must return a new slice")
			}
		})
	}
}

func TestClearKeyDecryptErrors(t *testing.T) {
	kid, key := randBytes(16), randBytes(16)
	b, s := openClearKey(t, kid, key)
	ctx := context.Background()
	data := randBytes(64)
	if _, err := b.Decrypt(ctx, &Sample{Data: data, KeyID: randBytes(16), IV: randBytes(8), Scheme: SchemeCenc}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("unknown key id: %v", err)
	}
	if _, err := b.Decrypt(ctx, &Sample{Data: data, KeyID: kid, IV: randBytes(8), Scheme: SchemeCenc, Subsamples: []Subsample{{10, 10}}}); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("subsamples not covering the sample: %v", err)
	}
	if _, err := b.Decrypt(ctx, &Sample{Data: data, KeyID: kid, IV: randBytes(5), Scheme: SchemeCenc}); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("bad IV: %v", err)
	}
	// clear samples pass through even without a key
	out, err := b.Decrypt(ctx, &Sample{Data: data, KeyID: randBytes(16)})
	if err != nil || !bytes.Equal(out, data) {
		t.Fatalf("clear sample: %v", err)
	}
	// closing the session drops its keys
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Decrypt(ctx, &Sample{Data: data, KeyID: kid, IV: randBytes(8), Scheme: SchemeCenc}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("after Close: %v", err)
	}
}

func TestClearKeyEvents(t *testing.T) {
	kid, key := randBytes(16), randBytes(16)
	_, s := openClearKey(t, kid, key)
	select {
	case ev := <-s.Events():
		if ev.Type != EventKeyStatusesChange || !ev.AdditionalUsableKey || len(ev.KeyStatuses) != 1 ||
			ev.KeyStatuses[0].Status != KeyUsable || !bytes.Equal(ev.KeyStatuses[0].KeyID, kid) {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no keystatuseschange event")
	}
	if ks := s.KeyStatuses(); len(ks) != 1 || ks[0].Status != KeyUsable {
		t.Fatalf("KeyStatuses %+v", ks)
	}
	if !s.Expiration().IsZero() {
		t.Fatal("Clear Key never expires")
	}
	if s.ID() == "" {
		t.Fatal("no session id")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case ev, ok := <-s.Events():
		if !ok || ev.Type != EventClosed {
			t.Fatalf("event %+v ok=%v", ev, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no closed event")
	}
	if _, ok := <-s.Events(); ok {
		t.Fatal("channel not closed after the closed event")
	}
}

func TestClearKeyInitDataAndLicenseValidation(t *testing.T) {
	ctx := context.Background()
	b := NewClearKey()
	defer b.Close()
	kid := randBytes(16)

	// cenc init data: a version 1 pssh box with the common system id
	pssh := func(version byte, sysID []byte, kids ...[]byte) []byte {
		var body bytes.Buffer
		body.WriteByte(version)
		body.Write([]byte{0, 0, 0})
		body.Write(sysID)
		if version == 1 {
			var n [4]byte
			binary.BigEndian.PutUint32(n[:], uint32(len(kids)))
			body.Write(n[:])
			for _, k := range kids {
				body.Write(k)
			}
		}
		body.Write([]byte{0, 0, 0, 0}) // data size
		box := make([]byte, 8, 8+body.Len())
		binary.BigEndian.PutUint32(box, uint32(8+body.Len()))
		copy(box[4:], "pssh")
		return append(box, body.Bytes()...)
	}
	s, _ := b.CreateSession(ctx, SessionTemporary)
	kid2 := randBytes(16)
	init := append(pssh(0, commonSystemID), pssh(1, commonSystemID, kid, kid2)...)
	msg, err := s.GenerateRequest(ctx, "cenc", init)
	if err != nil {
		t.Fatal(err)
	}
	var req struct{ Kids []string }
	_ = json.Unmarshal(msg.Data, &req)
	if len(req.Kids) != 2 {
		t.Fatalf("cenc init data yielded %d key ids: %s", len(req.Kids), msg.Data)
	}
	if err := s.Update(ctx, clearKeyLicense(kid, randBytes(16))); err != nil {
		t.Fatal(err)
	}
	// a second request on the same session is an InvalidStateError
	var ce *Error
	if _, err := s.GenerateRequest(ctx, "cenc", init); !errors.As(err, &ce) || ce.Exception != ExceptionInvalidStateError {
		t.Fatalf("second request: %v", err)
	}

	bad := []struct {
		name, typ string
		data      []byte
		want      Exception
	}{
		{"no kids", "keyids", []byte(`{"kids":[]}`), ExceptionTypeError},
		{"not json", "keyids", []byte(`nope`), ExceptionTypeError},
		{"truncated pssh", "cenc", []byte{0, 0, 0, 40, 'p', 's'}, ExceptionTypeError},
		{"unknown type", "skd", []byte("x"), ExceptionNotSupportedError},
	}
	for _, c := range bad {
		s2, _ := b.CreateSession(ctx, SessionTemporary)
		if _, err := s2.GenerateRequest(ctx, c.typ, c.data); !errors.As(err, &ce) || ce.Exception != c.want {
			t.Errorf("%s: got %v, want %s", c.name, err, c.want)
		}
	}

	s3, _ := b.CreateSession(ctx, SessionTemporary)
	if _, err := s3.GenerateRequest(ctx, "webm", kid); err != nil {
		t.Fatal(err)
	}
	for name, lic := range map[string]string{
		"not json":   `x`,
		"no keys":    `{"keys":[]}`,
		"wrong kty":  `{"keys":[{"kty":"RSA","kid":"AA","k":"AA"}]}`,
		"short key":  `{"keys":[{"kty":"oct","kid":"AA","k":"AAAA"}]}`,
		"bad base64": `{"keys":[{"kty":"oct","kid":"!!","k":"AAAAAAAAAAAAAAAAAAAAAA"}]}`,
	} {
		if err := s3.Update(ctx, []byte(lic)); !errors.As(err, &ce) || ce.Exception != ExceptionTypeError {
			t.Errorf("license %s: %v", name, err)
		}
	}
	if len(s3.KeyStatuses()) != 0 {
		t.Fatal("a rejected license must not add keys")
	}
	if _, err := b.CreateSession(ctx, SessionPersistentLicense); err == nil {
		t.Fatal("persistent sessions must be refused")
	}
	if ok, err := b.SetServerCertificate(ctx, []byte("cert")); ok || err != nil {
		t.Fatalf("server certificate: %v %v", ok, err)
	}
	if b.Mode() != ModeDecrypt || b.KeySystem() != ClearKeySystem {
		t.Fatal("mode or key system")
	}
}
