package engine

// media_audio.go: host functions driving internal/media/webaudio contexts
// from the media_audio_prelude.js Web Audio classes.

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/sweefbrowser/sweef/internal/audioout"
	"github.com/sweefbrowser/sweef/internal/media/webaudio"
)

//go:embed media_audio_prelude.js
var mediaAudioPreludeJS string

func init() { registerPrelude("media_audio", mediaAudioPreludeJS) }

type waState struct {
	ctxs  map[int]*webaudio.Context
	sinks map[int]*waSink
	next  int
}

func (m *mediaHost) waReset() {
	m.waShutdown()
	if m.wa != nil {
		m.wa.ctxs = map[int]*webaudio.Context{}
	}
}

func (m *mediaHost) waCtx(a []any) (*webaudio.Context, error) {
	c := m.wa.ctxs[int(argI(a, 0))]
	if c == nil {
		return nil, fmt.Errorf("InvalidStateError: audio context closed")
	}
	return c, nil
}

func anyF(v any) float64 { return argF([]any{v}, 0) }

func (m *mediaHost) waHost(f map[string]hostFn) {
	m.mseFns(f)
	m.wa = &waState{ctxs: map[int]*webaudio.Context{}, sinks: map[int]*waSink{}, next: 1}
	f["waNew"] = func(a []any) (any, error) {
		id := m.wa.next
		m.wa.next++
		c := webaudio.NewContext(argF(a, 0), argB(a, 1), int(argI(a, 2)), int(argI(a, 3)))
		m.wa.ctxs[id] = c
		if !c.Offline { // a real-time context plays through the audio output
			sk := &waSink{m: m, rate: int(c.SampleRate)}
			c.Sink = sk.write
			m.wa.sinks[id] = sk
		}
		return map[string]any{"id": id, "dest": c.Dest.ID}, nil
	}
	f["waClose"] = func(a []any) (any, error) {
		id := int(argI(a, 0))
		delete(m.wa.ctxs, id)
		if sk := m.wa.sinks[id]; sk != nil {
			sk.close()
			delete(m.wa.sinks, id)
		}
		return nil, nil
	}
	f["waNode"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		opts := map[string]any{}
		if js := argS(a, 2); js != "" {
			_ = json.Unmarshal([]byte(js), &opts)
		}
		typ := argS(a, 1)
		id, err := c.AddNode(typ, opts)
		if err != nil {
			return nil, err
		}
		// extra params for host-driven nodes (AudioWorklet descriptors)
		if ps, ok := opts["params"].([]any); ok {
			for _, pv := range ps {
				pm, _ := pv.(map[string]any)
				c.Node(id).AddParam(argS([]any{pm["name"]}, 0), anyF(pm["def"]), anyF(pm["min"]), anyF(pm["max"]), pm["arate"] == true)
			}
		}
		info := map[string]any{}
		for name, p := range c.Node(id).Params {
			info[name] = map[string]any{"def": p.Default, "min": p.Min, "max": p.Max, "arate": p.ARate}
		}
		return map[string]any{"id": id, "params": info}, nil
	}
	f["waSet"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		val := a[3]
		if argS(a, 2) == "curve" {
			if cs, ok := val.(string); ok {
				fl := b64f32(cs)
				arr := make([]any, len(fl))
				for i, f := range fl {
					arr[i] = float64(f)
				}
				val = arr
			}
		}
		return nil, c.Set(int(argI(a, 1)), argS(a, 2), val)
	}
	f["waSetBuffer"] = func(a []any) (any, error) { // ctx, bufId, rate, [b64...]
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		var chs []string
		_ = json.Unmarshal([]byte(argS(a, 3)), &chs)
		data := make([][]float32, len(chs))
		for i, s := range chs {
			data[i] = b64f32(s)
		}
		c.SetBuffer(int(argI(a, 1)), argF(a, 2), data)
		return nil, nil
	}
	f["waSetBufferPCM"] = func(a []any) (any, error) { // ctx, bufId, pcmId
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		p := m.pcm[int(argI(a, 2))]
		m.mu.Unlock()
		if p == nil {
			return nil, fmt.Errorf("InvalidStateError: missing audio data")
		}
		c.SetBuffer(int(argI(a, 1)), float64(p.SampleRate), p.Data)
		return nil, nil
	}
	f["waWave"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		var re, im []float64
		_ = json.Unmarshal([]byte(argS(a, 2)), &re)
		_ = json.Unmarshal([]byte(argS(a, 3)), &im)
		c.SetWave(int(argI(a, 1)), re, im, argB(a, 4))
		return nil, nil
	}
	param := func(c *webaudio.Context, a []any) (*webaudio.Param, error) {
		n := c.Node(int(argI(a, 1)))
		if n == nil || n.Params[argS(a, 2)] == nil {
			return nil, fmt.Errorf("bad param")
		}
		return n.Params[argS(a, 2)], nil
	}
	f["waParamEvent"] = func(a []any) (any, error) { // ctx,node,param,type,time,value,tc,duration,curve
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		p, err := param(c, a)
		if err != nil {
			return nil, err
		}
		e := webaudio.Event{Type: webaudio.EventType(argI(a, 3)), Time: argF(a, 4), Value: argF(a, 5), TimeCst: argF(a, 6), Duration: argF(a, 7)}
		if len(a) > 8 && a[8] != nil {
			switch cv := a[8].(type) {
			case string:
				e.Curve = b64f32(cv)
			case []any:
				for _, x := range cv {
					e.Curve = append(e.Curve, float32(anyF(x)))
				}
			}
		}
		p.AddEvent(e)
		return nil, nil
	}
	f["waParamSet"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		p, err := param(c, a)
		if err != nil {
			return nil, err
		}
		p.Value = argF(a, 3)
		return nil, nil
	}
	f["waParamRate"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		p, err := param(c, a)
		if err != nil {
			return nil, err
		}
		p.ARate = argB(a, 3)
		return nil, nil
	}
	f["waParamCancel"] = func(a []any) (any, error) { // ctx,node,param,time,hold
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		p, err := param(c, a)
		if err != nil {
			return nil, err
		}
		if argB(a, 4) {
			p.CancelAndHold(argF(a, 3))
		} else {
			p.CancelScheduled(argF(a, 3))
		}
		return nil, nil
	}
	f["waParamValue"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		p, err := param(c, a)
		if err != nil {
			return nil, err
		}
		v := p.ValueAt(c.Time())
		return float64(float32(v)), nil
	}
	f["waConnect"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		if s := argS(a, 5); s != "" {
			return nil, c.ConnectParam(int(argI(a, 1)), int(argI(a, 2)), int(argI(a, 3)), s)
		}
		return nil, c.Connect(int(argI(a, 1)), int(argI(a, 2)), int(argI(a, 3)), int(argI(a, 4)))
	}
	f["waDisconnect"] = func(a []any) (any, error) { // ctx, from, out, to, in, param
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		c.Disconnect(int(argI(a, 1)), int(argI(a, 2)), int(argI(a, 3)), int(argI(a, 4)), argS(a, 5))
		return nil, nil
	}
	f["waRemove"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		c.RemoveNode(int(argI(a, 1)))
		return nil, nil
	}
	f["waStart"] = func(a []any) (any, error) { // ctx,node,when,offset,duration(-1 none)
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		dur := argF(a, 4)
		return nil, c.Start(int(argI(a, 1)), argF(a, 2), argF(a, 3), dur, dur >= 0)
	}
	f["waStop"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		return nil, c.Stop(int(argI(a, 1)), argF(a, 2))
	}
	f["waRender"] = func(a []any) (any, error) { // ctx, frames
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		c.Render(int(argI(a, 1)))
		return map[string]any{"frame": c.Frame, "ended": toAnyInts(c.TakeEnded())}, nil
	}
	f["waTime"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		return c.Time(), nil
	}
	f["waEnded"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		return toAnyInts(c.TakeEnded()), nil
	}
	f["waHasExternal"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		return c.HasExternal(), nil
	}
	f["waOutput"] = func(a []any) (any, error) { // store offline output as PCM, return descriptor
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		return m.storePCMRaw(c.Output, int(c.SampleRate)), nil
	}
	f["waAnalyser"] = func(a []any) (any, error) { // ctx,node,kind
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		id := int(argI(a, 1))
		switch argS(a, 2) {
		case "float-time":
			return f32b64(c.AnalyserFloatTime(id)), nil
		case "float-freq":
			return f32b64(c.AnalyserFloatFreq(id)), nil
		case "byte-time":
			return base64.StdEncoding.EncodeToString(c.AnalyserByteTime(id)), nil
		case "byte-freq":
			return base64.StdEncoding.EncodeToString(c.AnalyserByteFreq(id)), nil
		}
		return "", nil
	}
	f["waFreqResp"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		var fs []float64
		_ = json.Unmarshal([]byte(argS(a, 2)), &fs)
		mag, ph := c.FrequencyResponse(int(argI(a, 1)), fs)
		toA := func(x []float64) []any {
			o := make([]any, len(x))
			for i, v := range x {
				if math.IsNaN(v) {
					o[i] = "NaN"
				} else {
					o[i] = v
				}
			}
			return o
		}
		return map[string]any{"mag": toA(mag), "phase": toA(ph)}, nil
	}
	f["waReduction"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		return c.CompressorReduction(int(argI(a, 1))), nil
	}
	f["waListener"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		n := c.ListenerNode()
		info := map[string]any{}
		for name, p := range c.Node(n).Params {
			info[name] = map[string]any{"def": p.Default, "min": p.Min, "max": p.Max, "arate": p.ARate}
		}
		return map[string]any{"id": n, "params": info}, nil
	}
	// host-driven nodes: waStep returns the next external node needing input
	f["waStep"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		n, in := c.Step()
		if n == nil {
			return map[string]any{"done": true, "frame": c.Frame, "ended": toAnyInts(c.TakeEnded())}, nil
		}
		ins := make([]any, len(in))
		for i, chans := range in {
			cs := make([]any, len(chans))
			for k, ch := range chans {
				cs[k] = f32b64(ch)
			}
			ins[i] = cs
		}
		params := map[string]any{}
		for name, p := range n.Params {
			params[name] = f32b64(c.ParamBlock(p))
		}
		return map[string]any{"node": n.ID, "inputs": ins, "params": params}, nil
	}
	f["waProvide"] = func(a []any) (any, error) { // ctx,node,[b64 channels]
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		var chs []string
		_ = json.Unmarshal([]byte(argS(a, 2)), &chs)
		out := make([][]float32, len(chs))
		for i, s := range chs {
			out[i] = b64f32(s)
		}
		c.Provide(c.Node(int(argI(a, 1))), out)
		return nil, nil
	}
}

