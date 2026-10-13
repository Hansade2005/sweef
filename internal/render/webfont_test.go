package render

import (
	"math"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

// A layout with a web font set asks for the face its text needs, shows the fallback meanwhile and
// uses the face (ligatures of an icon font included) in the next pass.
func TestWebFontSetDrivesTextLayout(t *testing.T) {
	var asked []string
	set := font.NewWebSet(func(f *font.WebFace) { asked = append(asked, f.Desc.Family) }, nil)
	defer set.Close()
	set.SetSheetFaces([]font.FaceDesc{
		{Family: "Icons", Src: []font.Source{{URL: "i.woff2"}}},
		{Family: "Unused", Src: []font.Source{{URL: "u.woff2"}}},
	})
	src := `<body style="margin:0;font:24px 'Icons',serif"><p><span id="i">arrow_back</span></p></body>`
	run := func() *Result {
		doc, _ := html.Parse(strings.NewReader(src))
		return Layout(doc, Options{Width: 400, Height: 300, DPR: 1, Fonts: set})
	}
	width := func(res *Result) (w float64, lig int) {
		for _, tr := range textRuns(res) {
			w += tr.Width
			for _, g := range tr.Glyphs {
				if g.GID != 0 {
					lig++
				}
			}
		}
		return
	}
	w0, l0 := width(run())
	if len(asked) != 1 || asked[0] != "Icons" {
		t.Fatalf("faces asked for: %v (only the family the text uses)", asked)
	}
	if l0 != 0 || w0 < 40 {
		t.Fatalf("fallback pass: width %.1f, ligatures %d", w0, l0)
	}
	sf, _, err := font.Decode(fonttest.Icons("Icons").SFNT(), font.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range set.Faces() {
		if f.Desc.Family == "Icons" {
			if err := f.SetData(sf); err != nil {
				t.Fatal(err)
			}
		}
	}
	w1, l1 := width(run())
	if l1 != 1 || math.Abs(w1-24) > 0.01 {
		t.Fatalf("web font pass: width %.2f (want one 24px icon), ligature glyphs %d", w1, l1)
	}
	if len(asked) != 1 {
		t.Fatalf("a loaded face must not be asked for again: %v", asked)
	}
}
