package engine

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// rawWS is a hand-written RFC 6455 server connection, so the tests can send what real servers send and
// golang.org/x/net/websocket cannot: fragmented messages, binary frames that happen to be valid UTF-8, pings in the
// middle of a fragmented message, arbitrary close codes, protocol violations.
type rawWS struct {
	c  net.Conn
	br *bufio.Reader
	mu sync.Mutex
}

func (s *rawWS) frame(fin bool, op byte, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := []byte{op}
	if fin {
		h[0] |= 0x80
	}
	switch n := len(payload); {
	case n < 126:
		h = append(h, byte(n))
	case n <= 0xFFFF:
		h = append(h, 126, byte(n>>8), byte(n))
	default:
		var l [8]byte
		binary.BigEndian.PutUint64(l[:], uint64(n))
		h = append(h, 127)
		h = append(h, l[:]...)
	}
	s.c.Write(h)
	s.c.Write(payload)
}

type rawFrame struct {
	fin     bool
	op      byte
	masked  bool
	payload []byte
}

func (s *rawWS) read() (rawFrame, error) {
	var h [2]byte
	if _, err := io.ReadFull(s.br, h[:]); err != nil {
		return rawFrame{}, err
	}
	f := rawFrame{fin: h[0]&0x80 != 0, op: h[0] & 0xF, masked: h[1]&0x80 != 0}
	n := uint64(h[1] & 0x7F)
	if n == 126 {
		var e [2]byte
		io.ReadFull(s.br, e[:])
		n = uint64(binary.BigEndian.Uint16(e[:]))
	} else if n == 127 {
		var e [8]byte
		io.ReadFull(s.br, e[:])
		n = binary.BigEndian.Uint64(e[:])
	}
	var mask [4]byte
	if f.masked {
		io.ReadFull(s.br, mask[:])
	}
	f.payload = make([]byte, n)
	if _, err := io.ReadFull(s.br, f.payload); err != nil {
		return f, err
	}
	if f.masked {
		for i := range f.payload {
			f.payload[i] ^= mask[i&3]
		}
	}
	return f, nil
}

// message reads one whole client message (the client never fragments), answering nothing.
func (s *rawWS) message() (op byte, payload []byte, err error) {
	for {
		f, err := s.read()
		if err != nil {
			return 0, nil, err
		}
		if f.op == 8 || f.op == 1 || f.op == 2 {
			return f.op, f.payload, nil
		}
	}
}

func newRawWSServer(t *testing.T, subprotocol string, handler func(r *http.Request, s *rawWS)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "no", 400)
			return
		}
		if r.URL.Path == "/forbidden" {
			http.Error(w, "nope", 403)
			return
		}
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		hj := w.(http.Hijacker)
		c, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n"
		if subprotocol != "" && strings.Contains(r.Header.Get("Sec-WebSocket-Protocol"), subprotocol) {
			resp += "Sec-WebSocket-Protocol: " + subprotocol + "\r\n"
		}
		resp += "Set-Cookie: wssess=abc; Path=/\r\n\r\n"
		rw.WriteString(resp)
		rw.Flush()
		handler(r, &rawWS{c: c, br: rw.Reader})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsTestPage(t *testing.T, srv *httptest.Server, script string) (*Tab, func(string) string) {
	t.Helper()
	// the page itself comes from a second server so that ws:// is cross-origin (as it is for real sites)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<!doctype html><title>ws</title><body>ok</body>")
	}))
	t.Cleanup(page.Close)
	e := newFeatureEngine(t)
	tab, err := e.NewTab(page.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	_, err = tab.Evaluate("window.WS = " + jsString(wsURL) + "; window.log = []; " + script)
	if err != nil {
		t.Fatal(err)
	}
	return tab, func(expr string) string {
		t.Helper()
		var out string
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			tab.Settle(30 * time.Millisecond)
			raw, err := tab.Evaluate("String(" + expr + ")")
			if err == nil {
				out = strings.Trim(string(raw), `"`)
			}
			if out != "" && out != "undefined" && out != "null" && out != "false" {
				return out
			}
		}
		return out
	}
}