func toAnyInts(v []int) []any {
	o := make([]any, len(v))
	for i, x := range v {
		o[i] = x
	}
	return o
}

func (m *mediaHost) storePCMRaw(ch [][]float32, rate int) map[string]any {
	cp := make([][]float32, len(ch))
	for i := range ch {
		cp[i] = append([]float32(nil), ch[i]...)
	}
	return m.storePCMData(cp, rate)
}

// waShutdown releases the audio output of every Web Audio context.
func (m *mediaHost) waShutdown() {
	if m.wa == nil {
		return
	}
	for id, sk := range m.wa.sinks {
		sk.close()
		delete(m.wa.sinks, id)
	}
}

// waSink plays the destination of a real-time AudioContext: the graph renders in wall-clock steps from
// the page's timer and each quantum is queued on an audioout stream (dropped, not blocked on, when the
// device falls behind, so the page's JavaScript never waits for the sound card).
type waSink struct {
	m       *mediaHost
	rate    int
	st      *audioout.Stream
	started bool
	buf     []float32
}

func (k *waSink) write(ch [][]float32) {
	if len(ch) == 0 || len(ch[0]) == 0 {
		return
	}
	if k.st == nil {
		var g *audioout.Group
		if k.m.j.tab != nil {
			g = k.m.j.tab.audioGroup()
		}
		k.st = audioout.Default().Open(k.rate, 2, g)
	}
	n := len(ch[0])
	if cap(k.buf) < n*2 {
		k.buf = make([]float32, n*2)
	}
	b := k.buf[:n*2]
	l, r := ch[0], ch[0]
	if len(ch) > 1 {
		r = ch[1]
	}
	for i := 0; i < n; i++ {
		b[2*i], b[2*i+1] = l[i], r[i]
	}
	if k.st.Buffered() > 400*time.Millisecond {
		return
	}
	if _, err := k.st.Write(b); err != nil {
		return
	}
	if !k.started && k.st.Buffered() >= 60*time.Millisecond {
		k.started = true
		k.st.Play()
	}
}

func (k *waSink) close() {
	if k.st != nil {
		k.st.Close()
		k.st = nil
	}
}
