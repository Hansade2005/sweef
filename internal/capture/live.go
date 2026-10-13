package capture

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

// VideoMode is the mode a running camera or screen source really delivers.
type VideoMode struct {
	W, H int
	FPS  float64
}

// AudioMode is the format a running microphone really delivers.
type AudioMode struct {
	Rate, Channels int
}

var (
	liveMu      sync.Mutex
	videoShares = map[string]*shared[Frame, VideoConstraints]{}
	audioShares = map[string]*shared[AudioChunk, AudioConstraints]{}
)

// DefaultStartTimeout is how long getUserMedia waits for the first frame or chunk before failing the way browsers
// do ("Timeout starting video source").
const DefaultStartTimeout = 4 * time.Second

// VideoLease is one holder of a running (possibly shared) video source.
type VideoLease struct {
	key  string
	sh   *shared[Frame, VideoConstraints]
	once sync.Once
}

// AcquireVideo starts the camera with this id, or joins the stream another consumer already runs. The device is
// stopped when the last lease is released. If the device is already running in another mode the existing mode is
// kept (a camera can only run one); use Reopen to change it.
func AcquireVideo(id string, c VideoConstraints, timeout time.Duration) (*VideoLease, error) {
	return AcquireVideoFrom(id, func() (VideoSource, error) { return OpenVideo(id) }, c, timeout)
}

// AcquireVideoFrom is AcquireVideo for a source that is not in the global registry (the app's screen sources).
// Leases with the same key share one source.
func AcquireVideoFrom(key string, open func() (VideoSource, error), c VideoConstraints, timeout time.Duration) (*VideoLease, error) {
	if timeout <= 0 {
		timeout = DefaultStartTimeout
	}
	liveMu.Lock()
	sh := videoShares[key]
	creator := false
	if sh != nil && sh.isDead() {
		delete(videoShares, key)
		sh = nil
	}
	if sh == nil {
		var self *shared[Frame, VideoConstraints]
		self = newShared[Frame, VideoConstraints](func() (srcAPI[Frame, VideoConstraints], error) {
			s, err := open()
			if err != nil {
				return nil, err
			}
			return s, nil
		}, func() {
			liveMu.Lock()
			if videoShares[key] == self {
				delete(videoShares, key)
			}
			liveMu.Unlock()
		})
		self.mode = func(src srcAPI[Frame, VideoConstraints], f *Frame) any {
			if m, ok := src.(Moder); ok {
				if w, h, fps := m.Mode(); w > 0 && h > 0 {
					return VideoMode{W: w, H: h, FPS: fps}
				}
			}
			fps := c.FrameRate
			if fps <= 0 {
				fps = 30
			}
			return VideoMode{W: f.W, H: f.H, FPS: fps}
		}
		videoShares[key] = self
		sh, creator = self, true
	}
	sh.mu.Lock()
	sh.refs++
	sh.mu.Unlock()
	liveMu.Unlock()
	if creator {
		sh.begin(c)
	}
	l := &VideoLease{key: key, sh: sh}
	if err := sh.wait(timeout); err != nil {
		l.Release()
		return nil, err
	}
	return l, nil
}

// Subscribe returns a new frame stream. Frames are shared and must be treated as read-only.
func (l *VideoLease) Subscribe() *Sub[Frame] { return l.sh.subscribe(3) }

// Mode is the negotiated mode.
func (l *VideoLease) Mode() VideoMode {
	l.sh.mu.Lock()
	defer l.sh.mu.Unlock()
	m, _ := l.sh.modeVal.(VideoMode)
	return m
}

// Reopen restarts the device with new constraints; every lease and subscription keeps working.
func (l *VideoLease) Reopen(c VideoConstraints) error { return l.sh.reopen(c) }

// Ended is closed when the device stops for good (unplugged, taken by another application, last release).
func (l *VideoLease) Ended() <-chan struct{} { return l.sh.ended }

// Err is why the device stopped (nil for a clean stop). Valid after Ended is closed.
func (l *VideoLease) Err() error {
	l.sh.mu.Lock()
	defer l.sh.mu.Unlock()
	return l.sh.err
}

// Release gives the lease back (idempotent).
func (l *VideoLease) Release() { l.once.Do(l.sh.release) }

// AudioLease is VideoLease for microphones.
type AudioLease struct {
	sh   *shared[AudioChunk, AudioConstraints]
	once sync.Once
}

// AcquireAudio starts the microphone with this id or joins the running stream.
func AcquireAudio(id string, c AudioConstraints, timeout time.Duration) (*AudioLease, error) {
	return AcquireAudioFrom(id, func() (AudioSource, error) { return OpenAudio(id) }, c, timeout)
}

