package webaudio

import "math"

// EventType is an AudioParam automation event kind.
type EventType int

const (
	EvSetValue EventType = iota
	EvLinear
	EvExponential
	EvSetTarget
	EvCurve
)

// Event is one automation event.
type Event struct {
	Type     EventType
	Time     float64 // seconds
	Value    float64
	TimeCst  float64   // SetTarget
	Duration float64   // Curve
	Curve    []float32 // Curve
}

// Param is an AudioParam.
type Param struct {
	Name     string
	Default  float64
	Min, Max float64
	Value    float64 // intrinsic value (used before the first event)
	ARate    bool    // a-rate (per frame) or k-rate
	Events   []Event
	inputs   []*conn // audio-rate modulation sources
	buf      [quantum]float32
	node     *Node
	cur      float64 // last computed value (end of the previous quantum)
}

// AddEvent inserts an event keeping the timeline sorted (stable by time).
func (p *Param) AddEvent(e Event) {
	i := len(p.Events)
	for i > 0 && p.Events[i-1].Time > e.Time {
		i--
	}
	// An event of the same type and time replaces the previous one.
	if i > 0 && p.Events[i-1].Time == e.Time && p.Events[i-1].Type == e.Type && e.Type != EvSetValue {
		p.Events[i-1] = e
		return
	}
	p.Events = append(p.Events, Event{})
	copy(p.Events[i+1:], p.Events[i:])
	p.Events[i] = e
}

// CancelScheduled removes events at or after t.
func (p *Param) CancelScheduled(t float64) {
	out := p.Events[:0]
	for _, e := range p.Events {
		if e.Time < t {
			out = append(out, e)
		}
	}
	p.Events = out
}

// CancelAndHold freezes the value the timeline has at t.
func (p *Param) CancelAndHold(t float64) {
	v := p.ValueAt(t)
	// drop everything after t; a ramp/curve in progress is replaced by a hold
	out := p.Events[:0]
	for _, e := range p.Events {
		if e.Time <= t && !(e.Type == EvCurve && e.Time+e.Duration > t) {
			out = append(out, e)
		}
	}
	p.Events = out
	p.AddEvent(Event{Type: EvSetValue, Time: t, Value: v})
}

// ValueAt evaluates the automation timeline at time t.
func (p *Param) ValueAt(t float64) float64 {
	ev := p.Events
	if len(ev) == 0 {
		return p.Value
	}
	// next = first event with Time > t
	n := 0
	for n < len(ev) && ev[n].Time <= t {
		n++
	}
	// An event exactly at t takes effect at t, except ramps which are
	// evaluated through their segment end value.
	if n < len(ev) && (ev[n].Type == EvLinear || ev[n].Type == EvExponential) {
		// ramp from the previous event
		t0, v0 := p.startOfRamp(n)
		t1, v1 := ev[n].Time, ev[n].Value
		if t1 <= t0 {
			return v1
		}
		if ev[n].Type == EvLinear {
			return v0 + (v1-v0)*(t-t0)/(t1-t0)
		}
		if v0*v1 <= 0 {
			return v0
		}
		return v0 * math.Pow(v1/v0, (t-t0)/(t1-t0))
	}
	if n == 0 {
		return p.Value
	}
	e := ev[n-1]
	switch e.Type {
	case EvSetValue, EvLinear, EvExponential:
		return e.Value
	case EvSetTarget:
		v0 := p.valueJustBefore(n - 1)
		return e.Value + (v0-e.Value)*math.Exp(-(t-e.Time)/e.TimeCst)
	case EvCurve:
		if t >= e.Time+e.Duration {
			return float64(e.Curve[len(e.Curve)-1])
		}
		nn := len(e.Curve)
		k := float64(nn-1) / e.Duration * (t - e.Time)
		i := int(math.Floor(k))
		if i >= nn-1 {
			return float64(e.Curve[nn-1])
		}
		f := k - float64(i)
		return float64(e.Curve[i])*(1-f) + float64(e.Curve[i+1])*f
	}
	return p.Value
}

// startOfRamp returns the (time, value) a ramp event n starts from.
func (p *Param) startOfRamp(n int) (float64, float64) {
	if n == 0 {
		return 0, p.Value
	}
	e := p.Events[n-1]
	switch e.Type {
	case EvCurve:
		return e.Time + e.Duration, float64(e.Curve[len(e.Curve)-1])
	case EvSetTarget:
		return e.Time, p.valueJustBefore(n - 1)
	}
	return e.Time, e.Value
}

// valueJustBefore is the timeline value immediately before event i starts.
func (p *Param) valueJustBefore(i int) float64 {
	if i == 0 {
		return p.Value
	}
	e := p.Events[i-1]
	t := p.Events[i].Time
	switch e.Type {
	case EvSetValue, EvLinear, EvExponential:
		// a ramp that ends at t gives its end value
		return e.Value
	case EvSetTarget:
		v0 := p.valueJustBefore(i - 1)
		return e.Value + (v0-e.Value)*math.Exp(-(t-e.Time)/e.TimeCst)
	case EvCurve:
		if t >= e.Time+e.Duration {
			return float64(e.Curve[len(e.Curve)-1])
		}
		nn := len(e.Curve)
		k := float64(nn-1) / e.Duration * (t - e.Time)
		j := int(math.Floor(k))
		if j >= nn-1 {
			return float64(e.Curve[nn-1])
		}
		f := k - float64(j)
		return float64(e.Curve[j])*(1-f) + float64(e.Curve[j+1])*f
	}
	return p.Value
}

func (p *Param) automated() bool { return len(p.Events) > 0 }

// compute fills p.buf for frames [start, start+n) and returns it. For k-rate
// params the value at the first frame is held for the quantum.
func (p *Param) compute(c *Context, start int64, n int) []float32 {
	sr := c.SampleRate
	hasInputs := len(p.inputs) > 0
	if !p.ARate && !hasInputs {
		v := float32(p.clamp(p.ValueAt(float64(start) / sr)))
		for i := 0; i < n; i++ {
			p.buf[i] = v
		}
		p.cur = float64(v)
		return p.buf[:n]
	}
	if p.automated() {
		for i := 0; i < n; i++ {
			p.buf[i] = float32(p.ValueAt(float64(start+int64(i)) / sr))
		}
	} else {
		v := float32(p.Value)
		for i := 0; i < n; i++ {
			p.buf[i] = v
		}
	}
	if hasInputs {
		sum := make([]float32, n)
		for _, cn := range p.inputs {
			src := cn.from.outputMono(cn.out)
			for i := 0; i < n; i++ {
				sum[i] += src[i]
			}
		}
		for i := 0; i < n; i++ {
			p.buf[i] += sum[i]
		}
	}
	for i := 0; i < n; i++ {
		p.buf[i] = float32(p.clamp(float64(p.buf[i])))
	}
	p.cur = float64(p.buf[n-1])
	return p.buf[:n]
}

func (p *Param) clamp(v float64) float64 {
	if math.IsNaN(v) {
		return p.Default
	}
	if v < p.Min {
		return p.Min
	}
	if v > p.Max {
		return p.Max
	}
	return v
}
