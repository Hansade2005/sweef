package render

import (
	"image"
	"math"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/style"
)

// ---- text --------------------------------------------------------------------------------------

type decoInfo struct {
	lines uint8
	style string
	color Color
	thick float64
}

func (p *painter) decorators(owner *lbox) []decoInfo {
	var out []decoInfo
	for x := owner; x != nil; x = x.parent {
		if x.TextDecorLineSet() {
			col := x.st.TextDecorColor
			if x.st.DecorCur {
				col = x.st.Color
			}
			th := 0.0
			if x.st.TextDecorationThickness.K == style.LPx {
				th = x.st.TextDecorationThickness.V
			}
			out = append(out, decoInfo{lines: x.st.TextDecorLine, style: x.st.TextDecorStyle, color: col, thick: th})
		}
		if x.floated || x.oof || x.kind == kAtomic {
			break
		}
	}
	return out
}

func (b *lbox) TextDecorLineSet() bool { return b.st.TextDecorLine != 0 }

func (p *painter) paintLines(b *lbox) {
	if len(b.lines) == 0 {
		return
	}
	for _, ln := range b.lines {
		// inline box decorations first
		for _, lb := range ln.boxes {
			bx := lb.box
			if bx.st.Visibility != "visible" && bx.st.Visibility != "" {
				continue
			}
			p.paintInlineFrag(bx, lb)
		}
		for _, fr := range ln.frags {
			switch fr.kind {
			case aText:
				p.paintTextFrag(b, ln, fr)
			case aAtomic:
				if !p.isDeferred(fr.box) {
					p.paintAtomic(fr.box)
				}
			}
		}
	}
}

func (p *painter) paintInlineFrag(bx *lbox, lb lineBox) {
	st := bx.st
	r := lb.rect
	if r.W < 0 || r.H <= 0 {
		return
	}
	vis := true
	_ = vis
	// radii for the fragment
	var rad Radii
	for i := 0; i < 4; i++ {
		h := rl(st.Radius[i][0], r.W)
		v := rl(st.Radius[i][1], r.H)
		if h > 0 && v > 0 {
			rad[i] = [2]float64{h, v}
		}
	}
	if lb.contL {
		rad[0], rad[3] = [2]float64{}, [2]float64{}
	}
	if lb.contR {
		rad[1], rad[2] = [2]float64{}, [2]float64{}
	}
	for _, sh := range st.BoxShadow {
		if !sh.Inset {
			p.emit(BoxShadow{Rect: r, Radii: rad, DX: sh.DX, DY: sh.DY, Blur: sh.Blur, Spread: sh.Spread, Color: sh.Color})
		}
	}
	if st.BgColor.A != 0 {
		p.emit(FillRect{Rect: r, Color: st.BgColor, Radii: rad})
	}
	for i := len(st.BgLayers) - 1; i >= 0; i-- {
		ly := st.BgLayers[i]
		if !ly.Image.IsNone() && ly.Clip != "text" {
			p.paintBgLayer(bx, ly, r, rad, r)
		}
	}
	bw := bx.bor
	if lb.contL {
		bw[3] = 0
	}
	if lb.contR {
		bw[1] = 0
	}
	if bw[0] > 0 || bw[1] > 0 || bw[2] > 0 || bw[3] > 0 {
		p.emit(Border{Rect: r, Widths: bw, Colors: st.BorderColor, Styles: st.BorderStyle, Radii: rad})
	}
	if bx.n != nil {
		p.addHit(bx, r)
	}
}

