package engine

// eme.go: Encrypted Media Extensions with the Clear Key key system (org.w3.clearkey) and nothing else. The API
// objects are JavaScript (media_prelude.js: MediaKeySystemAccess, MediaKeys, MediaKeySession, MediaKeyStatusMap,
// the "encrypted" and "waitingforkey" events); this file keeps the content keys (one KeyStore per MediaKeys) and
// binds a MediaKeys to the SourceBuffers of the element it is attached to, whose samples are then decrypted by
// media.Sample.Bytes as the player reads them (internal/media/cenc.go).
//
// Widevine, PlayReady and FairPlay need licensed content decryption modules: requestMediaKeySystemAccess rejects
// them with NotSupportedError. There is no persistent state (only temporary sessions), no server certificate and no
// hardware secure path.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/sweefbrowser/sweef/internal/media"
)

type emeKeys struct {
	mu     sync.Mutex
	next   int
	stores map[int]*media.KeyStore
}

func (e *emeKeys) get(id int) *media.KeyStore {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stores[id]
}

// parseClearKeyLicense reads a Clear Key license: {"keys":[{"kty":"oct","kid":"...","k":"..."}],"type":"temporary"}
// (base64url values). It returns key ID -> key.
func parseClearKeyLicense(s string) (map[[16]byte][]byte, error) {
	var lic struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			K   string `json:"k"`
		} `json:"keys"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(s), &lic); err != nil {
		return nil, fmt.Errorf("TypeError: the license is not valid JSON (%v)", err)
	}
	if len(lic.Keys) == 0 {
		return nil, fmt.Errorf("TypeError: the license has no keys")
	}
	if lic.Type != "" && lic.Type != "temporary" {
		return nil, fmt.Errorf("TypeError: unsupported license type %q", lic.Type)
	}
	out := map[[16]byte][]byte{}
	for _, k := range lic.Keys {
		if k.Kty != "oct" {
			return nil, fmt.Errorf("TypeError: key type %q is not supported (only \"oct\")", k.Kty)
		}
		kid, err := media.B64URL(k.Kid)
		if err != nil || len(kid) != 16 {
			return nil, fmt.Errorf("TypeError: the key ID is not 16 bytes of base64url")
		}
		key, err := media.B64URL(k.K)
		if err != nil || len(key) != 16 {
			return nil, fmt.Errorf("TypeError: the key is not 16 bytes of base64url")
		}
		var id [16]byte
		copy(id[:], kid)
		out[id] = key
	}
	return out, nil
}

func initDataAny(d []media.MSEInitData) []any {
	out := make([]any, 0, len(d))
	for _, x := range d {
		out = append(out, map[string]any{"type": x.Type, "data": base64.StdEncoding.EncodeToString(x.Data)})
	}
	return out
}

func (m *mediaHost) emeFns(f map[string]hostFn) {
	ek := m.eme
	f["emeNew"] = func(a []any) (any, error) {
		ek.mu.Lock()
		defer ek.mu.Unlock()
		ek.next++
		ek.stores[ek.next] = media.NewKeyStore()
		return ek.next, nil
	}
	f["emeFree"] = func(a []any) (any, error) {
		ek.mu.Lock()
		delete(ek.stores, int(argI(a, 0)))
		ek.mu.Unlock()
		return nil, nil
	}
	// emeLicense(keysId, json) -> the key IDs the license carried (base64url)
	f["emeLicense"] = func(a []any) (any, error) {
		ks := ek.get(int(argI(a, 0)))
		if ks == nil {
			return nil, fmt.Errorf("InvalidStateError: the MediaKeys object is closed")
		}
		keys, err := parseClearKeyLicense(argS(a, 1))
		if err != nil {
			return nil, err
		}
		var kids []any
		for kid, key := range keys {
			ks.Add(kid, key)
			kids = append(kids, base64.RawURLEncoding.EncodeToString(kid[:]))
		}
		return kids, nil
	}
	// emeBind(bufferId, keysId): decrypt the samples of a SourceBuffer with the keys (0 detaches)
	f["emeBind"] = func(a []any) (any, error) {
		if b := m.mse.get(int(argI(a, 0))); b != nil {
			b.SetKeyStore(ek.get(int(argI(a, 1))))
		}
		return nil, nil
	}
	f["emeSupportedType"] = func(a []any) (any, error) {
		// Clear Key plays whatever the pipeline plays; the container must be one that carries Common Encryption
		t := strings.ToLower(argS(a, 0))
		return strings.Contains(t, "mp4") || strings.Contains(t, "webm"), nil
	}
}
