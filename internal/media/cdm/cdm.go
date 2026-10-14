// Package cdm is the key-system layer behind Encrypted Media Extensions: one Go interface that Clear Key, Widevine,
// PlayReady and FairPlay backends all implement, so the EME JavaScript layer (MediaKeySystemAccess, MediaKeys,
// MediaKeySession) can drive every key system the same way. See docs/drm.md.
//
// This package never implements DRM itself. Clear Key is a public, keys-in-the-clear scheme and is implemented here
// in full. Every other key system hosts the vendor's own licensed Content Decryption Module (CDM) that the user
// already has on the machine: Google's Widevine CDM shared library (in a separate helper process, see remote.go),
// Windows' PlayReady in Media Foundation, Apple's FairPlay in AVFoundation.
//
// The package is pure Go (no cgo) and has no dependency on purego or golang.org/x/sys, so the static Linux
// browser binary can import it. The code that needs dlopen lives in internal/media/cdm/widevinehost, which only
// the separate helper binary cmd/sweef-cdm imports.
package cdm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SessionType is the MediaKeySessionType of the EME spec.
type SessionType string

const (
	SessionTemporary         SessionType = "temporary"
	SessionPersistentLicense SessionType = "persistent-license"
)

// MessageType is the MediaKeyMessageType of the EME spec.
type MessageType string

const (
	MessageLicenseRequest       MessageType = "license-request"
	MessageLicenseRenewal       MessageType = "license-renewal"
	MessageLicenseRelease       MessageType = "license-release"
	MessageIndividualizationReq MessageType = "individualization-request"
)

const (
	initDataTypeCenc   = "cenc"
	initDataTypeKeyIDs = "keyids"
	initDataTypeWebM   = "webm"

	defaultCallTimeout = 60 * time.Second
)

// Message is a MediaKeyMessageEvent: bytes the page has to send to a license server (or, for a release message,
// to the server that issued the license).
type Message struct {
	Type MessageType
	Data []byte
}

// Scheme is the common-encryption protection scheme of ISO/IEC 23001-7.
type Scheme string

const (
	SchemeNone Scheme = ""     // clear sample: Decrypt copies it
	SchemeCenc Scheme = "cenc" // AES-CTR, whole subsample ciphertext
	SchemeCens Scheme = "cens" // AES-CTR with a crypt:skip block pattern
	SchemeCbc1 Scheme = "cbc1" // AES-CBC, chain continues across subsamples
	SchemeCbcs Scheme = "cbcs" // AES-CBC with a crypt:skip block pattern, IV reset per subsample
)

// Pattern is the crypt/skip block pattern of cens and cbcs (blocks are 16 bytes).
type Pattern struct {
	CryptBlocks uint32
	SkipBlocks  uint32
}

// Subsample says how many leading bytes of a run are clear and how many following bytes are cipher text.
type Subsample struct {
	ClearBytes  uint32
	CipherBytes uint32
}

// Sample is one encrypted access unit as the demuxer saw it (the cenc auxiliary information is already parsed).
type Sample struct {
	Data        []byte
	KeyID       []byte // 16 bytes
	IV          []byte // 8 or 16 bytes (cenc/cens/cbc1) or the constant IV (cbcs)
	Subsamples  []Subsample
	Scheme      Scheme
	Pattern     Pattern
	TimestampUS int64
}

// KeyStatusKind is a MediaKeyStatus of the EME spec.
type KeyStatusKind string

const (
	KeyUsable           KeyStatusKind = "usable"
	KeyExpired          KeyStatusKind = "expired"
	KeyReleased         KeyStatusKind = "released"
	KeyOutputRestricted KeyStatusKind = "output-restricted"
	KeyOutputDownscaled KeyStatusKind = "output-downscaled"
	KeyStatusPending    KeyStatusKind = "status-pending"
	KeyInternalError    KeyStatusKind = "internal-error"
)

// KeyStatus is one entry of MediaKeySession.keyStatuses.
type KeyStatus struct {
	KeyID      []byte
	Status     KeyStatusKind
	SystemCode uint32
}

// EventType tells which field of an Event is set.
type EventType int

const (
	EventMessage           EventType = iota + 1 // Message
	EventKeyStatusesChange                      // KeyStatuses (the complete current list), AdditionalUsableKey
	EventExpirationChange                       // Expiration (zero time: NaN, no expiry)
	EventClosed                                 // the session was closed by the CDM or by Close
)

// Event is what a session emits after the call that created it returned (renewal messages, key status changes,
// expiry changes, closure). The first license request is returned by GenerateRequest, not delivered as an event.
// For Update, the EventKeyStatusesChange / EventExpirationChange events caused by the response are queued before
// Update returns, matching the order the EME spec requires for keystatuseschange and the promise.
type Event struct {
	Type                EventType
	Message             Message
	KeyStatuses         []KeyStatus
	AdditionalUsableKey bool
	Expiration          time.Time
}

