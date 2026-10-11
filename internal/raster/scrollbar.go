package raster

import (
	"github.com/sweefbrowser/sweef/internal/render"
)

// Colours measured from Chromium 1194 (Linux, light scheme).
var (
	sbTrack = RGBA8(0xfc, 0xfc, 0xfc, 255)
	sbThumb = RGBA8(0x8b, 0x8b, 0x8b, 255)
	// dark scheme (color-scheme: dark)
	sbTrackDark = RGBA8(0x42, 0x42, 0x42, 255)
	sbThumbDark = RGBA8(0xa8, 0xa8, 0xa8, 255)
)

func (p *Painter) scrollbar(o render.Scrollbar) {
	c := p.c
	c.Save()
	defer c.Restore()
	track, thumb := sbTrack, sbThumb
	if o.Dark {
		track, thumb = sbTrackDark, sbThumbDark
	}
	r := p.snap(o.Rect)
	if r.W <= 0 || r.H <= 0 {
		return
	}
	c.SetFill(Brush{Color: track})
	c.FillRect(r.X, r.Y, r.W, r.H)
	c.SetFill(Brush{Color: thumb})
	// arrows are stair-stepped, one row per pixel: 1,3,5,7,9 wide
	arrow := func(bx, by float64, dir int) {
		// bx,by is the top-left of the 15x15 button; dir 0 up 1 down 2 left 3 right
		for i := 0; i < 5; i++ {
			w := float64(2*i + 1)
			switch dir {
			case 0: // apex at row 6
				c.FillRect(bx+7-float64(i), by+6+float64(i), w, 1)
			case 1: // apex at row 7, base at row 3
				c.FillRect(bx+7-float64(i), by+7-float64(i), w, 1)
			case 2: // apex at col 6
				c.FillRect(bx+6+float64(i), by+7-float64(i), 1, w)
			case 3: // apex at col 7
				c.FillRect(bx+7-float64(i), by+7-float64(i), 1, w)
			}
		}
	}
	const btn = 15.0
	if o.Vertical {
		arrow(r.X+(r.W-15)/2, r.Y, 0)
		arrow(r.X+(r.W-15)/2, r.Y+r.H-btn, 1)
		if o.ThumbLen > 0 {
			c.SetFill(Brush{Color: thumb})
			pa := NewPath()
			pa.RoundRect(Rect{r.X + (r.W-15)/2 + 3, r.Y + o.ThumbOff, 9, o.ThumbLen}, [4][2]float64{{4.5, 4.5}, {4.5, 4.5}, {4.5, 4.5}, {4.5, 4.5}})
			c.FillPath(pa, NonZero)
		}
	} else {
		arrow(r.X, r.Y+(r.H-15)/2, 2)
		arrow(r.X+r.W-btn, r.Y+(r.H-15)/2, 3)
		if o.ThumbLen > 0 {
			c.SetFill(Brush{Color: thumb})
			pa := NewPath()
			pa.RoundRect(Rect{r.X + o.ThumbOff, r.Y + (r.H-15)/2 + 3, o.ThumbLen, 9}, [4][2]float64{{4.5, 4.5}, {4.5, 4.5}, {4.5, 4.5}, {4.5, 4.5}})
			c.FillPath(pa, NonZero)
		}
	}
}

func (p *Painter) scrollCorner(o render.ScrollCorner) {
	c := p.c
	t := sbTrack
	if o.Dark {
		t = sbTrackDark
	}
	c.SetFill(Brush{Color: t})
	cr := p.snap(o.Rect)
	c.FillRect(cr.X, cr.Y, cr.W, cr.H)
}
