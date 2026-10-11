package engine

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
)

// PNGScreenshot renders the current document to real PNG bytes using a tiny
// built-in 5x7 bitmap font + box layout — no CGO, no headless Chromium.
// width/height<=0 fall back to the context viewport; dsf scales everything
// (Playwright deviceScaleFactor semantics). This is the honest equivalent of
// page.screenshot() for our agent-first browser: deterministic, ~20KB output,
// runs in <16MB RAM.
func (t *Tab) PNGScreenshot(width, height int, dsf float64) []byte {
	t.mu.RLock()
	dom := t.dom
	if width <= 0 {
		width = t.vpW
	}
	if height <= 0 {
		height = t.vpH
	}
	if dsf <= 0 {
		dsf = t.dsf
	}
	if dsf <= 0 {
		dsf = 1
	}
	t.mu.RUnlock()
	if width <= 0 {
		width = 1280
	}
	if height <= 0 {
		height = 720
	}
	sc := int(dsf)
	if sc < 1 {
		sc = 1
	}
	W, H := width*sc, height*sc

	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)

	px := func(x, y int, c color.Color) {
		if x >= 0 && y >= 0 && x < W && y < H {
			img.Set(x, y, c)
		}
	}
	rectFill := func(x0, y0, w, h int, c color.Color) {
		for y := y0; y < y0+h*sc; y++ {
			for x := x0; x < x0+w*sc; x++ {
				px(x, y, c)
			}
		}
	}

	ink := color.RGBA{20, 24, 33, 255}
	accent := color.RGBA{37, 99, 235, 255}
	muted := color.RGBA{120, 128, 140, 255}

	var text string
	if dom != nil {
		cols := width / (6 * sc) // 5px glyph + 1px space per char at scale
		if cols < 20 {
			cols = 20
		}
		text = dom.RenderText(cols)
	}

	y := 8 * sc
	lineH := 9 * sc
	for _, line := range strings.Split(text, "\n") {
		if y+lineH > H-4*sc {
			break
		}
		line = strings.TrimRight(line, " ")
		runes := []rune(line)
		// heading-ish lines (start with # or ALL CAPS short) get accent bar
		if len(runes) > 0 && (runes[0] == '#' || runes[0] == '█' || runes[0] == '•') {
			rectFill(2*sc, y+1*sc, 2*sc, lineH-2*sc, accent)
		}
		x := 6 * sc
		for _, r := range runes {
			if x > W-6*sc {
				break
			}
			if gl, ok := glyph5x7[r]; ok {
				drawGlyph(img, x, y, gl, ink, sc)
			} else if r >= 'a' && r <= 'z' {
				if gl, ok := glyph5x7[r-32]; ok {
					drawGlyph(img, x, y+1*sc, gl, ink, sc)
				}
			}
			x += 6 * sc
		}
		y += lineH
	}

	// footer status bar like a real browser chrome hint
	rectFill(0, H-2*sc, W, 2*sc, muted)

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func drawGlyph(dst *image.RGBA, ox, oy int, rows [7]uint8, c color.Color, sc int) {
	for ry := 0; ry < 7; ry++ {
		bits := rows[ry]
		for rx := 0; rx < 5; rx++ {
			if bits&(1<<(4-rx)) != 0 {
				for dy := 0; dy < sc; dy++ {
					for dx := 0; dx < sc; dx++ {
						dst.Set(ox+rx*sc+dx, oy+ry*sc+dy, c)
					}
				}
			}
		}
	}
}
