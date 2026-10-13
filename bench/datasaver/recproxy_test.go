package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countListener counts bytes on the far side of the tunnel, independently of the recorder.
type countListener struct {
	net.Listener
	in, out *atomic.Int64
}

type countConn struct {
	net.Conn
	in, out *atomic.Int64
}

func (l countListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return countConn{c, l.in, l.out}, nil
}
func (c countConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.in.Add(int64(n))
	return n, err
}
func (c countConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.out.Add(int64(n))
	return n, err
}

// The recorder's in/out must equal what the server side saw (server out == recorder in, server in == recorder out),
// for a real TLS conversation with a large body, through the upstream-proxy path as well as the direct path.
func TestRecorderCountsExactBytes(t *testing.T) {
	var sIn, sOut atomic.Int64
	body := strings.Repeat("x", 3<<20)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(body))
	}))
	srv.Listener = countListener{srv.Listener, &sIn, &sOut}
	srv.StartTLS()
	defer srv.Close()

	// a fake upstream proxy that speaks CONNECT, to exercise the upstream path
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				n, _ := c.Read(buf)
				first := strings.SplitN(string(buf[:n]), " ", 3)
				d, err := net.Dial("tcp", first[1])
				if err != nil {
					c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
					return
				}
				defer d.Close()
				c.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
				done := make(chan struct{})
				go func() { copyAll(d, c); done <- struct{}{} }()
				copyAll(c, d)
				<-done
			}()
		}
	}()

	for _, upstream := range []string{"", "http://" + up.Addr().String()} {
		sIn.Store(0)
		sOut.Store(0)
		rec, err := NewRecorder(upstream)
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse("http://" + rec.Addr())
		cl := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		for i := 0; i < 2; i++ {
			resp, err := cl.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			buf := make([]byte, 64<<10)
			for {
				k, err := resp.Body.Read(buf)
				n += k
				if err != nil {
					break
				}
			}
			resp.Body.Close()
			if n != len(body) {
				t.Fatalf("body %d != %d", n, len(body))
			}
		}
		cl.CloseIdleConnections()
		time.Sleep(300 * time.Millisecond)
		tot := rec.Snapshot()
		rec.Close()
		if tot.Conns < 1 {
			t.Fatalf("no connections recorded")
		}
		if tot.In != sOut.Load() || tot.Out != sIn.Load() {
			t.Fatalf("upstream=%q: recorder in/out %d/%d, far side out/in %d/%d", upstream, tot.In, tot.Out, sOut.Load(), sIn.Load())
		}
		if tot.In < int64(2*len(body)) {
			t.Fatalf("recorder in %d is smaller than two bodies", tot.In)
		}
		t.Logf("upstream=%q: in=%d out=%d conns=%d (payload %d x2)", upstream, tot.In, tot.Out, tot.Conns, len(body))
	}
}

func copyAll(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			dst.Write(buf[:n])
		}
		if err != nil {
			if cw, ok := dst.(interface{ CloseWrite() error }); ok {
				cw.CloseWrite()
			}
			return
		}
	}
}

func TestRecorderRefusesPlainHTTPAndCountsIt(t *testing.T) {
	rec, err := NewRecorder("")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	pu, _ := url.Parse("http://" + rec.Addr())
	cl := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := cl.Get("http://example.invalid/")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != 502 {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	if rec.Snapshot().PlainRefused != 1 {
		t.Fatalf("plain refused not counted: %+v", rec.Snapshot())
	}
}
