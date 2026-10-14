package engine

// computedstyle.go: getComputedStyle values from the real cascade. The prelude used to
// answer from the inline style attribute and a table of tag defaults, so a grid, flex or
// positioned element styled from a stylesheet reported display "block", position "static"
// and empty sizes. Layout already keeps each element's computed style; this serialises the
// properties scripts read most, in the form Chromium returns them.

import (
	"fmt"
	"strconv"
	"strings"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render"
	"github.com/sweefbrowser/sweef/internal/style"
)

func cssPx(v float64) string {
	return strconv.FormatFloat(float64(int64(v*1000+copySign(0.5, v)))/1000, 'f', -1, 64) + "px"
}

func copySign(a, b float64) float64 {
	if b < 0 {
		return -a
	}
	return a
}

func cssColor(c style.Color) string {
	if c.A == 255 {
		return fmt.Sprintf("rgb(%d, %d, %d)", c.R, c.G, c.B)
	}
	// like Chromium: two decimals when they round-trip to the same byte, else three
	a2 := float64(int(float64(c.A)/255*100+0.5)) / 100
	a := strconv.FormatFloat(a2, 'f', -1, 64)
	if int(a2*255+0.5) != int(c.A) {
		a = strconv.FormatFloat(float64(int(float64(c.A)/255*1000+0.5))/1000, 'f', -1, 64)
	}
	return fmt.Sprintf("rgba(%d, %d, %d, %s)", c.R, c.G, c.B, a)
}

// cssFamilies serialises a family list: the internal web-font scope suffix ("name\x00w3")
// is dropped and names that are not generic keywords and contain spaces are quoted, as in
// Chromium. Family names were lowercased by the cascade, so their original case is not kept.
func cssFamilies(fams []string) string {
	out := make([]string, 0, len(fams))
	for _, f := range fams {
		if i := strings.IndexByte(f, 0); i >= 0 {
			f = f[:i]
		}
		if f == "" {
			continue
		}
		if strings.ContainsAny(f, " ") {
			f = `"` + f + `"`
		}
		out = append(out, f)
	}
	return strings.Join(out, ", ")
}

func cssNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

var replacedTag = map[string]bool{"img": true, "video": true, "canvas": true, "iframe": true, "embed": true, "object": true, "input": true, "select": true, "textarea": true, "button": true, "svg": true}

// computedStyleOf returns kebab-case property -> value for the element with backend id,
// or nil when the element has no style (not rendered by the layout provider).
func computedStyleOf(d *fnet.Doc, id int64) map[string]any {
	n := d.NodeByID(id)
	if n == nil {
		return nil
	}
	l := d.Layout(d.LayoutWidth())
	ext, _ := l.Ext.(*renderExt)
	if ext == nil || ext.res == nil {
		return nil
	}
	b := ext.res.Boxes[n]
	if b == nil || b.Style == nil {
		return nil
	}
	st := b.Style
	out := map[string]any{
		"display":          st.Display,
		"position":         st.Position,
		"visibility":       st.Visibility,
		"box-sizing":       st.BoxSizing,
		"color":            cssColor(st.Color),
		"background-color": cssColor(st.BgColor),
		"font-size":        cssPx(st.FontSize),
		"font-weight":      strconv.Itoa(st.FontWeight),
		"font-family":      cssFamilies(st.FontFamily),
		"opacity":          cssNum(st.Opacity),
	}
	switch st.FontStyle {
	case 1:
		out["font-style"] = "italic"
	case 2:
		out["font-style"] = "oblique"
	default:
		out["font-style"] = "normal"
	}
	switch st.LineHeight.K {
	case style.LAuto:
		out["line-height"] = "normal"
	case style.LNumber:
		out["line-height"] = cssPx(st.LineHeight.V * st.FontSize)
	case style.LPx:
		out["line-height"] = cssPx(st.LineHeight.V)
	}
	if st.ZAuto {
		out["z-index"] = "auto"
	} else {
		out["z-index"] = strconv.Itoa(st.ZIndex)
	}
	if b.Display == "none" {
		out["width"], out["height"] = "auto", "auto"
	} else {
		setBoxValues(out, b, st)
		if b.Display == "inline" && !replacedTag[n.Data] {
			out["width"], out["height"] = "auto", "auto" // as Chromium: no used size for inline boxes
		}
	}
	return out
}

// setBoxValues fills the used sizes, as getComputedStyle reports them for a rendered box.
func setBoxValues(out map[string]any, b *render.Box, st *style.Computed) {
	if st.BoxSizing == "border-box" {
		out["width"], out["height"] = cssPx(b.Border.W), cssPx(b.Border.H)
	} else {
		out["width"], out["height"] = cssPx(b.Content.W), cssPx(b.Content.H)
	}
	sides := []string{"top", "right", "bottom", "left"}
	for i, s := range sides {
		out["margin-"+s] = cssPx(b.Margin[i])
	}
	pad := [4]float64{b.Content.Y - b.Padding.Y, b.Padding.X + b.Padding.W - (b.Content.X + b.Content.W),
		b.Padding.Y + b.Padding.H - (b.Content.Y + b.Content.H), b.Content.X - b.Padding.X}
	bor := [4]float64{b.Padding.Y - b.Border.Y, b.Border.X + b.Border.W - (b.Padding.X + b.Padding.W),
		b.Border.Y + b.Border.H - (b.Padding.Y + b.Padding.H), b.Padding.X - b.Border.X}
	for i, s := range sides {
		out["padding-"+s] = cssPx(pad[i])
		out["border-"+s+"-width"] = cssPx(bor[i])
	}
}

// docQuirks reports whether the document renders in quirks mode (document.compatMode).
func docQuirks(d *fnet.Doc) bool { return render.IsQuirks(d.Root) }
