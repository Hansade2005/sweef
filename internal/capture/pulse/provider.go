package pulse

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// Provider is the capture.Provider for microphones on a PulseAudio or PipeWire-pulse server.
type Provider struct {
	opts Options
	// TTL is how long a source list is reused (default 1 s). The server is queried at most once per TTL however
	// often the engine asks; a failed query is remembered three times as long so that a machine with no sound
	// server does not pay a connection attempt per call.
	TTL time.Duration

	mu      sync.Mutex
	at      time.Time
	list    []Source
	defName string
	failed  bool
}

// NewProvider returns a provider using the environment's server settings.
func NewProvider() *Provider { return &Provider{} }

// NewProviderWith is NewProvider with explicit options (tests, tools).
func NewProviderWith(o Options) *Provider { return &Provider{opts: o} }

func (p *Provider) Name() string { return "pulse" }

// refresh returns the cached source list, re-reading it from the server when stale.
func (p *Provider) refresh(force bool) ([]Source, string) {
	ttl := p.TTL
	if ttl == 0 {
		ttl = time.Second
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	age := time.Since(p.at)
	if !force && !p.at.IsZero() && ((!p.failed && age < ttl) || (p.failed && age < 3*ttl)) {
		return p.list, p.defName
	}
	p.at = time.Now()
	c, err := Dial(p.opts)
	if err != nil {
		p.list, p.defName, p.failed = nil, "", true
		return nil, ""
	}
	defer c.Close()
	srcs, err := c.Sources()
	if err != nil {
		p.list, p.defName, p.failed = nil, "", true
		return nil, ""
	}
	def, _ := c.DefaultSource()
	p.list, p.defName, p.failed = srcs, def, false
	return p.list, p.defName
}

// Refresh forces the next Devices call to ask the server again.
func (p *Provider) Refresh() { p.refresh(true) }

// inputs returns the real input devices: monitors are hidden, the default source comes first.
func (p *Provider) inputs() []Source {
	srcs, def := p.refresh(false)
	var out []Source
	for _, s := range srcs {
		if !s.Monitor {
			out = append(out, s)
		}
	}
	for i, s := range out {
		if s.Name == def && i > 0 {
			copy(out[1:i+1], out[:i])
			out[0] = s
			break
		}
	}
	return out
}

func label(s Source) string {
	if s.Description != "" {
		return s.Description
	}
	return s.Name
}

func (p *Provider) Devices() []capture.DeviceInfo {
	var out []capture.DeviceInfo
	for _, s := range p.inputs() {
		out = append(out, capture.DeviceInfo{ID: s.Name, GroupID: s.Group, Label: label(s), Kind: capture.AudioInput})
	}
	return out
}

func (p *Provider) find(id string) (Source, bool) {
	for _, s := range p.inputs() {
		if s.Name == id {
			return s, true
		}
	}
	return Source{}, false
}

// AudioCaps implements capture.CapsProvider: the source's own rate, mono or stereo.
func (p *Provider) AudioCaps(id string) (capture.AudioCaps, bool) {
	s, ok := p.find(id)
	if !ok {
		return capture.AudioCaps{}, false
	}
	return capture.AudioCaps{Rates: []int{int(s.Rate)}, MinChannels: 1, MaxChannels: max(1, min(s.Channels, 2)), DefaultRate: int(s.Rate), DefaultChanns: 1}, true
}

func (p *Provider) VideoCaps(string) (capture.VideoCaps, bool) { return capture.VideoCaps{}, false }

func (p *Provider) OpenVideo(string) (capture.VideoSource, error) { return nil, capture.ErrUnsupported }

func (p *Provider) OpenAudio(id string) (capture.AudioSource, error) {
	s, ok := p.find(id)
	if !ok {
		return nil, capture.ErrNotFound
	}
	return &MicSource{opts: p.opts, src: s, info: capture.DeviceInfo{ID: s.Name, GroupID: s.Group, Label: label(s), Kind: capture.AudioInput}}, nil
}

// MicSource records from one source with its own connection.
type MicSource struct {
	opts Options
	src  Source
	info capture.DeviceInfo

	mu      sync.Mutex
	started bool
	conn    *Conn
	stream  *recStream
	stop    chan struct{}
	done    chan struct{}
	err     error
}

func (m *MicSource) Info() capture.DeviceInfo { return m.info }

func (m *MicSource) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// chunkMs is the length of the chunks delivered to consumers: one Opus frame.
const chunkMs = 10

// Start connects and creates the record stream. The audio is signed 16-bit PCM at the source's own rate; the
// channel count is c.Channels (1 or 2, default 1) and the server remixes if the source has more.
func (m *MicSource) Start(c capture.AudioConstraints) (<-chan *capture.AudioChunk, error) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil, errors.New("pulse: source already started")
	}
	m.started = true
	m.mu.Unlock()

	conn, err := Dial(m.opts)
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return nil, capture.ErrNotFound
		}
		return nil, err
	}
	chans := c.Channels
	if chans < 1 {
		chans = 1
	}
	chans = min(chans, 2, max(1, m.src.Channels))
	rate := m.src.Rate
	if rate == 0 {
		rate = 48000
	}
	frag := uint32(int(rate) * chans * 2 * chunkMs * 2 / 1000) // 20 ms: the server's fragments hold two of our chunks
	st, err := conn.openRecord(m.src.Name, rate, chans, frag)
	if err != nil {
		conn.Close()
		var se *ServerError
		if errors.As(err, &se) {
			switch se.Code {
			case errNoEntity:
				return nil, fmt.Errorf("%w: %v", capture.ErrNotFound, err)
			case errBusy:
				return nil, fmt.Errorf("%w: %v", capture.ErrInUse, err)
			}
		}
		return nil, err
	}
	m.mu.Lock()
	m.conn, m.stream = conn, st
	m.stop, m.done = make(chan struct{}), make(chan struct{})
	stop, done := m.stop, m.done
	m.mu.Unlock()
	out := make(chan *capture.AudioChunk, 16)
	go m.run(st, out, stop, done)
	return out, nil
}

