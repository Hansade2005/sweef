package style

import ("testing";"strings";"fmt")

func TestDbg(t *testing.T) {
	doc := parse(t, `<html><head><style>.align div { background: #fcc; }
.align .s { align-self: end; justify-self: end; width: 50px; height: 30px; }</style></head><body><div class="align"><div class="s"></div></div></body></html>`)
	e := NewEngine(&Env{Width: 1280, Height: 800}, []SheetIn{{Text: strings.Join(extractStyles(doc), "\n")}})
	h := e.Compute(find(doc, "html"), nil, "")
	b := e.Compute(find(doc, "body"), h, "")
	d := e.Compute(find(doc, "div"), b, "")
	s := e.Compute(find(find(doc,"div"),"div"), d, "")
	fmt.Printf("%q %q %v\n", s.JustifySelf, s.AlignSelf, s.Width)
}
