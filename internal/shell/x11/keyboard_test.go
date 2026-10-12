//go:build linux

package x11

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/shell"
)

func TestDeadKeyComposition(t *testing.T) {
	var tr keyTranslator
	k, txt := tr.translate(0xfe51, 0) // dead_acute
	if k != "Dead" || txt != "" {
		t.Fatal(k, txt)
	}
	k, txt = tr.translate('e', 0)
	if k != "e" || txt != "é" {
		t.Fatal(k, txt)
	}
	// dead + space gives the spacing accent
	tr.translate(0xfe57, 0) // diaeresis
	if _, txt = tr.translate(' ', 0); txt != "¨" {
		t.Fatalf("%q", txt)
	}
	// dead + non-composable: accent then char
	tr.translate(0xfe51, 0)
	if _, txt = tr.translate('x', 0); txt != "´x" {
		t.Fatalf("%q", txt)
	}
	// dead + dead: first accent emitted
	tr.translate(0xfe52, 0)
	if _, txt = tr.translate(0xfe51, 0); txt != "^" {
		t.Fatalf("%q", txt)
	}
	// Ctrl cancels composition and produces no text
	if k, txt = tr.translate('a', shell.ModCtrl); k != "a" || txt != "" {
		t.Fatal(k, txt)
	}
}

func TestKeysymLookup(t *testing.T) {
	// keycodes 10..: [a A] [é É at group2/AltGr col 2,3] and level-3 cols 4,5
	m := &kbdMap{min: 10, kpk: 6, level3: 1 << 7, numLock: 1 << 4}
	m.syms = []uint32{
		'a', 'A', 0xe4, 0xc4, 0x1000e5, 0x1000c5, // keycode 10
		0xff9c, 0xffb1, 0, 0, 0, 0, // keycode 11: KP_End / KP_1
		'1', '!', 0, 0, '@', 0xa1, // keycode 12
	}
	cases := []struct {
		kc    uint8
		state uint16
		want  uint32
	}{
		{10, 0, 'a'}, {10, 1, 'A'}, {10, 2, 'A'}, {10, 3, 'a'}, // shift, caps, both
		{10, 1 << 7, 0x1000e5}, {10, 1<<7 | 1, 0x1000c5}, // AltGr (+shift)
		{11, 0, 0xff9c}, {11, 1 << 4, 0xffb1}, {11, 1, 0xffb1}, {11, 1<<4 | 1, 0xff9c}, // keypad numlock
		{12, 2, '1'}, {12, 1, '!'}, // caps does not affect non-letters
	}
	for _, c := range cases {
		if got := m.lookup(c.kc, c.state); got != c.want {
			t.Errorf("lookup(%d,%#x) = %#x want %#x", c.kc, c.state, got, c.want)
		}
	}
	// second XKB group selects columns 2,3
	if got := m.lookup(10, 1<<13); got != 0xe4 {
		t.Errorf("group2 = %#x", got)
	}
}

func TestParseURIList(t *testing.T) {
	got := ParseURIList("# c\r\nfile:///tmp/a%20b.txt\r\nhttp://x/y\r\nfile://localhost/home/u/é\r\nfile://other/x\r\n")
	if len(got) != 2 || got[0] != "/tmp/a b.txt" || got[1] != "/home/u/é" {
		t.Fatalf("%q", got)
	}
}