func (m *MicSource) run(st *recStream, out chan *capture.AudioChunk, stop, done chan struct{}) {
	defer close(done)
	defer close(out)
	defer func() {
		st.close()
		m.conn.Close()
	}()
	frameBytes := 2 * st.chans
	chunkBytes := int(st.rate) / (1000 / chunkMs) * frameBytes
	var buf []byte
	var n int
	emit := func(raw []byte) bool {
		pcm := make([]int16, len(raw)/2)
		for i := range pcm {
			pcm[i] = int16(uint16(raw[2*i]) | uint16(raw[2*i+1])<<8)
		}
		c := &capture.AudioChunk{Rate: int(st.rate), Channels: st.chans, PCM: pcm,
			PTS: time.Duration(n) * chunkMs * time.Millisecond}
		n++
		select {
		case out <- c:
		default:
			select { // never wait for the consumer: drop the oldest chunk
			case <-out:
			default:
			}
			select {
			case out <- c:
			default:
			}
		}
		return true
	}
	for {
		select {
		case <-stop:
			return
		case <-st.done:
			m.fail(st.Err())
			return
		case p := <-st.data:
			buf = append(buf, p...)
			for len(buf) >= chunkBytes {
				emit(buf[:chunkBytes])
				buf = buf[chunkBytes:]
			}
			if len(buf) == 0 {
				buf = nil
			}
		}
	}
}

func (m *MicSource) fail(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	if m.err == nil {
		m.err = fmt.Errorf("%w: %v", capture.ErrNotFound, err)
	}
	m.mu.Unlock()
}

// Stop ends the stream and the connection.
func (m *MicSource) Stop() error {
	m.mu.Lock()
	stop, done := m.stop, m.done
	m.stop = nil
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
	return nil
}