// AcquireAudioFrom is AcquireAudio for an unregistered source.
func AcquireAudioFrom(key string, open func() (AudioSource, error), c AudioConstraints, timeout time.Duration) (*AudioLease, error) {
	if timeout <= 0 {
		timeout = DefaultStartTimeout
	}
	liveMu.Lock()
	sh := audioShares[key]
	creator := false
	if sh != nil && sh.isDead() {
		delete(audioShares, key)
		sh = nil
	}
	if sh == nil {
		var self *shared[AudioChunk, AudioConstraints]
		self = newShared[AudioChunk, AudioConstraints](func() (srcAPI[AudioChunk, AudioConstraints], error) {
			s, err := open()
			if err != nil {
				return nil, err
			}
			return s, nil
		}, func() {
			liveMu.Lock()
			if audioShares[key] == self {
				delete(audioShares, key)
			}
			liveMu.Unlock()
		})
		self.mode = func(_ srcAPI[AudioChunk, AudioConstraints], a *AudioChunk) any {
			return AudioMode{Rate: a.Rate, Channels: a.Channels}
		}
		audioShares[key] = self
		sh, creator = self, true
	}
	sh.mu.Lock()
	sh.refs++
	sh.mu.Unlock()
	liveMu.Unlock()
	if creator {
		sh.begin(c)
	}
	l := &AudioLease{sh: sh}
	if err := sh.wait(timeout); err != nil {
		l.Release()
		return nil, err
	}
	return l, nil
}

func (l *AudioLease) Subscribe() *Sub[AudioChunk] { return l.sh.subscribe(16) }

func (l *AudioLease) Mode() AudioMode {
	l.sh.mu.Lock()
	defer l.sh.mu.Unlock()
	m, _ := l.sh.modeVal.(AudioMode)
	return m
}

func (l *AudioLease) Ended() <-chan struct{} { return l.sh.ended }

func (l *AudioLease) Err() error {
	l.sh.mu.Lock()
	defer l.sh.mu.Unlock()
	return l.sh.err
}

func (l *AudioLease) Release() { l.once.Do(l.sh.release) }

// ---- tracks ---------------------------------------------------------------------------------------------------

// End reasons of a Track.
const (
	EndStopped = "stopped" // track.stop() or the page went away
	EndDevice  = "device"  // the device vanished or failed
	EndUser    = "user"    // the user pressed Stop sharing / revoked the permission
)

// Track is one MediaStreamTrack's worth of live media: a lease on a source plus the enabled flag. The JS side
// holds its id; the WebRTC and recorder pipelines look it up with LookupTrack and Subscribe.
type Track struct {
	ID     string
	Kind   Kind // VideoInput, AudioInput, or Screen (a video track made from a display source)
	Device DeviceInfo
	// Surface is "browser" or "window" for display tracks.
	Surface string
	Origin  string

	enabled atomic.Bool
	vl      *VideoLease
	al      *AudioLease
	endOnce sync.Once
	ended   chan struct{}
	mu      sync.Mutex
	reason  string
	err     error
	onEnd   func(*Track)
}

var (
	trackMu sync.Mutex
	tracks  = map[string]*Track{}
)

// NewVideoTrack wraps a lease. onEnd (optional) runs once, after the track ended for any reason.
func NewVideoTrack(l *VideoLease, dev DeviceInfo, origin string, onEnd func(*Track)) *Track {
	t := newTrack(dev, origin, onEnd)
	t.vl = l
	if dev.Kind != Screen {
		t.Kind = VideoInput
	} else {
		t.Kind = Screen
	}
	go t.watch(l.Ended(), func() error { return l.Err() }, l.Release)
	register(t)
	return t
}

// NewAudioTrack wraps an audio lease.
func NewAudioTrack(l *AudioLease, dev DeviceInfo, origin string, onEnd func(*Track)) *Track {
	t := newTrack(dev, origin, onEnd)
	t.al = l
	t.Kind = AudioInput
	go t.watch(l.Ended(), func() error { return l.Err() }, l.Release)
	register(t)
	return t
}

func newTrack(dev DeviceInfo, origin string, onEnd func(*Track)) *Track {
	var b [16]byte
	_, _ = rand.Read(b[:])
	t := &Track{ID: hex.EncodeToString(b[:]), Device: dev, Origin: origin, ended: make(chan struct{}), onEnd: onEnd}
	t.enabled.Store(true)
	return t
}

func register(t *Track) {
	trackMu.Lock()
	tracks[t.ID] = t
	trackMu.Unlock()
}

// LookupTrack finds a live track by the id the page sees as MediaStreamTrack.id.
func LookupTrack(id string) *Track {
	trackMu.Lock()
	defer trackMu.Unlock()
	return tracks[id]
}

