package engine

// mediaplayer.go: the playback engine of one <video>/<audio> element. It reads
// coded samples from a media.TrackStore (a progressive file or the
// SourceBuffers of a MediaSource), decodes audio into an audioout stream and
// video into the element's frame source, and keeps video in step with the
// audio clock.
//
// The audio device position is the master clock while audio plays. Without an
// audio track (or after the audio ended before the video) a wall clock stands
// in. The page's JavaScript only sends commands (play, pause, seek, rate,
// volume) and reads the clock; nothing here calls into the VM.

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/audioout"
	"github.com/sweefbrowser/sweef/internal/media"
)

// trackSource supplies the sample stores a player reads. MSE sources look the
// stores up on every call because init segments can replace them.
type trackSource interface {
	Video() *media.TrackStore
	Audio() *media.TrackStore
	// EOS reports that no more samples will arrive (progressive file, or a
	// MediaSource after endOfStream).
	EOS() bool
}

// fixedTracks are the stores of a progressive file. While a background indexer or decoder is still
// adding samples (done not nil) the file is not at its end yet.
type fixedTracks struct {
	v, a *media.TrackStore
	done <-chan struct{}
}

func (f fixedTracks) Video() *media.TrackStore { return f.v }
func (f fixedTracks) Audio() *media.TrackStore { return f.a }
func (f fixedTracks) EOS() bool {
	if f.done == nil {
		return true
	}
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// mediaFrameHook, when set (tests), is called whenever a player publishes a picture.
var mediaFrameHook atomic.Pointer[func(node int64, pts float64, at time.Time)]

// mediaLateHook, when set (tests), receives how late each published picture was: the player clock minus the
// picture's presentation time, in milliseconds.
var mediaLateHook atomic.Pointer[func(lateMS float64)]

// PlayerStats are the counters behind getVideoPlaybackQuality().
type PlayerStats struct {
	Total, Dropped, Corrupted int64
}

const (
	audioAhead   = 300 * time.Millisecond // decoded audio kept queued
	audioPrebuf  = 80 * time.Millisecond  // queued before the stream starts
	lateJumpSecs = 0.25                   // behind by more than this: jump to a later sync sample
	maxSilence   = 0.1                    // gaps up to this many seconds are filled with silence
)

type mediaPlayer struct {
	tab  *Tab
	src  trackSource
	vs   *videoSource
	node int64

	mu       sync.Mutex
	playing  bool
	pos      float64   // media time at the anchor
	wall     time.Time // wall time of the anchor (wall clock mode)
	rate     float64
	vol      float64
	muted    bool
	routed   bool // audio goes to a Web Audio graph instead of the device
	gen      int  // bumped by every command that restarts audio
	aStarted bool // the audio stream started: its position is the clock
	aDone    bool // audio ran out before the video: wall clock continues
	aBase    time.Duration
	aT0      float64
	keep     bool // paused with the audio loop and stream intact: play() can continue without a restart
	stream   *audioout.Stream
	audioEnd chan struct{} // closed when the current audio loop returned

	force  atomic.Bool // show the frame at the clock even if it is the one already shown
	hold   atomic.Bool // do not show pictures yet (the element shows its poster)
	closed atomic.Bool
	wake   chan struct{}
	wg     sync.WaitGroup

	total, dropped, corrupted atomic.Int64
	hasAudio                  atomic.Bool
	audioErr                  atomic.Bool
	videoErr                  atomic.Bool
	underruns                 atomic.Int64
	waitKey                   atomic.Bool // decoding is blocked on a content key the page has not supplied (EME)
}

func newMediaPlayer(tab *Tab, node int64, src trackSource, vs *videoSource) *mediaPlayer {
	p := &mediaPlayer{tab: tab, node: node, src: src, vs: vs, rate: 1, vol: 1, wake: make(chan struct{}, 1)}
	p.hasAudio.Store(src.Audio() != nil)
	p.wg.Add(1)
	go p.videoLoop()
	return p
}

func (p *mediaPlayer) kick() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Close stops all goroutines and releases the audio stream.
func (p *mediaPlayer) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	p.mu.Lock()
	p.gen++
	st := p.stream
	p.stream = nil
	p.mu.Unlock()
	if st != nil {
		st.Close()
	}
	p.kick()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond): // a loop blocked in a network read ends by itself
	}
}

