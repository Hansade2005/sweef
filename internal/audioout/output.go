package audioout

import (
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Errors returned by Stream.Write.
var (
	ErrClosed   = errors.New("audioout: stream closed")
	ErrFlushed  = errors.New("audioout: stream flushed while writing")
	ErrChannels = errors.New("audioout: channel count does not match the stream")
)

// Suspender is implemented by backends that can stop their device while
// nothing plays (so the desktop does not show the browser as an audio client
// forever and the device is not polled for silence).
type Suspender interface {
	Suspend()
	Resume()
}

// loudThreshold is the linear peak below which a stream counts as silent for
// the "tab is audible" indicator (about -60 dBFS).
const loudThreshold = 1e-3

// audibleHold keeps a group audible this long after its last loud sample, so
// the tab icon does not flicker between sentences.
const audibleHold = 1500 * time.Millisecond

// idleSuspend is how long the mixer must be idle before a Suspender backend
// is suspended.
const idleSuspend = 3 * time.Second

// Output is a mixer in front of one Backend. The zero value is not usable;
// use NewOutput or Default.
type Output struct {
	be Backend

	mu      sync.Mutex
	streams atomic.Pointer[[]*Stream]
	groups  []*Group
	closed  bool
	master  atomic.Uint64 // float64 bits
	mute    atomic.Bool

	lastActive atomic.Int64 // backend time (unix nanos) of the last pull that mixed something
	suspended  atomic.Bool
	pulls      atomic.Int64
	stop       chan struct{}
	tickOnce   sync.Once
}

// NewOutput starts backend and returns the mixer driving it.
func NewOutput(b Backend) (*Output, error) {
	o := &Output{be: b, stop: make(chan struct{})}
	o.master.Store(math.Float64bits(1))
	empty := []*Stream{}
	o.streams.Store(&empty)
	o.lastActive.Store(b.Now().UnixNano())
	if err := b.Start(o.pull); err != nil {
		return nil, err
	}
	if _, ok := b.(Suspender); ok {
		go o.idleLoop()
	}
	return o, nil
}

// Backend returns the device behind the mixer.
func (o *Output) Backend() Backend { return o.be }

// Real reports whether sound reaches an audio device.
func (o *Output) Real() bool { return o.be.Real() }

// Latency is the backend's output latency estimate.
func (o *Output) Latency() time.Duration { return o.be.Latency() }

// SetMasterVolume sets the volume applied to the whole mix (0..1).
func (o *Output) SetMasterVolume(v float64) {
	o.master.Store(math.Float64bits(clamp01(v)))
}

// SetMasterMuted mutes the whole output.
func (o *Output) SetMasterMuted(m bool) { o.mute.Store(m) }

// Close stops the backend and closes every stream.
func (o *Output) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	ss := *o.streams.Load()
	close(o.stop)
	o.mu.Unlock()
	for _, s := range ss {
		s.Close()
	}
	return o.be.Close()
}

// Open creates a stream that accepts interleaved float32 PCM with the given
// sample rate and channel count. The stream starts paused: write a little
// audio, then call Play. group may be nil.
func (o *Output) Open(rate, channels int, group *Group) *Stream {
	if rate <= 0 {
		rate = DeviceRate
	}
	if channels <= 0 {
		channels = 2
	}
	const capFrames = DeviceRate * 2
	s := &Stream{
		o: o, g: group, inCh: channels, inRate: float64(rate), speed: 1,
		ring: make([]float32, capFrames*DeviceChannels), capFrames: capFrames,
		vol: 1, curGain: 1,
		conv: newConverter(float64(rate), channels),
	}
	s.cond = sync.NewCond(&s.mu)
	o.mu.Lock()
	old := *o.streams.Load()
	n := make([]*Stream, len(old), len(old)+1)
	copy(n, old)
	n = append(n, s)
	o.streams.Store(&n)
	o.mu.Unlock()
	return s
}

func (o *Output) remove(s *Stream) {
	o.mu.Lock()
	old := *o.streams.Load()
	n := make([]*Stream, 0, len(old))
	for _, x := range old {
		if x != s {
			n = append(n, x)
		}
	}
	o.streams.Store(&n)
	o.mu.Unlock()
}

