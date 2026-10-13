// Package constraints solves MediaStreamConstraints against what the capture providers can really do.
//
// It follows the SelectSettings algorithm of the Media Capture spec: hard constraints (min, max, exact) remove
// candidates, "advanced" sets are applied in order when they leave something, and among the survivors the
// candidate with the smallest fitness distance to the "ideal" values wins. The result is always a mode the device
// really has, so MediaStreamTrack.getSettings() reports what the camera delivers rather than what was asked.
//
// The package has no dependency on the engine or on any device API; the engine feeds it JSON-decoded constraints.
package constraints

import (
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Num is a ConstrainDouble: a plain number is an ideal.
type Num struct{ Min, Max, Exact, Ideal *float64 }

// Str is a ConstrainDOMString.
type Str struct{ Exact, Ideal []string }

// Bool is a ConstrainBoolean.
type Bool struct{ Exact, Ideal *bool }

func (n Num) has() bool  { return n.Min != nil || n.Max != nil || n.Exact != nil || n.Ideal != nil }
func (s Str) has() bool  { return len(s.Exact) > 0 || len(s.Ideal) > 0 }
func (b Bool) has() bool { return b.Exact != nil || b.Ideal != nil }

// Set is one constraint dictionary (the basic set or one "advanced" entry).
type Set struct {
	Width, Height, FrameRate, AspectRatio Num
	FacingMode, ResizeMode                Str
	DeviceID, GroupID                     Str
	DisplaySurface                        Str

	SampleRate, SampleSize, ChannelCount, Latency Num
	EchoCancellation, AutoGainControl             Bool
	NoiseSuppression                              Bool
}

// Constraints is a parsed MediaTrackConstraints.
type Constraints struct {
	Set
	Advanced []Set
}

// TypeError is returned for malformed constraints; the engine turns it into a JS TypeError.
type TypeError struct{ Msg string }

func (e *TypeError) Error() string { return e.Msg }

// Overconstrained is returned when no device/mode satisfies the hard constraints. Constraint names the property
// that removed the last candidates.
type Overconstrained struct{ Constraint string }

func (e *Overconstrained) Error() string { return "Overconstrained: " + e.Constraint }

// Parse reads a JSON-decoded MediaTrackConstraints: true (or nil) means "no constraints", an object is read key by
// key, unknown keys are ignored like browsers do.
func Parse(v any) (Constraints, error) {
	var c Constraints
	switch m := v.(type) {
	case nil, bool:
		return c, nil
	case map[string]any:
		if err := parseSet(&c.Set, m); err != nil {
			return c, err
		}
		if adv, ok := m["advanced"]; ok && adv != nil {
			list, ok := adv.([]any)
			if !ok {
				return c, &TypeError{"The provided value for 'advanced' is not a sequence."}
			}
			for _, e := range list {
				em, ok := e.(map[string]any)
				if !ok {
					return c, &TypeError{"An 'advanced' entry is not a dictionary."}
				}
				var s Set
				if err := parseSet(&s, em); err != nil {
					return c, err
				}
				c.Advanced = append(c.Advanced, s)
			}
		}
		return c, nil
	}
	return c, &TypeError{"The constraints are not a boolean or a dictionary."}
}

func parseSet(s *Set, m map[string]any) error {
	nums := map[string]*Num{"width": &s.Width, "height": &s.Height, "frameRate": &s.FrameRate, "aspectRatio": &s.AspectRatio,
		"sampleRate": &s.SampleRate, "sampleSize": &s.SampleSize, "channelCount": &s.ChannelCount, "latency": &s.Latency}
	strs := map[string]*Str{"facingMode": &s.FacingMode, "resizeMode": &s.ResizeMode, "deviceId": &s.DeviceID,
		"groupId": &s.GroupID, "displaySurface": &s.DisplaySurface}
	bools := map[string]*Bool{"echoCancellation": &s.EchoCancellation, "autoGainControl": &s.AutoGainControl, "noiseSuppression": &s.NoiseSuppression}
	for k, v := range m {
		if v == nil {
			continue
		}
		switch {
		case nums[k] != nil:
			n, err := parseNum(k, v)
			if err != nil {
				return err
			}
			*nums[k] = n
		case strs[k] != nil:
			st, err := parseStr(k, v)
			if err != nil {
				return err
			}
			*strs[k] = st
		case bools[k] != nil:
			b, err := parseBool(k, v)
			if err != nil {
				return err
			}
			*bools[k] = b
		}
	}
	return nil
}

func toNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) {
			return 0, false
		}
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func parseNum(k string, v any) (Num, error) {
	var n Num
	bad := &TypeError{fmt.Sprintf("The provided value for '%s' is not a number or a dictionary.", k)}
	if f, ok := toNum(v); ok {
		n.Ideal = &f
		return n, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return n, bad
	}
	for key, dst := range map[string]**float64{"min": &n.Min, "max": &n.Max, "exact": &n.Exact, "ideal": &n.Ideal} {
		if e, ok := m[key]; ok && e != nil {
			f, ok := toNum(e)
			if !ok {
				return n, &TypeError{fmt.Sprintf("The provided value for '%s.%s' is not a number.", k, key)}
			}
			*dst = &f
		}
	}
	return n, nil
}

