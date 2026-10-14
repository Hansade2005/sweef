package media

import (
	"sort"
	"sync"
)

// Sample is one coded access unit of a track. Times are in seconds on the
// presentation timeline (for MSE, after timestampOffset was applied).
type Sample struct {
	DTS, PTS, Dur float64
	Key           bool
	Size          int
	// Cfg is the configuration of the initialization segment the sample came
	// with (MSE: a quality switch changes it mid-stream); nil means the store's.
	Cfg  *TrackConfig
	data []byte
	load func() ([]byte, error) // lazy payload (progressive files); nil when data is set

	// Common Encryption: the per-sample information, the track's protection and the key holder of the buffer
	// (cenc.go). Bytes decrypts.
	enc  *SampleEnc
	prot *Protection
	kh   *KeyHolder
}

// End is the end of the sample's presentation interval.
func (s Sample) End() float64 { return s.PTS + s.Dur }

// TrackConfig is what a decoder needs to know about a track.
type TrackConfig struct {
	ID         int
	Kind       string // "video" | "audio"
	Codec      string // RFC 6381 string (avc1.64001f, mp4a.40.2, opus, vp09...)
	CodecName  string // h264 hevc vp8 vp9 av1 aac opus vorbis mp3
	Cfg        []byte // avcC/hvcC/vpcC/av1C, AudioSpecificConfig, OpusHead or dOps
	CfgKind    string // "asc" "opushead" "dops" "avcc" ... (audio) / "" (video)
	Width      int
	Height     int
	FrameRate  float64
	SampleRate int
	Channels   int
	Skip       int // audio: samples (at the output rate) to discard at the start of the track (priming)

	Prot *Protection // Common Encryption protection of the track (nil: clear)
}

// TrackStore holds the samples of one track sorted by decode time. It backs
// both progressive files (filled once) and SourceBuffers (appended to,
// overwritten and trimmed while a decoder reads from it), so every method is
// safe for concurrent use.
type TrackStore struct {
	mu     sync.RWMutex
	cfg    TrackConfig
	s      []Sample
	bytes  int
	maxCTO float64 // largest PTS-DTS seen: how far back ShownAt must look
	minCTO float64 // smallest PTS-DTS seen (negative with negative composition offsets)
	maxDur float64
	ranges [][2]float64
	valid  bool // ranges is current
	gen    uint64
}

// NewTrackStore creates an empty store.
func NewTrackStore(cfg TrackConfig) *TrackStore { return &TrackStore{cfg: cfg} }

// Config returns the track configuration.
func (t *TrackStore) Config() TrackConfig {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cfg
}

// SetConfig replaces the configuration (a new init segment).
func (t *TrackStore) SetConfig(c TrackConfig) {
	t.mu.Lock()
	t.cfg = c
	t.gen++
	t.mu.Unlock()
}

// Gen changes whenever the contents or the configuration change.
func (t *TrackStore) Gen() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.gen
}

// Len is the number of samples.
func (t *TrackStore) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.s)
}

// Bytes is the total coded size.
func (t *TrackStore) Bytes() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.bytes
}

// Append adds samples that are already ordered after everything in the store
// (progressive loading).
func (t *TrackStore) Append(ss []Sample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range ss {
		t.s = append(t.s, s)
		t.bytes += s.Size
		t.noteLocked(s)
	}
	t.valid = false
	t.gen++
}

func (t *TrackStore) noteLocked(s Sample) {
	if d := s.PTS - s.DTS; d > t.maxCTO {
		t.maxCTO = d
	} else if d < t.minCTO {
		t.minCTO = d
	}
	if s.Dur > t.maxDur {
		t.maxDur = s.Dur
	}
}

// Insert adds a run of samples in decode order (one coded frame group),
// removing what it overwrites: existing samples whose decode time falls in
// the run's span, plus the non-key samples that followed them (they lost
// their references). It reports the bytes removed.
func (t *TrackStore) Insert(run []Sample) int {
	if len(run) == 0 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	first := run[0].DTS
	last := run[len(run)-1]
	end := last.DTS + last.Dur
	if end <= first {
		end = first + 1e-6
	}
	lo := sort.Search(len(t.s), func(i int) bool { return t.s[i].DTS >= first-1e-6 })
	hi := sort.Search(len(t.s), func(i int) bool { return t.s[i].DTS >= end-1e-6 })
	// dependents: after the overwritten span, drop non-key samples
	// until the next sync sample, but only when something was overwritten
	if hi > lo {
		for hi < len(t.s) && !t.s[hi].Key {
			hi++
		}
	}
	removed := 0
	for i := lo; i < hi; i++ {
		removed += t.s[i].Size
	}
	tail := append([]Sample(nil), t.s[hi:]...)
	t.s = append(t.s[:lo], run...)
	t.s = append(t.s, tail...)
	t.bytes += -removed
	for _, s := range run {
		t.bytes += s.Size
		t.noteLocked(s)
	}
	t.valid = false
	t.gen++
	return removed
}

