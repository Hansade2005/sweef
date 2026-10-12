//go:build linux

package x11

import (
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

// TestXDNDFileDrop plays the source side of an XDND file drop with a second
// connection and checks the target answers the protocol and delivers files.
func TestXDNDFileDrop(t *testing.T) {
	w := openTest(t, shellCfg(300, 200))
	sc := testConn(t)
	a := w.atoms
	sw := sc.allocID()
	cw := request(opCreateWindow, 0, 32)
	le.PutUint32(cw[4:], sw)
	le.PutUint32(cw[8:], sc.screen.root)
	le.PutUint16(cw[16:], 10)
	le.PutUint16(cw[18:], 10)
	le.PutUint16(cw[22:], 1)
	sc.send(cw, false)

	const payload = "file:///tmp/a%20b.txt\r\nfile:///home/u/c.png\r\n"
	status := make(chan [5]uint32, 4)
	finished := make(chan [5]uint32, 1)
	sc.handler = func(ev []byte) {
		switch ev[0] & 0x7f {
		case evSelectionRequest:
			req, prop := le.Uint32(ev[12:]), le.Uint32(ev[24:])
			sc.changeProperty(0, req, prop, a["text/uri-list"], 8, []byte(payload))
			n := make([]byte, 32)
			n[0] = evSelectionNotify
			le.PutUint32(n[8:], req)
			le.PutUint32(n[12:], le.Uint32(ev[16:]))
			le.PutUint32(n[16:], le.Uint32(ev[20:]))
			le.PutUint32(n[20:], prop)
			sc.sendEvent(req, false, 0, n)
		case evClientMessage:
			var d [5]uint32
			for i := range d {
				d[i] = le.Uint32(ev[12+4*i:])
			}
			switch le.Uint32(ev[8:]) {
			case a["XdndStatus"]:
				status <- d
			case a["XdndFinished"]:
				finished <- d
			}
		}
	}
	sc.setSelectionOwner(sw, a["XdndSelection"], 0)
	sc.sync()

	send := func(typ uint32, d [5]uint32) {
		sc.sendEvent(w.win, false, 0, clientMessage32(w.win, typ, d))
		sc.sync()
	}
	send(a["XdndEnter"], [5]uint32{sw, 5 << 24, a["text/uri-list"], 0, 0})
	send(a["XdndPosition"], [5]uint32{sw, 0, 20<<16 | 30, 0, a["XdndActionCopy"]})
	select {
	case s := <-status:
		if s[0] != w.win || s[1]&1 == 0 || s[4] != a["XdndActionCopy"] {
			t.Fatalf("status %v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no XdndStatus")
	}
	send(a["XdndDrop"], [5]uint32{sw, 0, 0, 0, 0})
	e := next(t, w, ofKind(shell.EvDropFiles))
	if len(e.Files) != 2 || e.Files[0] != "/tmp/a b.txt" || e.Files[1] != "/home/u/c.png" {
		t.Fatalf("files %q", e.Files)
	}
	select {
	case f := <-finished:
		if f[1]&1 == 0 {
			t.Fatalf("finished not accepted %v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no XdndFinished")
	}
}
