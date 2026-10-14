package pulse

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// fakeServer is an in-process PulseAudio native-protocol server for a version-13 client. It parses requests
// strictly in the order the real server reads them, so a client that sends a wrong layout fails here.
type fakeServer struct {
	t       *testing.T
	l       net.Listener
	path    string
	cookie  []byte
	mu      sync.Mutex
	conns   []net.Conn
	sources []fakeSource
	defName string
	version uint32
	streams int // record streams created
	deleted int // record streams deleted
	rate    uint32
	pktSize int // bytes per data packet (deliberately not sample aligned to 10 ms)

	// playback
	play fakePlay
}

// fakePlay is what the fake server saw of playback streams.
type fakePlay struct {
	created, deleted, flushes, drains int
	corked                            bool
	volume                            uint32
	tlen                              uint32
	playData                          []byte
	killOnWrite                       bool
	sinkUsec                          uint64
}

type fakeSource struct {
	name, desc    string
	rate          uint32
	ch            byte
	monitorOfSink uint32
}

func newFake(t *testing.T) *fakeServer {
	t.Helper()
	dir, err := shortTempDir(t)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{t: t, path: filepath.Join(dir, "native"), cookie: make([]byte, 256), version: 35, pktSize: 700,
		sources: []fakeSource{
			{"alsa_output.pci.analog-stereo.monitor", "Monitor of Built-in Audio", 44100, 2, 0},
			{"alsa_input.usb-cam.mono", "USB Camera Microphone", 48000, 1, invalidIndex},
			{"alsa_input.pci.analog-stereo", "Built-in Audio Analog Stereo", 44100, 2, invalidIndex},
		}, defName: "alsa_input.pci.analog-stereo"}
	l, err := net.Listen("unix", f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.l = l
	go f.accept()
	t.Cleanup(f.close)
	return f
}

func (f *fakeServer) close() {
	f.l.Close()
	f.mu.Lock()
	for _, c := range f.conns {
		c.Close()
	}
	f.mu.Unlock()
}

func (f *fakeServer) opts() Options {
	return Options{Getenv: func(k string) string {
		if k == "PULSE_SERVER" {
			return "unix:" + f.path
		}
		return ""
	}}
}

func (f *fakeServer) accept() {
	for {
		c, err := f.l.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		go f.serve(c)
	}
}

// killAll drops every client connection (the server crashed or was stopped).
func (f *fakeServer) killAll() {
	f.mu.Lock()
	for _, c := range f.conns {
		c.Close()
	}
	f.mu.Unlock()
}

type fconn struct {
	c  net.Conn
	mu sync.Mutex
}

func (c *fconn) send(ch uint32, payload []byte) {
	buf := make([]byte, 20+len(payload))
	putDescriptor(buf, uint32(len(payload)), ch)
	copy(buf[20:], payload)
	c.mu.Lock()
	c.c.Write(buf)
	c.mu.Unlock()
}

func okReply(tag uint32, body func(w *tagWriter)) []byte {
	var w tagWriter
	w.u32(cmdReply)
	w.u32(tag)
	if body != nil {
		body(&w)
	}
	return w.b
}

func errReply(tag, code uint32) []byte {
	var w tagWriter
	w.u32(cmdError)
	w.u32(tag)
	w.u32(code)
	return w.b
}

func (f *fakeServer) serve(nc net.Conn) {
	c := &fconn{c: nc}
	var authed bool
	var stop chan struct{}
	defer func() {
		if stop != nil {
			close(stop)
		}
	}()
	for {
		ch, p, err := readPacket(nc)
		if err != nil {
			return
		}
		if ch != controlChan {
			// stream data: keep it, and ask for as much again so the stream never starves
			f.mu.Lock()
			f.play.playData = append(f.play.playData, p...)
			kill := f.play.killOnWrite
			f.mu.Unlock()
			if kill {
				var w tagWriter
				w.u32(cmdPlaybackStreamKilled)
				w.u32(0xFFFFFFFF)
				w.u32(ch)
				c.send(controlChan, w.b)
				continue
			}
			var w tagWriter
			w.u32(cmdRequest)
			w.u32(0xFFFFFFFF)
			w.u32(ch)
			w.u32(uint32(len(p)))
			c.send(controlChan, w.b)
			continue
		}
		r := &tagReader{b: p}
		cmd, tag := r.u32(), r.u32()
		if cmd != cmdAuth && !authed {
			c.send(controlChan, errReply(tag, 1))
			continue
		}
		switch cmd {
		case cmdAuth:
			ver := r.u32()
			if !r.tag(tagArbitrary) {
				return
			}
			n := binary.BigEndian.Uint32(r.b)
			ck := r.b[4 : 4+n]
			if ver&0xFFFF != protocolVersion || string(ck) != string(f.cookie) {
				c.send(controlChan, errReply(tag, 1)) // access denied
				continue
			}
			authed = true
			c.send(controlChan, okReply(tag, func(w *tagWriter) { w.u32(f.version) }))
		case cmdSetClientName:
			props := r.proplist()
			if props["application.name"] == "" || r.err != nil || len(r.b) != 0 {
				c.send(controlChan, errReply(tag, 3))
				continue
			}
			c.send(controlChan, okReply(tag, func(w *tagWriter) { w.u32(7) }))
		case cmdGetServerInfo:
			c.send(controlChan, okReply(tag, func(w *tagWriter) {
				w.str("pulseaudio")
				w.str("16.1")
				w.str("user")
				w.str("host")
				w.sampleSpec(sampleS16LE, 2, 44100)
				w.str("alsa_output.pci.analog-stereo")
				w.str(f.defName)
				w.u32(0x1234)
				w.channelMap([]byte{1, 2}) // v15 field: a v13 client must not get it, but tolerates it
			}))
		case cmdGetSourceInfoList:
			c.send(controlChan, okReply(tag, func(w *tagWriter) {
				for i, s := range f.sources {
					w.u32(uint32(i))
					w.str(s.name)
					w.str(s.desc)
					w.sampleSpec(sampleS16LE, s.ch, s.rate)
					if s.ch == 1 {
						w.channelMap([]byte{0})
					} else {
						w.channelMap([]byte{1, 2})
					}
					w.u32(invalidIndex) // module
					w.b = append(w.b, tagCVolume, s.ch)
					for k := 0; k < int(s.ch); k++ {
						w.b = append(w.b, 0, 1, 0, 0)
					}
					w.boolean(false)
					w.u32(s.monitorOfSink)
					if s.monitorOfSink != invalidIndex {
						w.str("alsa_output.pci.analog-stereo")
					} else {
						w.nullStr()
					}
					w.b = append(w.b, tagUsec, 0, 0, 0, 0, 0, 0, 0, 0)
					w.str("module-alsa-card.c")
					w.u32(0)
					w.proplist("device.description", s.desc, "device.form_factor", "microphone")
					w.b = append(w.b, tagUsec, 0, 0, 0, 0, 0, 0, 0, 0)
				}
			}))
		case cmdCreateRecordStream:
			rate, chans, source, ok := f.parseCreate(r)
			if !ok {
				c.send(controlChan, errReply(tag, 3))
				continue
			}
			found := false
			for _, s := range f.sources {
				found = found || s.name == source
			}
			if !found {
				c.send(controlChan, errReply(tag, errNoEntity))
				continue
			}
			f.mu.Lock()
			f.streams++
			chn := uint32(f.streams)
			f.mu.Unlock()
			c.send(controlChan, okReply(tag, func(w *tagWriter) {
				w.u32(chn)
				w.u32(100 + chn)
				w.u32(0x100000)
				w.u32(3528)
				w.sampleSpec(sampleS16LE, byte(chans), rate)
				cm := []byte{0}
				if chans == 2 {
					cm = []byte{1, 2}
				}
				w.channelMap(cm)
				w.u32(1)
				w.str(source)
				w.boolean(false)
				w.b = append(w.b, tagUsec, 0, 0, 0, 0, 0, 0, 0, 0)
			}))
			stop = make(chan struct{})
			go f.feed(c, chn, rate, chans, stop)
		case cmdCreatePlaybackStream:
			f.handleCreatePlayback(c, r, tag)
		case cmdDeletePlaybackStream:
			f.mu.Lock()
			f.play.deleted++
			f.mu.Unlock()
			c.send(controlChan, okReply(tag, nil))
		case cmdCorkPlaybackStream:
			r.u32()
			cork := r.boolean()
			f.mu.Lock()
			f.play.corked = cork
			f.mu.Unlock()
			c.send(controlChan, okReply(tag, nil))
		case cmdFlushPlaybackStream:
			f.mu.Lock()
			f.play.flushes++
			f.mu.Unlock()
			c.send(controlChan, okReply(tag, nil))
		case cmdDrainPlaybackStream:
			f.mu.Lock()
			f.play.drains++
			f.mu.Unlock()
			c.send(controlChan, okReply(tag, nil))
		case cmdSetSinkInputVolume:
			idx := r.u32()
			if !r.tag(tagCVolume) || len(r.b) < 5 || idx != 4242 {
				c.send(controlChan, errReply(tag, 3))
				continue
			}
			f.mu.Lock()
			f.play.volume = binary.BigEndian.Uint32(r.b[1:])
			f.mu.Unlock()
			c.send(controlChan, okReply(tag, nil))
		case cmdGetPlaybackLatency:
			chn := r.u32()
			r.timeval()
			if r.err != nil || len(r.b) != 0 {
				f.t.Errorf("GET_PLAYBACK_LATENCY layout: err=%v leftover=%d", r.err, len(r.b))
				c.send(controlChan, errReply(tag, 3))
				continue
			}
			_ = chn
			f.mu.Lock()
			written := int64(len(f.play.playData))
			usec := f.play.sinkUsec
			f.mu.Unlock()
			c.send(controlChan, okReply(tag, func(w *tagWriter) {
				w.b = append(w.b, tagUsec)
				w.b = binary.BigEndian.AppendUint64(w.b, usec)     // sink latency
				w.b = append(w.b, tagUsec, 0, 0, 0, 0, 0, 0, 0, 0) // source latency
				w.boolean(true)
				w.timeval(1, 2)
				w.timeval(3, 4)
				w.b = append(w.b, tagS64)
				w.b = binary.BigEndian.AppendUint64(w.b, uint64(written))
				w.b = append(w.b, tagS64)
				w.b = binary.BigEndian.AppendUint64(w.b, uint64(written/2)) // read index: half of it is still queued
				w.b = append(w.b, tagU64, 0, 0, 0, 0, 0, 0, 0, 0, tagU64, 0, 0, 0, 0, 0, 0, 0, 0)
			}))
		case cmdDeleteRecordStream:
			f.mu.Lock()
			f.deleted++
			f.mu.Unlock()
			if stop != nil {
				close(stop)
				stop = nil
			}
			c.send(controlChan, okReply(tag, nil))
		default:
			c.send(controlChan, errReply(tag, 2))
		}
	}
}

// parseCreate reads CREATE_RECORD_STREAM the way a version-13 server does and fails on any deviation.
func (f *fakeServer) parseCreate(r *tagReader) (rate uint32, chans int, source string, ok bool) {
	ss := r.sampleSpec()
	cm := r.channelMap()
	idx := r.u32()
	source = r.str()
	r.u32() // maxlength
	r.boolean()
	frag := r.u32()
	for i := 0; i < 7; i++ {
		r.boolean()
	}
	r.boolean() // peak detect
	r.boolean() // adjust latency
	props := r.proplist()
	r.u32() // direct_on_input
	if r.err != nil || len(r.b) != 0 {
		f.t.Errorf("CREATE_RECORD_STREAM layout: err=%v leftover=%d bytes", r.err, len(r.b))
		return 0, 0, "", false
	}
	if ss.Format != sampleS16LE || len(cm) != int(ss.Channels) || idx != invalidIndex || frag == 0 || props["media.name"] == "" {
		f.t.Errorf("CREATE_RECORD_STREAM contents: spec=%+v map=%v idx=%x frag=%d props=%v", ss, cm, idx, frag, props)
		return 0, 0, "", false
	}
	return ss.Rate, int(ss.Channels), source, true
}

// feed sends a 440 Hz tone in packets that do not line up with 10 ms chunks.
func (f *fakeServer) feed(c *fconn, chn, rate uint32, chans int, stop chan struct{}) {
	n := 0
	tk := time.NewTicker(5 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
		}
		buf := make([]byte, f.pktSize*chans/chans)
		buf = buf[:f.pktSize/(2*chans)*(2*chans)]
		for i := 0; i < len(buf)/(2*chans); i++ {
			v := int16(8000 * math.Sin(2*math.Pi*440*float64(n)/float64(rate)))
			n++
			for k := 0; k < chans; k++ {
				o := (i*chans + k) * 2
				buf[o], buf[o+1] = byte(v), byte(uint16(v)>>8)
			}
		}
		c.send(chn, buf)
	}
}

