package media

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
)

// Errors returned by MSEBuffer.Append.
var (
	// ErrQuotaExceeded: the buffer is full and nothing before the current
	// playback position can be evicted (QuotaExceededError in the API).
	ErrQuotaExceeded = errors.New("mse: buffer full")
	// ErrBadStream: the appended bytes are not a valid stream (decode error).
	ErrBadStream = errors.New("mse: malformed byte stream")
)

// Default buffer limits, the same as Chromium's SourceBuffer limits.
const (
	QuotaVideo = 150 << 20
	QuotaAudio = 12 << 20
)

// MSEInit is reported with every initialization segment.
type MSEInit struct {
	Duration float64
	Tracks   []Track
	First    bool // the first initialization segment of this buffer
}

// MSEResult is what one append produced.
type MSEResult struct {
	Init     *MSEInit
	Frames   int // coded frames added to the buffer
	Dropped  int // frames discarded (append window, missing random access point)
	Evicted  int // bytes freed by coded frame eviction before the append
	Buffered [][2]float64
}

// MSEBuffer is the Go side of one SourceBuffer: a byte-stream parser plus the
// coded frame store of each track, with the MediaSource coded frame
// processing rules (timestampOffset, append window, segments/sequence mode,
// overlap removal, random access point handling) and the buffer limits.
type MSEBuffer struct {
	mu     sync.Mutex
	Mime   string
	parser segParser
	stores map[int]*TrackStore
	order  []int
	cfgs   map[int]*TrackConfig
	inited bool

	sequence      bool
	tso           float64
	winStart      float64
	winEnd        float64
	groupEnd      float64
	groupStart    float64
	groupStartSet bool
	lastDTS       map[int]float64
	lastDur       map[int]float64
	needRAP       map[int]bool
	quota         int
	gen           uint64
}

// NewMSEBuffer creates a buffer for a SourceBuffer of the given MIME type.
func NewMSEBuffer(mime string) *MSEBuffer {
	b := &MSEBuffer{Mime: mime, winEnd: math.Inf(1)}
	b.initParser(mime)
	return b
}

func (b *MSEBuffer) initParser(mime string) {
	if strings.Contains(strings.ToLower(mime), "webm") {
		b.parser = newWebMParser()
	} else {
		b.parser = newMP4Parser()
	}
	b.stores = map[int]*TrackStore{}
	b.cfgs = map[int]*TrackConfig{}
	b.order = nil
	b.inited = false
	b.resetState()
}

func (b *MSEBuffer) resetState() {
	b.lastDTS = map[int]float64{}
	b.lastDur = map[int]float64{}
	b.needRAP = map[int]bool{}
	for _, id := range b.order {
		b.needRAP[id] = true
	}
}

// ChangeType switches the buffer to another MIME type: the parser is
// replaced and the next appended initialization segment redefines the
// tracks. Already buffered frames stay (they keep their own configuration).
func (b *MSEBuffer) ChangeType(mime string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := b.stores
	order := b.order
	b.Mime = mime
	b.initParser(mime)
	// frames of the old type stay playable: keep their stores until the new
	// init segment maps tracks with the same ids onto them
	b.stores, b.order = keep, order
	b.inited = false
	b.resetState()
}

// SetMode selects "segments" (false) or "sequence" (true) timestamp mode.
func (b *MSEBuffer) SetMode(sequence bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sequence = sequence
	if sequence {
		b.groupStart, b.groupStartSet = b.groupEnd, true
	}
}

// SetTimestampOffset sets the offset added to every coded frame timestamp.
func (b *MSEBuffer) SetTimestampOffset(v float64) {
	b.mu.Lock()
	b.tso = v
	if b.sequence {
		b.groupStart, b.groupStartSet = v, true
	}
	b.mu.Unlock()
}

// TimestampOffset returns the current offset (sequence mode changes it).
func (b *MSEBuffer) TimestampOffset() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tso
}

// SetAppendWindow sets the append window; frames outside it are dropped.
func (b *MSEBuffer) SetAppendWindow(start, end float64) {
	b.mu.Lock()
	b.winStart, b.winEnd = start, end
	b.mu.Unlock()
}

