package mediarec

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media"
)

// AudioIn is one audio source. Rate and Channels are what the source declares; chunks that differ are converted.
// OwnClock says the samples are produced on a clock of their own (a Web Audio graph) and must not be padded or
// trimmed against the wall clock.
type AudioIn struct {
	C        <-chan *capture.AudioChunk
	Cancel   func()
	Rate     int
	Channels int
	OwnClock bool
}

// VideoIn is the video source; frames of another size are scaled to W x H.
type VideoIn struct {
	C      <-chan *capture.Frame
	Cancel func()
	W, H   int
	FPS    float64
}

// Config selects the container and the encoder settings.
type Config struct {
	Container          string // "webm" or "ogg"
	AudioBPS, VideoBPS int
	CBR                bool
	Timeslice          time.Duration
}

// Chunk is a piece of the recording; TimeMs is the recording time of its first block.
type Chunk struct {
	Data   []byte
	TimeMs float64
}

const (
	opusRate     = 48000
	opusFrame    = 960 // 20 ms
	maxVideoFPS  = 30
	holdBack     = time.Second // an encoded packet never waits longer than this for the other track
	stallPadding = 9600        // 200 ms: a clocked audio source that is this far behind gets silence
	lagFrames    = 3 * opusFrame
)

var errStopped = errors.New("mediarec: recorder stopped")

// Recorder encodes and muxes. Create it with New, start it with Start, and finish with Stop; Done is closed after
// the last chunk went to the emit function. emit is called from one goroutine at a time, in order.
type Recorder struct {
	cfg  Config
	emit func(Chunk)
	clk  clock

	mu      sync.Mutex // the container, the interleaver and the chunk bookkeeping
	ogg     *OggOpus
	webm    *WebM
	il      []ilItem
	marks   [3]atomic.Int64 // per track: no packet older than this will arrive (µs)
	firstMs float64
	haveBlk bool
	lastMs  float64
	num     [2]int // WebM track number of the audio and the video track

	am *mixer
	vp *vloop

	cmds    chan cmd
	quit    chan struct{}
	done    chan struct{}
	started atomic.Bool
	paused  atomic.Bool
	wg      sync.WaitGroup
	nextCut time.Duration
}

type ilItem struct {
	ts      int64 // µs
	track   int
	key     bool
	data    []byte
	samples int
	at      time.Time
}

type cmdKind int

const (
	cmdPause cmdKind = iota
	cmdResume
	cmdData
	cmdStop
)

type cmd struct {
	k    cmdKind
	done chan struct{}
}

// clock is the recording time: wall time minus the time spent paused.
type clock struct {
	mu       sync.Mutex
	start    time.Time
	pausedAt time.Time
	idle     time.Duration
}

func (c *clock) Now() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.start.IsZero() {
		return 0
	}
	t := time.Now()
	if !c.pausedAt.IsZero() {
		t = c.pausedAt
	}
	return t.Sub(c.start) - c.idle
}

func (c *clock) pause() {
	c.mu.Lock()
	if c.pausedAt.IsZero() {
		c.pausedAt = time.Now()
	}
	c.mu.Unlock()
}

func (c *clock) resume() {
	c.mu.Lock()
	if !c.pausedAt.IsZero() {
		c.idle += time.Since(c.pausedAt)
		c.pausedAt = time.Time{}
	}
	c.mu.Unlock()
}

const (
	trkAudio = 0
	trkVideo = 1
)

// New builds the encoders and the container. audio and video may be empty; at least one must be given.
func New(cfg Config, audio []*AudioIn, video *VideoIn, emit func(Chunk)) (*Recorder, error) {
	if len(audio) == 0 && video == nil {
		return nil, errors.New("no tracks to record")
	}
	if cfg.Container == "ogg" && video != nil {
		return nil, errors.New("Ogg holds audio only")
	}
	r := &Recorder{cfg: cfg, emit: emit, cmds: make(chan cmd, 8), quit: make(chan struct{}), done: make(chan struct{})}
	var tracks []WebMTrack
	if len(audio) > 0 {
		m, err := newMixer(r, audio)
		if err != nil {
			return nil, err
		}
		r.am = m
		tracks = append(tracks, WebMTrack{Channels: m.nch, Rate: opusRate, PreSkip: m.enc.PreSkip(), OpusHead: media.OpusHeadBytes(m.nch, firstRate(audio), m.enc.PreSkip())})
		r.num[trkAudio] = len(tracks)
	}
	if video != nil {
		vl, err := newVLoop(r, video)
		if err != nil {
			return nil, err
		}
		r.vp = vl
		tracks = append(tracks, WebMTrack{Video: true, Width: vl.w, Height: vl.h})
		r.num[trkVideo] = len(tracks)
	}
	if cfg.Container == "ogg" {
		r.ogg = NewOggOpus(r.am.nch, firstRate(audio), r.am.enc.PreSkip())
	} else {
		r.webm = NewWebM(tracks)
	}
	never := int64(math.MaxInt64)
	if r.am == nil {
		r.marks[trkAudio].Store(never)
	}
	if r.vp == nil {
		r.marks[trkVideo].Store(never)
	}
	return r, nil
}

func firstRate(a []*AudioIn) int {
	if len(a) > 0 && a[0].Rate > 0 {
		return a[0].Rate
	}
	return opusRate
}

// Start begins recording.
func (r *Recorder) Start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	r.clk.mu.Lock()
	r.clk.start = time.Now()
	r.clk.mu.Unlock()
	if r.cfg.Timeslice > 0 {
		r.nextCut = r.cfg.Timeslice
	}
	if r.am != nil {
		r.am.start()
	}
	if r.vp != nil {
		r.wg.Add(1)
		go r.vp.run()
	}
	go r.control()
}