func provider(f *fakeServer) *Provider {
	p := NewProviderWith(f.opts())
	p.TTL = time.Millisecond
	return p
}

func TestDevicesHideMonitorsDefaultFirst(t *testing.T) {
	f := newFake(t)
	devs := provider(f).Devices()
	if len(devs) != 2 {
		t.Fatalf("devices %+v", devs)
	}
	if devs[0].ID != "alsa_input.pci.analog-stereo" || devs[0].Label != "Built-in Audio Analog Stereo" || devs[0].Kind != capture.AudioInput {
		t.Errorf("default must come first: %+v", devs[0])
	}
	if devs[1].ID != "alsa_input.usb-cam.mono" || devs[1].Label != "USB Camera Microphone" {
		t.Errorf("second: %+v", devs[1])
	}
	for _, d := range devs {
		if d.ID == "alsa_output.pci.analog-stereo.monitor" {
			t.Error("monitor source exposed")
		}
	}
}

func TestCaps(t *testing.T) {
	f := newFake(t)
	p := provider(f)
	c, ok := p.AudioCaps("alsa_input.pci.analog-stereo")
	if !ok || c.Rates[0] != 44100 || c.MaxChannels != 2 || c.DefaultChanns != 1 {
		t.Fatalf("%+v", c)
	}
	c, _ = p.AudioCaps("alsa_input.usb-cam.mono")
	if c.Rates[0] != 48000 || c.MaxChannels != 1 {
		t.Fatalf("%+v", c)
	}
	if _, ok := p.AudioCaps("nope"); ok {
		t.Fatal("caps for unknown device")
	}
}

