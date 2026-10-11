package render

// Painting of boxes fragmented across columns (and column rules).
//
// A block that continues over several columns has one fragment rectangle per
// column in lbox.mcFrags (relative to the block's bounding box origin). The
// default box-decoration-break: slice paints the decorations of the
// unfragmented box (fragments stacked on top of each other) and clips them to
// every fragment, so borders, radii and padding appear only on the outer
// edges of the first and last fragment. clone paints a complete box in each
// fragment.

// absFrags returns the fragment rectangles of b in document coordinates.
func (b *lbox) colFrags() []Rect {
	out := make([]Rect, len(b.mcFrags))
	for i, f := range b.mcFrags {
		out[i] = Rect{b.ax + f.X, b.ay + f.Y, f.W, f.H}
	}
	return out
}

const fragReach = 1e4

func (p *painter) paintFragDecor(b *lbox, vis bool) {
	st := b.st
	frags := b.colFrags()
	if vis {
		clone := st.BoxDecorationBreak == "clone"
		total := 0.0
		for _, f := range frags {
			total += f.H
		}
		hasBorder := st.BorderStyle != [4]string{} && (b.bor[0] > 0 || b.bor[1] > 0 || b.bor[2] > 0 || b.bor[3] > 0) && !b.hasBorOv
		skipBG := b == p.c.bodyBox && p.c.bodyPropagates()
		used := 0.0
		for i, f := range frags {
			r := f
			clipped := false
			if !clone {
				// the unfragmented box, positioned so that this fragment shows its slice
				r = Rect{f.X, f.Y - used, f.W, total}
				cr := Rect{f.X - fragReach, f.Y - fragReach, f.W + 2*fragReach, f.H + 2*fragReach}
				if i > 0 {
					cr.H -= f.Y - cr.Y
					cr.Y = f.Y
				}
				if i < len(frags)-1 {
					cr.H = f.Bottom() - cr.Y
				}
				p.emit(PushClip{Rect: cr})
				clipped = true
			}
			used += f.H
			radii := p.radiiWH(b, r.W, r.H)
			for _, sh := range st.BoxShadow {
				if !sh.Inset {
					p.emit(BoxShadow{Rect: r, Radii: radii, DX: sh.DX, DY: sh.DY, Blur: sh.Blur, Spread: sh.Spread, Color: sh.Color})
				}
			}
			if !skipBG {
				p.paintBackground(b, r, radii)
			}
			for _, sh := range st.BoxShadow {
				if sh.Inset {
					p.emit(BoxShadow{Rect: r, Radii: radii, DX: sh.DX, DY: sh.DY, Blur: sh.Blur, Spread: sh.Spread, Color: sh.Color, Inset: true})
				}
			}
			if hasBorder {
				p.emit(Border{Rect: r, Widths: b.bor, Colors: st.BorderColor, Styles: st.BorderStyle, Radii: radii})
			}
			if clipped {
				p.emit(PopClip{})
			}
		}
		p.paintColumnRules(b)
		if st.OutlineWidth > 0 && st.OutlineStyle != "none" {
			p.outl = append(p.outl, b)
			for _, f := range frags {
				p.paintOutlineRect(b, f)
			}
		}
	}
	p.addHit(b, frags...)
}

// paintColumnRules draws the rules between the columns of a multicol
// container, within each column-span segment only.
func (p *painter) paintColumnRules(b *lbox) {
	st := b.st
	if b.mcN <= 1 || st.ColumnRuleW <= 0 || st.ColumnRuleStyle == "none" || st.ColumnRuleStyle == "hidden" {
		return
	}
	cx := b.ax + b.bor[3] + b.pad[3]
	cy := b.ay + b.bor[0] + b.pad[0]
	segs := b.mcSegs
	if len(segs) == 0 {
		segs = []mcSeg{{0, b.h - b.vExtra(), b.mcN}}
	}
	for _, sg := range segs {
		if sg.y1 <= sg.y0 {
			continue
		}
		for i := 1; i < sg.ncol; i++ {
			x := cx + float64(i)*(b.mcW+b.mcGap) - b.mcGap/2 - st.ColumnRuleW/2
			rr := Rect{x, cy + sg.y0, st.ColumnRuleW, sg.y1 - sg.y0}
			switch st.ColumnRuleStyle {
			case "dashed", "dotted", "double", "groove", "ridge", "inset", "outset":
				// painted like a one-sided border (additive: reuses the Border op)
				c := st.ColumnRuleColor
				p.emit(Border{Rect: rr, Widths: [4]float64{0, 0, 0, st.ColumnRuleW},
					Colors: [4]Color{c, c, c, c}, Styles: [4]string{"none", "none", "none", st.ColumnRuleStyle}})
			default:
				p.emit(FillRect{Rect: rr, Color: st.ColumnRuleColor})
			}
		}
	}
}
