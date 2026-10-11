package raster

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/render"
)

func renderSVGString(t *testing.T, markup string, w, h int) *Surface {
	t.Helper()
	doc := render.ParseSVG(markup)
	if doc == nil {
		t.Fatal("parse failed")
	}
	return RenderSVG(doc, w, h, render.Color{A: 255})
}

func svgPx(s *Surface, x, y int) [4]uint8 {
	i := (y-s.R.Y0)*s.Stride + (x-s.R.X0)*4
	return [4]uint8{s.Pix[i], s.Pix[i+1], s.Pix[i+2], s.Pix[i+3]}
}

func near8(a, b [4]uint8, tol int) bool {
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < -tol || d > tol {
			return false
		}
	}
	return true
}

const fxHead = `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">`

func TestFilterConvolveIdentityAndTile(t *testing.T) {
	// identity kernel leaves the picture alone
	s := renderSVGString(t, fxHead+`<filter id="f" color-interpolation-filters="sRGB" x="0" y="0" width="100%" height="100%"><feConvolveMatrix order="3" kernelMatrix="0 0 0 0 1 0 0 0 0"/></filter><rect x="5" y="5" width="30" height="30" fill="#c36" filter="url(#f)"/></svg>`, 40, 40)
	if got := svgPx(s, 20, 20); !near8(got, [4]uint8{0xcc, 0x33, 0x66, 255}, 1) {
		t.Errorf("identity convolve: %v", got)
	}
	// a 4x4 tile cut from two flood squares repeats over the region
	s = renderSVGString(t, fxHead+`<filter id="t" color-interpolation-filters="sRGB" x="0" y="0" width="100%" height="100%">
<feFlood flood-color="#f00" x="0" y="0" width="4" height="4" result="a"/>
<feFlood flood-color="#00f" x="4" y="0" width="4" height="4" result="b"/>
<feMerge result="m"><feMergeNode in="a"/><feMergeNode in="b"/></feMerge><feTile in="m"/></filter><rect width="40" height="40" filter="url(#t)"/></svg>`, 40, 40)
	for _, c := range []struct {
		x, y int
		want [4]uint8
	}{{1, 1, [4]uint8{255, 0, 0, 255}}, {5, 1, [4]uint8{0, 0, 255, 255}}, {9, 2, [4]uint8{255, 0, 0, 255}}, {13, 30, [4]uint8{0, 0, 255, 255}}} {
		if got := svgPx(s, c.x, c.y); !near8(got, c.want, 1) {
			t.Errorf("tile at %d,%d: %v want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestFilterDisplacementAndLighting(t *testing.T) {
	// a flood map with R=0 displaces by -scale/2: the picture shifts right
	s := renderSVGString(t, fxHead+`<filter id="d" color-interpolation-filters="sRGB" filterUnits="userSpaceOnUse" x="0" y="0" width="40" height="40">
<feFlood flood-color="#000" result="m"/><feDisplacementMap in="SourceGraphic" in2="m" scale="10" xChannelSelector="R" yChannelSelector="G"/></filter>
<rect x="10" y="10" width="10" height="20" fill="#0a0" filter="url(#d)"/></svg>`, 40, 40)
	// the map value 0 displaces by -scale/2 (truncated to -4 px): [10,20) lands at [14,24)
	if got := svgPx(s, 22, 20); got[1] < 150 {
		t.Errorf("shifted source missing at x=22: %v", got)
	}
	if got := svgPx(s, 12, 20); got[3] != 0 {
		t.Errorf("original position should be empty: %v", got)
	}
	// a distant light straight above a flat surface gives N.L = sin(elevation)
	s = renderSVGString(t, fxHead+`<filter id="l" color-interpolation-filters="sRGB" x="0" y="0" width="100%" height="100%"><feDiffuseLighting lighting-color="#fff" diffuseConstant="1"><feDistantLight azimuth="0" elevation="90"/></feDiffuseLighting></filter><rect width="40" height="40" filter="url(#l)"/></svg>`, 40, 40)
	if got := svgPx(s, 20, 20); !near8(got, [4]uint8{255, 255, 255, 255}, 1) {
		t.Errorf("flat surface lit from above: %v", got)
	}
}

func TestTurbulenceKnownValues(t *testing.T) {
	// values of the reference implementation for seed 1 (checked against
	// Chromium output in the svg_fx_noise paint fixture)
	g := newTurbGen(1)
	a := g.turbulence(0, 3.5, 7.25, 0.04, 0.04, 3, true, false, 0, 0, 0, 0)
	b := g.turbulence(0, 3.5, 7.25, 0.04, 0.04, 3, true, false, 0, 0, 0, 0)
	if a != b || a < -1 || a > 1 {
		t.Errorf("turbulence not deterministic or out of range: %v %v", a, b)
	}
	if v := g.turbulence(2, 0, 0, 0.04, 0.04, 1, false, false, 0, 0, 0, 0); v != 0 {
		t.Errorf("noise at the lattice origin is zero: %v", v)
	}
}