func record(t *testing.T, p *Provider, id string, ac capture.AudioConstraints, n int) (chunks []*capture.AudioChunk, src capture.AudioSource) {
	t.Helper()
	src, err := p.OpenAudio(id)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := src.Start(ac)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for len(chunks) < n {
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatalf("closed after %d chunks: %v", len(chunks), src.Err())
			}
			chunks = append(chunks, c)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d chunks", len(chunks))
		}
	}
	return chunks, src
}

func TestRecordMonoRechunksTo10ms(t *testing.T) {
	f := newFake(t)
	chunks, src := record(t, provider(f), "alsa_input.usb-cam.mono", capture.AudioConstraints{}, 30)
	defer src.Stop()
	var all []int16
	for i, c := range chunks {
		if c.Rate != 48000 || c.Channels != 1 || len(c.PCM) != 480 || c.PTS != time.Duration(i)*10*time.Millisecond {
			t.Fatalf("chunk %d: rate=%d ch=%d n=%d pts=%v", i, c.Rate, c.Channels, len(c.PCM), c.PTS)
		}
		all = append(all, c.PCM...)
	}
	// 30 chunks = 300 ms of 440 Hz = 132 cycles, sample-accurate across the odd 350-sample packets
	for i, v := range all {
		want := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/48000))
		if d := int(v) - int(want); d < -1 || d > 1 {
			t.Fatalf("sample %d = %d, want %d: the stream was re-chunked wrongly", i, v, want)
		}
	}
}