func strList(v any) ([]string, bool) {
	switch x := v.(type) {
	case string:
		return []string{x}, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case []string:
		return x, true
	}
	return nil, false
}

func parseStr(k string, v any) (Str, error) {
	var s Str
	if l, ok := strList(v); ok {
		s.Ideal = l
		return s, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return s, &TypeError{fmt.Sprintf("The provided value for '%s' is not a string, a sequence or a dictionary.", k)}
	}
	if e, ok := m["exact"]; ok && e != nil {
		if s.Exact, ok = strList(e); !ok {
			return s, &TypeError{fmt.Sprintf("The provided value for '%s.exact' is not a string or sequence.", k)}
		}
	}
	if e, ok := m["ideal"]; ok && e != nil {
		if s.Ideal, ok = strList(e); !ok {
			return s, &TypeError{fmt.Sprintf("The provided value for '%s.ideal' is not a string or sequence.", k)}
		}
	}
	return s, nil
}

func parseBool(k string, v any) (Bool, error) {
	var b Bool
	if x, ok := v.(bool); ok {
		b.Ideal = &x
		return b, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return b, &TypeError{fmt.Sprintf("The provided value for '%s' is not a boolean or a dictionary.", k)}
	}
	if e, ok := m["exact"].(bool); ok {
		b.Exact = &e
	}
	if e, ok := m["ideal"].(bool); ok {
		b.Ideal = &e
	}
	return b, nil
}

// ---- candidates -----------------------------------------------------------------------------------------------

type ivl [2]float64

type cand struct {
	dev int
	ord int
	num map[string]ivl
	str map[string]string
	b   map[string]bool
}

func (c *cand) clone() *cand {
	d := *c
	d.num = make(map[string]ivl, len(c.num))
	for k, v := range c.num {
		d.num[k] = v
	}
	return &d
}

// applyNum narrows c.num[name] by n; false when nothing is left.
func (c *cand) applyNum(name string, n Num) bool {
	r, ok := c.num[name]
	if !ok {
		return true // property not offered by this kind of source: not a constraint on it
	}
	lo, hi := r[0], r[1]
	const eps = 1e-9
	if n.Exact != nil {
		if *n.Exact < lo-eps || *n.Exact > hi+eps {
			return false
		}
		lo, hi = *n.Exact, *n.Exact
	}
	if n.Min != nil {
		if *n.Min > hi+eps {
			return false
		}
		lo = math.Max(lo, *n.Min)
	}
	if n.Max != nil {
		if *n.Max < lo-eps {
			return false
		}
		hi = math.Min(hi, *n.Max)
	}
	if lo > hi+eps {
		return false
	}
	c.num[name] = ivl{lo, hi}
	return true
}

func inList(l []string, v string) bool {
	for _, e := range l {
		if e == v {
			return true
		}
	}
	return false
}

