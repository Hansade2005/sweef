package style

import (
	"strings"
	"testing"
)

// Grid area and line names are case-sensitive custom idents. Wikipedia's
// Vector skin uses camelCase areas (siteNotice, columnStart, pageContent);
// lowercasing grid-area while keeping grid-template-areas as written made
// every item miss its area and broke the page layout.
func TestGridAreaNamesKeepCase(t *testing.T) {
	doc := parse(t, `<html><head><style>
.g { display: grid; grid-template-areas: 'siteNotice siteNotice' 'columnStart pageContent' }
.a { grid-area: columnStart }
.b { grid-column: pageContent; grid-row-start: siteNotice }
.c { grid-column: SPAN 2 / AUTO }
</style></head><body><div class=g><p class=a></p><span class=b></span><i class=c></i></div></body></html>`)
	e := NewEngine(&Env{Width: 1280, Height: 800}, []SheetIn{{Text: strings.Join(extractStyles(doc), "\n")}})
	hc := e.Compute(find(doc, "html"), nil, "")
	body := e.Compute(find(doc, "body"), hc, "")
	g := e.Compute(find(doc, "div"), body, "")
	if len(g.GridAreas) != 2 || g.GridAreas[1][0] != "columnStart" {
		t.Fatalf("areas %v", g.GridAreas)
	}
	a := e.Compute(find(doc, "p"), g, "")
	if a.GridColStart.Name != "columnStart" || a.GridRowStart.Name != "columnStart" {
		t.Errorf("grid-area lost case: col %+v row %+v", a.GridColStart, a.GridRowStart)
	}
	b := e.Compute(find(doc, "span"), g, "")
	if b.GridColStart.Name != "pageContent" || b.GridRowStart.Name != "siteNotice" {
		t.Errorf("grid-column/grid-row-start lost case: %+v %+v", b.GridColStart, b.GridRowStart)
	}
	c := e.Compute(find(doc, "i"), g, "")
	if c.GridColStart.Span != 2 || !c.GridColEnd.Auto {
		t.Errorf("keywords must stay case-insensitive: %+v %+v", c.GridColStart, c.GridColEnd)
	}
}