// Remove deletes the samples whose presentation time is in [start, end) and
// the non-key samples that depended on them. It reports the bytes freed.
func (t *TrackStore) Remove(start, end float64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.s[:0:0]
	freed := 0
	dropping := false
	for _, s := range t.s {
		if s.PTS >= start-1e-9 && s.PTS < end-1e-9 {
			freed += s.Size
			dropping = true
			continue
		}
		if dropping {
			if !s.Key {
				freed += s.Size
				continue
			}
			dropping = false
		}
		out = append(out, s)
	}
	if freed == 0 && len(out) == len(t.s) {
		return 0
	}
	t.s = out
	t.bytes -= freed
	t.valid = false
	t.gen++
	return freed
}

// RemoveAll empties the store.
func (t *TrackStore) RemoveAll() {
	t.mu.Lock()
	t.s = nil
	t.bytes = 0
	t.valid = false
	t.gen++
	t.mu.Unlock()
}

// EvictBefore frees at least need bytes by dropping whole groups of pictures
// (sync sample to the next sync sample) that end at or before limit, oldest
// first. It reports the bytes freed.
func (t *TrackStore) EvictBefore(limit float64, need int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.s) == 0 {
		return 0
	}
	freed := 0
	i := 0
	for freed < need && i < len(t.s) {
		j := i + 1
		for j < len(t.s) && !t.s[j].Key {
			j++
		}
		// group [i, j) must lie entirely before limit
		endT := 0.0
		for k := i; k < j; k++ {
			if e := t.s[k].End(); e > endT {
				endT = e
			}
		}
		if endT > limit+1e-9 {
			break
		}
		for k := i; k < j; k++ {
			freed += t.s[k].Size
		}
		i = j
	}
	if i > 0 {
		t.s = append(t.s[:0:0], t.s[i:]...)
		t.bytes -= freed
		t.valid = false
		t.gen++
	}
	return freed
}

// Ranges returns the buffered time ranges (presentation intervals merged
// across gaps smaller than twice the longest sample).
func (t *TrackStore) Ranges() [][2]float64 {
	t.mu.RLock()
	if t.valid {
		r := cloneRanges(t.ranges)
		t.mu.RUnlock()
		return r
	}
	t.mu.RUnlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.valid {
		t.ranges = computeRanges(t.s, 2*t.maxDur)
		t.valid = true
	}
	return cloneRanges(t.ranges)
}

func cloneRanges(r [][2]float64) [][2]float64 { return append([][2]float64(nil), r...) }

