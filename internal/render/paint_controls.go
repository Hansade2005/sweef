package render

import (
	"fmt"
	"math"
	"strings"

	"github.com/sweefbrowser/sweef/internal/style"
)

// Native form-control chrome. Shapes and colours were measured from Chromium
// 1194 on Linux (light scheme); each control is built as a tiny SVG scene and
// drawn through the SVG painter so edges are anti-aliased like Skia's.

const (
	ctlAccentHex   = "#0075ff"
	ctlBorderHex   = "#767676"
	ctlDisFillHex  = "#f8f8f8"
	ctlDisBorder   = "#d1d1d1"
	ctlTrackFill   = "#efefef"
	ctlTrackBorder = "#b2b2b2"
)

func (p *painter) snapRect(r Rect) Rect {
	d := p.c.opts.DPR
	if d <= 0 {
		d = 1
	}
	x0, y0 := math.Round(r.X*d), math.Round(r.Y*d)
	x1, y1 := math.Round((r.X+r.W)*d), math.Round((r.Y+r.H)*d)
	return Rect{x0 / d, y0 / d, (x1 - x0) / d, (y1 - y0) / d}
}

// ctlDoc returns the cached scene for generated markup.
func (p *painter) ctlDoc(markup string) *SVGDoc {
	if p.svgdocs == nil {
		p.svgdocs = map[string]*SVGDoc{}
	}
	key := "ctl:" + markup
	if d, ok := p.svgdocs[key]; ok {
		return d
	}
	d := parseStandaloneSVG(markup, nil, p.c)
	p.svgdocs[key] = d
	return d
}

// emitCtl draws markup (a full <svg> with a viewBox) into r.
func (p *painter) emitCtl(r Rect, markup string, b *lbox) {
	doc := p.ctlDoc(markup)
	if doc == nil {
		return
	}
	p.emit(SVGDraw{Dst: r, Doc: doc, Color: b.st.Color, FontSize: 16, Family: "serif", Weight: 400})
}

func f(v float64) string { return fmt.Sprintf("%.4g", v) }

func svgWrap(w, h float64, body string) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s">%s</svg>`, f(w), f(h), f(w), f(h), body)
}

// paintCheckRadio draws a checkbox or radio. It reports false when the
// control is author-styled (appearance:none etc.) and should not be touched.
func (p *painter) paintCheckRadio(b *lbox, radio bool) {
	r := p.snapRect(Rect{b.ax, b.ay, b.w, b.h})
	if r.W <= 0 || r.H <= 0 {
		return
	}
	w, h := r.W, r.H
	s := math.Min(w, h)
	checked := hasAttr(b.n, "checked")
	disabled := hasAttr(b.n, "disabled")
	var body string
	switch {
	case !radio:
		if checked {
			fill, chk := ctlAccentHex, "#fff"
			if disabled {
				fill, chk = ctlDisBorder, "#ededed"
			}
			body = fmt.Sprintf(`<rect width="%s" height="%s" rx="2" fill="%s"/><path d="M%s %s L%s %s L%s %s" fill="none" stroke="%s" stroke-width="%s"/>`,
				f(w), f(h), fill, f(0.2*w), f(0.5*h), f(0.4*w), f(0.705*h), f(0.8*w), f(0.2*h), chk, f(0.16*s))
		} else {
			fill, stroke := "#fff", ctlBorderHex
			if disabled {
				fill, stroke = ctlDisFillHex, ctlDisBorder
			}
			body = fmt.Sprintf(`<rect x=".5" y=".5" width="%s" height="%s" rx="2.25" fill="%s" stroke="%s"/>`, f(w-1), f(h-1), fill, stroke)
		}
	default:
		cx, cy, rr := w/2, h/2, s/2-0.5
		fill, stroke := "#fff", ctlBorderHex
		if disabled {
			fill, stroke = ctlDisFillHex, ctlDisBorder
		}
		if checked {
			stroke = ctlAccentHex
			if disabled {
				stroke = ctlDisBorder
			}
		}
		body = fmt.Sprintf(`<circle cx="%s" cy="%s" r="%s" fill="%s" stroke="%s"/>`, f(cx), f(cy), f(rr), fill, stroke)
		if checked {
			dot := ctlAccentHex
			if disabled {
				dot = ctlDisBorder
			}
			body += fmt.Sprintf(`<circle cx="%s" cy="%s" r="%s" fill="%s"/>`, f(cx), f(cy), f(0.3*s), dot)
		}
	}
	p.emitCtl(r, svgWrap(w, h, body), b)
}

