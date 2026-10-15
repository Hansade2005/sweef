package style

import (
	"reflect"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
)

var testEnv = &Env{Width: 1280, Height: 800, DPR: 1, ColorScheme: "light"}

func TestFontFaceDescriptors(t *testing.T) {
	css := `
@font-face {
  font-family: "Open Sans";
  src: local("Open Sans Regular"), local(OpenSans-Regular),
       url(/fonts/os.woff2) format("woff2") tech(variations),
       url('/fonts/os.woff') format('woff'), url(os.ttf);
  font-weight: 300 800;
  font-style: oblique 10deg 20deg;
  font-stretch: 75% 125%;
  unicode-range: U+0000-00FF, U+0131, U+0152-0153, U+4??, u+25;
  font-display: swap;
  font-feature-settings: "liga" 0, "kern";
  font-variation-settings: "wght" 450;
  size-adjust: 107.5%;
  ascent-override: 90%;
}
@font-face { font-family: Plain; src: url(a.woff2); }
@font-face { font-family: serif; src: url(b.woff2); }
@font-face { src: url(c.woff2); }
@media (max-width: 100px) { @font-face { font-family: Narrow; src: url(n.woff2); } }
@media (min-width: 100px) { @font-face { font-family: Wide; src: url(w.woff2); font-weight: bold; font-style: italic } }
@supports (display: grid) { @font-face { font-family: Sup; src: url(s.woff2) } }
@font-face { font-family: Data; src: url(data:font/woff2;base64,AAAA/BBB+CC==) format("woff2"); unicode-range: bogus }
`
	ffs := ParseFontFaces(css, testEnv)
	var names []string
	for _, f := range ffs {
		names = append(names, f.Desc.Family)
	}
	if want := []string{"Open Sans", "Plain", "Wide", "Sup", "Data"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("families %v, want %v", names, want)
	}
	d := ffs[0].Desc
	if d.WMin != 300 || d.WMax != 800 || d.Style != font.StyleOblique || d.SMin != 75 || d.SMax != 125 {
		t.Errorf("ranges: %+v", d)
	}
	if want := []font.URange{{0, 0xff}, {0x131, 0x131}, {0x152, 0x153}, {0x400, 0x4ff}, {0x25, 0x25}}; !reflect.DeepEqual(d.Ranges, want) {
		t.Errorf("unicode-range %v, want %v", d.Ranges, want)
	}
	wantSrc := []font.Source{
		{Local: "Open Sans Regular"}, {Local: "OpenSans-Regular"},
		{URL: "/fonts/os.woff2", Format: "woff2", Tech: []string{"variations"}},
		{URL: "/fonts/os.woff", Format: "woff"}, {URL: "os.ttf"},
	}
	if !reflect.DeepEqual(d.Src, wantSrc) {
		t.Errorf("src %+v, want %+v", d.Src, wantSrc)
	}
	if d.Display != "swap" || d.Features != `"liga" 0, "kern"` || d.Variations != `"wght" 450` {
		t.Errorf("settings: %q %q %q", d.Display, d.Features, d.Variations)
	}
	if d.SizeAdjust != 1.075 || d.AscentOverride != 0.9 || d.Overrides != font.OvrAscent {
		t.Errorf("metric overrides: %v %v %v", d.SizeAdjust, d.AscentOverride, d.DescentOverride)
	}
	if p := ffs[1].Desc; p.WMin != 400 || p.WMax != 400 || p.SMin != 100 || p.Style != 0 || p.Ranges != nil {
		t.Errorf("defaults: %+v", p)
	}
	if w := ffs[2].Desc; w.WMin != 700 || w.Style != font.StyleItalic {
		t.Errorf("wide: %+v", w)
	}
	data := ffs[4].Desc
	if len(data.Src) != 1 || data.Src[0].URL != "data:font/woff2;base64,AAAA/BBB+CC==" || data.Ranges != nil {
		t.Errorf("data face: %+v", data)
	}
}

func TestFontFaceInStyleEngine(t *testing.T) {
	e := NewEngine(testEnv, []SheetIn{{Text: `@font-face{font-family:X;src:url(x.woff)}`}})
	if len(e.Fonts) != 1 || e.Fonts[0].Desc.Src[0].URL != "x.woff" {
		t.Fatalf("engine fonts: %+v", e.Fonts)
	}
}

// Minified CSS writes `url(x.woff2)format("woff2")` with no space: nextjs.org's fonts were requested as
// `x.woff2)format("woff2"` and fell back.
func TestFaceSrcWithoutSpaces(t *testing.T) {
	got := parseFaceSrc(`url(../media/Geist.woff2)format("woff2"),url("a b.woff") format('woff'),local(Arial)`)
	if len(got) != 3 || got[0].URL != "../media/Geist.woff2" || got[0].Format != "woff2" || got[1].URL != "a b.woff" || got[1].Format != "woff" || got[2].Local != "Arial" {
		t.Fatalf("got %+v", got)
	}
}