// ---- commands (any goroutine) ---------------------------------------------------------------------

// Play starts (or restarts) playback at media time pos.
func (p *mediaPlayer) Play(pos, rate float64) {
	p.mu.Lock()
	if p.keep && !p.playing && p.stream != nil && rate == p.rate && math.Abs(pos-p.pos) < 0.002 {
		// resume where the pause left off: the queued audio is still right, no flush and no new start-up delay
		p.keep = false
		p.playing = true
		p.wall = time.Now()
		p.stream.Play()
	} else {
		p.restartLocked(pos, rate, true)
	}
	p.mu.Unlock()
	p.force.Store(true)
	p.kick()
}

// Pause freezes the clock at its current value and returns it.
func (p *mediaPlayer) Pause() float64 {
	p.mu.Lock()
	t := p.clockLocked()
	if p.playing && p.aStarted && !p.aDone && p.stream != nil && !p.closed.Load() {
		// keep the audio loop and the queued audio: pausing only stops consumption
		p.stream.Pause()
		p.pos, p.playing, p.keep = t, false, true
	} else {
		p.restartLocked(t, p.rate, false)
	}
	p.mu.Unlock()
	p.kick()
	return t
}

// Seek moves the clock to pos (playing or not) and shows the frame there.
func (p *mediaPlayer) Seek(pos float64) {
	p.mu.Lock()
	p.restartLocked(pos, p.rate, p.playing)
	p.mu.Unlock()
	p.force.Store(true)
	p.kick()
}

// SetRate changes the playback rate without moving the clock.
func (p *mediaPlayer) SetRate(rate float64) {
	p.mu.Lock()
	if rate == p.rate {
		p.mu.Unlock()
		return
	}
	t := p.clockLocked()
	p.restartLocked(t, rate, p.playing)
	p.mu.Unlock()
	p.kick()
}

// SetVolume sets the element volume and mute.
func (p *mediaPlayer) SetVolume(v float64, muted bool) {
	p.mu.Lock()
	p.vol, p.muted = v, muted
	st := p.stream
	routed := p.routed
	p.mu.Unlock()
	if st != nil {
		st.SetVolume(v)
		st.SetMuted(muted || routed)
	}
}

// SetHold keeps decoded pictures off the screen (a poster is showing).
func (p *mediaPlayer) SetHold(on bool) {
	p.hold.Store(on)
	if !on {
		p.force.Store(true)
		p.kick()
	}
}

// SetRouted sends the element's sound into a Web Audio graph (the stream is
// silenced, its clock keeps running).
func (p *mediaPlayer) SetRouted(on bool) {
	p.mu.Lock()
	p.routed = on
	st := p.stream
	m := p.muted
	p.mu.Unlock()
	if st != nil {
		st.SetMuted(m || on)
	}
}

// restartLocked re-anchors the clock and restarts the audio loop.
func (p *mediaPlayer) restartLocked(pos, rate float64, playing bool) {
	p.gen++
	p.keep = false
	if p.stream != nil {
		p.stream.Pause()
		p.stream.Flush()
	}
	p.pos, p.rate, p.playing = pos, rate, playing
	p.wall = time.Now()
	p.aStarted, p.aDone = false, false
	if playing && p.src.Audio() != nil && rate >= 0.25 && rate <= 4 && !p.audioErr.Load() {
		prev := p.audioEnd
		done := make(chan struct{})
		p.audioEnd = done
		gen := p.gen
		p.wg.Add(1)
		go p.audioLoop(gen, pos, rate, prev, done)
	}
}

// Clock returns the current media time.
func (p *mediaPlayer) Clock() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.clockLocked()
}