// LiveTracks counts tracks that have not ended (tests, the leak checks).
func LiveTracks() int {
	trackMu.Lock()
	defer trackMu.Unlock()
	return len(tracks)
}

func (t *Track) watch(src <-chan struct{}, errf func() error, release func()) {
	select {
	case <-src:
		if err := errf(); err != nil {
			t.End(EndDevice, err)
		} else {
			t.End(EndStopped, nil)
		}
	case <-t.ended:
	}
	release()
}

// End stops the track (idempotent); only the first reason counts.
func (t *Track) End(reason string, err error) {
	t.endOnce.Do(func() {
		t.mu.Lock()
		t.reason, t.err = reason, err
		t.mu.Unlock()
		close(t.ended)
		trackMu.Lock()
		delete(tracks, t.ID)
		trackMu.Unlock()
		if t.onEnd != nil {
			t.onEnd(t)
		}
	})
}

// Ended is closed once the track ended.
func (t *Track) Ended() <-chan struct{} { return t.ended }

// EndInfo is why the track ended ("" while live).
func (t *Track) EndInfo() (reason string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reason, t.err
}

// SetEnabled implements MediaStreamTrack.enabled: a disabled video track delivers black frames, a disabled audio
// track silence, at the normal rate.
func (t *Track) SetEnabled(on bool) { t.enabled.Store(on) }
func (t *Track) Enabled() bool      { return t.enabled.Load() }

// VideoMode and AudioMode are the real mode of the underlying source.
func (t *Track) VideoMode() VideoMode {
	if t.vl == nil {
		return VideoMode{}
	}
	return t.vl.Mode()
}
func (t *Track) AudioMode() AudioMode {
	if t.al == nil {
		return AudioMode{}
	}
	return t.al.Mode()
}

// Reopen changes the video mode of the shared source (applyConstraints).
func (t *Track) Reopen(c VideoConstraints) error {
	if t.vl == nil {
		return ErrUnsupported
	}
	return t.vl.Reopen(c)
}

// Clone makes another track on the same source.
func (t *Track) Clone() *Track {
	var c *Track
	switch {
	case t.vl != nil:
		t.vl.sh.mu.Lock()
		t.vl.sh.refs++
		t.vl.sh.mu.Unlock()
		l := &VideoLease{key: t.vl.key, sh: t.vl.sh}
		c = NewVideoTrack(l, t.Device, t.Origin, t.onEnd)
	case t.al != nil:
		t.al.sh.mu.Lock()
		t.al.sh.refs++
		t.al.sh.mu.Unlock()
		c = NewAudioTrack(&AudioLease{sh: t.al.sh}, t.Device, t.Origin, t.onEnd)
	default:
		return nil
	}
	c.Surface = t.Surface
	c.enabled.Store(t.enabled.Load())
	return c
}

// SubscribeVideo returns a stream of this track's frames. It honours Enabled by substituting black frames. cancel
// detaches the consumer. The channel closes when the track ends.
func (t *Track) SubscribeVideo() (<-chan *Frame, func()) {
	if t.vl == nil {
		ch := make(chan *Frame)
		close(ch)
		return ch, func() {}
	}
	sub := t.vl.Subscribe()
	out := make(chan *Frame, 3)
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(stop); sub.Cancel() }) }
	go func() {
		defer close(out)
		var black *Frame
		for {
			select {
			case f, ok := <-sub.C:
				if !ok {
					return
				}
				if !t.Enabled() {
					if black == nil || black.W != f.W || black.H != f.H {
						black = NewBlack(f.W, f.H)
					}
					b := *black
					b.PTS = f.PTS
					f = &b
				}
				select {
				case out <- f:
				default:
					select {
					case <-out:
					default:
					}
					select {
					case out <- f:
					default:
					}
				}
			case <-stop:
				return
			case <-t.ended:
				return
			}
		}
	}()
	return out, cancel
}

// SubscribeAudio is SubscribeVideo for microphones.
func (t *Track) SubscribeAudio() (<-chan *AudioChunk, func()) {
	if t.al == nil {
		ch := make(chan *AudioChunk)
		close(ch)
		return ch, func() {}
	}
	sub := t.al.Subscribe()
	out := make(chan *AudioChunk, 16)
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(stop); sub.Cancel() }) }
	go func() {
		defer close(out)
		for {
			select {
			case a, ok := <-sub.C:
				if !ok {
					return
				}
				if !t.Enabled() {
					z := *a
					z.PCM = make([]int16, len(a.PCM))
					a = &z
				}
				select {
				case out <- a:
				default:
					select {
					case <-out:
					default:
					}
					select {
					case out <- a:
					default:
					}
				}
			case <-stop:
				return
			case <-t.ended:
				return
			}
		}
	}()
	return out, cancel
}