// Exception names the DOMException a failed promise rejects with (the cdm::Exception of the CDM API).
type Exception string

const (
	ExceptionTypeError         Exception = "TypeError"
	ExceptionNotSupportedError Exception = "NotSupportedError"
	ExceptionInvalidStateError Exception = "InvalidStateError"
	ExceptionQuotaExceeded     Exception = "QuotaExceededError"
)

// Error is a CDM failure that EME maps to a promise rejection.
type Error struct {
	Exception  Exception
	SystemCode uint32
	Message    string
}

func (e *Error) Error() string {
	s := string(e.Exception)
	if s == "" {
		s = "cdm error"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.SystemCode != 0 {
		s += fmt.Sprintf(" (system code %d)", e.SystemCode)
	}
	return s
}

// Errorf builds an *Error.
func Errorf(ex Exception, format string, args ...any) *Error {
	return &Error{Exception: ex, Message: fmt.Sprintf(format, args...)}
}

// Sentinel errors of the decrypt path.
var (
	ErrNoKey        = errors.New("cdm: the required decryption key is not available")
	ErrDecrypt      = errors.New("cdm: decryption failed")
	ErrDecode       = errors.New("cdm: decoding failed")
	ErrNeedMoreData = errors.New("cdm: the decoder needs more data")
	ErrUnsupported  = errors.New("cdm: not supported by this key system")
	ErrClosed       = errors.New("cdm: closed")
	ErrHelperDied   = errors.New("cdm: the CDM host process exited")
)

// DecryptMode says how a backend's protected media reaches the screen and speakers.
type DecryptMode int

const (
	// ModeDecrypt: Decrypt returns clear (still compressed) samples that Sweef's own decoders decode. This is the
	// shape of Clear Key and of the desktop Widevine CDM when it is used decrypt-only.
	ModeDecrypt DecryptMode = iota + 1
	// ModeDecryptAndDecode: the CDM decrypts and decodes internally and returns raw frames (the Decoder
	// interface). Decrypt returns ErrUnsupported.
	ModeDecryptAndDecode
	// ModePlatformRender: the key system decodes and renders inside the operating system's media stack (PlayReady
	// in Media Foundation, FairPlay in AVFoundation). Neither Decrypt nor Decoder is available; the stream has to be
	// handed to the platform through PlatformRenderer.
	ModePlatformRender
)

func (m DecryptMode) String() string {
	switch m {
	case ModeDecrypt:
		return "decrypt"
	case ModeDecryptAndDecode:
		return "decrypt-and-decode"
	case ModePlatformRender:
		return "platform-render"
	}
	return fmt.Sprintf("DecryptMode(%d)", int(m))
}

// InstanceConfig is what MediaKeySystemAccess.createMediaKeys passes down: the negotiated configuration of the
// access object plus where the CDM may keep state.
type InstanceConfig struct {
	DistinctiveIdentifierAllowed bool
	PersistentStateAllowed       bool
	// HardwareSecure is true when the negotiated robustness level requires a hardware-backed CDM.
	HardwareSecure bool
	// Origin is the serialized origin (https://example.com) the MediaKeys belong to. CDM storage is per origin.
	Origin string
	// StorageDir is the root directory for per-origin CDM state (device certificates, persistent licenses). Empty
	// disables persistent state.
	StorageDir string
	// SessionTypes are the session types the access object was negotiated for.
	SessionTypes []SessionType
}

// Backend is one MediaKeys object: a CDM instance of a key system. All methods are safe for concurrent use.
//
// The method set maps one to one onto EME: createMediaKeys is Provider.New, setServerCertificate is
// SetServerCertificate, createSession is CreateSession, and Decrypt serves the media pipeline.
type Backend interface {
	// KeySystem is the key system string ("com.widevine.alpha").
	KeySystem() string
	// Mode says how protected media is consumed; see DecryptMode.
	Mode() DecryptMode
	// SetServerCertificate provides a license server certificate. It reports false (and no error) when the key
	// system does not support server certificates, which EME turns into a resolved false promise.
	SetServerCertificate(ctx context.Context, cert []byte) (supported bool, err error)
	// CreateSession creates a MediaKeySession without talking to the CDM yet.
	CreateSession(ctx context.Context, typ SessionType) (Session, error)
	// Decrypt decrypts one sample with a key of any open session. It returns ErrNoKey when no session holds the
	// key, ErrDecrypt on a malformed sample, ErrUnsupported in the non-ModeDecrypt modes.
	Decrypt(ctx context.Context, s *Sample) ([]byte, error)
	// Close releases the CDM instance and closes every session.
	Close() error
}

// Session is a MediaKeySession.
type Session interface {
	// ID is the sessionId. It is empty until GenerateRequest or Load succeeded.
	ID() string
	// Events delivers renewal messages, key status and expiration changes and closure. The channel is closed
	// when the session is closed. It never blocks the CDM: events queue without bound.
	Events() <-chan Event
	// GenerateRequest generates a license request from init data. initDataType is the EME init data type
	// ("cenc", "keyids", "webm"; "skd" and "sinf" for FairPlay). It returns the first message, normally a
	// license-request; Widevine without a device certificate returns an individualization-request first, which
	// the EME layer must dispatch to the page exactly as a license request.
	GenerateRequest(ctx context.Context, initDataType string, initData []byte) (Message, error)
	// Load loads a persistent session by id. It reports false when the session does not exist.
	Load(ctx context.Context, sessionID string) (found bool, err error)
	// Update supplies a license server response (or a certificate for an individualization request).
	Update(ctx context.Context, response []byte) error
	// Close ends the session; stored persistent data stays.
	Close(ctx context.Context) error
	// Remove removes stored session data. For persistent sessions the CDM emits a license-release message as an
	// event.
	Remove(ctx context.Context) error
	// KeyStatuses is the current MediaKeySession.keyStatuses.
	KeyStatuses() []KeyStatus
	// Expiration is MediaKeySession.expiration; the zero time stands for NaN (no expiration).
	Expiration() time.Time
}

// VideoCodecID, AudioCodecID and the config types below mirror the cdm::VideoDecoderConfig_3 and
// cdm::AudioDecoderConfig_2 structures of the CDM API for the decode-capable backends.
type StreamType int

const (
	StreamAudio StreamType = iota
	StreamVideo
)

// VideoConfig initializes a CDM video decoder.
type VideoConfig struct {
	Codec        string // "vp8", "avc1", "vp9", "av01"
	Profile      string // codec specific, "" if not needed
	CodedWidth   int
	CodedHeight  int
	ExtraData    []byte // avcC / vpcC / av1C payload
	Scheme       Scheme
	ColorPrimary uint8
	Transfer     uint8
	Matrix       uint8
	FullRange    bool
}

// AudioConfig initializes a CDM audio decoder.
type AudioConfig struct {
	Codec         string // "vorbis", "aac"
	Channels      int
	BitsPerSample int
	SampleRate    int
	ExtraData     []byte
	Scheme        Scheme
}

// VideoFrame is a decoded frame returned by a decode-capable CDM.
type VideoFrame struct {
	Format      string // "I420", "YV12", "YUV420P10", ...
	Width       int
	Height      int
	Planes      [3][]byte
	Strides     [3]int
	TimestampUS int64
}

// AudioFrames is decoded PCM returned by a decode-capable CDM.
type AudioFrames struct {
	Format string // "s16", "f32", "f32-planar", ...
	Data   []byte
}

// Decoder is implemented, in addition to Backend, by backends whose CDM decrypts and decodes in one step
// (ModeDecryptAndDecode). The media pipeline checks for it with a type assertion.
type Decoder interface {
	InitAudioDecoder(ctx context.Context, cfg AudioConfig) error
	InitVideoDecoder(ctx context.Context, cfg VideoConfig) error
	// DecryptAndDecodeVideo returns ErrNeedMoreData while the decoder is filling its pipeline. Passing a Sample
	// with nil Data drains it at end of stream until ErrNeedMoreData.
	DecryptAndDecodeVideo(ctx context.Context, s *Sample) (*VideoFrame, error)
	DecryptAndDecodeAudio(ctx context.Context, s *Sample) (*AudioFrames, error)
	ResetDecoder(ctx context.Context, st StreamType) error
	DeinitDecoder(ctx context.Context, st StreamType) error
}

// StreamHandoff describes protected content a platform renderer takes over.
type StreamHandoff struct {
	// URL of the manifest or media (the platform fetches and decrypts it itself).
	URL string
	// NativeLayer is a platform surface to render into: a CALayer / AVSampleBufferDisplayLayer pointer on macOS, an
	// HWND or IDXGI/DComp visual on Windows. Zero lets the backend create its own window.
	NativeLayer uintptr
}

// PlatformRenderer is implemented by ModePlatformRender backends (PlayReady, FairPlay). The media pipeline must
// route a protected stream through it instead of through Sweef's demuxers and decoders: the key never leaves the
// operating system, so Sweef cannot see decrypted samples.
type PlatformRenderer interface {
	Attach(ctx context.Context, h StreamHandoff) (PlatformPlayback, error)
}

// PlatformPlayback controls a stream that a PlatformRenderer is playing.
type PlatformPlayback interface {
	Play() error
	Pause() error
	Seek(position time.Duration) error
	Position() time.Duration
	Close() error
}
