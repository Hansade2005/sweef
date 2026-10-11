package render

// placeInline converts line data of an IFC root b into absolute fragments for
// its inline boxes and collects the atomic/float children for placement.
func (c *ctx) placeInline(b *lbox) {
	// reset inline boxes' fragments
	var reset func(x *lbox)
	reset = func(x *lbox) {
		for _, k := range x.kids {
			if k.kind == kInline {
				k.absFrags = k.absFrags[:0]
				reset(k)
			}
		}
	}
	reset(b)
	for _, ln := range b.lines {
		for li := range ln.boxes {
			lb := &ln.boxes[li]
			bx := lb.box
			fs := c.spec(bx.st)
			x := b.ax + ln.x + lb.x0
			y := b.ay + ln.y + lb.base - fs.asc - bx.pad[0] - bx.bor[0]
			w := lb.x1 - lb.x0
			h := fs.asc + fs.desc + bx.pad[0] + bx.pad[2] + bx.bor[0] + bx.bor[2]
			lb.rect = Rect{x, y, w, h}
			bx.absFrags = append(bx.absFrags, lb.rect)
		}
	}
}

func (b *lbox) inlineFrags() []struct{ R float64 } { return nil }

func (c *ctx) placeSpecial(b *lbox, acc func(*lbox)) {}

// collectInlineKids lists the atomic inline-level boxes and floats of an IFC.
func (c *ctx) collectInlineKids(b *lbox) {
	b.inlineKids = b.inlineKids[:0]
	for _, a := range b.atoms {
		if a.kind == aFloat {
			b.inlineKids = append(b.inlineKids, a.box)
		}
	}
	for _, ln := range b.lines {
		for _, fr := range ln.frags {
			if fr.kind == aAtomic {
				b.inlineKids = append(b.inlineKids, fr.box)
			}
		}
	}
}

// shiftContent moves child positions from content-box to border-box origin.
func (c *ctx) shiftContent(b *lbox) {
	dx, dy := b.bor[3]+b.pad[3], b.bor[0]+b.pad[0]
	if b.hasBase {
		b.baseline += dy
		b.lastBase += dy
	}
	if b.ifc {
		c.collectInlineKids(b)
		for _, k := range b.inlineKids {
			k.x += dx
			k.y += dy
		}
		for _, ln := range b.lines {
			ln.x += dx
			ln.y += dy
		}
		return
	}
	for _, k := range b.kids {
		if k.oof || k.kind == kText || k.kind == kInline || k.kind == kBreak || k.kind == kMarker {
			continue
		}
		k.x += dx
		k.y += dy
	}
}