func (c *cand) applyStr(name string, s Str) bool {
	v, ok := c.str[name]
	if !ok || len(s.Exact) == 0 {
		return true
	}
	return inList(s.Exact, v)
}

func (c *cand) applyBool(name string, b Bool) bool {
	v, ok := c.b[name]
	if !ok || b.Exact == nil {
		return true
	}
	return *b.Exact == v
}

// apply runs the named constraints of s against this candidate and returns the first property it fails.
func (c *cand) apply(s *Set, order []string) (string, bool) {
	for _, name := range order {
		var ok = true
		switch name {
		case "deviceId":
			ok = c.applyStr(name, s.DeviceID)
		case "groupId":
			ok = c.applyStr(name, s.GroupID)
		case "facingMode":
			ok = c.applyStr(name, s.FacingMode)
		case "resizeMode":
			ok = c.applyStr(name, s.ResizeMode)
		case "width":
			ok = c.applyNum(name, s.Width)
		case "height":
			ok = c.applyNum(name, s.Height)
		case "aspectRatio":
			ok = c.applyNum(name, s.AspectRatio)
		case "frameRate":
			ok = c.applyNum(name, s.FrameRate)
		case "sampleRate":
			ok = c.applyNum(name, s.SampleRate)
		case "sampleSize":
			ok = c.applyNum(name, s.SampleSize)
		case "channelCount":
			ok = c.applyNum(name, s.ChannelCount)
		case "latency":
			ok = c.applyNum(name, s.Latency)
		case "echoCancellation":
			ok = c.applyBool(name, s.EchoCancellation)
		case "autoGainControl":
			ok = c.applyBool(name, s.AutoGainControl)
		case "noiseSuppression":
			ok = c.applyBool(name, s.NoiseSuppression)
		}
		if !ok {
			return name, false
		}
	}
	return "", true
}

func ideal(n Num) (float64, bool) {
	switch {
	case n.Exact != nil:
		return *n.Exact, true
	case n.Ideal != nil:
		return *n.Ideal, true
	}
	return 0, false
}

func dist(a, b float64) float64 {
	d := math.Max(math.Abs(a), math.Abs(b))
	if d == 0 {
		return 0
	}
	return math.Abs(a-b) / d
}

func clampTo(v float64, r ivl) float64 { return math.Min(math.Max(v, r[0]), r[1]) }

// fitness is the sum of the distances of the candidate to every ideal of the basic set. Properties without an
// ideal contribute a tiny pull toward the defaults so {video:true} lands on 640x480@30 like browsers do.
func (c *cand) fitness(s *Set, defaults map[string]float64) float64 {
	f := 0.0
	nums := map[string]Num{"width": s.Width, "height": s.Height, "frameRate": s.FrameRate, "aspectRatio": s.AspectRatio,
		"sampleRate": s.SampleRate, "sampleSize": s.SampleSize, "channelCount": s.ChannelCount, "latency": s.Latency}
	for name, r := range c.num {
		n := nums[name]
		if want, ok := ideal(n); ok {
			f += dist(clampTo(want, r), want)
		} else if d, ok := defaults[name]; ok {
			f += 1e-3 * dist(clampTo(d, r), d)
		}
	}
	for name, v := range c.str {
		var st Str
		switch name {
		case "deviceId":
			st = s.DeviceID
		case "groupId":
			st = s.GroupID
		case "facingMode":
			st = s.FacingMode
		case "resizeMode":
			st = s.ResizeMode
		}
		if len(st.Ideal) > 0 && !inList(st.Ideal, v) {
			f++
		}
		if len(st.Exact) > 0 && !inList(st.Exact, v) {
			f++
		}
	}
	for name, v := range c.b {
		var b Bool
		switch name {
		case "echoCancellation":
			b = s.EchoCancellation
		case "autoGainControl":
			b = s.AutoGainControl
		case "noiseSuppression":
			b = s.NoiseSuppression
		}
		if b.Ideal != nil && *b.Ideal != v {
			f++
		}
	}
	return f
}

