//go:build linux

package x11

import (
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/shell"
)

func TestClipboardBetweenClients(t *testing.T) {
	a := openTest(t, shellCfg(100, 100))
	b := openTest(t, shellCfg(100, 100))
	if got := b.Clipboard(); got != "" {
		t.Fatalf("expected empty clipboard, got %q", got)
	}
	a.SetClipboard("héllo wörld ✓")
	if got := a.Clipboard(); got != "héllo wörld ✓" {
		t.Fatalf("owner read %q", got)
	}
	if got := b.Clipboard(); got != "héllo wörld ✓" {
		t.Fatalf("other client read %q", got)
	}
	// Ownership moves.
	b.SetClipboard("from b")
	if got := a.Clipboard(); got != "from b" {
		t.Fatalf("after ownership change %q", got)
	}
	// Empty string is a valid clipboard.
	b.SetClipboard("")
	if got := a.Clipboard(); got != "" {
		t.Fatalf("empty %q", got)
	}
}

func TestClipboardINCR(t *testing.T) {
	a := openTest(t, shellCfg(100, 100))
	b := openTest(t, shellCfg(100, 100))
	big := strings.Repeat("0123456789abcdef—✓\n", 400000) // ~10 MB
	a.SetClipboard(big)
	got := b.Clipboard()
	if len(got) != len(big) || got != big {
		t.Fatalf("INCR transfer mismatch: got %d bytes want %d", len(got), len(big))
	}
}

func TestClipboardTargetsAndLatin1(t *testing.T) {
	a := openTest(t, shellCfg(100, 100))
	tc := testConn(t)
	a.SetClipboard("café €")
	// Ask for STRING (latin-1): € becomes '?'.
	win := tc.allocID()
	cw := request(opCreateWindow, 0, 32)
	le.PutUint32(cw[4:], win)
	le.PutUint32(cw[8:], tc.screen.root)
	le.PutUint16(cw[16:], 10)
	le.PutUint16(cw[18:], 10)
	le.PutUint16(cw[22:], 1)
	tc.send(cw, false)
	got := make(chan selNotifyEvent, 1)
	tc.handler = func(ev []byte) {
		if ev[0]&0x7f == evSelectionNotify {
			got <- selNotifyEvent{le.Uint32(ev[12:]), le.Uint32(ev[16:]), le.Uint32(ev[20:])}
		}
	}
	prop := a.atoms["_SWEEF_CLIP"]
	tc.convertSelection(win, a.atoms["CLIPBOARD"], atomString, prop, 0)
	n := <-got
	if n.property != prop {
		t.Fatalf("refused: %+v", n)
	}
	pr, _ := tc.getProperty(win, prop, 0, false, 0, 100)
	if string(pr.value) != "caf\xe9 ?" {
		t.Fatalf("latin1 %q", pr.value)
	}
	tc.convertSelection(win, a.atoms["CLIPBOARD"], a.atoms["TARGETS"], prop, 0)
	n = <-got
	pr, _ = tc.getProperty(win, prop, 0, false, 0, 100)
	if n.property != prop || pr.typ != atomAtom || len(pr.value) < 8 {
		t.Fatalf("targets %+v %+v", n, pr)
	}
	// unsupported target is refused
	tc.convertSelection(win, a.atoms["CLIPBOARD"], a.atoms["XdndAware"], prop, 0)
	if n = <-got; n.property != 0 {
		t.Fatalf("expected refusal %+v", n)
	}
	_ = shell.Event{}
}
