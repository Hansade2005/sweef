package raster

import (
	"reflect"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/gofont/goregular"
)

// The embedded Go fonts are stored without their TrueType hinting program (tools/zpack/dehint.go). The rasteriser
// reads the raw 'glyf' data of a face itself (text_fx_glyf.go), so check that path too: every glyph of every
// embedded face yields exactly the contours the original x/image font does, at the identity scale and at another.
func TestEmbeddedGoFontsGlyfMatchOriginal(t *testing.T) {
	cases := []struct {
		family string
		st     font.Style
		orig   []byte
	}{
		{"go", font.Style{Weight: 400}, goregular.TTF},
		{"go", font.Style{Weight: 700}, gobold.TTF},
		{"go", font.Style{Weight: 400, Italic: true}, goitalic.TTF},
		{"go", font.Style{Weight: 700, Italic: true}, gobolditalic.TTF},
		{"go mono", font.Style{Weight: 400}, gomono.TTF},
		{"go mono", font.Style{Weight: 700}, gomonobold.TTF},
		{"go mono", font.Style{Weight: 400, Italic: true}, gomonoitalic.TTF},
		{"go mono", font.Style{Weight: 700, Italic: true}, gomonobolditalic.TTF},
	}
	for _, c := range cases {
		f := font.Lookup(c.family, c.st)
		if f == nil || f.Family != c.family || f.Style.Weight != c.st.Weight || f.Style.Italic != c.st.Italic {
			t.Fatalf("%s %+v: no embedded face (got %v)", c.family, c.st, f)
		}
		got, want := parseGlyf(f.Data()), parseGlyf(c.orig)
		if got == nil || want == nil || got.n != want.n || got.n < 500 {
			t.Fatalf("%s %+v: glyph tables unreadable or differ in size", c.family, c.st)
		}
		for _, sc := range [][2]int{{0x10000, 0x10000}, {0x2aaab, 0x24000}} {
			for gid := 0; gid < got.n; gid++ {
				a, okA := got.contours(gid, 0, sc[0], sc[1])
				b, okB := want.contours(gid, 0, sc[0], sc[1])
				if okA != okB || !reflect.DeepEqual(a, b) {
					t.Fatalf("%s %+v glyph %d at scale %x: contours differ from the original font", c.family, c.st, gid, sc)
				}
			}
		}
	}
}