func (p *mediaPlayer) clockLocked() float64 {
	if !p.playing {
		return p.pos
	}
	if p.aStarted && p.stream != nil && !p.aDone {
		d := p.stream.Position() - p.aBase
		if d < 0 {
			d = 0
		}
		return p.aT0 + d.Seconds()*p.rate
	}
	if p.waitingForAudioLocked() {
		return p.pos
	}
	return p.pos + time.Since(p.wall).Seconds()*p.rate
}

// waitingForAudioLocked: the player has an audio loop that has not produced
// sound yet, so the clock must not run ahead of it.
func (p *mediaPlayer) waitingForAudioLocked() bool {
	return !p.aStarted && !p.aDone && p.src.Audio() != nil && p.rate >= 0.25 && p.rate <= 4 && !p.audioErr.Load()
}

// State reports the clock and whether audio drives it.
func (p *mediaPlayer) State() (t float64, playing, audioClock bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.clockLocked(), p.playing, p.aStarted && !p.aDone
}

// Stats returns the frame counters.
func (p *mediaPlayer) Stats() PlayerStats {
	return PlayerStats{Total: p.total.Load(), Dropped: p.dropped.Load(), Corrupted: p.corrupted.Load()}
}

func (p *mediaPlayer) stale(gen int) bool {
	if p.closed.Load() {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gen != gen
}

// ---- audio ------------------------------------------------------------------------------------

func interleave(pcm [][]float32, from int, dst []float32) []float32 {
	ch := len(pcm)
	n := len(pcm[0]) - from
	if n <= 0 {
		return dst[:0]
	}
	if cap(dst) < n*ch {
		dst = make([]float32, n*ch)
	}
	dst = dst[:n*ch]
	for c := 0; c < ch; c++ {
		src := pcm[c][from:]
		for i := 0; i < n; i++ {
			dst[i*ch+c] = src[i]
		}
	}
	return dst
}

func sameAudioCfg(a, b *media.TrackConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a == b || (a.CodecName == b.CodecName && string(a.Cfg) == string(b.Cfg))
}

func (p *mediaPlayer) audioLoop(gen int, start, rate float64, prev, done chan struct{}) {
	defer p.wg.Done()
	defer close(done)
	if prev != nil {
		<-prev // the previous loop owns the stream until it has returned
	}
	var (
		dec     media.AudioDecoder
		decCfg  *media.TrackConfig
		st      *audioout.Stream
		buf     []float32
		curDTS  float64
		have    bool
		expect  float64 // media time of the next PCM frame to write
		wrote   bool
		playing bool
		eosSent bool
	)
	sleep := func(d time.Duration) bool {
		time.Sleep(d)
		return !p.stale(gen)
	}
	for !p.stale(gen) {
		as := p.src.Audio()
		if as == nil {
			if !sleep(20 * time.Millisecond) {
				return
			}
			continue
		}
		if !have {
			s, ok := as.ShownAt(start)
			if !ok {
				if f, ok2 := as.FirstAfter(start); ok2 && f.PTS-start < 0.05 {
					s, ok = f, true
				}
			}
			if !ok {
				if !sleep(20 * time.Millisecond) {
					return
				}
				continue
			}
			// back up a few access units so the decoder state has converged at `start`
			first := s
			for i := 0; i < 4; i++ {
				pr, ok := as.Prev(first.DTS)
				if !ok || first.DTS-pr.End() > 0.01 {
					break
				}
				first = pr
			}
			curDTS = first.DTS - 1e-6
			have = true
			expect = start
		}
		if st != nil && st.Buffered() >= audioAhead {
			if !playing {
				playing = p.startStream(gen, st)
			}
			if !sleep(8 * time.Millisecond) {
				return
			}
			continue
		}
		n, ok, gap := as.Next(curDTS)
		if !ok || (gap && n.PTS-expect > maxSilence) {
			// out of buffered data (or a hole): what is queued plays out, then the clock stands still
			if st != nil && !playing && wrote {
				playing = p.startStream(gen, st)
			}
			if !ok && p.src.EOS() && wrote && !eosSent {
				eosSent = true
				st.End()
				p.wg.Add(1)
				go func(st *audioout.Stream) {
					defer p.wg.Done()
					select {
					case <-st.Done():
						p.audioDrained(gen, st)
					case <-time.After(30 * time.Second):
					}
				}(st)
			}
			if !sleep(15 * time.Millisecond) {
				return
			}
			continue
		}
		data, err := n.Bytes()
		if errors.Is(err, media.ErrNoKey) { // encrypted and the key is not here yet: wait, do not skip the sample
			p.waitKey.Store(true)
			if !sleep(25 * time.Millisecond) {
				return
			}
			continue
		}
		if err != nil {
			curDTS = n.DTS
			continue
		}
		if n.Encrypted() {
			p.waitKey.Store(false)
		}
		cfg := n.Cfg
		if cfg == nil {
			c := as.Config()
			cfg = &c
		}
		if dec == nil || !sameAudioCfg(cfg, decCfg) {
			d, err := media.NewAudioDecoder(*cfg)
			if err != nil {
				p.audioErr.Store(true)
				p.audioFailed(gen)
				return
			}
			dec, decCfg = d, cfg
		}
		pcm, err := dec.Decode(data)
		curDTS = n.DTS
		if err != nil || len(pcm) == 0 || len(pcm[0]) == 0 {
			continue
		}
		sr := dec.Rate()
		frames := len(pcm[0])
		skip := 0
		if n.PTS < start-1e-9 {
			skip = int((start-n.PTS)*float64(sr) + 0.5)
		}
		if wrote {
			gapSec := n.PTS - expect
			if gapSec > 0.002 && gapSec <= maxSilence {
				sil := make([][]float32, len(pcm))
				k := int(gapSec*float64(sr) + 0.5)
				for c := range sil {
					sil[c] = make([]float32, k)
				}
				buf = interleave(sil, 0, buf)
				if _, err := st.Write(buf); err != nil {
					return
				}
				expect = n.PTS
			} else if gapSec < -0.002 {
				if o := int(-gapSec*float64(sr) + 0.5); o > skip {
					skip = o
				}
			}
		}
		endT := n.PTS + float64(frames)/float64(sr)
		if skip >= frames {
			if endT > expect {
				expect = endT
			}
			continue
		}
		if st == nil {
			p.mu.Lock()
			if p.gen != gen {
				p.mu.Unlock()
				return
			}
			st = p.openStreamLocked(sr, len(pcm))
			p.mu.Unlock()
			if st == nil {
				p.audioErr.Store(true)
				p.audioFailed(gen)
				return
			}
		}
		if !wrote {
			t0 := n.PTS + float64(skip)/float64(sr)
			p.mu.Lock()
			if p.gen != gen {
				p.mu.Unlock()
				return
			}
			p.aBase = st.Written()
			p.aT0 = t0
			p.mu.Unlock()
			wrote = true
		}
		buf = interleave(pcm, skip, buf)
		if _, err := st.Write(buf); err != nil {
			return
		}
		expect = endT
		if !playing && st.Buffered() >= audioPrebuf {
			playing = p.startStream(gen, st)
		}
	}
}

// startStream starts consumption of the queued audio and makes the stream
// position the clock. It reports whether the stream is running.
func (p *mediaPlayer) startStream(gen int, st *audioout.Stream) bool {
	p.mu.Lock()
	if p.gen != gen {
		p.mu.Unlock()
		return false
	}
	p.aStarted = true
	p.mu.Unlock()
	st.Play()
	return true
}

// openStreamLocked returns the player's audio stream, (re)opening it when the
// format changed. p.mu is held.
func (p *mediaPlayer) openStreamLocked(rate, ch int) *audioout.Stream {
	if p.stream != nil {
		p.stream.Close()
		p.stream = nil
	}
	out := audioout.Default()
	st := out.Open(rate, ch, p.tab.audioGroup())
	st.SetVolume(p.vol)
	st.SetMuted(p.muted || p.routed)
	st.SetSpeed(p.rate)
	p.stream = st
	return st
}

// audioDrained: every queued audio frame was played and no more will come; the
// clock continues on the wall clock from where the audio ended.
func (p *mediaPlayer) audioDrained(gen int, st *audioout.Stream) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen != gen || p.stream != st {
		return
	}
	t := p.clockLocked()
	p.pos, p.wall, p.aDone = t, time.Now(), true
}

