package webaudio

import (
	"fmt"
	"math"
)

const quantum = 128

// Quantum is the render quantum size in frames.
const Quantum = quantum

type conn struct {
	from  *Node
	out   int
	to    *Node
	in    int
	param string
}

type processor interface {
	process(n *Node, c *Context, start int64, frames int, in [][][]float32)
}

// Node is one AudioNode.
type Node struct {
	ID       int
	Type     string
	ctx      *Context
	Params   map[string]*Param
	nIn      int
	nOut     int
	inputs   [][]*conn
	ChCount  int
	ChMode   string // "max", "clamped-max", "explicit"
	ChInterp string // "speakers", "discrete"
	outBufs  [][][]float32
	proc     processor
	external bool // output supplied by the host (AudioWorklet, ScriptProcessor)
	provided bool
	cyclic   bool
	removed  bool
	inCh     []int // computed input channel count (last quantum)
	// scheduling for sources
	startT, stopT float64
	started       bool
	endedFlag     bool
	Props         map[string]any
}

// Context is a Web Audio graph plus its render clock.
type Context struct {
	SampleRate  float64
	Offline     bool
	Frame       int64
	nodes       []*Node
	byID        map[int]*Node
	nextID      int
	Dest        *Node
	order       []*Node
	dirty       bool
	idx         int
	started     bool
	Output      [][]float32 // offline destination capture
	outPos      int
	Ended       []int
	Buffers     map[int]*Buffer
	Listener    *Listener
	hasExternal bool
	quantumOut  [][]float32
	// stepping state
	stepping bool
	waves    map[int]*PeriodicWave
}

// Buffer is audio data held by the graph (AudioBuffer contents, IRs).
type Buffer struct {
	Rate float64
	Data [][]float32
}

func (b *Buffer) Frames() int {
	if len(b.Data) == 0 {
		return 0
	}
	return len(b.Data[0])
}

// NewContext creates a context. For offline contexts frames is the length.
func NewContext(sampleRate float64, offline bool, channels, frames int) *Context {
	c := &Context{SampleRate: sampleRate, Offline: offline, byID: map[int]*Node{}, nextID: 1, dirty: true, Buffers: map[int]*Buffer{}}
	c.Listener = newListener(c)
	if offline {
		c.Output = make([][]float32, channels)
		for i := range c.Output {
			c.Output[i] = make([]float32, frames)
		}
	}
	d, _ := c.AddNode("destination", map[string]any{"channels": float64(channels)})
	c.Dest = c.byID[d]
	return c
}

// Time is the current context time in seconds.
func (c *Context) Time() float64 { return float64(c.Frame) / c.SampleRate }

// Node returns a node by id.
func (c *Context) Node(id int) *Node { return c.byID[id] }

func (c *Context) newNode(typ string, nIn, nOut int) *Node {
	n := &Node{ID: c.nextID, Type: typ, ctx: c, Params: map[string]*Param{}, nIn: nIn, nOut: nOut, ChCount: 2, ChMode: "max", ChInterp: "speakers", Props: map[string]any{}}
	c.nextID++
	n.inputs = make([][]*conn, nIn)
	n.outBufs = make([][][]float32, nOut)
	for i := range n.outBufs {
		n.outBufs[i] = [][]float32{make([]float32, quantum)}
	}
	n.inCh = make([]int, nIn)
	c.nodes = append(c.nodes, n)
	c.byID[n.ID] = n
	c.dirty = true
	return n
}

func (n *Node) addParam(name string, def, min, max float64, arate bool) *Param {
	p := &Param{Name: name, Default: def, Min: min, Max: max, Value: def, ARate: arate, node: n}
	n.Params[name] = p
	return p
}

// setOut prepares output o with ch channels (zeroed) and returns them.
func (n *Node) setOut(o, ch int) [][]float32 {
	if ch < 1 {
		ch = 1
	}
	b := n.outBufs[o]
	for len(b) < ch {
		b = append(b, make([]float32, quantum))
	}
	b = b[:ch]
	for _, s := range b {
		for i := range s {
			s[i] = 0
		}
	}
	n.outBufs[o] = b
	return b
}

