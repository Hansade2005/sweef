package style

import (
	"strings"
	"testing"
)

// A sheet met again at another starting rule order is answered from the cache with shifted orders that
// equal a fresh parse.
func TestParseCacheShiftsRuleOrder(t *testing.T) {
	css := strings.Repeat(".a{color:red} @media (min-width:1px){.b,.c>d{margin:1px}} @layer x{e{top:0}} ", 20)
	env := &Env{Width: 1000, Height: 800}
	o1 := 0
	ParseSheet(css, env, OriginAuthor, &o1, map[string]int{})
	o2 := 1234
	got := ParseSheet(css, env, OriginAuthor, &o2, map[string]int{})
	o3 := 1234
	want := parseSheetQ(css, env, OriginAuthor, &o3, map[string]int{}, false)
	if o2 != o3 || len(got.Rules) != len(want.Rules) {
		t.Fatalf("order %d vs %d, rules %d vs %d", o2, o3, len(got.Rules), len(want.Rules))
	}
	for i := range got.Rules {
		g, w := got.Rules[i], want.Rules[i]
		if g.Order != w.Order || g.Layer != w.Layer || len(g.Entries) != len(w.Entries) || len(g.Decls) != len(w.Decls) {
			t.Fatalf("rule %d: order %d/%d layer %d/%d", i, g.Order, w.Order, g.Layer, w.Layer)
		}
		for _, en := range g.Entries {
			if en.Rule != g {
				t.Fatalf("rule %d: entry points at another rule", i)
			}
		}
	}
}
