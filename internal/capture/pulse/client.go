package pulse

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNoServer is returned when no PulseAudio / PipeWire server can be reached. Callers treat it as "no
// microphone", not as a failure.
var ErrNoServer = errors.New("pulse: no server")

// serverPaths lists the unix sockets to try, in the order libpulse uses: PULSE_SERVER, then the per-user runtime
// directory.
func serverPaths(getenv func(string) string, uid int) []string {
	var out []string
	if s := strings.TrimSpace(getenv("PULSE_SERVER")); s != "" {
		for _, e := range strings.Fields(s) {
			if i := strings.IndexByte(e, '}'); strings.HasPrefix(e, "{") && i > 0 { // "{machine-id}" prefix
				e = e[i+1:]
			}
			e = strings.TrimPrefix(e, "unix:")
			if strings.HasPrefix(e, "/") {
				out = append(out, e)
			}
			// tcp: and other transports are not supported (a remote server is not "the microphone")
		}
		return out
	}
	if d := getenv("PULSE_RUNTIME_PATH"); d != "" {
		out = append(out, filepath.Join(d, "native"))
	}
	if d := getenv("XDG_RUNTIME_DIR"); d != "" {
		out = append(out, filepath.Join(d, "pulse", "native"))
	}
	out = append(out, "/run/user/"+strconv.Itoa(uid)+"/pulse/native")
	return out
}

// cookie returns the 256-byte authentication cookie, or zeros (servers on a unix socket also authenticate by the
// peer's credentials, and a zero cookie is what libpulse sends when it has none).
func cookie(getenv func(string) string) []byte {
	var paths []string
	if p := getenv("PULSE_COOKIE"); p != "" {
		paths = append(paths, p)
	}
	if h := getenv("HOME"); h != "" {
		paths = append(paths, filepath.Join(h, ".config", "pulse", "cookie"), filepath.Join(h, ".pulse-cookie"))
	}
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		paths = append([]string{filepath.Join(x, "pulse", "cookie")}, paths...)
	}
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			c := make([]byte, 256)
			copy(c, b) // a longer file is truncated, a shorter one zero-padded, as libpulse does
			return c
		}
	}
	return make([]byte, 256)
}

// Conn is one connection to the server.
type Conn struct {
	c       net.Conn
	version uint32 // negotiated protocol version

	wmu sync.Mutex // serialises writes

	mu      sync.Mutex
	tag     uint32
	pending map[uint32]chan reply
	streams map[uint32]*recStream
	play    map[uint32]*PlayStream
	orphans map[uint32][][]byte // data that arrived between a stream's reply and its registration
	closed  bool
	err     error
	done    chan struct{}
}

type reply struct {
	r   *tagReader
	err error
}

// Options configure Dial.
type Options struct {
	Getenv      func(string) string // default os.Getenv
	UID         int                 // default os.Getuid
	AppName     string              // default "Sweef"
	DialTimeout time.Duration       // default 1s
	// Dialer overrides the unix-socket dial (tests).
	Dial func(path string, timeout time.Duration) (net.Conn, error)
}

func (o *Options) fill() {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.UID == 0 {
		o.UID = os.Getuid()
	}
	if o.AppName == "" {
		o.AppName = "Sweef"
	}
	if o.DialTimeout == 0 {
		o.DialTimeout = time.Second
	}
	if o.Dial == nil {
		o.Dial = func(path string, t time.Duration) (net.Conn, error) { return net.DialTimeout("unix", path, t) }
	}
}

// Dial connects, authenticates and names the client.
func Dial(o Options) (*Conn, error) {
	o.fill()
	var nc net.Conn
	for _, p := range serverPaths(o.Getenv, o.UID) {
		if _, err := os.Stat(p); err != nil && o.Dial == nil {
			continue
		}
		c, err := o.Dial(p, o.DialTimeout)
		if err == nil {
			nc = c
			break
		}
	}
	if nc == nil {
		return nil, ErrNoServer
	}
	c := &Conn{c: nc, pending: map[uint32]chan reply{}, streams: map[uint32]*recStream{}, play: map[uint32]*PlayStream{}, orphans: map[uint32][][]byte{}, done: make(chan struct{})}
	go c.readLoop()
	_ = nc.SetDeadline(time.Time{})

	// AUTH: version and the 256-byte cookie
	var w tagWriter
	w.u32(protocolVersion)
	w.arbitrary(cookie(o.Getenv))
	r, err := c.call(cmdAuth, w.b, 3*time.Second)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("pulse: authentication: %w", err)
	}
	sv := r.u32()
	if r.err != nil {
		c.Close()
		return nil, r.err
	}
	c.version = min(sv&0xFFFF, protocolVersion)
	if c.version < 13 {
		c.Close()
		return nil, fmt.Errorf("pulse: server speaks protocol %d, need 13", sv&0xFFFF)
	}
	w = tagWriter{}
	w.proplist("application.name", o.AppName, "application.id", "org.sweef.browser", "media.role", "phone")
	if _, err := c.call(cmdSetClientName, w.b, 3*time.Second); err != nil {
		c.Close()
		return nil, fmt.Errorf("pulse: set client name: %w", err)
	}
	return c, nil
}

// call sends a command and waits for its reply. args are the tagged arguments after the command and tag.
func (c *Conn) call(cmd uint32, args []byte, timeout time.Duration) (*tagReader, error) {
	c.mu.Lock()
	if c.closed {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return nil, err
	}
	c.tag++
	tag := c.tag
	ch := make(chan reply, 1)
	c.pending[tag] = ch
	c.mu.Unlock()

	var w tagWriter
	w.u32(cmd)
	w.u32(tag)
	w.b = append(w.b, args...)
	if err := c.send(controlChan, w.b); err != nil {
		c.mu.Lock()
		delete(c.pending, tag)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case rep := <-ch:
		return rep.r, rep.err
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.pending, tag)
		c.mu.Unlock()
		return nil, errors.New("pulse: timeout waiting for the server")
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return nil, err
	}
}