func (n *Node) outputMono(o int) []float32 {
	b := n.outBufs[o]
	if len(b) == 1 {
		return b[0]
	}
	m := make([]float32, quantum)
	mixed := mixTo(b, 1, "speakers")
	copy(m, mixed[0])
	return m
}

// Connect wires output `out` of from to input `in` of to.
func (c *Context) Connect(from, out, to, in int) error {
	f, t := c.byID[from], c.byID[to]
	if f == nil || t == nil {
		return fmt.Errorf("bad node")
	}
	if out >= f.nOut {
		return fmt.Errorf("IndexSizeError: output index")
	}
	if in >= t.nIn {
		return fmt.Errorf("IndexSizeError: input index")
	}
	for _, cn := range t.inputs[in] {
		if cn.from == f && cn.out == out {
			return nil
		}
	}
	t.inputs[in] = append(t.inputs[in], &conn{from: f, out: out, to: t, in: in})
	c.dirty = true
	return nil
}

// ConnectParam wires an output to an AudioParam.
func (c *Context) ConnectParam(from, out, to int, param string) error {
	f, t := c.byID[from], c.byID[to]
	if f == nil || t == nil {
		return fmt.Errorf("bad node")
	}
	p := t.Params[param]
	if p == nil {
		return fmt.Errorf("bad param")
	}
	for _, cn := range p.inputs {
		if cn.from == f && cn.out == out {
			return nil
		}
	}
	p.inputs = append(p.inputs, &conn{from: f, out: out, to: t, param: param})
	c.dirty = true
	return nil
}

// Disconnect removes connections from `from`. to < 0 removes every outgoing
// connection; out/in < 0 are wildcards; a non-empty param targets only that
// AudioParam of node `to`.
func (c *Context) Disconnect(from, out, to, in int, param string) {
	f := c.byID[from]
	if f == nil {
		return
	}
	for _, t := range c.nodes {
		if to >= 0 && t.ID != to {
			continue
		}
		if param == "" {
			for i := range t.inputs {
				if in >= 0 && i != in {
					continue
				}
				keep := t.inputs[i][:0]
				for _, cn := range t.inputs[i] {
					if cn.from == f && (out < 0 || cn.out == out) {
						continue
					}
					keep = append(keep, cn)
				}
				t.inputs[i] = keep
			}
		}
		for name, p := range t.Params {
			if param != "" && name != param {
				continue
			}
			if param == "" && to >= 0 {
				continue
			}
			keep := p.inputs[:0]
			for _, cn := range p.inputs {
				if cn.from == f && (out < 0 || cn.out == out) {
					continue
				}
				keep = append(keep, cn)
			}
			p.inputs = keep
		}
	}
	c.dirty = true
}

// RemoveNode detaches every connection of a node (used on GC/close).
func (c *Context) RemoveNode(id int) {
	n := c.byID[id]
	if n == nil {
		return
	}
	c.Disconnect(id, -1, -1, -1, "")
	for i := range n.inputs {
		n.inputs[i] = nil
	}
	n.removed = true
	c.dirty = true
}

// order computes a topological processing order. Delay nodes in feedback
// cycles are treated as sources (they read before they write).
func (c *Context) computeOrder() {
	state := map[*Node]int{}
	var out []*Node
	var visit func(n *Node)
	visit = func(n *Node) {
		if state[n] == 2 {
			return
		}
		if state[n] == 1 {
			return
		}
		state[n] = 1
		deps := func(cn *conn) {
			if state[cn.from] == 1 {
				// cycle: break at a delay node
				if cn.from.Type == "delay" {
					cn.from.cyclic = true
				} else if n.Type == "delay" {
					n.cyclic = true
				}
				return
			}
			visit(cn.from)
		}
		for _, in := range n.inputs {
			for _, cn := range in {
				deps(cn)
			}
		}
		for _, p := range n.Params {
			for _, cn := range p.inputs {
				deps(cn)
			}
		}
		state[n] = 2
		out = append(out, n)
	}
	// cyclic delays must come first so their output exists before consumers run
	for _, n := range c.nodes {
		if n.Type == "delay" {
			n.cyclic = false
		}
	}
	for _, n := range c.nodes {
		if !n.removed {
			visit(n)
		}
	}
	// move cyclic delays to the front (they produce from stored state)
	var front, rest []*Node
	for _, n := range out {
		if n.cyclic {
			front = append(front, n)
		} else {
			rest = append(rest, n)
		}
	}
	c.order = append(front, rest...)
	c.dirty = false
	c.hasExternal = false
	for _, n := range c.nodes {
		if n.external && !n.removed {
			c.hasExternal = true
		}
	}
}

