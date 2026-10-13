package main

// recproxy.go: a tiny recording HTTP CONNECT proxy. Every browser under test is pointed at one of these, so all
// browsers are measured the same way, by a counter that neither browser controls.
//
// What is counted: the bytes that cross the browser-side socket after the CONNECT handshake has completed, split into
// "out" (browser to network) and "in" (network to browser). For https the proxy only tunnels, so these are the
// encrypted TLS bytes (handshake, records, h2 frames), which is what an ISP bills (TCP/IP headers excluded, like the
// engine's own socket counter will be).
//
// What is NOT counted: the CONNECT request/response lines between browser and this proxy, and the CONNECT exchange
// between this proxy and the upstream (sandbox) proxy. Both exist only because of the measurement setup. DNS is
// resolved by the upstream proxy and is not counted either.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// ConnRecord is one tunnelled connection.
type ConnRecord struct {
	Host    string  `json:"host"`
	In      int64   `json:"in"`
	Out     int64   `json:"out"`
	StartMs float64 `json:"start_ms"` // since recorder start
	DurMs   float64 `json:"dur_ms"`
	Open    bool    `json:"open,omitempty"` // still open when the snapshot was taken
}

// Totals is a snapshot of everything the recorder has seen.
type Totals struct {
	In, Out        int64
	Conns          int
	HSIn, HSOut    int64 // CONNECT exchange between client and this proxy: NOT in In/Out, kept for cross-checks
	PlainRefused   int   // plain-http (non CONNECT) requests, which this proxy does not forward
	PlainHosts     []string
	ConnectFailed  int // CONNECT requests the upstream refused or could not reach
	Records        []ConnRecord
	FailedConnects []string
}

type liveConn struct {
	host    string
	in, out atomic.Int64
	hsIn    int64
	hsOut   int64
	start   time.Time
	end     atomic.Int64 // unix nanos, 0 while open
}

// Recorder is the proxy. Create one per measured launch so runs cannot contaminate each other.
type Recorder struct {
	ln       net.Listener
	upstream *url.URL // nil = dial the target directly
	started  time.Time

	mu     sync.Mutex
	conns  []*liveConn
	failed []string
	plain  []string

	plainRefused  atomic.Int64
	connectFailed atomic.Int64
	wg            sync.WaitGroup
	closed        atomic.Bool
}

// NewRecorder listens on 127.0.0.1:0. upstream is the sandbox proxy URL ("" dials targets directly).
func NewRecorder(upstream string) (*Recorder, error) {
	var up *url.URL
	if upstream != "" {
		u, err := url.Parse(upstream)
		if err != nil {
			return nil, err
		}
		up = u
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &Recorder{ln: ln, upstream: up, started: time.Now()}
	go r.accept()
	return r, nil
}

// Addr is host:port to give to the browser.
func (r *Recorder) Addr() string { return r.ln.Addr().String() }

func (r *Recorder) accept() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(c)
		}()
	}
}

