package cdm

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// The CDM host protocol: how the browser talks to a helper process that hosts a CDM shared library (see
// cmd/sweef-cdm and docs/drm.md). It runs over any ordered byte stream (the helper's stdin/stdout, a socketpair,
// net.Pipe in tests).
//
// A frame is
//
//	uint32 length (big endian; counts everything after this field, so at least 5)
//	uint8  type
//	uint32 id      request id chosen by the client; 0 for events
//	[]byte payload
//
// The client sends requests (types 1..15) and the helper answers each with exactly one TypeOK or TypeErr frame
// carrying the same id. The helper sends events (0x90..) with id 0 at any time. Payloads are JSON objects, except
// that Decrypt requests and responses carry raw sample bytes after a small JSON header: see packHeaderData.
//
// Protocol version 1 is decrypt-only (ModeDecrypt): there are no decoder frames.

// ProtocolVersion is bumped on any incompatible change to the frames or payloads.
const ProtocolVersion = 1

const (
	protocolMagic = "SWEEF-CDM"
	// MaxFrameSize bounds a frame (a 4K intra frame is a few MiB); a larger length means a corrupt stream.
	MaxFrameSize = 64 << 20
	frameHeader  = 5
)

// FrameType identifies a protocol message.
type FrameType uint8

const (
	TypeHello                FrameType = 1
	TypeInitialize           FrameType = 2
	TypeSetServerCertificate FrameType = 3
	TypeCreateSession        FrameType = 4
	TypeLoadSession          FrameType = 5
	TypeUpdateSession        FrameType = 6
	TypeCloseSession         FrameType = 7
	TypeRemoveSession        FrameType = 8
	TypeDecrypt              FrameType = 10
	TypeShutdown             FrameType = 15

	TypeOK  FrameType = 0x80
	TypeErr FrameType = 0x81

	TypeEventMessage    FrameType = 0x90
	TypeEventKeys       FrameType = 0x91
	TypeEventExpiration FrameType = 0x92
	TypeEventClosed     FrameType = 0x93
)

func (t FrameType) String() string {
	switch t {
	case TypeHello:
		return "Hello"
	case TypeInitialize:
		return "Initialize"
	case TypeSetServerCertificate:
		return "SetServerCertificate"
	case TypeCreateSession:
		return "CreateSession"
	case TypeLoadSession:
		return "LoadSession"
	case TypeUpdateSession:
		return "UpdateSession"
	case TypeCloseSession:
		return "CloseSession"
	case TypeRemoveSession:
		return "RemoveSession"
	case TypeDecrypt:
		return "Decrypt"
	case TypeShutdown:
		return "Shutdown"
	case TypeOK:
		return "OK"
	case TypeErr:
		return "Err"
	case TypeEventMessage:
		return "EventMessage"
	case TypeEventKeys:
		return "EventKeys"
	case TypeEventExpiration:
		return "EventExpiration"
	case TypeEventClosed:
		return "EventClosed"
	}
	return fmt.Sprintf("FrameType(%d)", uint8(t))
}

// Frame is one protocol message.
type Frame struct {
	Type    FrameType
	ID      uint32
	Payload []byte
}

// ErrFrameTooLarge is returned for a frame longer than MaxFrameSize.
var ErrFrameTooLarge = errors.New("cdm: protocol frame too large")

// WriteFrame writes f with a single Write call (so concurrent writers that hold a lock never interleave).
func WriteFrame(w io.Writer, f Frame) error {
	n := frameHeader + len(f.Payload)
	if n > MaxFrameSize {
		return ErrFrameTooLarge
	}
	buf := make([]byte, 4+n)
	binary.BigEndian.PutUint32(buf, uint32(n))
	buf[4] = byte(f.Type)
	binary.BigEndian.PutUint32(buf[5:], f.ID)
	copy(buf[9:], f.Payload)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one frame. It returns io.EOF only on a clean end of stream between frames.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n < frameHeader {
		return Frame{}, fmt.Errorf("cdm: protocol frame length %d is shorter than its header", n)
	}
	if n > MaxFrameSize {
		return Frame{}, ErrFrameTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return Frame{Type: FrameType(body[0]), ID: binary.BigEndian.Uint32(body[1:5]), Payload: body[5:]}, nil
}

// packHeaderData encodes uint32 header length, JSON header, raw data.
func packHeaderData(hdr any, data []byte) ([]byte, error) {
	h, err := json.Marshal(hdr)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 4, 4+len(h)+len(data))
	binary.BigEndian.PutUint32(out, uint32(len(h)))
	out = append(out, h...)
	out = append(out, data...)
	return out, nil
}