func TestRecordStereoAtSourceRate(t *testing.T) {
	f := newFake(t)
	chunks, src := record(t, provider(f), "alsa_input.pci.analog-stereo", capture.AudioConstraints{Channels: 2}, 5)
	defer src.Stop()
	c := chunks[0]
	if c.Rate != 44100 || c.Channels != 2 || len(c.PCM) != 441*2 {
		t.Fatalf("rate=%d ch=%d n=%d", c.Rate, c.Channels, len(c.PCM))
	}
	// requesting 2 channels from a mono source gives mono; 6 from a stereo source gives stereo
	chunks, src2 := record(t, provider(f), "alsa_input.usb-cam.mono", capture.AudioConstraints{Channels: 2}, 1)
	defer src2.Stop()
	if chunks[0].Channels != 1 {
		t.Fatalf("mono source delivered %d channels", chunks[0].Channels)
	}
}

func TestStopDeletesStreamAndClosesConnection(t *testing.T) {
	f := newFake(t)
	_, src := record(t, provider(f), "alsa_input.usb-cam.mono", capture.AudioConstraints{}, 2)
	start := time.Now()
	src.Stop()
	if time.Since(start) > time.Second {
		t.Fatalf("Stop took %v", time.Since(start))
	}
	if src.Err() != nil {
		t.Fatalf("clean stop: %v", src.Err())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleted != 1 {
		t.Fatalf("server saw %d DELETE_RECORD_STREAM", f.deleted)
	}
}

func TestServerGoneEndsStream(t *testing.T) {
	f := newFake(t)
	p := provider(f)
	src, _ := p.OpenAudio("alsa_input.usb-cam.mono")
	ch, err := src.Start(capture.AudioConstraints{})
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	f.killAll()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if ok {
				continue
			}
			if err := src.Err(); !errors.Is(err, capture.ErrNotFound) {
				t.Fatalf("Err = %v, want ErrNotFound", err)
			}
			return
		case <-deadline:
			t.Fatal("stream did not end when the server went away")
		}
	}
}