// Close stops listening and tears down every tunnel.
func (r *Recorder) Close() {
	if r.closed.Swap(true) {
		return
	}
	r.ln.Close()
	r.mu.Lock()
	// tunnels end when their sockets close; the browser is normally gone by now. Give them a moment, then move on.
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// Snapshot returns the totals right now. Safe while tunnels are live: counters are atomic.
func (r *Recorder) Snapshot() Totals {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := Totals{PlainRefused: int(r.plainRefused.Load()), ConnectFailed: int(r.connectFailed.Load()), FailedConnects: append([]string(nil), r.failed...), PlainHosts: append([]string(nil), r.plain...)}
	for _, c := range r.conns {
		in, out := c.in.Load(), c.out.Load()
		rec := ConnRecord{Host: c.host, In: in, Out: out, StartMs: float64(c.start.Sub(r.started).Microseconds()) / 1000}
		if e := c.end.Load(); e != 0 {
			rec.DurMs = float64(e-c.start.UnixNano()) / 1e6
		} else {
			rec.DurMs = float64(time.Since(c.start).Microseconds()) / 1000
			rec.Open = true
		}
		t.In += in
		t.Out += out
		t.HSIn += c.hsIn
		t.HSOut += c.hsOut
		t.Records = append(t.Records, rec)
	}
	t.Conns = len(r.conns)
	return t
}

// Diff returns b minus a for the counters and the connections that started after a was taken.
func (b Totals) Diff(a Totals) Totals {
	d := Totals{In: b.In - a.In, Out: b.Out - a.Out, Conns: b.Conns - a.Conns, HSIn: b.HSIn - a.HSIn, HSOut: b.HSOut - a.HSOut,
		PlainRefused: b.PlainRefused - a.PlainRefused, ConnectFailed: b.ConnectFailed - a.ConnectFailed}
	// Per connection deltas, matched by index (the recorder only appends).
	for i, rec := range b.Records {
		if i < len(a.Records) {
			p := a.Records[i]
			rec.In -= p.In
			rec.Out -= p.Out
			if rec.In == 0 && rec.Out == 0 {
				continue
			}
		}
		d.Records = append(d.Records, rec)
	}
	return d
}

func (r *Recorder) handle(c net.Conn) {
	defer c.Close()
	cr := &countReader{r: c}
	br := bufio.NewReader(cr)
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	headBytes := cr.n - int64(br.Buffered())
	c.SetReadDeadline(time.Time{})
	if req.Method != http.MethodConnect {
		// The sandbox proxy only does CONNECT; benchmark URLs are https. Count and refuse so it is visible.
		r.plainRefused.Add(1)
		r.mu.Lock()
		r.plain = append(r.plain, req.Host)
		r.mu.Unlock()
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	target := req.Host
	up, err := r.dialTarget(target)
	if err != nil {
		r.connectFailed.Add(1)
		r.mu.Lock()
		r.failed = append(r.failed, target+": "+err.Error())
		r.mu.Unlock()
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer up.Close()
	const established = "HTTP/1.1 200 Connection established\r\n\r\n"
	if _, err := io.WriteString(c, established); err != nil {
		return
	}
	lc := &liveConn{host: target, start: time.Now(), hsIn: int64(len(established)), hsOut: headBytes}
	r.mu.Lock()
	r.conns = append(r.conns, lc)
	r.mu.Unlock()
	defer lc.end.Store(time.Now().UnixNano())

	var wg sync.WaitGroup
	wg.Add(2)
	// browser -> network ("out"). Bytes the browser pipelined behind the CONNECT line are still in br.
	go func() {
		defer wg.Done()
		pump(up, br, &lc.out)
		closeWrite(up)
	}()
	// network -> browser ("in")
	go func() {
		defer wg.Done()
		pump(c, up, &lc.in)
		closeWrite(c)
	}()
	wg.Wait()
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

// pump copies src to dst and adds to n the bytes that were actually WRITTEN to dst, which is what crossed the wire.
func pump(dst io.Writer, src io.Reader, n *atomic.Int64) {
	buf := make([]byte, 32<<10)
	for {
		k, err := src.Read(buf)
		if k > 0 {
			w, werr := dst.Write(buf[:k])
			n.Add(int64(w))
			if werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}

// dialTarget returns a tunnel to host:port, through the upstream proxy when configured. The upstream CONNECT
// handshake is not counted.
func (r *Recorder) dialTarget(target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var d net.Dialer
	if r.upstream == nil {
		return d.DialContext(ctx, "tcp", target)
	}
	pc, err := d.DialContext(ctx, "tcp", r.upstream.Host)
	if err != nil {
		return nil, err
	}
	pc.SetDeadline(time.Now().Add(20 * time.Second))
	fmt.Fprintf(pc, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(pc)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		pc.Close()
		return nil, err
	}
	if resp.StatusCode != 200 {
		pc.Close()
		return nil, fmt.Errorf("upstream proxy answered %s", resp.Status)
	}
	pc.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		// server-first bytes already read behind the 200: hand them back ahead of the socket
		return &prefixConn{Conn: pc, r: io.MultiReader(io.LimitReader(br, int64(br.Buffered())), pc)}, nil
	}
	return pc, nil
}

type prefixConn struct {
	net.Conn
	r io.Reader
}

func (p *prefixConn) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *prefixConn) CloseWrite() error {
	if cw, ok := p.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return p.Conn.Close()
}