func TestWebSocketMessagesFragmentsAndCloseCodes(t *testing.T) {
	var cookieSeen sync.Mutex
	var sawCookie string
	srv := newRawWSServer(t, "chat", func(r *http.Request, s *rawWS) {
		cookieSeen.Lock()
		sawCookie = r.Header.Get("Cookie")
		cookieSeen.Unlock()
		// text, binary that is valid UTF-8, binary that is not, a fragmented text message with a ping in the middle
		s.frame(true, 1, []byte("héllo"))
		s.frame(true, 2, []byte("plain ascii bytes"))
		s.frame(true, 2, []byte{0xff, 0x00, 0xfe})
		s.frame(false, 1, []byte("frag"))
		s.frame(true, 9, []byte("p"))
		s.frame(false, 0, []byte("men"))
		s.frame(true, 0, []byte("ted"))
		// a large binary message in many fragments
		big := make([]byte, 300000)
		for i := range big {
			big[i] = byte(i * 7)
		}
		for off := 0; off < len(big); off += 70000 {
			end := off + 70000
			if end > len(big) {
				end = len(big)
			}
			s.frame(end == len(big), map[bool]byte{true: 2, false: 0}[off == 0], big[off:end])
		}
		// echo what the client sends until it closes; reply to its close with a custom status
		for {
			op, p, err := s.message()
			if err != nil {
				return
			}
			if op == 8 {
				s.frame(true, 8, append([]byte{0x0f, 0xa0}, "bye"...)) // 4000
				return
			}
			s.frame(true, op, p)
		}
	})
	tab, wait := wsTestPage(t, srv, `
	  var ws = new WebSocket(WS, ['other', 'chat']); ws.binaryType = 'arraybuffer'; window.ws = ws;
	  ws.onopen = function () { log.push('open:' + ws.protocol + ':' + ws.extensions); };
	  ws.onmessage = function (e) {
	    var d = e.data;
	    if (typeof d === 'string') log.push('t:' + d);
	    else { var u = new Uint8Array(d); log.push('b:' + u.length + ':' + u[0] + (u.length > 100000 ? ':' + u[299999] : '')); }
	  };
	  ws.onclose = function (e) { log.push('close:' + e.code + ':' + e.reason + ':' + e.wasClean); };`)
	if got := wait(`log.length >= 6 && log.join()`); !strings.Contains(got, "open:chat:") {
		t.Fatalf("log: %s\n%s", got, consoleText(tab))
	}
	var log string
	waitLog := func(n int) {
		t.Helper()
		wait(`log.length >= ` + itoa(n) + ` && 1`)
	}
	waitLog(6)
	raw, _ := tab.Evaluate(`log.join('|')`)
	log = string(raw)
	want := []string{"open:chat:", "t:héllo", "b:17:112", "b:3:255", "t:fragmented", "b:300000:0:"}
	for _, w := range want {
		if !strings.Contains(log, w) {
			t.Errorf("missing %q in %s", w, log)
		}
	}
	// the last byte of the reassembled big message: (299999*7) mod 256
	if !strings.Contains(log, "b:300000:0:"+itoa(int((299999*7)&0xff))) {
		t.Errorf("big message corrupted: %s", log)
	}
	cookieSeen.Lock()
	_ = sawCookie
	cookieSeen.Unlock()

	// sends: typed-array views send only their window, ArrayBuffers all, strings as text frames
	_, _ = tab.Evaluate(`
	  var buf = new Uint8Array([1,2,3,4,5,6,7,8,9,10]);
	  ws.send(buf.subarray(2, 5)); ws.send(buf.buffer); ws.send(new DataView(buf.buffer, 8, 2)); ws.send('text'); ws.send(new Blob(['blobby']));
	  1`)
	wait(`log.length >= 11 && 1`)
	raw, _ = tab.Evaluate(`log.slice(6).join('|')`)
	if got := string(raw); got != `"b:3:3|b:10:1|b:2:9|t:text|b:6:98"` {
		t.Errorf("echoes: %s", got)
	}
	// close with a code and a reason: the server answers with its own status, the close is clean
	_, _ = tab.Evaluate(`ws.close(3001, 'done'); 1`)
	if got := wait(`log[log.length - 1].indexOf('close:') === 0 && log[log.length - 1]`); got != "close:4000:bye:true" {
		t.Errorf("close event: %q", got)
	}
}