func TestRecordStreamKilled(t *testing.T) {
	f := newFake(t)
	src, _ := provider(f).OpenAudio("alsa_input.usb-cam.mono")
	ch, _ := src.Start(capture.AudioConstraints{})
	<-ch
	f.mu.Lock()
	conn := f.conns[len(f.conns)-1]
	f.mu.Unlock()
	var w tagWriter
	w.u32(cmdRecordStreamKilled)
	w.u32(invalidIndex)
	w.u32(1) // channel of the stream
	(&fconn{c: conn}).send(controlChan, w.b)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if src.Err() == nil {
					t.Fatal("killed stream reported no error")
				}
				return
			}
		case <-deadline:
			t.Fatal("RECORD_STREAM_KILLED ignored")
		}
	}
}

func TestUnknownSourceAndAuth(t *testing.T) {
	f := newFake(t)
	bad := &MicSource{opts: f.opts(), src: Source{Name: "ghost", Rate: 48000, Channels: 1}}
	if _, err := bad.Start(capture.AudioConstraints{}); !errors.Is(err, capture.ErrNotFound) {
		t.Fatalf("unknown source: %v", err)
	}
	// the server wants a real cookie; a client without one is refused
	for i := range f.cookie {
		f.cookie[i] = byte(i*7 + 3)
	}
	if _, err := Dial(f.opts()); err == nil {
		t.Fatal("authentication with a wrong cookie succeeded")
	} else {
		var se *ServerError
		if !errors.As(err, &se) || se.Code != 1 {
			t.Fatalf("auth error: %v", err)
		}
	}
	// ...and accepted with the cookie from PULSE_COOKIE
	dir := t.TempDir()
	cf := filepath.Join(dir, "cookie")
	if err := writeFile(cf, f.cookie); err != nil {
		t.Fatal(err)
	}
	o := f.opts()
	base := o.Getenv
	o.Getenv = func(k string) string {
		if k == "PULSE_COOKIE" {
			return cf
		}
		return base(k)
	}
	c, err := Dial(o)
	if err != nil {
		t.Fatalf("with cookie file: %v", err)
	}
	c.Close()
}

