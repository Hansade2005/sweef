package raster

import (
	"fmt"

	"github.com/sweefbrowser/sweef/internal/render"
)

// svgCell is one 140x90 SVG scene placed on the 300x200 paint canvas.
type svgCell struct {
	x, y   float64
	markup string
}

func svgPaintCase(name string, cells ...svgCell) paintCase {
	var html string
	for _, c := range cells {
		html += fmt.Sprintf(`<div style="position:absolute;left:%gpx;top:%gpx">%s</div>`, c.x, c.y, c.markup)
	}
	return paintCase{name, html, func() []render.Op {
		var ops []render.Op
		for _, c := range cells {
			ops = append(ops, render.SVGDraw{Dst: render.Rect{X: c.x, Y: c.y, W: 140, H: 90}, Doc: render.ParseSVG(c.markup), Color: rc(0, 0, 0), FontSize: 16, Family: "serif", Weight: 400})
		}
		return ops
	}}
}

const svgNS = `xmlns="http://www.w3.org/2000/svg"`

func svgCellMarkup(body string) string {
	return `<svg ` + svgNS + ` width="140" height="90" viewBox="0 0 140 90">` + body + `</svg>`
}

// paintSVGCases are the SVG fixtures that go through Chromium references:
// the feTurbulence / displacement / lighting / convolve / tile / image
// primitives, text on a path and per-glyph positioning, CSS basic shapes as
// clip-path on SVG elements and nested SVG images.
func paintSVGCases() []paintCase {
	var cases []paintCase
	cases = append(cases, svgPaintCase("svg_fx_noise",
		svgCell{5, 5, svgCellMarkup(`<filter id="t1" x="0" y="0" width="100%" height="100%"><feTurbulence type="fractalNoise" baseFrequency="0.05" numOctaves="3" seed="4"/></filter><rect width="140" height="90" filter="url(#t1)"/>`)},
		svgCell{155, 5, svgCellMarkup(`<filter id="t2" x="0" y="0" width="100%" height="100%"><feTurbulence type="turbulence" baseFrequency="0.04 0.09" numOctaves="2" seed="1"/><feColorMatrix type="saturate" values="0.2"/></filter><rect width="140" height="90" filter="url(#t2)"/>`)},
		svgCell{5, 105, svgCellMarkup(`<filter id="d" x="-10%" y="-10%" width="120%" height="120%"><feTurbulence type="turbulence" baseFrequency="0.05" numOctaves="2" result="n"/><feDisplacementMap in="SourceGraphic" in2="n" scale="18" xChannelSelector="R" yChannelSelector="B"/></filter><g filter="url(#d)"><rect x="20" y="15" width="100" height="60" fill="#38a"/><circle cx="70" cy="45" r="20" fill="#fc3"/></g>`)},
		svgCell{155, 105, svgCellMarkup(`<filter id="l"><feDiffuseLighting surfaceScale="4" diffuseConstant="1" lighting-color="#fff" in="SourceAlpha"><fePointLight x="30" y="20" z="45"/></feDiffuseLighting><feComposite in2="SourceGraphic" operator="in"/></filter><rect x="15" y="10" width="110" height="70" rx="18" fill="#9ab" filter="url(#l)"/>`)},
	))
	cases = append(cases, svgPaintCase("svg_fx_misc",
		svgCell{5, 5, svgCellMarkup(`<filter id="c"><feConvolveMatrix order="3" kernelMatrix="0 -1 0 -1 5 -1 0 -1 0"/></filter><rect x="15" y="12" width="50" height="40" fill="#36c" filter="url(#c)"/><circle cx="100" cy="45" r="28" fill="#c63" filter="url(#c)"/>`)},
		svgCell{155, 5, svgCellMarkup(`<filter id="tl" x="0" y="0" width="100%" height="100%"><feFlood flood-color="#e33" x="0" y="0" width="10" height="10"/><feOffset dx="10" dy="10" result="a"/><feFlood flood-color="#33e" x="10" y="0" width="10" height="10" result="b"/><feMerge result="m"><feMergeNode in="a"/><feMergeNode in="b"/></feMerge><feTile in="m"/></filter><rect width="140" height="90" filter="url(#tl)"/>`)},
		svgCell{5, 105, svgCellMarkup(`<filter id="i" x="0" y="0" width="100%" height="100%"><feImage x="10" y="10" width="60" height="60" href="data:image/svg+xml;utf8,%3Csvg xmlns='http://www.w3.org/2000/svg' width='40' height='40'%3E%3Ccircle cx='20' cy='20' r='18' fill='%23c33'/%3E%3C/svg%3E"/></filter><rect width="140" height="90" fill="#eef" filter="url(#i)"/>`)},
		svgCell{155, 105, svgCellMarkup(`<linearGradient id="g" x2="1"><stop offset="0" stop-color="#f00"/><stop offset=".5" stop-color="#0f0"/><stop offset="1" stop-color="#00f"/></linearGradient><filter id="ct"><feComponentTransfer><feFuncR type="gamma" amplitude="1.4" exponent="2" offset="0.05"/><feFuncG type="discrete" tableValues="0 1 0.5"/><feFuncB type="table" tableValues="1"/></feComponentTransfer></filter><rect x="10" y="10" width="120" height="70" fill="url(#g)" filter="url(#ct)"/>`)},
	))
	cases = append(cases, svgPaintCase("svg_text2",
		svgCell{5, 5, svgCellMarkup(`<path id="p" d="M8 75 C 30 10, 90 10, 132 60" fill="none" stroke="#ccc"/><text font-family="Liberation Sans" font-size="14" fill="#c23"><textPath href="#p" startOffset="6">Text on a curved path</textPath></text>`)},
		svgCell{155, 5, svgCellMarkup(`<text x="8 30 52 74 96" y="25 29 23 31 26" font-family="Liberation Sans" font-size="18" fill="#05a">ABCDE</text><text x="10" y="62" rotate="0 8 16 24 32 40 48" font-family="Liberation Sans" font-size="16" fill="#080">Rotated</text><text x="10" y="82" dx="3 6 9" dy="0 -3 3" font-family="Liberation Sans" font-size="14" fill="#a50">dx dy list</text>`)},
		svgCell{5, 105, svgCellMarkup(`<text x="8" y="22" font-family="Liberation Sans" font-size="14" textLength="120" fill="#609">spacing adjusted</text><text x="8" y="46" font-family="Liberation Sans" font-size="14" textLength="120" lengthAdjust="spacingAndGlyphs" fill="#c23">glyphs stretched</text><text x="8" y="72" font-family="Liberation Sans" font-size="16" fill="#333">base<tspan baseline-shift="super" font-size="10" fill="#c00">sup</tspan><tspan baseline-shift="sub" font-size="10" fill="#00c">sub</tspan></text>`)},
		svgCell{155, 105, svgCellMarkup(`<line x1="70" y1="0" x2="70" y2="90" stroke="#ccc"/><text x="70" y="20" text-anchor="start" font-family="Liberation Sans" font-size="13" fill="#c23">start anchor</text><text x="70" y="45" text-anchor="middle" font-family="Liberation Sans" font-size="13" fill="#05a">middle anchor</text><text x="70" y="70" text-anchor="end" font-family="Liberation Sans" font-size="13" fill="#080">end anchor</text>`)},
	))
	// foreignObject inside pattern, mask, clipPath and use'd symbols/elements:
	// Chromium paints no HTML content in these contexts (a pattern tile or mask
	// from HTML is empty, a clipPath ignores foreignObject, use never
	// instantiates it), so only the SVG shapes appear; the symbol's own
	// width/height size the use.
	const fo = `<foreignObject x="0" y="0" width="40" height="30"><div xmlns="http://www.w3.org/1999/xhtml" style="width:100%;height:100%;background:#fc6"></div></foreignObject>`
	cases = append(cases, svgPaintCase("svg_foreign_ctx",
		svgCell{5, 5, svgCellMarkup(`<defs><pattern id="p" width="40" height="30" patternUnits="userSpaceOnUse">` + fo + `</pattern></defs><rect x="10" y="10" width="120" height="70" fill="url(#p)" stroke="#333"/>`)},
		svgCell{155, 5, svgCellMarkup(`<defs><mask id="m"><foreignObject x="0" y="0" width="140" height="90"><div xmlns="http://www.w3.org/1999/xhtml" style="width:140px;height:90px;background:#fff"></div></foreignObject></mask></defs><rect x="10" y="10" width="120" height="70" fill="#c33" mask="url(#m)"/><circle cx="70" cy="45" r="12" fill="#36c"/>`)},
		svgCell{5, 105, svgCellMarkup(`<defs><clipPath id="c"><circle cx="45" cy="45" r="35"/><foreignObject x="80" y="10" width="50" height="60"><div xmlns="http://www.w3.org/1999/xhtml" style="width:100%;height:100%;background:#000"></div></foreignObject></clipPath></defs><rect width="140" height="90" fill="#e90" clip-path="url(#c)"/>`)},
		svgCell{155, 105, svgCellMarkup(`<defs><symbol id="s" viewBox="0 0 50 30" width="80" height="48"><rect width="50" height="30" fill="#dfd" stroke="#070"/>` + fo + `</symbol><foreignObject id="f" width="40" height="30"><div xmlns="http://www.w3.org/1999/xhtml" style="width:100%;height:100%;background:#fc6"></div></foreignObject></defs><use href="#s" x="5" y="5"/><use href="#s" x="90" y="40" width="40" height="40"/><use href="#f" x="5" y="60"/>`)},
	))
	cases = append(cases, svgPaintCase("svg_clipshape",
		svgCell{5, 5, svgCellMarkup(`<rect x="15" y="10" width="110" height="70" fill="#36c" stroke="#c33" stroke-width="10" style="clip-path:circle(28px at 50% 50%)"/>`)},
		svgCell{155, 5, svgCellMarkup(`<rect x="15" y="10" width="110" height="70" fill="#6a3" stroke="#222" stroke-width="8" style="clip-path:inset(8px 16px round 10px)"/>`)},
		svgCell{5, 105, svgCellMarkup(`<rect x="15" y="10" width="110" height="70" fill="#c63" style="clip-path:polygon(50% 0,100% 100%,0 100%)"/>`)},
		svgCell{155, 105, svgCellMarkup(`<g transform="translate(20 8) rotate(12)" style="clip-path:ellipse(40% 30% at 50% 50%)"><rect width="100" height="60" fill="#a3c"/><circle cx="50" cy="30" r="14" fill="#fc3"/></g>`)},
	))
	cases = append(cases, svgPaintCase("svg_image",
		svgCell{5, 5, svgCellMarkup(`<image width="140" height="90" href="data:image/svg+xml;utf8,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 40 30'%3E%3Crect width='40' height='30' fill='%23fe8'/%3E%3Ccircle cx='20' cy='15' r='10' fill='%23c33'/%3E%3C/svg%3E"/>`)},
		svgCell{155, 5, svgCellMarkup(`<image x="10" y="5" width="120" height="40" href="data:image/svg+xml;utf8,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 40 30'%3E%3Crect width='40' height='30' fill='%2358c'/%3E%3C/svg%3E"/>`)},
		svgCell{5, 105, svgCellMarkup(`<style>@media (prefers-color-scheme:dark){rect{fill:#206}}</style><rect width="140" height="90" fill="#3a7"/>`)},
	))
	return cases
}
