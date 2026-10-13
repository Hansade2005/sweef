// Package pulse is a small pure-Go client for the PulseAudio native protocol over a unix socket. It speaks to
// PulseAudio and to PipeWire's pulse server alike and needs no library: enough of the protocol to enumerate
// sources, find the default one and record from it.
//
// The protocol version announced is 13: the oldest version with property lists, which every server since
// PulseAudio 0.9.22 and PipeWire accepts. A server always answers in the version the client announces, so the
// layouts below are the version-13 ones and do not change when the server is newer.
package pulse

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// protocolVersion is announced in AUTH. Bits 16 and above are flags (shared memory, memfd); none is set.
const protocolVersion = 13

// Commands (pulsecore/native-common.h).
const (
	cmdError                = 0
	cmdReply                = 2
	cmdCreatePlaybackStream = 3
	cmdDeletePlaybackStream = 4
	cmdCreateRecordStream   = 5
	cmdDeleteRecordStream   = 6
	cmdAuth                 = 8
	cmdSetClientName        = 9
	cmdDrainPlaybackStream  = 12
	cmdGetPlaybackLatency   = 14
	cmdGetServerInfo        = 20
	cmdGetSourceInfoList    = 24
	cmdSetSinkInputVolume   = 37
	cmdCorkPlaybackStream   = 41
	cmdFlushPlaybackStream  = 42
	cmdRequest              = 61
	cmdOverflow             = 62
	cmdUnderflow            = 63
	cmdPlaybackStreamKilled = 64
	cmdRecordStreamKilled   = 65
)

// Tags of the tagged-struct encoding (pulsecore/tagstruct.h).
const (
	tagString     = 't'
	tagStringNull = 'N'
	tagU32        = 'L'
	tagU8         = 'B'
	tagU64        = 'R'
	tagSampleSpec = 'a'
	tagArbitrary  = 'x'
	tagBoolTrue   = '1'
	tagBoolFalse  = '0'
	tagUsec       = 'U'
	tagChannelMap = 'm'
	tagCVolume    = 'v'
	tagProplist   = 'P'
	tagVolume     = 'V'
	tagTimeval    = 'T'
	tagS64        = 'r'
	tagFormatInfo = 'f'
)

// invalidIndex is PA_INVALID_INDEX.
const invalidIndex = 0xFFFFFFFF

// Sample formats (pa_sample_format_t).
const (
	sampleS16LE     = 3
	sampleFloat32LE = 5
)

// Channel positions (pa_channel_position_t).
const (
	posMono       = 0
	posFrontLeft  = 1
	posFrontRight = 2
)

// Error codes of an ERROR reply (pa_error_code_t) that the client distinguishes.
const (
	errAccess   = 1
	errNoEntity = 5
	errBusy     = 6
)

// ServerError is an ERROR reply.
type ServerError struct{ Code uint32 }

func (e *ServerError) Error() string {
	names := map[uint32]string{1: "access denied", 2: "unknown command", 3: "invalid argument", 4: "entity exists",
		5: "no such entity", 6: "connection refused", 7: "protocol error", 8: "timeout", 9: "no authentication key",
		10: "internal error", 11: "connection terminated", 12: "entity killed", 13: "invalid server", 14: "module init failed",
		15: "bad state", 16: "no data", 17: "incompatible protocol version", 18: "too large", 19: "not supported"}
	if n, ok := names[e.Code]; ok {
		return "pulse: " + n
	}
	return fmt.Sprintf("pulse: server error %d", e.Code)
}

// ---- writer ---------------------------------------------------------------------------------------------------

type tagWriter struct{ b []byte }