// audioFailed: audio cannot be decoded; play silently on the wall clock.
func (p *mediaPlayer) audioFailed(gen int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen != gen {
		return
	}
	p.pos, p.wall = p.clockLocked(), time.Now()
	p.aDone = true
}

// ---- video --------------------------------------------------------------------------------

// aheadPic is a picture decoded before its time, kept until the clock reaches it.
type aheadPic struct {
	s   media.Sample
	pic media.Picture
}

const (
	aheadMax    = 6                      // pictures decoded ahead of the clock
	aheadWindow = 400 * time.Millisecond // how far ahead of the clock (media time at rate 1) the decoder may run
	// A late decoder: a picture that is decoded late is still shown (video that moves, behind the sound, beats a
	// frozen frame). When the clock is more than resyncLag seconds ahead of the next sample to decode, decoding
	// skips ahead to the latest key frame that is already due, which cuts the delay to the age of that key
	// frame and never freezes the picture. Only when the delay passes resyncPanic seconds with no such key
	// frame does it wait for one that is due within resyncWait seconds. Pictures that were skipped count as
	// dropped, which is what a page that adapts its quality to getVideoPlaybackQuality() needs to see.
	resyncLag   = 0.25
	resyncPanic = 1.5
	resyncWait  = 2.5
)

// videoLoop shows the picture that is due at the player's clock.
//
// Playing forward it decodes in decode order, one sample per step, re-reading the clock between steps:
// pictures decoded early wait in a short queue (so a key frame that takes three frame times does not make the
// next ones late), a picture that is already late is skipped without decoding when nothing refers to it
// (non-reference B-frames), a late picture that is decoded anyway is still shown when it is the newest one
// available (better than a frozen frame), and when the decoder is hopelessly behind it resynchronises on the
// next key frame. A seek, the first picture and a configuration change decode straight through from the key
// frame instead.
func (p *mediaPlayer) videoLoop() {
	defer p.wg.Done()
	var (
		dec       *media.VideoDecoder
		decCfg    *media.TrackConfig
		decDTS    float64
		haveDec   bool
		shown     media.Sample
		haveShown bool
		shownPic  media.Picture
		ready     []aheadPic // decoded, not yet shown; decode order
	)
	nap := func(d time.Duration) {
		select {
		case <-p.wake:
		case <-time.After(d):
		}
	}
	sameCfg := func(a, b *media.TrackConfig) bool {
		return a == b || (a != nil && b != nil && a.CodecName == b.CodecName && string(a.Cfg) == string(b.Cfg) && a.Width == b.Width)
	}
	cfgOf := func(vs *media.TrackStore, s media.Sample) *media.TrackConfig {
		if s.Cfg != nil {
			return s.Cfg
		}
		c := vs.Config()
		return &c
	}
	// purge forgets pictures that will never be shown (due at or before pts).
	purge := func(pts float64) {
		k := 0
		for _, e := range ready {
			if e.s.PTS > pts {
				ready[k] = e
				k++
			}
		}
		for i := k; i < len(ready); i++ {
			ready[i] = aheadPic{}
		}
		ready = ready[:k]
	}
	// present publishes a picture and does the frame accounting.
	present := func(vstore *media.TrackStore, s media.Sample, pic media.Picture, playing bool) {
		n := int64(1)
		again := haveShown && s.DTS == shown.DTS
		if haveShown && s.PTS > shown.PTS {
			// frames that fell due since the last picture and were skipped count as dropped
			if c := vstore.CountPTS(shown.PTS, s.PTS); c > 1 {
				n = int64(c)
			}
		}
		if !again {
			p.total.Add(n)
			if n > 1 && playing {
				p.dropped.Add(n - 1)
			}
		}
		shown, haveShown, shownPic = s, true, pic
		purge(s.PTS)
		resized := p.vs.setPicture(pic)
		if h := mediaFrameHook.Load(); h != nil {
			(*h)(p.node, s.PTS, time.Now())
		}
		if h := mediaLateHook.Load(); h != nil {
			(*h)((p.Clock() - s.PTS) * 1000)
		}
		p.tab.videoFrameReady(resized)
	}
	for !p.closed.Load() {
		vstore := p.src.Video()
		if vstore == nil || p.videoErr.Load() || p.hold.Load() {
			nap(50 * time.Millisecond)
			continue
		}
		t, playing, _ := p.State()
		want, ok := vstore.ShownAt(t)
		if !ok {
			// just before the first picture (a stream that starts a little after 0): show it
			if f, ok2 := vstore.FirstAfter(t); ok2 && f.PTS-t < 0.2 && !haveShown {
				want, ok = f, true
			}
		}
		if !ok {
			nap(5 * time.Millisecond)
			continue
		}
		forced := p.force.Swap(false)

		if playing && !forced && haveShown && haveDec {
			// steady playback
			best := -1
			for i, e := range ready {
				if e.s.PTS <= t+1e-9 && e.s.PTS > shown.PTS && (best < 0 || e.s.PTS > ready[best].s.PTS) {
					best = i
				}
			}
			if best >= 0 {
				e := ready[best]
				ready = append(ready[:best], ready[best+1:]...)
				present(vstore, e.s, e.pic, playing)
				continue
			}
			nxt, ok2, gap := vstore.Next(decDTS)
			stepOK := ok2 && !gap && sameCfg(cfgOf(vstore, nxt), decCfg)
			if want.DTS != shown.DTS && stepOK && want.DTS > decDTS {
				lag := t - nxt.PTS
				if lag > resyncLag {
					if k, ok3 := lastKeyDue(vstore, nxt, t); ok3 && k.DTS != nxt.DTS {
						dec.Reset() // behind: continue from the newest key frame that is already due
						ready = ready[:0]
						nxt = k
					} else if lag > resyncPanic {
						if k, ok3 := nextKeyFrom(vstore, nxt, t); ok3 && k.DTS != nxt.DTS && k.PTS-t <= resyncWait {
							dec.Reset() // far behind and no key frame due: wait for the next one
							ready = ready[:0]
							nxt = k
						}
					}
				}
				data, err := nxt.Bytes()
				if errors.Is(err, media.ErrNoKey) {
					p.waitKey.Store(true)
					nap(25 * time.Millisecond)
					continue
				}
				if err == nil && nxt.Encrypted() {
					p.waitKey.Store(false)
				}
				if err == nil && lag > 0 && nxt.DTS != want.DTS && dec.Disposable(data) {
					decDTS = nxt.DTS // late and nothing refers to it: not decoded, counted as dropped when the next picture shows
					continue
				}
				var pc media.Picture
				if err == nil {
					pc, err = dec.Decode(data)
				}
				decDTS = nxt.DTS
				if err != nil {
					p.corrupted.Add(1)
				} else if pc.Valid() && nxt.PTS > shown.PTS {
					ready = append(ready, aheadPic{nxt, pc})
				}
				continue
			}
			if want.DTS == shown.DTS {
				// nothing new is due: decode ahead
				if len(ready) < aheadMax && stepOK && nxt.PTS-t < aheadWindow.Seconds()*p.rate {
					data, err := nxt.Bytes()
					if errors.Is(err, media.ErrNoKey) {
						p.waitKey.Store(true)
						nap(25 * time.Millisecond)
						continue
					}
					var pc media.Picture
					if err == nil {
						pc, err = dec.Decode(data)
					}
					decDTS = nxt.DTS
					if err != nil {
						p.corrupted.Add(1)
					} else if pc.Valid() && nxt.PTS > shown.PTS {
						ready = append(ready, aheadPic{nxt, pc})
					}
					continue
				}
				nap(3 * time.Millisecond)
				continue
			}
			// anything else (a gap, a configuration change, a jump backwards) takes the full path below
		} else if haveShown && want.DTS == shown.DTS && !forced {
			nap(60 * time.Millisecond)
			continue
		}

		key, hasKey := vstore.KeyAtOrBefore(want.DTS)
		if !hasKey {
			nap(10 * time.Millisecond)
			continue
		}
		cfg := cfgOf(vstore, want)
		cfgChanged := dec == nil || decCfg == nil || !sameCfg(decCfg, cfg)
		var pic media.Picture
		failed := false
		got := false
		if haveShown && want.DTS == shown.DTS {
			pic, got = shownPic, true // shown again (forced): no decoding
		} else {
			for i, e := range ready {
				if e.s.DTS == want.DTS {
					pic, got = e.pic, true
					ready = append(ready[:i], ready[i+1:]...)
					break
				}
			}
		}
		if !got {
			from := key
			cont := false
			if haveDec && !cfgChanged && want.DTS > decDTS+1e-9 {
				if nxt, ok2, gap := vstore.Next(decDTS); ok2 && !gap && (key.DTS <= decDTS+1e-9 || key.DTS-decDTS < lateJumpSecs) {
					from, cont = nxt, true
				}
			}
			if !cont {
				ready = ready[:0]
				if cfgChanged {
					d, err := media.NewVideoDecoder(*cfg)
					if err != nil {
						p.videoErr.Store(true)
						continue
					}
					dec, decCfg = d, cfg
				} else {
					dec.Reset()
				}
				haveDec = true
			}
			s := from
			for {
				data, err := s.Bytes()
				if errors.Is(err, media.ErrNoKey) { // the key arrives with the page's license: hold here
					p.waitKey.Store(true)
					nap(25 * time.Millisecond)
					if p.closed.Load() {
						break
					}
					continue
				}
				if err == nil && s.Encrypted() {
					p.waitKey.Store(false)
				}
				var pc media.Picture
				if err == nil {
					pc, err = dec.Decode(data)
				}
				if err != nil {
					p.corrupted.Add(1)
					if s.DTS == want.DTS {
						failed = true
					}
				} else if s.DTS == want.DTS {
					pic = pc
				} else if pc.Valid() && s.PTS > want.PTS {
					ready = append(ready, aheadPic{s, pc}) // displays after the wanted picture (B-frame reordering)
				}
				decDTS = s.DTS
				if s.DTS >= want.DTS-1e-9 || p.closed.Load() {
					break
				}
				nx, ok2, gap := vstore.Next(s.DTS)
				if !ok2 || gap {
					break
				}
				s = nx
			}
		}
		if p.closed.Load() {
			return
		}
		if pic.Valid() && !failed {
			present(vstore, want, pic, playing)
		} else {
			purge(want.PTS)
			shown, haveShown = want, true
		}
	}
}

// nextKeyFrom returns the first sync sample at or after the clock (a little before it counts), searching on
// from from in decode order.
func nextKeyFrom(vs *media.TrackStore, from media.Sample, clock float64) (media.Sample, bool) {
	s := from
	for i := 0; i < 2000; i++ {
		if s.Key && s.PTS >= clock-0.05 {
			return s, true
		}
		nx, ok, gap := vs.Next(s.DTS)
		if !ok || gap {
			return media.Sample{}, false
		}
		s = nx
	}
	return media.Sample{}, false
}

// lastKeyDue returns the newest sync sample, at or after from in decode order, whose presentation time has
// already come (it is not after the clock).
func lastKeyDue(vs *media.TrackStore, from media.Sample, clock float64) (media.Sample, bool) {
	var best media.Sample
	found := false
	s := from
	for i := 0; i < 4000; i++ {
		if s.Key && s.PTS <= clock {
			best, found = s, true
		}
		if s.PTS > clock+1 {
			break
		}
		nx, ok, gap := vs.Next(s.DTS)
		if !ok || gap {
			break
		}
		s = nx
	}
	return best, found
}