func unpackHeaderData(p []byte, hdr any) ([]byte, error) {
	if len(p) < 4 {
		return nil, errors.New("cdm: short header+data payload")
	}
	n := binary.BigEndian.Uint32(p)
	if uint64(n) > uint64(len(p)-4) {
		return nil, errors.New("cdm: header length exceeds payload")
	}
	if err := json.Unmarshal(p[4:4+n], hdr); err != nil {
		return nil, err
	}
	return p[4+n:], nil
}

// Payload shapes (JSON). []byte fields travel as base64.

type helloReq struct {
	Magic   string `json:"magic"`
	Version int    `json:"version"`
	Client  string `json:"client,omitempty"`
}

// EngineInfo is what the helper reports in its Hello answer.
type EngineInfo struct {
	KeySystem        string      `json:"keySystem"`
	CDMVersion       string      `json:"cdmVersion"`
	InterfaceVersion int         `json:"interfaceVersion"`
	Mode             DecryptMode `json:"mode"`
	// VMPSigned is true when the helper handed a VMP signature file to the CDM (it says nothing about the CDM
	// accepting it).
	VMPSigned bool `json:"vmpSigned,omitempty"`
}

type helloResp struct {
	Version int `json:"version"`
	EngineInfo
}

// InitParams configures the CDM instance (cdm::ContentDecryptionModule::Initialize and its storage).
type InitParams struct {
	DistinctiveIdentifier bool   `json:"distinctiveIdentifier"`
	PersistentState       bool   `json:"persistentState"`
	HardwareSecure        bool   `json:"hardwareSecure"`
	Origin                string `json:"origin,omitempty"`
	StorageDir            string `json:"storageDir,omitempty"`
}

type certReq struct {
	Cert []byte `json:"cert"`
}
type supportedResp struct {
	Supported bool `json:"supported"`
}
type createReq struct {
	Type         SessionType `json:"type"`
	InitDataType string      `json:"initDataType"`
	InitData     []byte      `json:"initData"`
}
type sessionResp struct {
	SessionID string `json:"sessionId"`
}
type loadReq struct {
	Type      SessionType `json:"type"`
	SessionID string      `json:"sessionId"`
}
type foundResp struct {
	Found bool `json:"found"`
}
type updateReq struct {
	SessionID string `json:"sessionId"`
	Response  []byte `json:"response"`
}
type sessionReq struct {
	SessionID string `json:"sessionId"`
}

type decryptHdr struct {
	KeyID       []byte      `json:"keyId,omitempty"`
	IV          []byte      `json:"iv,omitempty"`
	Scheme      Scheme      `json:"scheme"`
	Crypt       uint32      `json:"crypt,omitempty"`
	Skip        uint32      `json:"skip,omitempty"`
	Subsamples  [][2]uint32 `json:"subsamples,omitempty"`
	TimestampUS int64       `json:"ts,omitempty"`
}

func encodeSample(s *Sample) ([]byte, error) {
	h := decryptHdr{KeyID: s.KeyID, IV: s.IV, Scheme: s.Scheme, Crypt: s.Pattern.CryptBlocks, Skip: s.Pattern.SkipBlocks, TimestampUS: s.TimestampUS}
	for _, ss := range s.Subsamples {
		h.Subsamples = append(h.Subsamples, [2]uint32{ss.ClearBytes, ss.CipherBytes})
	}
	return packHeaderData(h, s.Data)
}