func numAttr(b *lbox, k string, d float64) float64 {
	if v, ok := parseNum(attr(b.n, k)); ok {
		return v
	}
	return d
}

func parseNum(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%g", &v); err != nil {
		return 0, false
	}
	return v, true
}

func (p *painter) paintRange(b *lbox) {
	r := p.snapRect(Rect{b.ax, b.ay, b.w, b.h})
	// the control box includes 2px margins in Chromium's reported size only;
	// our box is the element box: 16px tall thumb area
	w, h := r.W, r.H
	if w <= 0 || h <= 0 {
		return
	}
	min, max := numAttr(b, "min", 0), numAttr(b, "max", 100)
	if max <= min {
		max = min + 100
	}
	val := numAttr(b, "value", min+(max-min)/2)
	frac := math.Max(0, math.Min(1, (val-min)/(max-min)))
	disabled := hasAttr(b.n, "disabled")
	trackFill, trackBorder := ctlTrackFill, ctlTrackBorder
	fillCol, fillBorder, thumb := ctlAccentHex, "#3b75ba", ctlAccentHex
	if disabled {
		trackFill, trackBorder = "#fafafa", "#d2d2d2"
		fillCol, fillBorder, thumb = "#cbcbcb", "#b1b1b1", "#cbcbcb"
	}
	th := 16.0
	tcy := h / 2
	tx := th/2 + (w-th)*frac
	ty := tcy - 4
	var sb strings.Builder
	fmt.Fprintf(&sb, `<defs><clipPath id="c"><rect x="0" y="0" width="%s" height="%s"/></clipPath></defs>`, f(tx), f(h))
	fmt.Fprintf(&sb, `<rect x="1.5" y="%s" width="%s" height="7" rx="3.5" fill="%s" stroke="%s"/>`, f(ty+.5), f(w-3), trackFill, trackBorder)
	fmt.Fprintf(&sb, `<rect x="1.5" y="%s" width="%s" height="7" rx="3.5" fill="%s" stroke="%s" clip-path="url(#c)"/>`, f(ty+.5), f(w-3), fillCol, fillBorder)
	fmt.Fprintf(&sb, `<circle cx="%s" cy="%s" r="8" fill="%s"/>`, f(tx), f(tcy), thumb)
	p.emitCtl(r, svgWrap(w, h, sb.String()), b)
}

// bar geometry shared by progress and meter: 7px (half the box, rounded)
// tall, vertically centred.
func (p *painter) barRect(b *lbox) (Rect, float64) {
	r := p.snapRect(Rect{b.ax, b.ay, b.w, b.h})
	bh := math.Round(r.H / 2)
	if bh < 1 {
		bh = 1
	}
	top := math.Round(r.Y + (r.H-bh)/2)
	return Rect{r.X, top, r.W, bh}, bh
}