// HasExternal reports whether host-driven nodes (worklets) exist.
func (c *Context) HasExternal() bool {
	if c.dirty {
		c.computeOrder()
	}
	return c.hasExternal
}

// gatherInputs mixes all connections of every input of n.
func (c *Context) gatherInputs(n *Node) [][][]float32 {
	in := make([][][]float32, n.nIn)
	for i := 0; i < n.nIn; i++ {
		conns := n.inputs[i]
		maxCh := 1
		for _, cn := range conns {
			if k := len(cn.from.outBufs[cn.out]); k > maxCh {
				maxCh = k
			}
		}
		ch := maxCh
		switch n.ChMode {
		case "explicit":
			ch = n.ChCount
		case "clamped-max":
			if ch > n.ChCount {
				ch = n.ChCount
			}
		}
		if n.Type == "channelmerger" {
			ch = 1
		}
		n.inCh[i] = ch
		mix := make([][]float32, ch)
		for k := range mix {
			mix[k] = make([]float32, quantum)
		}
		interp := n.ChInterp
		for _, cn := range conns {
			src := cn.from.outBufs[cn.out]
			if cn.from.removed {
				continue
			}
			m := src
			if len(src) != ch {
				m = mixTo(src, ch, interp)
			}
			for k := 0; k < ch && k < len(m); k++ {
				d := mix[k]
				s := m[k]
				for j := 0; j < quantum; j++ {
					d[j] += s[j]
				}
			}
		}
		in[i] = mix
	}
	return in
}

// mixTo up/down-mixes src to ch channels following the spec tables.
func mixTo(src [][]float32, ch int, interp string) [][]float32 {
	n := len(src)
	out := make([][]float32, ch)
	for i := range out {
		out[i] = make([]float32, quantum)
	}
	if n == ch {
		for i := range out {
			copy(out[i], src[i])
		}
		return out
	}
	if interp == "discrete" || !(n <= 6 && ch <= 6 && n != 3 && n != 5 && ch != 3 && ch != 5) {
		for i := 0; i < ch && i < n; i++ {
			copy(out[i], src[i])
		}
		return out
	}
	add := func(dst int, s int, g float32) {
		for j := 0; j < quantum; j++ {
			out[dst][j] += src[s][j] * g
		}
	}
	h := float32(math.Sqrt(0.5))
	switch {
	case n == 1 && ch == 2:
		add(0, 0, 1)
		add(1, 0, 1)
	case n == 1 && ch == 4:
		add(0, 0, 1)
		add(1, 0, 1)
	case n == 1 && ch == 6:
		add(2, 0, 1)
	case n == 2 && ch == 4:
		add(0, 0, 1)
		add(1, 1, 1)
	case n == 2 && ch == 6:
		add(0, 0, 1)
		add(1, 1, 1)
	case n == 4 && ch == 6:
		add(0, 0, 1)
		add(1, 1, 1)
		add(4, 2, 1)
		add(5, 3, 1)
	case n == 2 && ch == 1:
		add(0, 0, 0.5)
		add(0, 1, 0.5)
	case n == 4 && ch == 1:
		for i := 0; i < 4; i++ {
			add(0, i, 0.25)
		}
	case n == 6 && ch == 1:
		add(0, 0, h)
		add(0, 1, h)
		add(0, 2, 1)
		add(0, 4, 0.5)
		add(0, 5, 0.5)
	case n == 4 && ch == 2:
		add(0, 0, 0.5)
		add(0, 2, 0.5)
		add(1, 1, 0.5)
		add(1, 3, 0.5)
	case n == 6 && ch == 2:
		add(0, 0, 1)
		add(0, 2, h)
		add(0, 4, h)
		add(1, 1, 1)
		add(1, 2, h)
		add(1, 5, h)
	case n == 6 && ch == 4:
		add(0, 0, 1)
		add(0, 2, h)
		add(1, 1, 1)
		add(1, 2, h)
		add(2, 4, 1)
		add(3, 5, 1)
	default:
		for i := 0; i < ch && i < n; i++ {
			copy(out[i], src[i])
		}
	}
	return out
}

