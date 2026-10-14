package engine

// media_audio_rt.go: what makes a real-time AudioContext independent of the page's JavaScript thread, and what lets
// MediaStreams in and out of the graph.
//
//   - A running context that needs no JavaScript to render (no AudioWorklet, no ScriptProcessor) is rendered by a
//     goroutine that keeps about 100 ms of audio queued on the audio output. currentTime is the render clock. A
//     busy page therefore does not make sound stutter. Contexts with host-driven nodes keep the page-driven clock
//     (media_audio_prelude.js _catchUp).
//   - MediaStreamAudioSourceNode: a goroutine reads the track (microphone, remote WebRTC audio, generated audio),
//     resamples it to the context rate and fills a ring the node drains one quantum at a time.
//   - MediaStreamAudioDestinationNode: its input is collected into a generated audio track, so a peer connection,
//     a MediaRecorder or a track processor can use what the graph produced.

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media/webaudio"
	"github.com/sweefbrowser/sweef/internal/rtcmedia"
)

// waAhead is how much audio the render goroutine keeps queued.
const waAhead = 100 * time.Millisecond

type waRT struct {
	c    *webaudio.Context
	sk   *waSink
	stop chan struct{}
	done chan struct{}
}

func (k *waSink) ahead() time.Duration {
	if k.st == nil {
		return 0
	}
	return k.st.Buffered()
}

func (r *waRT) loop() {
	defer close(r.done)
	tk := time.NewTicker(3 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-tk.C:
		}
		r.c.Lock()
		if !r.c.HasExternal() {
			for n := 0; n < 64 && r.sk.ahead() < waAhead; n++ {
				r.c.Render(128)
			}
		}
		r.c.Unlock()
	}
}

func (r *waRT) halt() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	<-r.done
}

// waFeed is a goroutine feeding a MediaStreamAudioSourceNode.
type waFeed struct{ cancel func() }

func (m *mediaHost) waStopCtx(id int) {
	if m.wa == nil {
		return
	}
	if r := m.wa.rts[id]; r != nil {
		r.halt()
		delete(m.wa.rts, id)
	}
	for _, f := range m.wa.feeds[id] {
		f.cancel()
	}
	delete(m.wa.feeds, id)
}