func (p *painter) paintTextFrag(b *lbox, ln *line, fr *frag) {
	it := fr.it
	st := it.st
	owner := fr.box
	if owner == nil {
		owner = b
	}
	if st.Visibility != "visible" && st.Visibility != "" {
		return
	}
	x := b.ax + ln.x + fr.x
	y := b.ay + ln.y + fr.base
	fs := it.fs
	e := fr.e
	// drop hanging trailing spaces
	if fr.trail > 0 {
		for e > fr.a && it.runes[e-1] == ' ' && st.WhiteSpace != "pre-wrap" {
			e--
		}
	}
	var gl []Glyph
	sh := it.sh
	extra := 0.0
	for k := fr.a; k < e; k++ {
		r := it.runes[k]
		if r == '\n' || r == '\t' || isZeroWidth(r) && r != 0xad {
			if r == ' ' {
				extra += fr.justSp
			}
			continue
		}
		if r == 0xad {
			continue
		}
		if r != ' ' && r != 0xa0 {
			gl = append(gl, Glyph{R: r, X: sh.x[k] - sh.x[fr.a] + extra, Face: sh.faces[k]})
		}
		if r == ' ' {
			extra += fr.justSp
		}
	}
	w := fr.w
	if len(gl) == 0 && st.TextDecorLine == 0 {
		return
	}
	col := st.TextFill
	if !st.TextFillSet {
		col = st.Color
	}
	tr := TextRun{X: x, Y: y, Size: fs.size, Color: col, Glyphs: gl, Width: w}
	if fs.synthBold {
		tr.Bold = true
	}
	if st.FontStyle != 0 && fs.primary != nil && !fs.primary.Style.Italic {
		tr.Italic = true
	}
	for _, s := range st.TextShadow {
		tr.Shadows = append(tr.Shadows, TextShadow{DX: s.DX, DY: s.DY, Blur: s.Blur, Color: s.Color})
	}
	if st.TextStrokeW > 0 {
		tr.Stroke, tr.StrokeColor = st.TextStrokeW, st.TextStrokeC
	}
	decos := p.decorators(owner)
	// the first decorator rides on the glyph run
	if len(decos) > 0 {
		d := decos[0]
		tr.Underline = d.lines&style.DecoUnderline != 0
		tr.Overline = d.lines&style.DecoOverline != 0
		tr.LineThrough = d.lines&style.DecoLineThrough != 0
		tr.DecoStyle, tr.DecoColor = d.style, d.color
	}
	if len(gl) == 0 && len(decos) == 0 {
		return
	}
	p.emit(tr)
	for _, d := range decos[min(1, len(decos)):] {
		p.emit(TextRun{X: x, Y: y, Size: fs.size, Color: d.color, Width: w, Underline: d.lines&style.DecoUnderline != 0,
			Overline: d.lines&style.DecoOverline != 0, LineThrough: d.lines&style.DecoLineThrough != 0, DecoStyle: d.style, DecoColor: d.color})
	}
	if owner.n != nil {
		p.hits = append(p.hits, hitRec{n: owner.n, rects: []Rect{{x, y - fs.asc, w, fs.asc + fs.desc}}, clips: p.clips, m: p.m, hasM: p.hasM, noPE: owner.st.PointerEvents == "none"})
	} else if b.n != nil {
		p.hits = append(p.hits, hitRec{n: b.n, rects: []Rect{{x, y - fs.asc, w, fs.asc + fs.desc}}, clips: p.clips, m: p.m, hasM: p.hasM, noPE: b.st.PointerEvents == "none"})
	}
}

// ---- replaced content, controls and markers -------------------------------------------------

func (p *painter) paintSpecial(b *lbox) {
	if b.marker != nil {
		p.paintMarker(b)
	}
	if b.rep == nil {
		if b.isBtn {
			p.paintButtonChrome(b)
		}
		return
	}
	if b.inner == iReplaced {
		p.paintReplaced(b)
		return
	}
	if b.inner == iControl {
		p.paintControl(b)
	}
}

func (p *painter) contentRect(b *lbox) Rect {
	return Rect{b.ax + b.bor[3] + b.pad[3], b.ay + b.bor[0] + b.pad[0], b.w - b.hExtra() - b.sbW, b.h - b.vExtra() - b.sbH}
}