// Render advances the graph by `frames` frames (rounded up to quanta);
// offline contexts accumulate destination samples in c.Output.
func (c *Context) Render(frames int) {
	for done := 0; done < frames; done += quantum {
		c.beginQuantum()
		for c.idx < len(c.order) {
			c.stepNode(c.order[c.idx])
			c.idx++
		}
		c.endQuantum()
	}
}

func (c *Context) beginQuantum() {
	if c.dirty {
		c.computeOrder()
	}
	c.idx = 0
}

func (c *Context) stepNode(n *Node) {
	if n.removed {
		return
	}
	in := c.gatherInputs(n)
	if n.proc != nil {
		n.proc.process(n, c, c.Frame, quantum, in)
	}
}

func (c *Context) endQuantum() {
	for _, n := range c.order {
		if n.cyclic && n.Type == "delay" {
			if d, ok := n.proc.(*delayProc); ok {
				d.post(n, c, c.Frame, c.gatherInputs(n))
			}
		}
	}
	if c.Offline && c.Dest != nil {
		d := c.Dest.outBufs[0]
		for ch := range c.Output {
			for i := 0; i < quantum && c.outPos+i < len(c.Output[ch]); i++ {
				if ch < len(d) {
					c.Output[ch][c.outPos+i] = d[ch][i]
				}
			}
		}
		c.outPos += quantum
	}
	c.Frame += quantum
}

// Done reports whether an offline context has produced all of its frames.
func (c *Context) Done() bool {
	return c.Offline && len(c.Output) > 0 && c.outPos >= len(c.Output[0])
}

// Step advances externally-driven rendering. It returns the next external
// node needing input (with its mixed input) or nil when the quantum is
// complete (and the clock has advanced).
func (c *Context) Step() (*Node, [][][]float32) {
	if !c.stepping {
		c.beginQuantum()
		c.stepping = true
	}
	for c.idx < len(c.order) {
		n := c.order[c.idx]
		if n.external && !n.removed {
			if !n.provided {
				return n, c.gatherInputs(n)
			}
			n.provided = false
			c.idx++
			continue
		}
		c.stepNode(n)
		c.idx++
	}
	c.stepping = false
	c.endQuantum()
	return nil, nil
}

// Provide sets the output of an external node for the current quantum.
func (c *Context) Provide(n *Node, out [][]float32) {
	if len(out) == 0 {
		out = [][]float32{make([]float32, quantum)}
	}
	b := n.setOut(0, len(out))
	for ch := range out {
		copy(b[ch], out[ch])
	}
	n.provided = true
}

// TakeEnded returns and clears the list of source nodes that finished.
func (c *Context) TakeEnded() []int {
	e := c.Ended
	c.Ended = nil
	return e
}

// SetBuffer stores audio data (AudioBuffer contents) under id.
func (c *Context) SetBuffer(id int, rate float64, data [][]float32) {
	c.Buffers[id] = &Buffer{Rate: rate, Data: data}
}

// AddParam registers an extra AudioParam on a node (AudioWorklet descriptors).
func (n *Node) AddParam(name string, def, min, max float64, arate bool) *Param {
	return n.addParam(name, def, min, max, arate)
}

// ParamBlock evaluates a parameter for the current quantum: 128 values for
// a-rate/automated params, otherwise a single value.
func (c *Context) ParamBlock(p *Param) []float32 {
	if p.ARate && (p.automated() || len(p.inputs) > 0) {
		return append([]float32(nil), p.compute(c, c.Frame, quantum)...)
	}
	if p.ARate {
		return append([]float32(nil), p.compute(c, c.Frame, quantum)...)
	}
	return []float32{p.compute(c, c.Frame, 1)[0]}
}
