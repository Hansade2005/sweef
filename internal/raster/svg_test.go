package raster

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/render"
)

func TestRenderSVGBasics(t *testing.T) {
	doc := render.ParseSVG(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10" fill="#ff0000"/><circle cx="5" cy="5" r="2" fill="#00f"/></svg>`)
	if doc == nil {
		t.Fatal("no doc")
	}
	s := RenderSVG(doc, 20, 20, render.Color{A: 255})
	if c := s.ColorAt(1, 1); c.R != 255 || c.G != 0 || c.A != 255 {
		t.Errorf("corner = %+v, want red", c)
	}
	if c := s.ColorAt(10, 10); c.B != 255 || c.R != 0 {
		t.Errorf("centre = %+v, want blue", c)
	}
}

func TestRenderSVGHostile(t *testing.T) {
	cases := []string{
		`<svg><use href="#a"/><g id="a"><use href="#a"/></g></svg>`,
		`<svg><rect width="1e999" height="-4" fill="url(#nope)" stroke="url(#nope) red"/></svg>`,
		`<svg viewBox="0 0 0 0"><path d="M 1 2 L"/><path d="Z Z M"/><path d="M0 0 A 0 0 0 0 0 1 1"/></svg>`,
		`<svg><filter id="f"><feGaussianBlur stdDeviation="1e9"/></filter><rect width="10" height="10" filter="url(#f)"/></svg>`,
		`<svg><mask id="m"><rect mask="url(#m)" width="5" height="5" fill="#fff"/></mask><rect mask="url(#m)" width="5" height="5"/></svg>`,
		`<svg><pattern id="p" width="0" height="0"><rect/></pattern><rect width="4" height="4" fill="url(#p)"/></svg>`,
		`<svg><linearGradient id="g" href="#g"><stop/></linearGradient><rect width="4" height="4" fill="url(#g)"/></svg>`,
		`<svg><text x="1 2 3" y="4">abc<tspan dx="2">d</tspan></text><text/></svg>`,
		`<svg><g transform="scale(0)"><rect width="4" height="4"/></g><rect transform="matrix(1e30 0 0 1e30 0 0)" width="4" height="4"/></svg>`,
	}
	for i, m := range cases {
		doc := render.ParseSVG(m)
		if doc == nil {
			t.Errorf("case %d: nil doc", i)
			continue
		}
		_ = RenderSVG(doc, 16, 16, render.Color{A: 255})
	}
	if RenderSVG(nil, 4, 4, render.Color{}) == nil {
		t.Error("nil doc should still return a surface")
	}
}