// Done is closed after the final chunk was emitted.
func (r *Recorder) Done() <-chan struct{} { return r.done }

func (r *Recorder) send(k cmdKind) chan struct{} {
	c := cmd{k: k, done: make(chan struct{})}
	select {
	case r.cmds <- c:
	case <-r.done:
		close(c.done)
	}
	return c.done
}

// Pause stops gathering; the recording clock stops with it.
func (r *Recorder) Pause() {
	r.paused.Store(true)
	r.clk.pause()
	<-r.send(cmdPause)
}

// Resume continues after Pause.
func (r *Recorder) Resume() {
	r.clk.resume()
	<-r.send(cmdResume)
	r.paused.Store(false)
}

// RequestData emits what has been recorded since the last chunk.
func (r *Recorder) RequestData() { r.send(cmdData) }

// Stop ends the recording: sources are released, the last chunk is emitted, then Done closes.
func (r *Recorder) Stop() { r.send(cmdStop) }

// AudioEnded says audio source i delivers nothing more (its track ended).
func (r *Recorder) AudioEnded(i int) {
	if r.am != nil {
		r.am.end(i)
	}
}

// VideoEnded says the video source delivers nothing more.
func (r *Recorder) VideoEnded() {
	if r.vp != nil {
		r.vp.end()
	}
}

// Elapsed is the recording time so far.
func (r *Recorder) Elapsed() time.Duration { return r.clk.Now() }

func (r *Recorder) control() {
	tk := time.NewTicker(10 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case c := <-r.cmds:
			switch c.k {
			case cmdPause:
				if r.am != nil {
					r.am.pause()
				}
			case cmdResume:
				if r.am != nil {
					r.am.resume()
				}
			case cmdData:
				r.cut(false)
			case cmdStop:
				r.finish()
				close(c.done)
				return
			}
			close(c.done)
		case <-tk.C:
			if r.paused.Load() {
				continue
			}
			now := r.clk.Now()
			if r.am != nil {
				r.am.tick(now)
			}
			if r.vp != nil {
				r.vp.idleMark(now)
			}
			r.mu.Lock()
			r.release(false)
			r.mu.Unlock()
			if r.cfg.Timeslice > 0 && now >= r.nextCut {
				for r.nextCut <= now {
					r.nextCut += r.cfg.Timeslice
				}
				r.cut(true)
			}
		}
	}
}

// finish ends the sources, drains the encoders and writes the last chunk.
func (r *Recorder) finish() {
	close(r.quit)
	if r.am != nil {
		r.am.cancelAll()
	}
	if r.vp != nil {
		r.vp.in.Cancel()
	}
	r.wg.Wait()
	if r.am != nil {
		r.am.final()
	}
	end := r.clk.Now()
	r.mu.Lock()
	r.release(true)
	if r.ogg != nil {
		real := int64(0)
		if r.am != nil {
			real = r.am.real
		}
		r.ogg.Finish(real)
	} else {
		r.webm.Finish(end.Milliseconds())
	}
	data := r.take()
	first := r.firstMs
	if !r.haveBlk {
		first = r.lastMs
	}
	r.mu.Unlock()
	r.emit(Chunk{Data: data, TimeMs: first})
	close(r.done)
}

// cut emits the finished part of the recording. quiet skips empty or header-only chunks (timeslice ticks).
func (r *Recorder) cut(quiet bool) {
	r.mu.Lock()
	r.release(false)
	if quiet && !r.haveBlk {
		r.mu.Unlock()
		return
	}
	if r.ogg != nil {
		r.ogg.Flush()
	} else {
		r.webm.Flush()
	}
	data := r.take()
	first := r.firstMs
	if !r.haveBlk {
		first = r.lastMs
	}
	r.haveBlk = false
	r.mu.Unlock()
	if len(data) > 0 {
		r.emit(Chunk{Data: data, TimeMs: first})
	}
}

func (r *Recorder) take() []byte {
	if r.ogg != nil {
		return r.ogg.Take()
	}
	return r.webm.Take()
}

// put hands an encoded packet to the interleaver.
func (r *Recorder) put(track int, tsUs int64, key bool, data []byte, samples int) {
	r.mu.Lock()
	it := ilItem{ts: tsUs, track: track, key: key, data: data, samples: samples, at: time.Now()}
	i := len(r.il)
	r.il = append(r.il, it)
	for i > 0 && r.il[i-1].ts > it.ts {
		r.il[i] = r.il[i-1]
		i--
	}
	r.il[i] = it
	r.release(false)
	r.mu.Unlock()
}

// release writes the packets no other track can still precede. Callers hold r.mu.
func (r *Recorder) release(force bool) {
	limit := r.marks[trkAudio].Load()
	if v := r.marks[trkVideo].Load(); v < limit {
		limit = v
	}
	n := 0
	for n < len(r.il) {
		it := r.il[n]
		if !force && it.ts > limit && time.Since(it.at) < holdBack {
			break
		}
		ms := it.ts / 1000
		if r.ogg != nil {
			r.ogg.AddPacket(it.data, it.samples)
		} else {
			r.webm.AddBlock(r.num[it.track], ms, it.key, it.data)
		}
		if !r.haveBlk {
			r.haveBlk, r.firstMs = true, float64(it.ts)/1000
		}
		r.lastMs = float64(it.ts) / 1000
		n++
	}
	if n > 0 {
		r.il = append(r.il[:0], r.il[n:]...)
	}
}
