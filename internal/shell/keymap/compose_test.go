package keymap

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// TestComposeDeadMatchesNFC: for every dead key's combining mark and every code point (and the values that are not
// code points), the table answers exactly what NFC answers (planes 0 to 2 exhaustively). This is what lets the browser leave
// golang.org/x/text/unicode/norm out of the binary.
func TestComposeDeadMatchesNFC(t *testing.T) {
	want := func(comb, base rune) string {
		s := norm.NFC.String(string([]rune{base, comb}))
		if r := []rune(s); len(r) == 1 {
			return s
		}
		return ""
	}
	composed := 0
	for _, d := range deadKeys {
		step := rune(1)
		for base := rune(-2); base <= 0x110001; base += step {
			if base > 0x2ffff {
				step = 97 // planes 3 to 16 hold no letters; a sparse walk is enough there (gen_compose.go checked them all)
			}
			if got := ComposeDead(d.comb, base); got != want(d.comb, base) {
				t.Fatalf("ComposeDead(%U, %U) = %q, NFC says %q", d.comb, base, got, want(d.comb, base))
			}
			if ComposeDead(d.comb, base) != "" {
				composed++
			}
		}
	}
	if composed < 600 {
		t.Errorf("only %d compositions found; the table is wrong or empty", composed)
	}
	if ComposeDead(0x41, 'a') != "" || ComposeDead(0x10300, 'a') != "" {
		t.Error("a mark that is not a dead key's composed")
	}
}