func (p *painter) paintReplaced(b *lbox) {
	r := b.rep
	cr := p.contentRect(b)
	if cr.W <= 0 || cr.H <= 0 {
		return
	}
	rad := shrinkRadii(p.radii(b), [4]float64{b.bor[0] + b.pad[0], b.bor[1] + b.pad[1], b.bor[2] + b.pad[2], b.bor[3] + b.pad[3]})
	if r.bitmap || r.kind == "canvas" || r.kind == "video" {
		if r.img != nil || r.bitmap {
			fit := b.st.ObjectFit
			p.emit(Bitmap{Node: b.n, Dst: cr, Fit: fit, Pos: [2]float64{b.st.ObjectPos[0].Resolve(1), b.st.ObjectPos[1].Resolve(1)}, Radii: rad})
		}
		return
	}
	if r.img == nil {
		return
	}
	iw, ih := r.iw, r.ih
	if iw <= 0 || ih <= 0 {
		return
	}
	dst, src := fitImage(b.st.ObjectFit, b.st.ObjectPos, cr, iw, ih)
	p.emit(Image{Img: r.img, Dst: dst, Src: src, Radii: rad, Smooth: b.st.ImageRendering != "pixelated" && b.st.ImageRendering != "crisp-edges"})
}

// fitImage computes the destination and source rectangles for object-fit.
func fitImage(fit string, pos [2]style.Length, box Rect, iw, ih float64) (Rect, Rect) {
	var dw, dh float64
	switch fit {
	case "contain":
		s := math.Min(box.W/iw, box.H/ih)
		dw, dh = iw*s, ih*s
	case "cover":
		s := math.Max(box.W/iw, box.H/ih)
		dw, dh = iw*s, ih*s
	case "none":
		dw, dh = iw, ih
	case "scale-down":
		s := math.Min(1, math.Min(box.W/iw, box.H/ih))
		dw, dh = iw*s, ih*s
	default:
		return box, Rect{}
	}
	dx := box.X + pos[0].Resolve(box.W-dw)
	dy := box.Y + pos[1].Resolve(box.H-dh)
	// clip to the box
	x0, y0 := math.Max(dx, box.X), math.Max(dy, box.Y)
	x1, y1 := math.Min(dx+dw, box.X+box.W), math.Min(dy+dh, box.Y+box.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}, Rect{}
	}
	sx, sy := iw/dw, ih/dh
	return Rect{x0, y0, x1 - x0, y1 - y0}, Rect{(x0 - dx) * sx, (y0 - dy) * sy, (x1 - x0) * sx, (y1 - y0) * sy}
}

func (p *painter) markerText(b *lbox) (string, string) {
	st := b.st
	t := st.ListStyleType
	if strings.HasPrefix(t, "\x00") {
		return t[1:], "text"
	}
	switch t {
	case "disc", "circle", "square":
		return "", t
	case "none":
		return "", "none"
	case "disclosure-open", "disclosure-closed":
		return "", t
	}
	n := b.marker.liIndex
	s := formatCounter(n, t)
	switch t {
	case "decimal", "decimal-leading-zero", "lower-alpha", "upper-alpha", "lower-latin", "upper-latin", "lower-roman", "upper-roman", "lower-greek":
		s += ". "
	default:
		s += ". "
	}
	return s, "text"
}

func (p *painter) paintMarker(b *lbox) {
	m := b.marker
	st := b.st
	if !m.markerOutside {
		return
	}
	if st.Visibility != "visible" && st.Visibility != "" {
		return
	}
	if !b.hasBase {
		return
	}
	fs := p.c.spec(st)
	content := b.ax + b.bor[3] + b.pad[3]
	base := b.ay + b.baseline
	if !st.ListStyleImage.IsNone() {
		if im := p.c.env.Image(st.ListStyleImage.URL); im != nil {
			bd := im.Bounds()
			iw, ih := float64(bd.Dx()), float64(bd.Dy())
			p.emit(Image{Img: im, Dst: Rect{content - iw - 8, base - ih, iw, ih}, Smooth: true})
			return
		}
	}
	txt, kind := p.markerText(b)
	switch kind {
	case "none":
		return
	case "disc", "circle", "square":
		asc := fs.asc
		bw := math.Floor((math.Floor(asc*2/3) + 1) / 2)
		if bw < 1 {
			bw = 1
		}
		yo := math.Floor(3 * (asc - math.Floor(asc*2/3)) / 2)
		w := bw + 7
		x := content - w + 1
		y := base - asc + yo
		r := Rect{x, y, bw, bw}
		switch kind {
		case "disc":
			rad := bw / 2
			p.emit(FillRect{Rect: r, Color: st.Color, Radii: Radii{{rad, rad}, {rad, rad}, {rad, rad}, {rad, rad}}})
		case "circle":
			rad := bw / 2
			p.emit(Border{Rect: r, Widths: [4]float64{1, 1, 1, 1}, Colors: [4]Color{st.Color, st.Color, st.Color, st.Color},
				Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: Radii{{rad, rad}, {rad, rad}, {rad, rad}, {rad, rad}}})
		default:
			p.emit(FillRect{Rect: r, Color: st.Color})
		}
	case "disclosure-closed", "disclosure-open":
		txt = "▸ "
		if kind == "disclosure-open" {
			txt = "▾ "
		}
		fallthrough
	default:
		if txt == "" {
			return
		}
		w := p.c.textWidth(st, txt)
		p.emitSimpleText(st, fs, txt, content-w, base, st.Color)
	}
}