func (p *painter) paintProgress(b *lbox) {
	bar, bh := p.barRect(b)
	if bar.W <= 0 {
		return
	}
	w := bar.W
	rad := bh / 2
	var sb strings.Builder
	// track: three nested rounded rects reproduce the 1px edge + soft inner row
	layer := func(inset float64, col string, width float64) {
		hh := bh - 2*inset
		if hh <= 0 || width-2*inset <= 0 {
			return
		}
		fmt.Fprintf(&sb, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`, f(inset), f(inset), f(width-2*inset), f(hh), f(math.Max(0, rad-inset)), col)
	}
	layer(0, "#cfcfcf", w)
	layer(1, "#dfdfdf", w)
	layer(2, ctlTrackFill, w)
	if v := attr(b.n, "value"); v != "" || false {
		max := numAttr(b, "max", 1)
		if max <= 0 {
			max = 1
		}
		val, _ := parseNum(v)
		frac := math.Max(0, math.Min(1, val/max))
		fw := math.Floor(w * frac)
		if frac >= 1 {
			fw = w
		}
		if fw > 0 {
			sb.WriteString(fmt.Sprintf(`<defs><clipPath id="c"><rect x="0" y="0" width="%s" height="%s"/></clipPath></defs><g clip-path="url(#c)">`, f(fw), f(bh)))
			layer(0, "#6c9bd4", w)
			layer(1, "#0e74ed", w)
			layer(2, ctlAccentHex, w)
			sb.WriteString(`</g>`)
		}
	}
	p.emitCtl(bar, svgWrap(w, bh, sb.String()), b)
}

func (p *painter) paintMeter(b *lbox) {
	bar, bh := p.barRect(b)
	if bar.W <= 0 {
		return
	}
	w := bar.W
	rad := bh / 2
	min, max := numAttr(b, "min", 0), numAttr(b, "max", 1)
	if max < min {
		max = min
	}
	val := math.Max(min, math.Min(max, numAttr(b, "value", 0)))
	low := math.Max(min, math.Min(max, numAttr(b, "low", min)))
	high := math.Max(low, math.Min(max, numAttr(b, "high", max)))
	opt := math.Max(min, math.Min(max, numAttr(b, "optimum", (min+max)/2)))
	region := func(v float64) int {
		switch {
		case v < low:
			return 0
		case v > high:
			return 2
		}
		return 1
	}
	ro, rv := region(opt), region(val)
	col := "#107c10"
	switch d := ro - rv; {
	case d == 0:
	case d == 1 || d == -1:
		col = "#ffb900"
	default:
		col = "#d83b01"
	}
	frac := 0.0
	if max > min {
		frac = (val - min) / (max - min)
	}
	fw := math.Round(w * frac)
	var sb strings.Builder
	fmt.Fprintf(&sb, `<rect width="%s" height="%s" rx="%s" fill="#cbcbcb"/>`, f(w), f(bh), f(rad))
	fmt.Fprintf(&sb, `<rect x="1" y="1" width="%s" height="%s" rx="%s" fill="%s"/>`, f(w-2), f(bh-2), f(math.Max(0, rad-1)), ctlTrackFill)
	if fw > 0 {
		fmt.Fprintf(&sb, `<defs><clipPath id="c"><rect width="%s" height="%s"/></clipPath></defs><rect x="1" y="1" width="%s" height="%s" rx="%s" fill="%s" clip-path="url(#c)"/>`,
			f(fw), f(bh), f(w-2), f(bh-2), f(math.Max(0, rad-1)), col)
	}
	p.emitCtl(bar, svgWrap(w, bh, sb.String()), b)
}

// paintSelectChevron draws the dropdown chevron of a (non-list) select.
func (p *painter) paintSelectChevron(b *lbox, col string) {
	r := p.snapRect(Rect{b.ax, b.ay, b.w, b.h})
	// chevron 8x4, stroke 2, 5px from the right edge, centred vertically
	cx := r.X + r.W - 9
	cy := r.Y + r.H/2 - 0.5
	rr := Rect{cx - 6, cy - 4, 12, 8}
	body := fmt.Sprintf(`<path d="M2 2 L6 6 L10 2" fill="none" stroke="%s" stroke-width="2"/>`, col)
	p.emitCtl(rr, svgWrap(12, 8, body), b)
}

// paintDateTime draws the placeholder fields and the picker icon of
// date/time inputs (Chromium shows "mm/dd/yyyy" / "--:-- --" in monospace).
func (p *painter) paintDateTime(b *lbox, st *style.Computed, fs *fontSpec, cr Rect, baseY float64, col Color) {
	t := b.rep.ctrl
	txt := "mm/dd/yyyy"
	switch t {
	case "time":
		txt = "--:-- --"
	case "datetime-local":
		txt = "mm/dd/yyyy, --:-- --"
	case "month":
		txt = "---------- ----"
	case "week":
		txt = "Week --, ----"
	}
	if v := attr(b.n, "value"); v != "" {
		txt = v
	}
	cp := *st
	cp.FontFamily = []string{"monospace"}
	mfs := p.c.spec(&cp)
	p.emitClippedText(&cp, mfs, txt, Rect{cr.X + 1, cr.Y, cr.W - 22, cr.H}, baseY, col)
	// picker icon, 11px, 16px from the right edge, vertically centred
	r := p.snapRect(Rect{b.ax, b.ay, b.w, b.h})
	ix := r.X + r.W - 16
	iy := r.Y + math.Round((r.H-11)/2)
	ic := Rect{ix, iy, 12, 12}
	var body string
	if t == "time" {
		body = `<circle cx="6" cy="6" r="5.3" fill="none" stroke="#000" stroke-width="1.2"/><path d="M6 3 V6.2 L8.2 7.6" fill="none" stroke="#000" stroke-width="1.1"/>`
	} else {
		body = `<path d="M1 3 V10 H10 V3" fill="none" stroke="#000" stroke-opacity=".5" stroke-width="1"/><rect x="1" y="1" width="9" height="2" fill="#000"/>`
	}
	p.emitCtl(ic, svgWrap(12, 12, body), b)
}