// selectBest implements SelectSettings over a candidate list.
func selectBest(cands []*cand, c *Constraints, order []string, defaults map[string]float64) (*cand, *Overconstrained) {
	// hard constraints of the basic set; the property that empties the list is the one reported
	cur := cands
	for _, name := range order {
		var next []*cand
		for _, k := range cur {
			kk := k.clone()
			if _, ok := kk.apply(&c.Set, []string{name}); ok {
				next = append(next, kk)
			}
		}
		if len(next) == 0 {
			return nil, &Overconstrained{Constraint: name}
		}
		cur = next
	}
	for i := range c.Advanced {
		var next []*cand
		for _, k := range cur {
			kk := k.clone()
			if _, ok := kk.apply(&c.Advanced[i], order); ok {
				next = append(next, kk)
			}
		}
		if len(next) > 0 {
			cur = next
		}
	}
	sort.SliceStable(cur, func(i, j int) bool {
		fi, fj := cur[i].fitness(&c.Set, defaults), cur[j].fitness(&c.Set, defaults)
		if fi != fj {
			return fi < fj
		}
		return cur[i].ord < cur[j].ord
	})
	return cur[0], nil
}

// ---- video ----------------------------------------------------------------------------------------------------

// VideoDevice is one camera as the solver sees it. ID and GroupID are the identifiers the page sees (already hashed
// per origin); the caller maps the result back with Device.
type VideoDevice struct {
	ID, GroupID string
	Facing      string // "user", "environment" or "" (a laptop webcam: treated as "user")
	Modes       []Mode
}

// Mode is a resolution with the frame-rate range available at it.
type Mode struct {
	W, H           int
	FPSMin, FPSMax float64
}

// VideoResult is the winning combination.
type VideoResult struct {
	Device int
	Mode   int
	W, H   int
	FPS    float64
	Facing string
}

var videoOrder = []string{"deviceId", "groupId", "facingMode", "width", "height", "aspectRatio", "frameRate", "resizeMode"}

// SolveVideo picks the device and mode for a video request.
func SolveVideo(c Constraints, devs []VideoDevice) (VideoResult, error) {
	var cands []*cand
	ord := 0
	for di, d := range devs {
		facing := d.Facing
		if facing == "" {
			facing = "user"
		}
		for mi, m := range d.Modes {
			cands = append(cands, &cand{dev: di, ord: ord,
				num: map[string]ivl{"width": {float64(m.W), float64(m.W)}, "height": {float64(m.H), float64(m.H)},
					"aspectRatio": {float64(m.W) / float64(m.H), float64(m.W) / float64(m.H)}, "frameRate": {m.FPSMin, m.FPSMax}},
				str: map[string]string{"deviceId": d.ID, "groupId": d.GroupID, "facingMode": facing, "resizeMode": "none"}})
			_ = mi
			ord++
		}
	}
	if len(cands) == 0 {
		return VideoResult{}, &Overconstrained{Constraint: "deviceId"}
	}
	// aspectRatio compares with a tolerance: 1280/720 must satisfy exact 1.7777777777777777 and 16/9
	for _, k := range cands {
		r := k.num["aspectRatio"]
		k.num["aspectRatio"] = ivl{r[0] - 1e-3*r[0], r[1] + 1e-3*r[1]}
	}
	best, err := selectBest(cands, &c, videoOrder, map[string]float64{"width": 640, "height": 480, "frameRate": 30})
	if err != nil {
		return VideoResult{}, err
	}
	w, h := int(best.num["width"][0]), int(best.num["height"][0])
	fr := best.num["frameRate"]
	want := 30.0
	if v, ok := ideal(c.FrameRate); ok {
		want = v
	}
	fps := clampTo(want, fr)
	d := devs[best.dev]
	mi := 0
	for i, m := range d.Modes {
		if m.W == w && m.H == h {
			mi = i
			break
		}
	}
	return VideoResult{Device: best.dev, Mode: mi, W: w, H: h, FPS: math.Round(fps*1000) / 1000, Facing: best.str["facingMode"]}, nil
}

// ---- audio ----------------------------------------------------------------------------------------------------