func TestWebSocketFailuresAndValidation(t *testing.T) {
	srv := newRawWSServer(t, "", func(r *http.Request, s *rawWS) {
		switch r.URL.Path {
		case "/badutf8":
			s.frame(true, 1, []byte{0xc3, 0x28})
			time.Sleep(500 * time.Millisecond)
		case "/closecode":
			s.frame(true, 8, append([]byte{0x03, 0xe8}, "normal"...)) // 1000
			time.Sleep(200 * time.Millisecond)
		case "/drop":
			s.c.Close()
		case "/reserved":
			s.frame(true, 1|0x40, []byte("x"))
			time.Sleep(500 * time.Millisecond)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
			s.frame(true, 1, []byte("late"))
			time.Sleep(300 * time.Millisecond)
		default:
			for {
				if _, _, err := s.message(); err != nil {
					return
				}
			}
		}
	})
	tab, wait := wsTestPage(t, srv, `
	  window.run = function (path, protos) { var o = { ev: [] }; try { var w = protos ? new WebSocket(WS + path, protos) : new WebSocket(WS + path); } catch (e) { o.ev.push('throw:' + e.name); return o; }
	    o.ws = w; w.onopen = function () { o.ev.push('open'); }; w.onerror = function () { o.ev.push('error'); }; w.onmessage = function (e) { o.ev.push('msg:' + e.data); };
	    w.onclose = function (e) { o.ev.push('close:' + e.code + ':' + e.wasClean); }; return o; };`)
	check := func(path, want string) {
		t.Helper()
		_, _ = tab.Evaluate(`window.o = run(` + jsString(path) + `); 1`)
		got := wait(`o.ev.some(function (e) { return e.indexOf('close') === 0 || e.indexOf('throw') === 0; }) && o.ev.join()`)
		if got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
	check("/badutf8", "open,error,close:1006:false")
	check("/closecode", "open,close:1000:true")
	check("/drop", "open,error,close:1006:false")
	check("/reserved", "open,error,close:1006:false")
	check("/forbidden", "error,close:1006:false")
	check("/slow", "open,msg:late,error,close:1006:false")

	// the page's own mistakes
	var res string
	raw, _ := tab.Evaluate(`(function () {
	  var r = {};
	  function t(name, f) { try { f(); r[name] = 'ok'; } catch (e) { r[name] = e.name; } }
	  t('scheme', function () { new WebSocket('ftp://x/'); });
	  t('fragment', function () { new WebSocket(WS + '/#frag'); });
	  t('dupProto', function () { new WebSocket(WS, ['a', 'a']); });
	  t('badProto', function () { new WebSocket(WS, ['a b']); });
	  t('noNew', function () { WebSocket(WS); });
	  var w = new WebSocket(WS + '/x');
	  t('sendConnecting', function () { w.send('x'); });
	  t('badCode', function () { w.close(1001); });
	  t('longReason', function () { w.close(1000, new Array(125).fill('a').join('')); });
	  t('relative', function () { new WebSocket('/relative').close(); });
	  r.bt = (w.binaryType = 'bogus', w.binaryType);
	  r.states = [WebSocket.CONNECTING, WebSocket.OPEN, WebSocket.CLOSING, WebSocket.CLOSED].join();
	  w.close();
	  r.afterClose = w.readyState;
	  return JSON.stringify(r); })()`)
	_ = json2(raw, &res)
	for _, w := range []string{`"scheme":"SyntaxError"`, `"fragment":"SyntaxError"`, `"dupProto":"SyntaxError"`, `"badProto":"SyntaxError"`, `"noNew":"TypeError"`,
		`"sendConnecting":"InvalidStateError"`, `"badCode":"InvalidAccessError"`, `"longReason":"SyntaxError"`, `"relative":"ok"`, `"bt":"blob"`, `"states":"0,1,2,3"`, `"afterClose":2`} {
		if !strings.Contains(res, w) {
			t.Errorf("missing %s in %s", w, res)
		}
	}
	// closing while connecting ends in error + 1006, never open
	_, _ = tab.Evaluate(`window.o = run('/slow'); o.ws.close(); 1`)
	if got := wait(`o.ev.some(function (e) { return e.indexOf('close') === 0; }) && o.ev.join()`); got != "error,close:1006:false" {
		t.Errorf("close while connecting: %q", got)
	}
}

func TestWebSocketBufferedAmountAndBackpressure(t *testing.T) {
	release := make(chan struct{})
	srv := newRawWSServer(t, "", func(r *http.Request, s *rawWS) {
		<-release // do not read: the client's send buffer fills
		for {
			if _, _, err := s.message(); err != nil {
				return
			}
		}
	})
	defer close(release)
	tab, wait := wsTestPage(t, srv, `var w = new WebSocket(WS); w.onopen = function () { window.opened = true; }; w.onclose = function () { window.closed = true; };`)
	wait(`window.opened`)
	// 16 MiB goes into the queue at once without blocking the page; bufferedAmount reports what the socket has not taken
	t0 := time.Now()
	if _, err := tab.Evaluate(`var chunk = new Uint8Array(1 << 20); for (var i = 0; i < 16; i++) w.send(chunk); window.buffered = w.bufferedAmount; 1`); err != nil {
		t.Fatalf("sending 16 MiB: %v (%v)\n%s", err, time.Since(t0), consoleText(tab))
	}
	t.Logf("queueing 16 MiB took %v", time.Since(t0))
	raw, _ := tab.Evaluate(`window.buffered`)
	if n := atoiSafe(strings.Trim(string(raw), `"`)); n < 4<<20 {
		t.Errorf("bufferedAmount right after queueing 16 MiB: %s", raw)
	}
}

func json2(raw []byte, out *string) error {
	s := string(raw)
	s = strings.TrimPrefix(strings.TrimSuffix(s, `"`), `"`)
	*out = strings.ReplaceAll(s, `\"`, `"`)
	return nil
}