func (p *painter) emitSimpleText(st *style.Computed, fs *fontSpec, txt string, x, y float64, col Color) {
	rs := []rune(txt)
	sh := p.c.shape(fs, st, rs)
	var gl []Glyph
	for i, r := range rs {
		if r == ' ' {
			continue
		}
		gl = append(gl, Glyph{R: r, X: sh.x[i], Face: sh.faces[i]})
	}
	p.emit(TextRun{X: x, Y: y, Size: fs.size, Color: col, Glyphs: gl, Width: sh.x[len(rs)], Bold: fs.synthBold})
}

// ---- controls ---------------------------------------------------------------------------------

var (
	ctlBorder = Color{118, 118, 118, 255}
	ctlFace   = Color{239, 239, 239, 255}
	ctlAccent = Color{0, 117, 255, 255}
)

func uniform(r float64) Radii { return Radii{{r, r}, {r, r}, {r, r}, {r, r}} }

func (p *painter) authorStyled(b *lbox, btn bool) bool {
	st := b.st
	wantBG := Color{255, 255, 255, 255}
	if btn {
		wantBG = ctlFace
	}
	if st.BgColor != wantBG && !(btn && st.BgColor == ctlFace) {
		return true
	}
	for i := 0; i < 4; i++ {
		if st.BorderWidth[i] != 2 && !(st.BorderWidth[i] == 1 && !btn) {
			return true
		}
		if st.BorderColor[i] != ctlBorder {
			return true
		}
	}
	for i := range st.Radius {
		if st.Radius[i][0].K != style.LAuto && st.Radius[i][0].V != 0 {
			return true
		}
	}
	return len(st.BgLayers) > 0 && !st.BgLayers[0].Image.IsNone()
}

func (p *painter) paintThemedBox(b *lbox, btn bool) {
	r := rectOf(b)
	bg := Color{255, 255, 255, 255}
	rad := 2.0
	if btn {
		bg = ctlFace
		rad = 3
	}
	p.emit(FillRect{Rect: r, Color: bg, Radii: uniform(rad)})
	p.emit(Border{Rect: r, Widths: [4]float64{1, 1, 1, 1}, Colors: [4]Color{ctlBorder, ctlBorder, ctlBorder, ctlBorder},
		Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: uniform(rad)})
}

func (p *painter) paintButtonChrome(b *lbox) {}

