package mediarec

import (
	"encoding/binary"
	"math/rand/v2"
)

// OggOpus writes an Ogg Opus stream (RFC 7845): an OpusHead page, an OpusTags page, then audio pages whose
// granule position counts 48 kHz samples including the pre-skip. Bytes accumulate in an internal buffer that Take
// hands out; whatever was taken, concatenated, is the stream so far.
type OggOpus struct {
	serial   uint32
	seq      uint32
	preSkip  int
	granule  int64 // 48 kHz samples of the packets already closed into a page (plus pre-skip)
	pend     [][]byte
	pendSamp int
	pendSegs int
	out      []byte
	done     bool
}

var oggCRC = func() *[256]uint32 {
	var t [256]uint32
	for i := range t {
		c := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return &t
}()

// NewOggOpus starts a stream and writes the two header pages. inputRate is informational (OpusHead).
func NewOggOpus(channels, inputRate, preSkip int) *OggOpus {
	o := &OggOpus{serial: rand.Uint32(), preSkip: preSkip, granule: int64(preSkip)}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8], head[9] = 1, byte(channels)
	binary.LittleEndian.PutUint16(head[10:], uint16(preSkip))
	binary.LittleEndian.PutUint32(head[12:], uint32(inputRate))
	o.page([][]byte{head}, 0x02, 0)
	const vendor = "Sweef"
	tags := make([]byte, 0, 8+4+len(vendor)+4)
	tags = append(tags, "OpusTags"...)
	tags = binary.LittleEndian.AppendUint32(tags, uint32(len(vendor)))
	tags = append(tags, vendor...)
	tags = binary.LittleEndian.AppendUint32(tags, 0)
	o.page([][]byte{tags}, 0, 0)
	return o
}

// page writes one page holding whole packets.
func (o *OggOpus) page(pk [][]byte, flags byte, granule int64) {
	var segs []byte
	size := 0
	for _, p := range pk {
		n := len(p)
		for n >= 255 {
			segs = append(segs, 255)
			n -= 255
		}
		segs = append(segs, byte(n))
		size += len(p)
	}
	start := len(o.out)
	o.out = append(o.out, "OggS"...)
	o.out = append(o.out, 0, flags)
	o.out = binary.LittleEndian.AppendUint64(o.out, uint64(granule))
	o.out = binary.LittleEndian.AppendUint32(o.out, o.serial)
	o.out = binary.LittleEndian.AppendUint32(o.out, o.seq)
	o.seq++
	crcAt := len(o.out)
	o.out = append(o.out, 0, 0, 0, 0, byte(len(segs)))
	o.out = append(o.out, segs...)
	for _, p := range pk {
		o.out = append(o.out, p...)
	}
	var c uint32
	for _, b := range o.out[start:] {
		c = c<<8 ^ oggCRC[byte(c>>24)^b]
	}
	binary.LittleEndian.PutUint32(o.out[crcAt:], c)
}

func segsOf(n int) int { return n/255 + 1 }

// AddPacket appends one Opus packet that covers samples 48 kHz samples. A page is closed when it would exceed
// 255 segments or hold about a second of audio.
func (o *OggOpus) AddPacket(p []byte, samples int) {
	if o.done {
		return
	}
	if o.pendSegs+segsOf(len(p)) > 255 || o.pendSamp >= 48000 {
		o.Flush()
	}
	o.pend = append(o.pend, append([]byte(nil), p...))
	o.pendSegs += segsOf(len(p))
	o.pendSamp += samples
}

// Flush closes the page being built (if any packets wait) so Take returns everything written so far.
func (o *OggOpus) Flush() {
	if len(o.pend) == 0 || o.done {
		return
	}
	o.granule += int64(o.pendSamp)
	o.page(o.pend, 0, o.granule)
	o.pend, o.pendSamp, o.pendSegs = nil, 0, 0
}

// Finish closes the stream with an end-of-stream page. realSamples is the number of 48 kHz samples of real audio
// in the packets; a larger granule position than preSkip+realSamples would count encoder padding as music.
func (o *OggOpus) Finish(realSamples int64) {
	if o.done {
		return
	}
	total := o.granule + int64(o.pendSamp)
	if g := int64(o.preSkip) + realSamples; realSamples >= 0 && g < total {
		total = g
	}
	o.done = true
	o.page(o.pend, 0x04, total)
	o.pend = nil
}

// Take returns the bytes written since the last call.
func (o *OggOpus) Take() []byte {
	b := o.out
	o.out = nil
	return b
}
