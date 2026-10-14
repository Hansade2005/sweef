package cdm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// ClearKeySystem is the key system string of W3C Clear Key.
const ClearKeySystem = "org.w3.clearkey"

// W3C common PSSH system ID (1077efec-c0b2-4d02-ace3-3c1e52e2fb4b), the one Clear Key uses in "cenc" init data.
var commonSystemID = []byte{0x10, 0x77, 0xef, 0xec, 0xc0, 0xb2, 0x4d, 0x02, 0xac, 0xe3, 0x3c, 0x1e, 0x52, 0xe2, 0xfb, 0x4b}

type clearKeyProvider struct{}

// NewClearKeyProvider returns the in-process Clear Key key system (W3C EME, section 9.1): keys travel in the
// clear as a JSON Web Key Set, so no license server is needed beyond whatever hands the keys to the page.
func NewClearKeyProvider() Provider { return clearKeyProvider{} }

func (clearKeyProvider) KeySystem() string { return ClearKeySystem }

func (clearKeyProvider) Availability() Availability { return Availability{Supported: true} }

func (clearKeyProvider) Capabilities() Capabilities {
	return Capabilities{
		InitDataTypes: []string{initDataTypeCenc, initDataTypeKeyIDs, initDataTypeWebM},
		SessionTypes:  []SessionType{SessionTemporary},
		VideoCodecs:   []string{"avc1", "hevc", "vp8", "vp9", "av01"},
		AudioCodecs:   []string{"mp4a", "opus", "vorbis", "flac"},
		Containers:    []string{"video/mp4", "audio/mp4", "video/webm", "audio/webm"},
		Schemes:       []Scheme{SchemeCenc, SchemeCens, SchemeCbc1, SchemeCbcs},
		Robustness:    []RobustnessLevel{{Name: ""}},
		Mode:          ModeDecrypt,
		Version:       "builtin",
		Notes:         "codec lists name what Sweef can decode; the page-facing canPlayType answer is the final word",
	}
}

func (clearKeyProvider) New(ctx context.Context, cfg InstanceConfig) (Backend, error) {
	return NewClearKey(), nil
}

// ClearKey is the Clear Key Backend.
type ClearKey struct {
	mu       sync.Mutex
	sessions map[string]*ckSession
	closed   bool
}

// NewClearKey creates a standalone Clear Key backend (what Provider.New returns).
func NewClearKey() *ClearKey { return &ClearKey{sessions: map[string]*ckSession{}} }

func (*ClearKey) KeySystem() string { return ClearKeySystem }
func (*ClearKey) Mode() DecryptMode { return ModeDecrypt }

// SetServerCertificate: Clear Key has no server certificates.
func (*ClearKey) SetServerCertificate(ctx context.Context, cert []byte) (bool, error) {
	return false, nil
}

func (c *ClearKey) CreateSession(ctx context.Context, typ SessionType) (Session, error) {
	if typ != SessionTemporary {
		return nil, Errorf(ExceptionNotSupportedError, "Clear Key only supports temporary sessions")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	return &ckSession{owner: c, q: NewEventQueue(), keys: map[string][]byte{}}, nil
}

// Decrypt looks the key up in every open session.
func (c *ClearKey) Decrypt(ctx context.Context, s *Sample) ([]byte, error) {
	if s.Scheme == SchemeNone {
		return append([]byte(nil), s.Data...), nil
	}
	c.mu.Lock()
	var key []byte
	for _, sess := range c.sessions {
		if k := sess.key(s.KeyID); k != nil {
			key = k
			break
		}
	}
	c.mu.Unlock()
	if key == nil {
		return nil, ErrNoKey
	}
	return DecryptSample(key, s)
}

func (c *ClearKey) Close() error {
	c.mu.Lock()
	c.closed = true
	sessions := c.sessions
	c.sessions = map[string]*ckSession{}
	c.mu.Unlock()
	for _, s := range sessions {
		s.shutdown(false)
	}
	return nil
}

type ckSession struct {
	owner *ClearKey
	q     *EventQueue

	mu     sync.Mutex
	id     string
	keys   map[string][]byte // key id (raw string) -> key
	order  [][]byte          // key ids in arrival order
	closed bool
}

func (s *ckSession) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

func (s *ckSession) Events() <-chan Event { return s.q.C() }

func (s *ckSession) key(id []byte) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.keys[string(id)]
}