func TestNoServerMeansNoMicrophone(t *testing.T) {
	p := NewProviderWith(Options{Getenv: func(k string) string {
		if k == "PULSE_SERVER" {
			return "unix:/nonexistent/pulse/native"
		}
		return ""
	}})
	if d := p.Devices(); len(d) != 0 {
		t.Fatalf("devices without a server: %+v", d)
	}
	if _, err := p.OpenAudio("x"); !errors.Is(err, capture.ErrNotFound) {
		t.Fatalf("OpenAudio: %v", err)
	}
	if _, err := Dial(p.opts); !errors.Is(err, ErrNoServer) {
		t.Fatalf("Dial: %v", err)
	}
	// a remote (tcp) server is not "the microphone"
	if got := serverPaths(func(k string) string {
		if k == "PULSE_SERVER" {
			return "tcp:10.0.0.2 {abcd}unix:/run/x/native"
		}
		return ""
	}, 1000); len(got) != 1 || got[0] != "/run/x/native" {
		t.Fatalf("paths %v", got)
	}
}

func TestServerPaths(t *testing.T) {
	env := map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}
	got := serverPaths(func(k string) string { return env[k] }, 1000)
	want := filepath.Join("/run/user/1000", "pulse", "native") // OS separators: the paths are joined with filepath
	if len(got) != 2 || got[0] != want || got[1] != "/run/user/1000/pulse/native" {
		t.Fatalf("%v", got)
	}
	env["PULSE_RUNTIME_PATH"] = "/custom"
	got = serverPaths(func(k string) string { return env[k] }, 1000)
	if got[0] != filepath.Join("/custom", "native") {
		t.Fatalf("%v", got)
	}
}

func TestTagStructRoundTrip(t *testing.T) {
	var w tagWriter
	w.u32(0xDEADBEEF)
	w.str("héllo")
	w.nullStr()
	w.boolean(true)
	w.boolean(false)
	w.sampleSpec(3, 2, 44100)
	w.channelMap([]byte{1, 2})
	w.proplist("a", "b", "c", "dd")
	r := &tagReader{b: w.b}
	if r.u32() != 0xDEADBEEF || r.str() != "héllo" || r.str() != "" || !r.boolean() || r.boolean() {
		t.Fatal("scalars")
	}
	if s := r.sampleSpec(); s.Format != 3 || s.Channels != 2 || s.Rate != 44100 {
		t.Fatalf("%+v", s)
	}
	if m := r.channelMap(); len(m) != 2 || m[1] != 2 {
		t.Fatal("map")
	}
	if p := r.proplist(); p["a"] != "b" || p["c"] != "dd" {
		t.Fatalf("props %v", p)
	}
	if r.err != nil || len(r.b) != 0 {
		t.Fatalf("err=%v left=%d", r.err, len(r.b))
	}
	// truncated input fails cleanly
	r = &tagReader{b: w.b[:7]}
	r.u32()
	r.str()
	if r.err == nil {
		t.Fatal("truncation not detected")
	}
	// the exact bytes of a u32: tag 'L' then big endian
	if got := (&tagWriter{}); true {
		got.u32(1)
		if string(got.b) != "L\x00\x00\x00\x01" {
			t.Fatalf("% x", got.b)
		}
	}
	// descriptor layout
	var d [20]byte
	putDescriptor(d[:], 7, controlChan)
	if binary.BigEndian.Uint32(d[0:]) != 7 || binary.BigEndian.Uint32(d[4:]) != 0xFFFFFFFF || d[19] != 0 {
		t.Fatalf("% x", d)
	}
	_ = io.EOF
}