// SetQuota overrides the buffer limit in bytes (0 restores the default).
func (b *MSEBuffer) SetQuota(n int) {
	b.mu.Lock()
	b.quota = n
	b.mu.Unlock()
}

// Abort resets the parser and the append window (SourceBuffer.abort()).
func (b *MSEBuffer) Abort() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.parser.reset()
	b.winStart, b.winEnd = 0, math.Inf(1)
	if b.sequence {
		b.groupStart, b.groupStartSet = b.groupEnd, true
	}
	b.resetState()
}

func (b *MSEBuffer) limitLocked() int {
	if b.quota > 0 {
		return b.quota
	}
	for _, id := range b.order {
		if b.stores[id].Config().Kind == "video" {
			return QuotaVideo
		}
	}
	return QuotaAudio
}

func (b *MSEBuffer) bytesLocked() int {
	n := 0
	for _, s := range b.stores {
		n += s.Bytes()
	}
	return n
}

// Append feeds data to the parser and stores the coded frames it yields. cur
// is the playback position: when the buffer is full, frames before it are
// evicted, oldest group of pictures first.
func (b *MSEBuffer) Append(data []byte, cur float64) (*MSEResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := &MSEResult{}
	if lim := b.limitLocked(); b.bytesLocked()+len(data) > lim {
		need := b.bytesLocked() + len(data) - lim
		freed := 0
		for _, id := range b.order {
			if freed >= need {
				break
			}
			freed += b.stores[id].EvictBefore(cur-0.1, need-freed)
		}
		res.Evicted = freed
		if b.bytesLocked()+len(data) > lim {
			return res, ErrQuotaExceeded
		}
	}
	init, frames, err := b.parser.feed(data)
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrBadStream, err)
	}
	if init != nil {
		b.applyInit(init, res)
	}
	if len(frames) > 0 && !b.inited {
		return res, fmt.Errorf("%w: media segment before initialization segment", ErrBadStream)
	}
	b.process(frames, res)
	res.Buffered = b.bufferedLocked()
	return res, nil
}

func (b *MSEBuffer) applyInit(in *initSegment, res *MSEResult) {
	first := !b.inited && len(b.order) == 0
	var tracks []Track
	for i := range in.tracks {
		c := in.tracks[i]
		cp := c
		b.cfgs[c.ID] = &cp
		if st, ok := b.stores[c.ID]; ok {
			st.SetConfig(c)
		} else {
			b.stores[c.ID] = NewTrackStore(c)
			b.order = append(b.order, c.ID)
		}
		tracks = append(tracks, Track{ID: c.ID, Kind: c.Kind, Codec: c.Codec, CodecName: c.CodecName, Width: c.Width, Height: c.Height,
			FrameRate: c.FrameRate, SampleRate: c.SampleRate, Channels: c.Channels})
	}
	b.inited = true
	b.resetState()
	res.Init = &MSEInit{Duration: in.duration, Tracks: tracks, First: first}
}