func (p *painter) paintControl(b *lbox) {
	st := b.st
	r := b.rep
	box := rectOf(b)
	cr := p.contentRect(b)
	fs := p.c.spec(st)
	ci := p.c.controlInfo(b)
	disabled := hasAttr(b.n, "disabled")
	txtCol := st.Color
	if disabled {
		txtCol = Color{109, 109, 109, 255}
	}
	baseY := b.ay + b.ctrlBase
	switch r.kind {
	case "input":
		switch r.ctrl {
		case "checkbox", "radio":
			checked := hasAttr(b.n, "checked")
			sq := Rect{b.ax, b.ay, b.w, b.h}
			if r.ctrl == "checkbox" {
				if checked {
					p.emit(FillRect{Rect: sq, Color: ctlAccent, Radii: uniform(2)})
					fsz := 11.0
					gl := []Glyph{{R: '✓', X: 0, Face: fontFor('✓', fs)}}
					p.emit(TextRun{X: sq.X + 1.5, Y: sq.Y + sq.H - 2, Size: fsz, Color: Color{255, 255, 255, 255}, Glyphs: gl, Width: fsz})
				} else {
					p.emit(FillRect{Rect: sq, Color: Color{255, 255, 255, 255}, Radii: uniform(2)})
					p.emit(Border{Rect: sq, Widths: [4]float64{1, 1, 1, 1}, Colors: [4]Color{ctlBorder, ctlBorder, ctlBorder, ctlBorder}, Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: uniform(2)})
				}
			} else {
				rr := uniform(sq.W / 2)
				if checked {
					p.emit(FillRect{Rect: sq, Color: Color{255, 255, 255, 255}, Radii: rr})
					p.emit(Border{Rect: sq, Widths: [4]float64{1, 1, 1, 1}, Colors: [4]Color{ctlAccent, ctlAccent, ctlAccent, ctlAccent}, Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: rr})
					in := Rect{sq.X + 3, sq.Y + 3, sq.W - 6, sq.H - 6}
					p.emit(FillRect{Rect: in, Color: ctlAccent, Radii: uniform(in.W / 2)})
				} else {
					p.emit(FillRect{Rect: sq, Color: Color{255, 255, 255, 255}, Radii: rr})
					p.emit(Border{Rect: sq, Widths: [4]float64{1, 1, 1, 1}, Colors: [4]Color{ctlBorder, ctlBorder, ctlBorder, ctlBorder}, Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: rr})
				}
			}
		case "range":
			tr := Rect{cr.X, cr.Y + cr.H/2 - 2, cr.W, 4}
			p.emit(FillRect{Rect: tr, Color: Color{176, 176, 176, 255}, Radii: uniform(2)})
			val := 0.5
			p.emit(FillRect{Rect: Rect{tr.X, tr.Y, tr.W * val, 4}, Color: ctlAccent, Radii: uniform(2)})
			p.emit(FillRect{Rect: Rect{cr.X + (cr.W-16)*val, cr.Y, 16, 16}, Color: ctlAccent, Radii: uniform(8)})
		case "color":
			p.paintThemedBox(b, true)
			col := Color{0, 0, 0, 255}
			if v := attr(b.n, "value"); v != "" {
				if c, _, ok := style.ParseColor(v); ok {
					col = c
				}
			}
			p.emit(FillRect{Rect: Rect{cr.X, cr.Y, cr.W, cr.H}, Color: col})
		case "button", "submit", "reset":
			txt := ci.text
			w := p.c.textWidth(st, txt)
			p.emitSimpleText(st, fs, txt, cr.X+(cr.W-w)/2, baseY, txtCol)
		case "file":
			bw := p.c.textWidth(st, "Choose File") + 16
			p.paintThemedBox(&lbox{ax: b.ax, ay: b.ay, w: bw, h: b.h, st: st}, true)
			p.emitSimpleText(st, fs, "Choose File", b.ax+8, baseY, txtCol)
			p.emitSimpleText(st, fs, "No file chosen", b.ax+bw+8, baseY, txtCol)
		default:
			txt := ci.text
			col := txtCol
			if txt == "" {
				if ph := attr(b.n, "placeholder"); ph != "" {
					txt, col = ph, Color{117, 117, 117, 255}
				}
			} else if r.ctrl == "password" {
				txt = strings.Repeat("•", len([]rune(txt)))
			}
			if txt != "" {
				p.emitClippedText(st, fs, txt, cr, baseY, col)
			}
		}
	case "textarea":
		txt := ci.text
		col := txtCol
		if txt == "" {
			if ph := attr(b.n, "placeholder"); ph != "" {
				txt, col = ph, Color{117, 117, 117, 255}
			}
		}
		txt = strings.TrimPrefix(txt, "\n")
		y := cr.Y
		half := math.Floor((lhOf(st, fs) - (fs.asc + fs.desc)) / 2)
		for _, line := range strings.Split(txt, "\n") {
			p.emitClippedText(st, fs, line, cr, y+half+fs.asc, col)
			y += lhOf(st, fs)
		}
	case "select":
		sel := ""
		for i, o := range ci.options {
			t := collapseSpaces(textContent(o))
			if i == 0 || hasAttr(o, "selected") {
				sel = t
			}
		}
		if ci.list {
			y := cr.Y
			for _, o := range ci.options {
				t := collapseSpaces(textContent(o))
				if hasAttr(o, "selected") {
					p.emit(FillRect{Rect: Rect{cr.X, y, cr.W - 15, ci.optH}, Color: Color{181, 213, 255, 255}})
				}
				p.emitClippedText(st, fs, t, Rect{cr.X + 2, y, cr.W - 19, ci.optH}, y+1+fs.asc+math.Floor((lhOf(st, fs)-(fs.asc+fs.desc))/2), txtCol)
				y += ci.optH
			}
		} else {
			p.emitClippedText(st, fs, sel, Rect{cr.X + 4, cr.Y, cr.W - 20, cr.H}, baseY, txtCol)
			// arrow
			ax := b.ax + b.w - 14
			ay := b.ay + b.h/2
			p.emitSimpleText(st, fs, "▾", ax, ay+4, Color{16, 16, 16, 255})
		}
	case "progress":
		p.emit(FillRect{Rect: box, Color: Color{230, 230, 230, 255}, Radii: uniform(b.h / 2)})
		if v := attr(b.n, "value"); v != "" {
			mx := 1.0
			if m := attr(b.n, "max"); m != "" {
				if f, ok := parseDim(m); ok && f > 0 {
					mx = f
				}
			}
			if f, ok := parseDim(v); ok {
				frac := math.Max(0, math.Min(1, f/mx))
				p.emit(FillRect{Rect: Rect{box.X, box.Y, box.W * frac, box.H}, Color: ctlAccent, Radii: uniform(b.h / 2)})
			}
		}
	case "meter":
		p.emit(FillRect{Rect: box, Color: Color{230, 230, 230, 255}, Radii: uniform(b.h / 2)})
		frac := 0.0
		if v, ok := parseDim(attr(b.n, "value")); ok {
			mx := 1.0
			if m, ok := parseDim(attr(b.n, "max")); ok && m > 0 {
				mx = m
			}
			frac = math.Max(0, math.Min(1, v/mx))
		}
		p.emit(FillRect{Rect: Rect{box.X, box.Y, box.W * frac, box.H}, Color: Color{16, 163, 70, 255}, Radii: uniform(b.h / 2)})
	}
}