// AudioDevice is one microphone as the solver sees it.
type AudioDevice struct {
	ID, GroupID               string
	Rates                     []int
	MinChannels, MaxChannels  int
	DefaultRate, DefaultChans int
}

// AudioResult is the winning combination.
type AudioResult struct {
	Device   int
	Rate     int
	Channels int
}

var audioOrder = []string{"deviceId", "groupId", "sampleRate", "sampleSize", "channelCount", "latency", "echoCancellation", "autoGainControl", "noiseSuppression"}

// SolveAudio picks the microphone. The browser does no echo cancellation, noise suppression or gain control, so
// those properties are always false and an exact:true request for one is overconstrained.
func SolveAudio(c Constraints, devs []AudioDevice) (AudioResult, error) {
	var cands []*cand
	ord := 0
	for di, d := range devs {
		for _, r := range d.Rates {
			cands = append(cands, &cand{dev: di, ord: ord,
				num: map[string]ivl{"sampleRate": {float64(r), float64(r)}, "sampleSize": {16, 16},
					"channelCount": {float64(d.MinChannels), float64(d.MaxChannels)}, "latency": {0.01, 0.01}},
				str: map[string]string{"deviceId": d.ID, "groupId": d.GroupID},
				b:   map[string]bool{"echoCancellation": false, "autoGainControl": false, "noiseSuppression": false}})
			ord++
		}
	}
	if len(cands) == 0 {
		return AudioResult{}, &Overconstrained{Constraint: "deviceId"}
	}
	def := map[string]float64{"sampleRate": 48000}
	best, err := selectBest(cands, &c, audioOrder, def)
	if err != nil {
		return AudioResult{}, err
	}
	d := devs[best.dev]
	chans := float64(max(d.DefaultChans, 1))
	if v, ok := ideal(c.ChannelCount); ok {
		chans = v
	}
	chans = clampTo(chans, best.num["channelCount"])
	return AudioResult{Device: best.dev, Rate: int(best.num["sampleRate"][0]), Channels: int(chans)}, nil
}

// ---- display --------------------------------------------------------------------------------------------------

// DisplayResult is the size and rate a display source should deliver.
type DisplayResult struct {
	W, H int
	FPS  float64
}

// SolveDisplay applies width/height/frameRate to a surface of native size w x h. Dimensions are upper bounds:
// the surface is scaled down (never up) to fit, keeping its aspect ratio. frameRate defaults to 30 and is
// capped at 60.
func SolveDisplay(c Constraints, w, h int) (DisplayResult, error) {
	if w <= 0 || h <= 0 {
		return DisplayResult{}, &Overconstrained{Constraint: "width"}
	}
	tw, th := float64(w), float64(h)
	bound := func(n Num, native float64) float64 {
		b := native
		if v, ok := ideal(n); ok {
			b = math.Min(b, v)
		}
		if n.Max != nil {
			b = math.Min(b, *n.Max)
		}
		return math.Max(b, 1)
	}
	bw, bh := bound(c.Width, tw), bound(c.Height, th)
	if c.Width.Min != nil && *c.Width.Min > tw*4 { // a minimum that no scaling of ours reaches
		return DisplayResult{}, &Overconstrained{Constraint: "width"}
	}
	s := math.Min(bw/tw, bh/th)
	if s > 1 {
		s = 1
	}
	ow, oh := int(math.Round(tw*s)), int(math.Round(th*s))
	ow, oh = max(ow&^1, 2), max(oh&^1, 2) // even sizes keep I420 chroma exact
	fps := 30.0
	if v, ok := ideal(c.FrameRate); ok {
		fps = v
	}
	if c.FrameRate.Max != nil {
		fps = math.Min(fps, *c.FrameRate.Max)
	}
	if c.FrameRate.Min != nil && *c.FrameRate.Min > 60 {
		return DisplayResult{}, &Overconstrained{Constraint: "frameRate"}
	}
	fps = math.Min(math.Max(fps, 1), 60)
	return DisplayResult{W: ow, H: oh, FPS: fps}, nil
}