func computeRanges(ss []Sample, fudge float64) [][2]float64 {
	if len(ss) == 0 {
		return nil
	}
	iv := make([][2]float64, len(ss))
	for i, s := range ss {
		iv[i] = [2]float64{s.PTS, s.PTS + s.Dur}
	}
	sort.Slice(iv, func(a, b int) bool { return iv[a][0] < iv[b][0] })
	if fudge < 0.002 {
		fudge = 0.002
	}
	if fudge > 0.25 {
		fudge = 0.25
	}
	out := [][2]float64{iv[0]}
	for _, x := range iv[1:] {
		l := &out[len(out)-1]
		if x[0] <= l[1]+fudge {
			if x[1] > l[1] {
				l[1] = x[1]
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// End is the largest end time of any sample (0 when empty).
func (t *TrackStore) End() float64 {
	r := t.Ranges()
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1][1]
}

// ShownAt returns the sample presented at time tm: the one with the largest
// PTS not after tm. ok is false when tm is before the first sample, or past
// the end of the range it falls in (a gap).
func (t *TrackStore) ShownAt(tm float64) (Sample, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := len(t.s)
	if n == 0 {
		return Sample{}, false
	}
	// PTS - DTS lies in [minCTO, maxCTO]; a sample shown at tm has
	// DTS <= tm - minCTO, and (no gap) PTS > tm - maxDur
	hi := sort.Search(n, func(i int) bool { return t.s[i].DTS > tm-t.minCTO+1e-9 })
	low := tm - t.maxDur - t.maxCTO - 1e-6
	best := -1
	for i := hi - 1; i >= 0; i-- {
		s := &t.s[i]
		if s.DTS < low && best >= 0 {
			break
		}
		if s.PTS <= tm+1e-9 && (best < 0 || s.PTS > t.s[best].PTS) {
			best = i
		}
		if s.DTS < low-4 {
			break // far below the window and nothing found: tm is in a gap
		}
	}
	if best < 0 {
		return Sample{}, false
	}
	s := t.s[best]
	if tm >= s.PTS+s.Dur+t.fudgeLocked() {
		return Sample{}, false
	}
	return s, true
}

func (t *TrackStore) fudgeLocked() float64 {
	f := 2 * t.maxDur
	if f < 0.002 {
		f = 0.002
	}
	if f > 0.25 {
		f = 0.25
	}
	return f
}

// Next returns the first sample after dts in decode order. ok is false at
// the end of the store; gap is true when a hole in the timeline separates
// it from dts (the caller must restart from a sync sample).
func (t *TrackStore) Next(dts float64) (s Sample, ok, gap bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	i := sort.Search(len(t.s), func(i int) bool { return t.s[i].DTS > dts+1e-9 })
	if i >= len(t.s) {
		return Sample{}, false, false
	}
	s = t.s[i]
	if i > 0 && t.s[i].DTS-(t.s[i-1].DTS+t.s[i-1].Dur) > t.fudgeLocked() {
		gap = true
	}
	return s, true, gap
}

// At returns the sample with the given decode time exactly (within 1 us).
func (t *TrackStore) At(dts float64) (Sample, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	i := sort.Search(len(t.s), func(i int) bool { return t.s[i].DTS >= dts-1e-6 })
	if i < len(t.s) && t.s[i].DTS <= dts+1e-6 {
		return t.s[i], true
	}
	return Sample{}, false
}

// KeyAtOrBefore returns the nearest sync sample with decode time <= dts
// that is connected to dts without a gap.
func (t *TrackStore) KeyAtOrBefore(dts float64) (Sample, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	i := sort.Search(len(t.s), func(i int) bool { return t.s[i].DTS > dts+1e-9 }) - 1
	for ; i >= 0; i-- {
		if t.s[i].Key {
			return t.s[i], true
		}
		if i > 0 && t.s[i].DTS-(t.s[i-1].DTS+t.s[i-1].Dur) > t.fudgeLocked() {
			return Sample{}, false
		}
	}
	return Sample{}, false
}

// FirstAfter returns the first sample whose PTS is >= tm (audio seeking and
// buffering checks).
func (t *TrackStore) FirstAfter(tm float64) (Sample, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	i := sort.Search(len(t.s), func(i int) bool { return t.s[i].PTS >= tm-1e-9 })
	if i < len(t.s) {
		return t.s[i], true
	}
	return Sample{}, false
}

// Snapshot returns a copy of all samples (tests, diagnostics).
func (t *TrackStore) Snapshot() []Sample {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]Sample(nil), t.s...)
}

// Duration is the end of the last sample by decode order (progressive files).
func (t *TrackStore) Duration() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	d := 0.0
	for i := len(t.s) - 1; i >= 0 && i > len(t.s)-64; i-- {
		if e := t.s[i].End(); e > d {
			d = e
		}
	}
	return d
}

// NewSample builds a sample from an in-memory payload.
func NewSample(dts, pts, dur float64, key bool, data []byte) Sample {
	return Sample{DTS: dts, PTS: pts, Dur: dur, Key: key, Size: len(data), data: data}
}

// NewLazySample builds a sample whose payload is fetched on demand.
func NewLazySample(dts, pts, dur float64, key bool, size int, load func() ([]byte, error)) Sample {
	return Sample{DTS: dts, PTS: pts, Dur: dur, Key: key, Size: size, load: load}
}

// Prev returns the last sample before dts in decode order.
func (t *TrackStore) Prev(dts float64) (Sample, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	i := sort.Search(len(t.s), func(i int) bool { return t.s[i].DTS >= dts-1e-9 }) - 1
	if i < 0 {
		return Sample{}, false
	}
	return t.s[i], true
}

// Index is the decode-order position of the sample at dts, or -1.
func (t *TrackStore) Index(dts float64) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	i := sort.Search(len(t.s), func(i int) bool { return t.s[i].DTS >= dts-1e-6 })
	if i < len(t.s) && t.s[i].DTS <= dts+1e-6 {
		return i
	}
	return -1
}

// CountPTS counts the samples presented in (after, upTo].
func (t *TrackStore) CountPTS(after, upTo float64) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	lo := sort.Search(len(t.s), func(i int) bool { return t.s[i].DTS > after-t.maxCTO-1e-9 })
	n := 0
	for i := lo; i < len(t.s) && t.s[i].DTS <= upTo-t.minCTO+1e-9; i++ {
		if p := t.s[i].PTS; p > after+1e-9 && p <= upTo+1e-9 {
			n++
		}
	}
	return n
}