func fontFor(r rune, fs *fontSpec) *font.Face { return fs.faceFor(r) }

func (p *painter) emitClippedText(st *style.Computed, fs *fontSpec, txt string, cr Rect, baseY float64, col Color) {
	if txt == "" {
		return
	}
	p.pushClip(cr, Radii{}, nil)
	p.emitSimpleText(st, fs, txt, cr.X, baseY, col)
	p.popClip(nil)
}

// ---- hit testing ------------------------------------------------------------------------------

func invert(m [6]float64) ([6]float64, bool) {
	det := m[0]*m[3] - m[1]*m[2]
	if math.Abs(det) < 1e-12 {
		return identity, false
	}
	return [6]float64{m[3] / det, -m[1] / det, -m[2] / det, m[0] / det,
		(m[2]*m[5] - m[3]*m[4]) / det, (m[1]*m[4] - m[0]*m[5]) / det}, true
}

func (c *ctx) installHit(rb *lbox) {
	p := c.painter
	hits := p.hits
	c.res.Hit = func(x, y float64) *html.Node {
		for i := len(hits) - 1; i >= 0; i-- {
			h := hits[i]
			if h.noPE {
				continue
			}
			px, py := x, y
			if h.hasM {
				inv, ok := invert(h.m)
				if !ok {
					continue
				}
				px, py = inv[0]*x+inv[2]*y+inv[4], inv[1]*x+inv[3]*y+inv[5]
			}
			ok := false
			for _, r := range h.rects {
				if px >= r.X && px < r.X+r.W && py >= r.Y && py < r.Y+r.H {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
			for _, cl := range h.clips {
				// clips were recorded in the untransformed space of the stack at push time
				if px < cl.r.X || px >= cl.r.X+cl.r.W || py < cl.r.Y || py >= cl.r.Y+cl.r.H {
					ok = false
					break
				}
			}
			if ok {
				return h.n
			}
		}
		return nil
	}
}

var _ = image.Rect