func (s *ckSession) GenerateRequest(ctx context.Context, initDataType string, initData []byte) (Message, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Message{}, Errorf(ExceptionInvalidStateError, "session is closed")
	}
	if s.id != "" {
		s.mu.Unlock()
		return Message{}, Errorf(ExceptionInvalidStateError, "a request was already generated")
	}
	s.mu.Unlock()
	kids, err := clearKeyIDs(initDataType, initData)
	if err != nil {
		return Message{}, err
	}
	if len(kids) == 0 {
		return Message{}, Errorf(ExceptionTypeError, "init data holds no key ids")
	}
	var rb [8]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return Message{}, err
	}
	id := hex.EncodeToString(rb[:])
	enc := make([]string, len(kids))
	for i, k := range kids {
		enc[i] = b64.EncodeToString(k)
	}
	body, _ := json.Marshal(struct {
		Kids []string `json:"kids"`
		Type string   `json:"type"`
	}{enc, "temporary"})
	s.mu.Lock()
	s.id = id
	s.mu.Unlock()
	s.owner.mu.Lock()
	if s.owner.closed {
		s.owner.mu.Unlock()
		return Message{}, ErrClosed
	}
	s.owner.sessions[id] = s
	s.owner.mu.Unlock()
	return Message{Type: MessageLicenseRequest, Data: body}, nil
}

func (s *ckSession) Load(ctx context.Context, sessionID string) (bool, error) {
	return false, Errorf(ExceptionNotSupportedError, "Clear Key has no persistent sessions")
}

// jwkSet is the license (response) format of Clear Key.
type jwkSet struct {
	Keys []struct {
		Kty string `json:"kty"`
		K   string `json:"k"`
		Kid string `json:"kid"`
	} `json:"keys"`
	Type string `json:"type"`
}

func (s *ckSession) Update(ctx context.Context, response []byte) error {
	s.mu.Lock()
	if s.closed || s.id == "" {
		s.mu.Unlock()
		return Errorf(ExceptionInvalidStateError, "session is not open")
	}
	s.mu.Unlock()
	var set jwkSet
	if err := json.Unmarshal(response, &set); err != nil {
		return Errorf(ExceptionTypeError, "the license is not a JSON Web Key Set: %v", err)
	}
	if len(set.Keys) == 0 {
		return Errorf(ExceptionTypeError, "the license holds no keys")
	}
	type pair struct{ kid, key []byte }
	var add []pair
	for i, k := range set.Keys {
		if k.Kty != "oct" {
			return Errorf(ExceptionTypeError, "key %d has kty %q, want oct", i, k.Kty)
		}
		kid, err := b64.DecodeString(k.Kid)
		if err != nil || len(kid) == 0 {
			return Errorf(ExceptionTypeError, "key %d has a bad kid", i)
		}
		key, err := b64.DecodeString(k.K)
		if err != nil || len(key) != 16 {
			return Errorf(ExceptionTypeError, "key %d has a bad k (must be 16 bytes, base64url)", i)
		}
		add = append(add, pair{kid, key})
	}
	s.mu.Lock()
	for _, p := range add {
		if _, dup := s.keys[string(p.kid)]; !dup {
			s.order = append(s.order, p.kid)
		}
		s.keys[string(p.kid)] = p.key
	}
	statuses := s.statusesLocked()
	s.mu.Unlock()
	s.q.Push(Event{Type: EventKeyStatusesChange, KeyStatuses: statuses, AdditionalUsableKey: true})
	return nil
}

func (s *ckSession) statusesLocked() []KeyStatus {
	out := make([]KeyStatus, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, KeyStatus{KeyID: append([]byte(nil), id...), Status: KeyUsable})
	}
	return out
}

