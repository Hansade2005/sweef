package render

import (
	"math"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func recLayout(t *testing.T, src string) (*html.Node, *Result) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return doc, Layout(doc, Options{Width: 800, Height: 600, DPR: 1})
}

func textNodeOf(n *html.Node, want string) *html.Node {
	if n.Type == html.TextNode && strings.Contains(n.Data, want) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := textNodeOf(c, want); r != nil {
			return r
		}
	}
	return nil
}

func TestTextBoxesMapCollapsedWhitespaceToTheSource(t *testing.T) {
	doc, res := recLayout(t, "<!doctype html><body style='margin:0;font:16px monospace'><p id=p>Hello \n   world, and more</p>")
	n := textNodeOf(doc, "Hello")
	ix := NewTextIndex(res)
	bx := ix.Boxes(n) // one run per word with its trailing blank: "Hello ", "world, ", "and ", "more"
	if len(bx) != 4 {
		t.Fatalf("want one run per word, got %d", len(bx))
	}
	// the runs follow each other through the text node, and the blanks in the source (a space, a newline, three
	// spaces) that show as one blank are skipped by the offsets
	if bx[0].FirstOff() != 0 || bx[0].LastOff() != 6 {
		t.Fatalf("first run covers %d..%d", bx[0].FirstOff(), bx[0].LastOff())
	}
	if bx[1].FirstOff() != 10 {
		t.Fatalf("\"world\" starts at source offset %d, want 10 (after 'Hello' and 5 blanks)", bx[1].FirstOff())
	}
	if bx[3].LastOff() != len(n.Data) {
		t.Fatalf("last stop is offset %d, node has %d characters", bx[3].LastOff(), len(n.Data))
	}
	for _, tb := range bx {
		for i := 1; i < len(tb.Xs); i++ {
			if tb.Xs[i] < tb.Xs[i-1] {
				t.Fatal("stops must advance left to right")
			}
		}
	}
	w := bx[1]
	// a point inside the third character of "world" lands next to it
	x := (w.X(2) + w.X(3)) / 2
	p, box, stop, ok := ix.PosAt(x+1, w.Rect.Y+w.Rect.H/2, false)
	if !ok || p.Node != n || box != &res.Text[ix.byNode[n][1]] || (stop != 2 && stop != 3) {
		t.Fatalf("PosAt: %+v stop %d ok %v", p, stop, ok)
	}
	// above and below the text: the nearest line
	if _, _, _, ok := ix.PosAt(5, -50, false); !ok {
		t.Fatal("a point above the text still has a nearest position")
	}
	// the rectangles of a range that spans two runs
	rs := ix.RectsFor(n, bx[1].FirstOff(), bx[2].LastOff())
	if len(rs) != 2 || rs[0].X >= rs[1].X {
		t.Fatalf("rects %v", rs)
	}
	if math.Abs(rs[0].X-bx[1].X(0)) > 0.01 || rs[0].H <= 0 {
		t.Fatalf("rect %+v", rs[0])
	}
}

func TestTextBoxesAcrossLinesAndInlineElements(t *testing.T) {
	doc, res := recLayout(t, "<!doctype html><body style='margin:0;font:16px monospace;width:200px'><p>alpha beta <b>gamma delta</b> epsilon zeta eta theta</p>")
	ix := NewTextIndex(res)
	n := textNodeOf(doc, "gamma")
	bx := ix.Boxes(n)
	if len(bx) == 0 {
		t.Fatal("no runs for the bold text")
	}
	// 200px of 9.6px characters: the paragraph wraps, so a text node can have runs on several lines
	lines := map[float64]bool{}
	for k := range res.Text {
		lines[res.Text[k].Rect.Y] = true
	}
	if len(lines) < 2 {
		t.Fatalf("expected wrapped text, got %d line(s)", len(lines))
	}
	last := 0
	for k := range res.Text {
		if res.Text[k].LineEnd {
			last++
		}
	}
	if last != len(lines) {
		t.Fatalf("%d runs end a line, %d lines", last, len(lines))
	}
}

func TestUserSelectNoneIsRecordedAndSkipped(t *testing.T) {
	doc, res := recLayout(t, "<!doctype html><body style='margin:0;font:16px monospace'><p>visible</p><p style='user-select:none'>hidden</p>")
	ix := NewTextIndex(res)
	hid := ix.Boxes(textNodeOf(doc, "hidden"))
	if len(hid) != 1 || !hid[0].NoSelect {
		t.Fatalf("user-select:none must be recorded as such: %+v", hid)
	}
	// the position under the hidden line is not selectable: the nearest selectable line is the visible one
	p, _, _, ok := ix.PosAt(20, hid[0].Rect.Y+3, false)
	if !ok || p.Node != textNodeOf(doc, "visible") {
		t.Fatalf("PosAt over user-select:none text must land on selectable text, got %v", p.Node)
	}
	p, _, _, _ = ix.PosAt(20, hid[0].Rect.Y+3, true)
	if p.Node != textNodeOf(doc, "hidden") {
		t.Fatal("with all, the hidden text is found")
	}
}

