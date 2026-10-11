package font

import "testing"

func visual(s string, rtl bool) string {
	rs := []rune(s)
	mir := map[rune]rune{'(': ')', ')': '('}
	var out []rune
	for _, r := range BidiRuns(rs, rtl) {
		seg := rs[r.Start:r.End]
		if !r.RTL {
			out = append(out, seg...)
			continue
		}
		for i := len(seg) - 1; i >= 0; i-- {
			c := seg[i]
			if m, ok := mir[c]; ok {
				c = m
			}
			out = append(out, c)
		}
	}
	return string(out)
}

func TestBidiVisualOrder(t *testing.T) {
	const alef, bet, gimel = "א", "ב", "ג"
	shalom := "שלום"
	rev := "םולש"
	cases := []struct {
		in   string
		rtl  bool
		want string
	}{
		{"abc def", false, "abc def"},
		{"abc " + alef + bet + gimel + " def 123", false, "abc " + gimel + bet + alef + " def 123"},
		{"car means " + alef + bet + gimel + ".", false, "car means " + gimel + bet + alef + "."},
		{"(abc) " + shalom + ", world!", true, "!world ," + rev + " (abc)"},
		{"rtl text.", true, ".rtl text"},
		{alef + " 12 " + bet, false, bet + " 12 " + alef},
		{"", true, ""},
	}
	for _, c := range cases {
		if got := visual(c.in, c.rtl); got != c.want {
			t.Errorf("%q (rtl=%v): visual %q, want %q", c.in, c.rtl, got, c.want)
		}
	}
}

func TestArabicShaping(t *testing.T) {
	// "العربية" joins into initial, medial, final forms; lam-alef ligates
	in := []rune("العربية")
	got := ShapeArabic(in)
	want := []rune{0xFE8D, 0xFEDF, 0xFECC, 0xFEAE, 0xFE91, 0xFEF4, 0xFE94}
	// alef isolated; lam initial; ain medial; reh final; beh initial; yeh medial; teh marbuta final
	if len(got) != len(want) {
		t.Fatalf("got %U", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pos %d: %U want %U", i, got[i], want[i])
		}
	}
	if lg := ShapeArabic([]rune("لا")); len(lg) != 1 || lg[0] != 0xFEFB {
		t.Errorf("lam-alef: %U", lg)
	}
	if s := ShapeArabic([]rune("abc")); string(s) != "abc" {
		t.Errorf("latin changed: %q", string(s))
	}
}
