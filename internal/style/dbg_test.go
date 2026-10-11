package style

import ("testing";"strings";"fmt")

func TestDbg(t *testing.T) {
	doc := parse(t, `<html><head><style>.row{display:flex;gap:12px} .between { justify-content: space-between; }</style></head><body><div class="row between"></div></body></html>`)
	e := NewEngine(&Env{Width: 1280, Height: 800}, []SheetIn{{Text: strings.Join(extractStyles(doc), "\n")}})
	h := e.Compute(find(doc, "html"), nil, "")
	b := e.Compute(find(doc, "body"), h, "")
	d := e.Compute(find(doc, "div"), b, "")
	fmt.Printf("%q %q\n", d.JustifyContent, d.Display)
}