func TestApplyHighlightsInsertsFillAndRecoloursGlyphs(t *testing.T) {
	doc, res := recLayout(t, "<!doctype html><body style='margin:0;font:16px monospace;color:#000'><p>select me please</p>")
	ix := NewTextIndex(res)
	n := textNodeOf(doc, "select")
	hl := []Highlight{{Node: n, From: 7, To: 9, Fill: Color{0, 120, 215, 255}, Text: Color{255, 255, 255, 255}, HasText: true}}
	list := ApplyHighlights(res.List, ix, hl, nil)
	var fill *FillRect
	runs := 0
	var white, black int
	for i, op := range list {
		switch o := op.(type) {
		case FillRect:
			if o.Color == (Color{0, 120, 215, 255}) {
				f := o
				fill = &f
				// the highlight comes right before the text it highlights
				if _, ok := list[i+1].(TextRun); !ok {
					t.Fatalf("the highlight must be followed by the text run, got %T", list[i+1])
				}
			}
		case TextRun:
			runs++
			if o.Color.R == 255 {
				white += len(o.Glyphs)
			} else {
				black += len(o.Glyphs)
			}
		}
	}
	if fill == nil {
		t.Fatal("no highlight rectangle")
	}
	tb := ix.Boxes(n)[1] // "me ": offsets 7..10
	if math.Abs(fill.Rect.X-tb.X(0)) > 0.01 || math.Abs(fill.Rect.W-(tb.X(2)-tb.X(0))) > 0.01 {
		t.Fatalf("highlight %+v does not cover characters 7..9", fill.Rect)
	}
	if white != 2 {
		t.Fatalf("2 glyphs inside the highlight should be white, got %d white and %d other", white, black)
	}
	if black != len("select me please")-2-2 { // the two spaces have no glyph
		t.Fatalf("the rest should stay black: %d", black)
	}
	// the display list of the layout is untouched
	for _, op := range res.List {
		if f, ok := op.(FillRect); ok && f.Color == (Color{0, 120, 215, 255}) {
			t.Fatal("ApplyHighlights modified the layout's list")
		}
	}
	// a caret is a thin filled bar at the offset
	list = ApplyHighlights(res.List, ix, nil, []Caret{{Node: n, Off: 3, Color: Color{0, 0, 0, 255}}})
	found := false
	for _, op := range list {
		if f, ok := op.(FillRect); ok && f.Rect.W == 1 && math.Abs(f.Rect.X-(ix.Boxes(n)[0].X(3)-0.5)) < 0.01 {
			found = true
		}
	}
	if !found {
		t.Fatal("no caret bar")
	}
}

func TestControlTextIsRecordedWithSoftWrap(t *testing.T) {
	doc, res := recLayout(t, "<!doctype html><body style='margin:0;font:16px monospace'><textarea id=ta cols=10 rows=4>one two three four five\nsix</textarea><input id=in value='hello field'><input id=em>")
	ix := NewTextIndex(res)
	var ta, in, em *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "id" {
					switch a.Val {
					case "ta":
						ta = n
					case "in":
						in = n
					case "em":
						em = n
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	lines := ix.Boxes(ta)
	if len(lines) < 4 {
		t.Fatalf("a 10-column textarea must wrap 'one two three four five' onto several lines, got %d lines in all", len(lines))
	}
	// lines cover the value without gaps: each starts where the previous ended (a newline takes one character)
	total := 0
	for i, l := range lines {
		if !l.Ctl {
			t.Fatal("control text is marked Ctl")
		}
		if i > 0 && l.FirstOff() < lines[i-1].LastOff() {
			t.Fatalf("line %d starts at %d before the previous ends at %d", i, l.FirstOff(), lines[i-1].LastOff())
		}
		total = l.LastOff()
	}
	if want := len("one two three four five\nsix"); total != want {
		t.Fatalf("last line ends at %d, value has %d characters", total, want)
	}
	ib := ix.Boxes(in)
	if len(ib) != 1 || len(ib[0].Xs) != len("hello field")+1 {
		t.Fatalf("input run: %+v", ib)
	}
	eb := ix.Boxes(em)
	if len(eb) != 1 || len(eb[0].Xs) != 1 {
		t.Fatalf("an empty input still has a caret position: %+v", eb)
	}
}