// process runs the coded frame processing algorithm over frames in decode
// order and inserts the accepted ones.
func (b *MSEBuffer) process(frames []codedFrame, res *MSEResult) {
	runs := map[int][]Sample{}
	flush := func(id int) {
		if r := runs[id]; len(r) > 0 {
			b.stores[id].Insert(r)
			res.Frames += len(r)
			delete(runs, id)
			b.gen++
		}
	}
	for _, f := range frames {
		st := b.stores[f.track]
		if st == nil {
			continue
		}
		for retry := 0; ; retry++ {
			pts, dts := f.pts+b.tso, f.dts+b.tso
			if b.sequence && b.groupStartSet {
				b.tso = b.groupStart - f.pts
				b.groupEnd = b.groupStart
				b.groupStartSet = false
				for id := range b.needRAP {
					b.needRAP[id] = true
				}
				pts, dts = f.pts+b.tso, f.dts+b.tso
			}
			if ld, ok := b.lastDTS[f.track]; ok && retry == 0 && (dts < ld-1e-9 || dts-ld > 2*b.lastDur[f.track]+1e-9) {
				if b.sequence {
					b.groupStart, b.groupStartSet = b.groupEnd, true
				} else {
					b.groupEnd = pts
				}
				for id := range b.needRAP {
					b.needRAP[id] = true
					delete(b.lastDTS, id)
					delete(b.lastDur, id)
				}
				for id := range runs {
					flush(id)
				}
				continue
			}
			if b.needRAP[f.track] {
				if !f.key {
					res.Dropped++
					flush(f.track)
					break
				}
				b.needRAP[f.track] = false
			}
			// audio frames that straddle the start of the window are kept (a decoder needs the
			// priming frame); everything else outside the window is dropped
			if (pts < b.winStart-1e-9 && !(st.Config().Kind == "audio" && pts+f.dur > b.winStart+1e-9)) || pts+f.dur > b.winEnd+1e-9 {
				res.Dropped++
				b.needRAP[f.track] = true
				flush(f.track)
				break
			}
			b.lastDTS[f.track] = dts
			b.lastDur[f.track] = f.dur
			if e := pts + f.dur; e > b.groupEnd {
				b.groupEnd = e
			}
			s := NewSample(dts, pts, f.dur, f.key, f.data)
			s.Cfg = b.cfgs[f.track]
			runs[f.track] = append(runs[f.track], s)
			break
		}
	}
	for id := range runs {
		flush(id)
	}
}

// Remove deletes buffered frames in [start, end) (SourceBuffer.remove()).
func (b *MSEBuffer) Remove(start, end float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.stores {
		s.Remove(start, end)
	}
	b.gen++
}

// Truncate removes everything at or after d (a duration shorter than the
// buffered data, MediaSource.duration setter).
func (b *MSEBuffer) Truncate(d float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.stores {
		s.Remove(d, math.Inf(1))
	}
	b.gen++
}

// Buffered is the intersection of the tracks' buffered ranges.
func (b *MSEBuffer) Buffered() [][2]float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bufferedLocked()
}

func (b *MSEBuffer) bufferedLocked() [][2]float64 {
	var out [][2]float64
	for i, id := range b.order {
		r := clampRanges(b.stores[id].Ranges())
		if i == 0 {
			out = r
		} else {
			out = IntersectRanges(out, r)
		}
		if len(out) == 0 {
			return nil
		}
	}
	return out
}

// IntersectRanges returns the intersection of two sorted range lists.
func IntersectRanges(a, b [][2]float64) [][2]float64 {
	var o [][2]float64
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		s, e := math.Max(a[i][0], b[j][0]), math.Min(a[i][1], b[j][1])
		if e > s {
			o = append(o, [2]float64{s, e})
		}
		if a[i][1] < b[j][1] {
			i++
		} else {
			j++
		}
	}
	return o
}

// Store returns the sample store of a track id (nil when unknown).
func (b *MSEBuffer) Store(id int) *TrackStore {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stores[id]
}

// FirstStore returns the first store of the given kind ("video" or "audio").
func (b *MSEBuffer) FirstStore(kind string) *TrackStore {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range b.order {
		if b.stores[id].Config().Kind == kind {
			return b.stores[id]
		}
	}
	return nil
}

// Tracks lists the track configurations in init order.
func (b *MSEBuffer) Tracks() []TrackConfig {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []TrackConfig
	for _, id := range b.order {
		out = append(out, b.stores[id].Config())
	}
	return out
}

// Bytes is the amount of coded data held.
func (b *MSEBuffer) Bytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytesLocked()
}

// Gen changes whenever the buffered data changes.
func (b *MSEBuffer) Gen() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen
}

// clampRanges keeps buffered ranges on the non-negative timeline.
func clampRanges(r [][2]float64) [][2]float64 {
	for i := range r {
		if r[i][0] < 0 {
			r[i][0] = 0
		}
	}
	if len(r) > 0 && r[0][1] <= 0 {
		r = r[1:]
	}
	return r
}