// waRTHost adds the real-time and stream host functions and serialises every context call with the render
// goroutine.
func (m *mediaHost) waRTHost(f map[string]hostFn) {
	f["waRealtime"] = func(a []any) (any, error) { // ctx, on
		id := int(argI(a, 0))
		c := m.wa.ctxs[id]
		if c == nil || c.Offline {
			return false, nil
		}
		if argB(a, 1) {
			if m.wa.rts[id] != nil {
				return true, nil
			}
			sk := m.wa.sinks[id]
			if sk == nil {
				return false, nil
			}
			r := &waRT{c: c, sk: sk, stop: make(chan struct{}), done: make(chan struct{})}
			m.wa.rts[id] = r
			go r.loop()
			return true, nil
		}
		if r := m.wa.rts[id]; r != nil {
			r.halt()
			delete(m.wa.rts, id)
		}
		return false, nil
	}
	f["waFrame"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		return float64(c.Frame), nil
	}
	// waStreamIn(ctx, node, specJSON): feed the node from a track. spec: {cap: trackId} or {rtc: [pc, tr]}.
	f["waStreamIn"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		var spec struct {
			Cap string  `json:"cap"`
			RTC []int64 `json:"rtc"`
		}
		_ = json.Unmarshal([]byte(argS(a, 2)), &spec)
		id, node := int(argI(a, 0)), int(argI(a, 1))
		rate := int(c.SampleRate)
		ring := webaudio.NewRing(rate/2, 2)
		var detach func()
		switch {
		case spec.Cap != "":
			ct := capture.LookupTrack(spec.Cap)
			if ct == nil {
				return nil, errors.New("InvalidStateError: the track is not live")
			}
			ch, cancel := ct.SubscribeAudio()
			detach = cancel
			go func() {
				fd := &waResampler{rate: rate}
				for ck := range ch {
					fd.push(ring, ck.PCM, ck.Rate, ck.Channels)
				}
			}()
		case len(spec.RTC) == 2:
			rh := m.j.rtc
			if rh == nil {
				return nil, errors.New("InvalidStateError: no peer connection")
			}
			rh.mu.Lock()
			p := rh.peers[spec.RTC[0]]
			rh.mu.Unlock()
			var x *rtcTR
			if p != nil {
				x = p.tr(spec.RTC[1])
			}
			if x == nil {
				return nil, errors.New("InvalidStateError: unknown receiver")
			}
			fd := &waResampler{rate: rate}
			var mu sync.Mutex
			detach = x.tapAudio(func(pcm []int16, r, ch int) {
				mu.Lock()
				fd.push(ring, pcm, r, ch)
				mu.Unlock()
			})
		default:
			return false, nil
		}
		c.Lock()
		err = c.SetStreamIn(node, ring)
		c.Unlock()
		if err != nil {
			detach()
			return nil, err
		}
		if m.wa.feeds == nil {
			m.wa.feeds = map[int][]*waFeed{}
		}
		m.wa.feeds[id] = append(m.wa.feeds[id], &waFeed{cancel: detach})
		return true, nil
	}
	// waStreamOut(ctx, node) -> the generated track that carries what reaches the node
	f["waStreamOut"] = func(a []any) (any, error) {
		c, err := m.waCtx(a)
		if err != nil {
			return nil, err
		}
		ch := m.j.capture
		if ch == nil {
			return nil, errors.New("InvalidStateError: no capture support")
		}
		ct, src, err := ch.newGenerated("audio", "MediaStreamAudioDestinationNode")
		if err != nil {
			return nil, errors.New("OperationError: " + err.Error())
		}
		gen := src.(*genAudio)
		genMu.Lock()
		genReg[ct.t.ID] = gen
		genMu.Unlock()
		go func() {
			<-ct.t.Ended()
			genMu.Lock()
			delete(genReg, ct.t.ID)
			genMu.Unlock()
		}()
		rate := int(c.SampleRate)
		var acc []int16
		var stopped atomic.Bool
		go func() { <-ct.t.Ended(); stopped.Store(true) }()
		tap := func(chs [][]float32) {
			if stopped.Load() || len(chs) == 0 {
				return
			}
			n := len(chs[0])
			for i := 0; i < n; i++ {
				l, r := chs[0][i], chs[0][i]
				if len(chs) > 1 {
					r = chs[1][i]
				}
				acc = append(acc, wf32to16(l), wf32to16(r))
			}
			if len(acc) >= 480*2 { // 10 ms blocks
				gen.push(&capture.AudioChunk{Rate: rate, Channels: 2, PCM: append([]int16(nil), acc...)})
				acc = acc[:0]
			}
		}
		c.Lock()
		err = c.SetTap(int(argI(a, 1)), tap)
		c.Unlock()
		if err != nil {
			ct.t.End(capture.EndStopped, nil)
			return nil, err
		}
		b, _ := json.Marshal(ch.trackJSON(ct))
		return string(b), nil
	}
	// serialise every graph call with the render goroutine
	for name, fn := range f {
		switch name {
		case "waNew", "waClose", "waRealtime", "waStreamIn", "waStreamOut":
			continue
		}
		fn := fn
		f[name] = func(a []any) (any, error) {
			if c := m.wa.ctxs[int(argI(a, 0))]; c != nil {
				c.Lock()
				defer c.Unlock()
			}
			return fn(a)
		}
	}
}

// waResampler converts interleaved s16 chunks of any rate and channel count to planar float at the context rate.
type waResampler struct {
	rate  int
	from  int
	chans []*rtcmedia.Resampler
	tmp   [][]float32
}

func (w *waResampler) push(ring *webaudio.Ring, pcm []int16, rate, ch int) {
	if ch < 1 || rate < 3000 || len(pcm) < ch {
		return
	}
	if w.from != rate || len(w.chans) != 2 {
		w.from = rate
		w.chans = []*rtcmedia.Resampler{rtcmedia.NewResampler(rate, w.rate), rtcmedia.NewResampler(rate, w.rate)}
		w.tmp = make([][]float32, 2)
	}
	n := len(pcm) / ch
	in := [2][]float32{make([]float32, n), make([]float32, n)}
	for i := 0; i < n; i++ {
		l := float32(pcm[i*ch]) / 32768
		r := l
		if ch > 1 {
			r = float32(pcm[i*ch+1]) / 32768
		}
		in[0][i], in[1][i] = l, r
	}
	out := make([][]float32, 2)
	for c := 0; c < 2; c++ {
		if rate == w.rate {
			out[c] = in[c]
			continue
		}
		w.tmp[c] = w.chans[c].Process(w.tmp[c][:0], in[c])
		out[c] = append([]float32(nil), w.tmp[c]...)
	}
	ring.Push(out)
}

func wf32to16(v float32) int16 {
	v *= 32767
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
