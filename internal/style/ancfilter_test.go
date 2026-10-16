package style

import (
	"strings"
	"testing"
)

// The ancestor filter (Engine.BeginPass) must never reject a rule the matcher accepts: quirks-mode class
// and id case-folding, child and descendant combinators, mixed-case tags, and a class added to an ancestor
// between two passes.
func TestAncestorFilterKeepsMatches(t *testing.T) {
	src := `<html><head><style>
.Menu .item { color: red }
#Box > p { font-size: 30px }
DIV.wrap span { padding-top: 7px }
.later * { margin-top: 9px }
</style></head><body><div class=wrap id=box><p class=item><span>x</span></p></div></body></html>`
	for _, quirks := range []bool{false, true} {
		doc := parse(t, src)
		e := NewEngineQ(&Env{Width: 1280, Height: 800}, []SheetIn{{Text: strings.Join(extractStyles(doc), "\n")}}, quirks)
		div := find(doc, "div")
		setClass := func(v string) {
			for i := range div.Attr {
				if div.Attr[i].Key == "class" {
					div.Attr[i].Val = v
				}
			}
		}
		setClass("wrap menu")
		pass := func() (p, span *Computed) {
			e.BeginPass()
			hc := e.Compute(find(doc, "html"), nil, "")
			b := e.Compute(find(doc, "body"), hc, "")
			d := e.Compute(div, b, "")
			p = e.Compute(find(doc, "p"), d, "")
			return p, e.Compute(find(doc, "span"), p, "")
		}
		p, span := pass()
		if got := p.Color == (Color{255, 0, 0, 255}); got != quirks {
			t.Errorf("quirks=%v: .Menu .item matched=%v", quirks, got)
		}
		if got := p.FontSize == 30; got != quirks {
			t.Errorf("quirks=%v: #Box > p matched=%v (font-size %v)", quirks, got, p.FontSize)
		}
		if span.Padding[0].V != 7 {
			t.Errorf("quirks=%v: DIV.wrap span padding %v", quirks, span.Padding[0])
		}
		if span.Margin[0].V == 9 {
			t.Errorf("quirks=%v: .later * matched before the class was added", quirks)
		}
		setClass("wrap menu later")
		if _, span = pass(); span.Margin[0].V != 9 {
			t.Errorf("quirks=%v: .later * not matched after the class was added: %v", quirks, span.Margin[0])
		}
	}
}