func (s *ckSession) KeyStatuses() []KeyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusesLocked()
}

func (s *ckSession) Expiration() time.Time { return time.Time{} }

func (s *ckSession) Close(ctx context.Context) error {
	s.shutdown(true)
	return nil
}

func (s *ckSession) Remove(ctx context.Context) error {
	return Errorf(ExceptionInvalidStateError, "Remove is for persistent sessions")
}

// shutdown closes the session; unregister also removes it from the owner (false when the owner is closing).
func (s *ckSession) shutdown(unregister bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	id := s.id
	s.keys = map[string][]byte{}
	s.order = nil
	s.mu.Unlock()
	if unregister && id != "" {
		s.owner.mu.Lock()
		delete(s.owner.sessions, id)
		s.owner.mu.Unlock()
	}
	s.q.Push(Event{Type: EventClosed})
	s.q.Close()
}

var b64 = base64.RawURLEncoding

// clearKeyIDs extracts the key ids of Clear Key init data.
func clearKeyIDs(initDataType string, data []byte) ([][]byte, error) {
	switch initDataType {
	case initDataTypeKeyIDs:
		var v struct {
			Kids []string `json:"kids"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, Errorf(ExceptionTypeError, "keyids init data is not JSON: %v", err)
		}
		var out [][]byte
		for _, k := range v.Kids {
			id, err := b64.DecodeString(k)
			if err != nil || len(id) == 0 {
				return nil, Errorf(ExceptionTypeError, "keyids init data has a bad key id")
			}
			out = append(out, id)
		}
		return out, nil
	case initDataTypeWebM:
		if len(data) == 0 || len(data) > 512 {
			return nil, Errorf(ExceptionTypeError, "webm init data must be one key id")
		}
		return [][]byte{append([]byte(nil), data...)}, nil
	case initDataTypeCenc:
		return psshKeyIDs(data)
	}
	return nil, Errorf(ExceptionNotSupportedError, "init data type %q is not supported by Clear Key", initDataType)
}

// psshKeyIDs collects the key ids of the version 1 'pssh' boxes in cenc init data. Only boxes of the common
// system ID (or any box with a key id list) count; version 0 boxes carry no key ids.
func psshKeyIDs(data []byte) ([][]byte, error) {
	var out [][]byte
	rest := data
	for len(rest) > 0 {
		if len(rest) < 8 {
			return nil, Errorf(ExceptionTypeError, "truncated pssh box")
		}
		size := uint64(binary.BigEndian.Uint32(rest))
		if size == 1 {
			if len(rest) < 16 {
				return nil, Errorf(ExceptionTypeError, "truncated pssh box")
			}
			size = binary.BigEndian.Uint64(rest[8:])
		} else if size == 0 {
			size = uint64(len(rest))
		}
		if size < 8 || size > uint64(len(rest)) {
			return nil, Errorf(ExceptionTypeError, "pssh box size %d is out of range", size)
		}
		box := rest[:size]
		rest = rest[size:]
		if string(box[4:8]) != "pssh" {
			continue
		}
		hdr := 8
		if binary.BigEndian.Uint32(box) == 1 {
			hdr = 16
		}
		if len(box) < hdr+4+16 {
			return nil, Errorf(ExceptionTypeError, "pssh box is too short")
		}
		version := box[hdr]
		body := box[hdr+4:]
		sysID := body[:16]
		body = body[16:]
		if version == 0 {
			continue
		}
		if len(body) < 4 {
			return nil, Errorf(ExceptionTypeError, "pssh box is too short")
		}
		n := int(binary.BigEndian.Uint32(body))
		body = body[4:]
		if n < 0 || len(body) < n*16 {
			return nil, Errorf(ExceptionTypeError, "pssh key id count %d does not fit", n)
		}
		if !bytes.Equal(sysID, commonSystemID) && n == 0 {
			continue
		}
		for i := 0; i < n; i++ {
			out = append(out, append([]byte(nil), body[i*16:i*16+16]...))
		}
	}
	return out, nil
}