func (c *Conn) send(channel uint32, payload []byte) error {
	buf := make([]byte, descSize+len(payload))
	putDescriptor(buf, uint32(len(payload)), channel)
	copy(buf[descSize:], payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.c.Write(buf)
	return err
}

func (c *Conn) readLoop() {
	var err error
	for {
		var ch uint32
		var p []byte
		ch, p, err = readPacket(c.c)
		if err != nil {
			break
		}
		if ch != controlChan {
			c.mu.Lock()
			s := c.streams[ch]
			if s == nil && len(c.orphans[ch]) < 32 {
				c.orphans[ch] = append(c.orphans[ch], p)
			}
			c.mu.Unlock()
			if s != nil {
				s.push(p)
			}
			continue
		}
		r := &tagReader{b: p}
		cmd := r.u32()
		tag := r.u32()
		if r.err != nil {
			err = r.err
			break
		}
		switch cmd {
		case cmdReply, cmdError:
			c.mu.Lock()
			ch := c.pending[tag]
			delete(c.pending, tag)
			c.mu.Unlock()
			if ch == nil {
				continue
			}
			if cmd == cmdError {
				ch <- reply{err: &ServerError{Code: r.u32()}}
			} else {
				ch <- reply{r: r}
			}
		case cmdRecordStreamKilled:
			chn := r.u32()
			c.mu.Lock()
			s := c.streams[chn]
			c.mu.Unlock()
			if s != nil {
				s.finish(errors.New("pulse: the source went away"))
			}
		case cmdRequest:
			chn, n := r.u32(), r.u32()
			if s := c.playStream(chn); s != nil && r.err == nil {
				s.addRequest(int(n))
			}
		case cmdUnderflow:
			if s := c.playStream(r.u32()); s != nil {
				s.underflows.Add(1)
			}
		case cmdPlaybackStreamKilled:
			if s := c.playStream(r.u32()); s != nil {
				s.finish(errors.New("pulse: the playback stream was killed (device removed or moved)"))
			}
		default:
			// subscription events, buffer attribute changes, suspend/move notifications: not needed
		}
	}
	c.shutdown(err)
}

func (c *Conn) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		err = io.EOF
	}
	c.err = err
	streams := c.streams
	c.streams = map[uint32]*recStream{}
	plays := c.play
	c.play = map[uint32]*PlayStream{}
	close(c.done)
	c.mu.Unlock()
	_ = c.c.Close()
	for _, s := range streams {
		s.finish(fmt.Errorf("pulse: connection to the server ended: %w", err))
	}
	for _, s := range plays {
		s.finish(fmt.Errorf("pulse: connection to the server ended: %w", err))
	}
}

// Close drops the connection (and with it every stream).
func (c *Conn) Close() error {
	c.shutdown(io.EOF)
	return nil
}

// ---- queries --------------------------------------------------------------------------------------------------

// Source is one entry of the server's source list.
type Source struct {
	Index       uint32
	Name        string
	Description string
	Rate        uint32
	Channels    int
	Format      byte
	// Monitor is true for the monitor of an output (it records what is playing, not a microphone).
	Monitor bool
	// Group identifies the physical device (device.bus_path, else the part of the name shared with its output).
	Group string
	Form  string // device.form_factor property, e.g. "microphone", "headset", "webcam"
}

// Sources lists every source the server has, monitors included.
func (c *Conn) Sources() ([]Source, error) {
	r, err := c.call(cmdGetSourceInfoList, nil, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return parseSources(r)
}

// parseSources reads a GET_SOURCE_INFO_LIST reply as a version-13 client receives it: the server omits every field
// introduced after the announced version, so an entry ends with the requested latency.
func parseSources(r *tagReader) ([]Source, error) {
	var out []Source
	for len(r.b) > 0 {
		var s Source
		s.Index = r.u32()
		s.Name = r.str()
		s.Description = r.str()
		ss := r.sampleSpec()
		s.Format, s.Channels, s.Rate = ss.Format, int(ss.Channels), ss.Rate
		r.channelMap()
		r.u32() // owner module
		r.cvolume()
		r.boolean() // mute
		monSink := r.u32()
		monName := r.str()
		r.usec() // latency
		r.str()  // driver
		r.u32()  // flags
		props := r.proplist()
		r.usec() // configured latency
		if r.err != nil {
			return out, r.err
		}
		s.Monitor = monSink != invalidIndex || monName != "" || strings.HasSuffix(s.Name, ".monitor")
		s.Form = props["device.form_factor"]
		s.Group = props["device.bus_path"]
		if s.Group == "" {
			s.Group = s.Name
			if i := strings.IndexByte(s.Group, '.'); i >= 0 {
				s.Group = s.Group[i+1:]
			}
			if i := strings.LastIndexByte(s.Group, '.'); i > 0 {
				s.Group = s.Group[:i]
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// DefaultSource returns the name of the server's default source.
func (c *Conn) DefaultSource() (string, error) {
	r, err := c.call(cmdGetServerInfo, nil, 3*time.Second)
	if err != nil {
		return "", err
	}
	r.str() // server name
	r.str() // version
	r.str() // user
	r.str() // host
	r.sampleSpec()
	r.str() // default sink
	def := r.str()
	if r.err != nil {
		return "", r.err
	}
	return def, nil
}