func (w *tagWriter) u32(v uint32) {
	w.b = append(w.b, tagU32, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func (w *tagWriter) u8(v byte) { w.b = append(w.b, tagU8, v) }

func (w *tagWriter) str(s string) {
	w.b = append(w.b, tagString)
	w.b = append(w.b, s...)
	w.b = append(w.b, 0)
}

func (w *tagWriter) nullStr() { w.b = append(w.b, tagStringNull) }

func (w *tagWriter) boolean(v bool) {
	if v {
		w.b = append(w.b, tagBoolTrue)
	} else {
		w.b = append(w.b, tagBoolFalse)
	}
}

func (w *tagWriter) arbitrary(p []byte) {
	w.b = append(w.b, tagArbitrary, byte(len(p)>>24), byte(len(p)>>16), byte(len(p)>>8), byte(len(p)))
	w.b = append(w.b, p...)
}

func (w *tagWriter) sampleSpec(format, channels byte, rate uint32) {
	w.b = append(w.b, tagSampleSpec, format, channels, byte(rate>>24), byte(rate>>16), byte(rate>>8), byte(rate))
}

func (w *tagWriter) channelMap(m []byte) {
	w.b = append(w.b, tagChannelMap, byte(len(m)))
	w.b = append(w.b, m...)
}

// proplist writes a property list of string values.
func (w *tagWriter) proplist(kv ...string) {
	w.b = append(w.b, tagProplist)
	for i := 0; i+1 < len(kv); i += 2 {
		w.str(kv[i])
		v := append([]byte(kv[i+1]), 0)
		w.u32(uint32(len(v)))
		w.arbitrary(v)
	}
	w.nullStr()
}

func (w *tagWriter) cvolume(channels int, vol uint32) {
	w.b = append(w.b, tagCVolume, byte(channels))
	for i := 0; i < channels; i++ {
		w.b = append(w.b, byte(vol>>24), byte(vol>>16), byte(vol>>8), byte(vol))
	}
}

func (w *tagWriter) timeval(sec, usec uint32) {
	w.b = append(w.b, tagTimeval, byte(sec>>24), byte(sec>>16), byte(sec>>8), byte(sec), byte(usec>>24), byte(usec>>16), byte(usec>>8), byte(usec))
}

// ---- reader ---------------------------------------------------------------------------------------------------

var errShort = errors.New("pulse: truncated message")

type tagReader struct {
	b   []byte
	err error
}

func (r *tagReader) fail() { r.err = errShort }

func (r *tagReader) tag(want byte) bool {
	if r.err != nil {
		return false
	}
	if len(r.b) < 1 || r.b[0] != want {
		r.err = fmt.Errorf("pulse: unexpected tag in reply (want %q)", want)
		return false
	}
	r.b = r.b[1:]
	return true
}

func (r *tagReader) u32() uint32 {
	if !r.tag(tagU32) {
		return 0
	}
	if len(r.b) < 4 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *tagReader) u8() byte {
	if !r.tag(tagU8) {
		return 0
	}
	if len(r.b) < 1 {
		r.fail()
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *tagReader) boolean() bool {
	if r.err != nil {
		return false
	}
	if len(r.b) < 1 || (r.b[0] != tagBoolTrue && r.b[0] != tagBoolFalse) {
		r.err = errors.New("pulse: expected a boolean")
		return false
	}
	v := r.b[0] == tagBoolTrue
	r.b = r.b[1:]
	return v
}

// str reads a string or a null string (returned as "").
func (r *tagReader) str() string {
	if r.err != nil {
		return ""
	}
	if len(r.b) > 0 && r.b[0] == tagStringNull {
		r.b = r.b[1:]
		return ""
	}
	if !r.tag(tagString) {
		return ""
	}
	for i, c := range r.b {
		if c == 0 {
			s := string(r.b[:i])
			r.b = r.b[i+1:]
			return s
		}
	}
	r.fail()
	return ""
}

func (r *tagReader) usec() uint64 {
	if !r.tag(tagUsec) {
		return 0
	}
	if len(r.b) < 8 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v
}

func (r *tagReader) timeval() {
	if !r.tag(tagTimeval) {
		return
	}
	if len(r.b) < 8 {
		r.fail()
		return
	}
	r.b = r.b[8:]
}

func (r *tagReader) s64() int64 {
	if !r.tag(tagS64) {
		return 0
	}
	if len(r.b) < 8 {
		r.fail()
		return 0
	}
	v := int64(binary.BigEndian.Uint64(r.b))
	r.b = r.b[8:]
	return v
}

func (r *tagReader) u64() uint64 {
	if !r.tag(tagU64) {
		return 0
	}
	if len(r.b) < 8 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v
}

type sampleSpec struct {
	Format, Channels byte
	Rate             uint32
}

func (r *tagReader) sampleSpec() sampleSpec {
	var s sampleSpec
	if !r.tag(tagSampleSpec) {
		return s
	}
	if len(r.b) < 6 {
		r.fail()
		return s
	}
	s.Format, s.Channels, s.Rate = r.b[0], r.b[1], binary.BigEndian.Uint32(r.b[2:])
	r.b = r.b[6:]
	return s
}

func (r *tagReader) channelMap() []byte {
	if !r.tag(tagChannelMap) {
		return nil
	}
	if len(r.b) < 1 || len(r.b) < 1+int(r.b[0]) {
		r.fail()
		return nil
	}
	n := int(r.b[0])
	m := append([]byte(nil), r.b[1:1+n]...)
	r.b = r.b[1+n:]
	return m
}

func (r *tagReader) cvolume() {
	if !r.tag(tagCVolume) {
		return
	}
	if len(r.b) < 1 || len(r.b) < 1+4*int(r.b[0]) {
		r.fail()
		return
	}
	r.b = r.b[1+4*int(r.b[0]):]
}

func (r *tagReader) volume() {
	if !r.tag(tagVolume) {
		return
	}
	if len(r.b) < 4 {
		r.fail()
		return
	}
	r.b = r.b[4:]
}

// proplist skips (and returns the string entries of) a property list.
func (r *tagReader) proplist() map[string]string {
	if !r.tag(tagProplist) {
		return nil
	}
	props := map[string]string{}
	for r.err == nil {
		if len(r.b) > 0 && r.b[0] == tagStringNull {
			r.b = r.b[1:]
			return props
		}
		k := r.str()
		n := r.u32()
		if !r.tag(tagArbitrary) {
			return props
		}
		if len(r.b) < 4 {
			r.fail()
			return props
		}
		l := int(binary.BigEndian.Uint32(r.b))
		r.b = r.b[4:]
		if l < 0 || l > len(r.b) || uint32(l) != n {
			r.err = errors.New("pulse: bad property length")
			return props
		}
		v := r.b[:l]
		r.b = r.b[l:]
		if l > 0 && v[l-1] == 0 {
			props[k] = string(v[:l-1])
		}
	}
	return props
}

// ---- framing --------------------------------------------------------------------------------------------------

// Every message is a 20-byte big-endian descriptor {length, channel, offset_hi, offset_lo, flags} and a payload.
// Channel 0xFFFFFFFF carries a tagged-struct control packet; any other channel carries stream data.
const (
	descSize      = 20
	controlChan   = 0xFFFFFFFF
	maxPacketSize = 64 << 20
)

func putDescriptor(b []byte, length, channel uint32) {
	binary.BigEndian.PutUint32(b[0:], length)
	binary.BigEndian.PutUint32(b[4:], channel)
	binary.BigEndian.PutUint32(b[8:], 0)
	binary.BigEndian.PutUint32(b[12:], 0)
	binary.BigEndian.PutUint32(b[16:], 0)
}

func readPacket(r io.Reader) (channel uint32, payload []byte, err error) {
	var d [descSize]byte
	if _, err = io.ReadFull(r, d[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint32(d[0:])
	channel = binary.BigEndian.Uint32(d[4:])
	if n > maxPacketSize {
		return 0, nil, fmt.Errorf("pulse: packet of %d bytes", n)
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return
}