func TestOpenCloseNoLeaks(t *testing.T) {
	f := newFake(t)
	p := provider(f)
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		src, err := p.OpenAudio("alsa_input.usb-cam.mono")
		if err != nil {
			t.Fatal(err)
		}
		ch, err := src.Start(capture.AudioConstraints{})
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		<-ch
		src.Stop()
	}
	f.mu.Lock()
	created, deleted := f.streams, f.deleted
	f.mu.Unlock()
	if created != 100 || deleted != 100 {
		t.Fatalf("streams created %d deleted %d", created, deleted)
	}
	for i := 0; i < 200 && runtime.NumGoroutine() > before+8; i++ { // fake-server goroutines of closed conns wind down
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+8 {
		t.Fatalf("goroutines %d -> %d", before, n)
	}
}

// shortTempDir returns a temp directory with a short path: unix socket paths are limited to about 104 bytes on
// macOS (and 108 on Linux), and t.TempDir() under macOS's /var/folders/... exceeds that.
func shortTempDir(t *testing.T) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake PulseAudio server uses unix sockets and Linux semantics")
	}
	dir, err := os.MkdirTemp("/tmp", "sw")
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.EvalSymlinks(dir)
}

// handleCreatePlayback parses CREATE_PLAYBACK_STREAM the way a version-13 server does, strictly.
func (f *fakeServer) handleCreatePlayback(c *fconn, r *tagReader, tag uint32) {
	ss := r.sampleSpec()
	cm := r.channelMap()
	sinkIdx := r.u32()
	sinkName := r.str()
	maxlen := r.u32()
	corked := r.boolean()
	tlen, prebuf, minreq, syncID := r.u32(), r.u32(), r.u32(), r.u32()
	if !r.tag(tagCVolume) || len(r.b) < 1 {
		f.t.Errorf("CREATE_PLAYBACK_STREAM: no volume (err=%v)", r.err)
		c.send(controlChan, errReply(tag, 3))
		return
	}
	n := int(r.b[0])
	r.b = r.b[1+4*n:]
	for i := 0; i < 7; i++ {
		r.boolean()
	}
	r.boolean() // muted
	adjust := r.boolean()
	props := r.proplist()
	if r.err != nil || len(r.b) != 0 {
		f.t.Errorf("CREATE_PLAYBACK_STREAM layout: err=%v leftover=%d bytes", r.err, len(r.b))
		c.send(controlChan, errReply(tag, 3))
		return
	}
	if ss.Format != sampleFloat32LE || len(cm) != int(ss.Channels) || sinkIdx != invalidIndex || sinkName != "" ||
		maxlen != invalidIndex || corked || tlen == 0 || prebuf != 0 || minreq == 0 || syncID != 0 || n != int(ss.Channels) ||
		!adjust || props["media.name"] == "" {
		f.t.Errorf("CREATE_PLAYBACK_STREAM contents: spec=%+v map=%v sink=%x/%q max=%x tlen=%d prebuf=%d minreq=%d adjust=%v props=%v",
			ss, cm, sinkIdx, sinkName, maxlen, tlen, prebuf, minreq, adjust, props)
		c.send(controlChan, errReply(tag, 3))
		return
	}
	f.mu.Lock()
	f.play.created++
	f.play.tlen = tlen
	f.mu.Unlock()
	chn := uint32(7)
	c.send(controlChan, okReply(tag, func(w *tagWriter) {
		w.u32(chn)
		w.u32(4242) // sink input index
		w.u32(tlen) // requested bytes
		w.u32(tlen * 4)
		w.u32(tlen)
		w.u32(0)
		w.u32(minreq)
		w.sampleSpec(sampleFloat32LE, byte(ss.Channels), ss.Rate)
		w.channelMap(cm)
		w.u32(1)
		w.str("alsa_output.pci.analog-stereo")
		w.boolean(false)
		w.b = append(w.b, tagUsec, 0, 0, 0, 0, 0, 0, 0x4e, 0x20) // 20 ms
	}))
}
