package audioout

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Group is a set of streams that share a volume, a mute and an audible
// indicator: the browser keeps one per tab. Muting a group silences its
// streams but not their clocks, so a muted video keeps playing in sync.
type Group struct {
	o    *Output
	name string

	vol      atomic.Uint64 // float64 bits
	muted    atomic.Bool
	lastLoud atomic.Int64 // backend time (unix nanos) of the last non-silent chunk

	mu      sync.Mutex
	audible bool
	cb      func(audible bool)
}

// Name is the label given at creation.
func (g *Group) Name() string { return g.name }

func (g *Group) gain() float32 { return float32(math.Float64frombits(g.vol.Load())) }

// SetVolume sets the group gain (0..1).
func (g *Group) SetVolume(v float64) { g.vol.Store(math.Float64bits(clamp01(v))) }

// SetMuted mutes or unmutes every stream of the group.
func (g *Group) SetMuted(m bool) { g.muted.Store(m) }

// Muted reports the mute state.
func (g *Group) Muted() bool { return g.muted.Load() }

// OnAudible registers the function called (from the output's poll goroutine)
// when the group starts or stops making sound. A group is audible while one
// of its streams played a non-silent chunk within the last 1.5 s; muting
// does not change that (like a browser tab, it stays marked while muted).
func (g *Group) OnAudible(f func(audible bool)) {
	g.mu.Lock()
	g.cb = f
	g.mu.Unlock()
}

// Audible reports the current indicator.
func (g *Group) Audible() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.audible
}

// Close detaches the group from the output. Its streams keep playing.
func (g *Group) Close() {
	g.o.dropGroup(g)
	g.mu.Lock()
	was := g.audible
	g.audible = false
	cb := g.cb
	g.mu.Unlock()
	if was && cb != nil {
		cb(false)
	}
}

func (g *Group) poll(now time.Time) {
	last := g.lastLoud.Load()
	loud := last != 0 && now.Sub(time.Unix(0, last)) < audibleHold
	g.mu.Lock()
	changed := loud != g.audible
	g.audible = loud
	cb := g.cb
	g.mu.Unlock()
	if changed && cb != nil {
		cb(loud)
	}
}
