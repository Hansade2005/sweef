package mediarec

import (
	"encoding/binary"
	"math"
)

// WebMTrack describes one track of a recording.
type WebMTrack struct {
	Video         bool
	Width, Height int // video
	Channels      int // audio
	Rate          int // audio: sampling frequency (48000 for Opus)
	OpusHead      []byte
	PreSkip       int // samples at 48 kHz
}

// WebM writes a Matroska/WebM stream for live use: an unknown-size Segment, an Info and a Tracks element, then
// Clusters of SimpleBlocks. A cluster is written (with its real size) when it is closed, so every byte handed out
// by Take belongs to a complete element and the concatenation of the chunks is a parseable file. When nothing has
// been handed out before Finish, the duration is patched into the Info element and the file is complete; later
// chunks cannot be patched, like in browsers, and such a stream has no Duration (players find it by scanning).
type WebM struct {
	out      []byte
	taken    bool
	durAt    int // offset of the Duration placeholder in out, valid while !taken
	cl       []byte
	clTC     int64
	clOpen   bool
	hasVideo bool
	maxTC    int64
	blocks   int
}

func ebmlID(id uint32) []byte {
	switch {
	case id > 0xFFFFFF:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFF:
		return []byte{byte(id >> 8), byte(id)}
	}
	return []byte{byte(id)}
}

func ebmlSize(n int) []byte {
	switch {
	case n < 0x7F:
		return []byte{0x80 | byte(n)}
	case n < 0x3FFF:
		return []byte{0x40 | byte(n>>8), byte(n)}
	case n < 0x1FFFFF:
		return []byte{0x20 | byte(n>>16), byte(n >> 8), byte(n)}
	case n < 0x0FFFFFFF:
		return []byte{0x10 | byte(n>>24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	return []byte{0x08 | byte(n>>32), byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

func el(id uint32, body ...[]byte) []byte {
	n := 0
	for _, b := range body {
		n += len(b)
	}
	out := append(ebmlID(id), ebmlSize(n)...)
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

func elUint(id uint32, v uint64) []byte {
	n := 1
	for t := v >> 8; t != 0; t >>= 8 {
		n++
	}
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return el(id, b)
}

func elFloat(id uint32, v float64) []byte {
	return el(id, binary.BigEndian.AppendUint64(nil, math.Float64bits(v)))
}

func elStr(id uint32, s string) []byte { return el(id, []byte(s)) }

// NewWebM writes the headers for the tracks (video, when present, is track 1 and audio the next number).
func NewWebM(tracks []WebMTrack) *WebM {
	w := &WebM{}
	hdr := el(0x1A45DFA3,
		elUint(0x4286, 1), elUint(0x42F7, 1), elUint(0x42F2, 4), elUint(0x42F3, 8),
		elStr(0x4282, "webm"), elUint(0x4287, 4), elUint(0x4285, 2))
	w.out = append(w.out, hdr...)
	w.out = append(w.out, 0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF) // Segment, unknown size
	// Info carries a Void of 11 bytes where the Duration element fits; Finish fills it while the header has not left yet
	tcs := elUint(0x2AD7B1, 1000000)
	void := append([]byte{0xEC, 0x89}, make([]byte, 9)...)
	body := append(append(append([]byte(nil), tcs...), void...), append(elStr(0x4D80, "Sweef"), elStr(0x5741, "Sweef")...)...)
	info := append(ebmlID(0x1549A966), ebmlSize(len(body))...)
	w.durAt = len(w.out) + len(info) + len(tcs)
	w.out = append(append(w.out, info...), body...)
	var ents []byte
	for i, t := range tracks {
		num := uint64(i + 1)
		if t.Video {
			w.hasVideo = true
			ents = append(ents, el(0xAE, elUint(0xD7, num), elUint(0x73C5, num), elUint(0x83, 1), elUint(0x9C, 0), elStr(0x86, "V_VP8"),
				el(0xE0, elUint(0xB0, uint64(t.Width)), elUint(0xBA, uint64(t.Height))))...)
			continue
		}
		ents = append(ents, el(0xAE, elUint(0xD7, num), elUint(0x73C5, num), elUint(0x83, 2), elUint(0x9C, 0), elStr(0x86, "A_OPUS"),
			el(0x63A2, t.OpusHead), elUint(0x56AA, uint64(t.PreSkip)*1000000000/48000), elUint(0x56BB, 80000000),
			el(0xE1, elFloat(0xB5, float64(t.Rate)), elUint(0x9F, uint64(t.Channels))))...)
	}
	w.out = append(w.out, el(0x1654AE6B, ents)...)
	return w
}

// AddBlock adds one frame or packet. tsMs is its presentation time; track is the 1-based track number.
func (w *WebM) AddBlock(track int, tsMs int64, key bool, data []byte) {
	if tsMs < 0 {
		tsMs = 0
	}
	if w.clOpen {
		rel := tsMs - w.clTC
		// a new cluster every 2 s at the next key frame (or audio packet when there is no video), and always before the
		// 16-bit relative timecode could overflow
		if rel > 30000 || (rel >= 2000 && (key || !w.hasVideo)) {
			w.closeCluster()
		}
	}
	if !w.clOpen {
		w.clOpen, w.clTC = true, tsMs
		w.cl = append(w.cl[:0], elUint(0xE7, uint64(tsMs))...)
	}
	rel := tsMs - w.clTC
	flags := byte(0)
	if key {
		flags = 0x80
	}
	n := 4 + len(data)
	w.cl = append(w.cl, 0xA3)
	w.cl = append(w.cl, ebmlSize(n)...)
	w.cl = append(w.cl, 0x80|byte(track), byte(rel>>8), byte(rel), flags)
	w.cl = append(w.cl, data...)
	if tsMs > w.maxTC {
		w.maxTC = tsMs
	}
	w.blocks++
}

func (w *WebM) closeCluster() {
	if !w.clOpen {
		return
	}
	w.out = append(w.out, ebmlID(0x1F43B675)...)
	w.out = append(w.out, ebmlSize(len(w.cl))...)
	w.out = append(w.out, w.cl...)
	w.cl = w.cl[:0]
	w.clOpen = false
}

// Flush closes the open cluster so that Take returns every block added so far.
func (w *WebM) Flush() { w.closeCluster() }

// Finish closes the stream. endMs is the end time of the recording (it becomes the Duration when that still can).
func (w *WebM) Finish(endMs int64) {
	w.closeCluster()
	if !w.taken && w.durAt > 0 {
		d := el(0x4489, binary.BigEndian.AppendUint64(nil, math.Float64bits(float64(endMs))))
		if len(d) == 11 {
			copy(w.out[w.durAt:], d)
		}
	}
}

// Take returns the bytes written since the last call.
func (w *WebM) Take() []byte {
	b := w.out
	w.out = nil
	w.taken = true
	return b
}