// NewGroup creates a mix group (one per tab): a volume, a mute and an
// "audible" indicator over all its streams.
func (o *Output) NewGroup(name string) *Group {
	g := &Group{o: o, name: name}
	g.vol.Store(math.Float64bits(1))
	o.mu.Lock()
	o.groups = append(o.groups, g)
	o.mu.Unlock()
	o.tickOnce.Do(func() { go o.pollLoop() })
	return g
}

func (o *Output) dropGroup(g *Group) {
	o.mu.Lock()
	for i, x := range o.groups {
		if x == g {
			o.groups = append(o.groups[:i:i], o.groups[i+1:]...)
			break
		}
	}
	o.mu.Unlock()
}

// Poll updates every group's audible state and fires its callbacks. It runs
// from a timer; tests with a virtual clock call it directly.
func (o *Output) Poll() {
	now := o.be.Now()
	o.mu.Lock()
	gs := append([]*Group(nil), o.groups...)
	o.mu.Unlock()
	for _, g := range gs {
		g.poll(now)
	}
}

func (o *Output) pollLoop() {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			o.Poll()
		case <-o.stop:
			return
		}
	}
}

func (o *Output) idleLoop() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if o.suspended.Load() {
				continue
			}
			if o.be.Now().Sub(time.Unix(0, o.lastActive.Load())) > idleSuspend && !o.anyPlaying() {
				if o.suspended.CompareAndSwap(false, true) {
					o.be.(Suspender).Suspend()
				}
			}
		case <-o.stop:
			return
		}
	}
}

func (o *Output) anyPlaying() bool {
	for _, s := range *o.streams.Load() {
		if s.isPlaying() {
			return true
		}
	}
	return false
}

// wake resumes a suspended backend.
func (o *Output) wake() {
	o.lastActive.Store(o.be.Now().UnixNano())
	if o.suspended.CompareAndSwap(true, false) {
		if sp, ok := o.be.(Suspender); ok {
			sp.Resume()
		}
	}
}

// pull is the backend callback: it mixes every playing stream into dst.
func (o *Output) pull(dst []float32) {
	for i := range dst {
		dst[i] = 0
	}
	now := o.be.Now()
	o.pulls.Add(1)
	frames := len(dst) / DeviceChannels
	mixed := false
	for _, s := range *o.streams.Load() {
		if s.mix(dst, frames, now) {
			mixed = true
		}
	}
	if mixed {
		o.lastActive.Store(now.UnixNano())
	}
	m := math.Float64frombits(o.master.Load())
	if o.mute.Load() {
		m = 0
	}
	if m != 1 || mixed {
		g := float32(m)
		for i, v := range dst {
			v *= g
			if v > 1 {
				v = 1
			} else if v < -1 {
				v = -1
			}
			dst[i] = v
		}
	}
}

func clamp01(v float64) float64 {
	if !(v > 0) {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ---- default output -------------------------------------------------------------------

var (
	defMu  sync.Mutex
	defOut *Output
)

// Default returns the process-wide output, creating it on first use. It uses
// the real device when one can be opened and the null sink otherwise;
// SWEEF_AUDIO=null forces the null sink. The result never is nil.
func Default() *Output {
	defMu.Lock()
	defer defMu.Unlock()
	if defOut != nil {
		return defOut
	}
	if strings.EqualFold(os.Getenv("SWEEF_AUDIO"), "null") {
		defOut = mustOutput(NewNullBackend())
		return defOut
	}
	if b, err := newDeviceBackend(); err == nil {
		if o, err := NewOutput(b); err == nil {
			defOut = o
			return o
		}
	}
	defOut = mustOutput(NewNullBackend())
	return defOut
}

// SetDefault replaces the process-wide output (tests install a TestSink).
// The previous default is closed.
func SetDefault(o *Output) {
	defMu.Lock()
	prev := defOut
	defOut = o
	defMu.Unlock()
	if prev != nil && prev != o {
		_ = prev.Close()
	}
}

func mustOutput(b Backend) *Output {
	o, err := NewOutput(b)
	if err != nil {
		panic("audioout: " + err.Error())
	}
	return o
}