func decodeSample(p []byte) (*Sample, error) {
	var h decryptHdr
	data, err := unpackHeaderData(p, &h)
	if err != nil {
		return nil, err
	}
	s := &Sample{Data: data, KeyID: h.KeyID, IV: h.IV, Scheme: h.Scheme, Pattern: Pattern{h.Crypt, h.Skip}, TimestampUS: h.TimestampUS}
	for _, ss := range h.Subsamples {
		s.Subsamples = append(s.Subsamples, Subsample{ss[0], ss[1]})
	}
	return s, nil
}

type errKind string

const (
	errKindException    errKind = "exception"
	errKindNoKey        errKind = "no-key"
	errKindDecrypt      errKind = "decrypt"
	errKindDecode       errKind = "decode"
	errKindNeedMoreData errKind = "need-more-data"
	errKindUnsupported  errKind = "unsupported"
	errKindClosed       errKind = "closed"
	errKindOther        errKind = "other"
)

type errPayload struct {
	Kind       errKind   `json:"kind"`
	Exception  Exception `json:"exception,omitempty"`
	SystemCode uint32    `json:"systemCode,omitempty"`
	Message    string    `json:"message,omitempty"`
}

func encodeError(err error) []byte {
	var p errPayload
	var ce *Error
	switch {
	case errors.As(err, &ce):
		p = errPayload{Kind: errKindException, Exception: ce.Exception, SystemCode: ce.SystemCode, Message: ce.Message}
	case errors.Is(err, ErrNoKey):
		p.Kind = errKindNoKey
	case errors.Is(err, ErrDecrypt):
		p.Kind, p.Message = errKindDecrypt, err.Error()
	case errors.Is(err, ErrDecode):
		p.Kind, p.Message = errKindDecode, err.Error()
	case errors.Is(err, ErrNeedMoreData):
		p.Kind = errKindNeedMoreData
	case errors.Is(err, ErrUnsupported):
		p.Kind, p.Message = errKindUnsupported, err.Error()
	case errors.Is(err, ErrClosed):
		p.Kind = errKindClosed
	default:
		p.Kind, p.Message = errKindOther, err.Error()
	}
	b, _ := json.Marshal(p)
	return b
}

func decodeError(payload []byte) error {
	var p errPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("cdm: undecodable error from the CDM host: %v", err)
	}
	switch p.Kind {
	case errKindException:
		return &Error{Exception: p.Exception, SystemCode: p.SystemCode, Message: p.Message}
	case errKindNoKey:
		return ErrNoKey
	case errKindDecrypt:
		return rewrap(ErrDecrypt, p.Message)
	case errKindDecode:
		return rewrap(ErrDecode, p.Message)
	case errKindNeedMoreData:
		return ErrNeedMoreData
	case errKindUnsupported:
		return rewrap(ErrUnsupported, p.Message)
	case errKindClosed:
		return ErrClosed
	}
	return errors.New("cdm: " + p.Message)
}

// rewrap rebuilds a sentinel error with the detail that followed it on the sending side.
func rewrap(sentinel error, msg string) error {
	detail := strings.TrimPrefix(msg, sentinel.Error())
	detail = strings.TrimPrefix(detail, ": ")
	if detail == "" {
		return sentinel
	}
	return fmt.Errorf("%w: %s", sentinel, detail)
}

type eventMessageP struct {
	SessionID string      `json:"sessionId"`
	Type      MessageType `json:"type"`
	Data      []byte      `json:"data"`
}
type keyJSON struct {
	KeyID      []byte        `json:"keyId"`
	Status     KeyStatusKind `json:"status"`
	SystemCode uint32        `json:"systemCode,omitempty"`
}
type eventKeysP struct {
	SessionID  string    `json:"sessionId"`
	Additional bool      `json:"additionalUsableKey"`
	Keys       []keyJSON `json:"keys"`
}
type eventExpirationP struct {
	SessionID string `json:"sessionId"`
	// UnixMilli is the expiry in Unix milliseconds; 0 means none.
	UnixMilli int64 `json:"unixMilli"`
}
type eventClosedP struct {
	SessionID string `json:"sessionId"`
}

func timeToMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func milliToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
