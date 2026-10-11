package render

import (
	"math"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			sb.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// textWidth measures s in the font of st (no wrapping).
func (c *ctx) textWidth(st *style.Computed, s string) float64 {
	fs := c.spec(st)
	rs := []rune(s)
	if len(rs) == 0 {
		return 0
	}
	sh := c.shape(fs, st, rs)
	return sh.x[len(rs)]
}

func attrInt(n *html.Node, k string, d int) int {
	if v := attr(n, k); v != "" {
		if x, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && x > 0 {
			return x
		}
	}
	return d
}

type ctrlInfo struct {
	cw, ch  float64
	base    float64 // baseline from content-box top
	text    string
	rows    int
	list    bool
	optH    float64
	options []*html.Node
}

func (c *ctx) optionNodes(n *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		for ch := x.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.ElementNode {
				if ch.Data == "option" {
					out = append(out, ch)
				} else if ch.Data == "optgroup" {
					walk(ch)
				}
			}
		}
	}
	walk(n)
	return out
}

func (c *ctx) controlInfo(b *lbox) ctrlInfo {
	if v, ok := b.extra.(*ctrlInfo); ok {
		return *v
	}
	r := b.rep
	st := b.st
	fs := c.spec(st)
	n := b.n
	lineH := lhOf(st, fs)
	half := math.Floor((lineH - (fs.asc + fs.desc)) / 2)
	textBase := half + fs.asc
	var ci ctrlInfo
	avgMax := func() (float64, float64) {
		if fs.primary == nil {
			return fs.size / 2, fs.size
		}
		_, _, _, avg, _ := fs.primary.RawMetrics()
		bb := fs.primary.BBoxWidth()
		k := fs.size / float64(fs.primary.UnitsPerEm())
		return round(avg * k), math.Ceil(bb*k - 1e-6)
	}
	switch r.kind {
	case "input":
		t := r.ctrl
		switch t {
		case "checkbox", "radio":
			ci = ctrlInfo{cw: 13, ch: 13, base: 13}
		case "range":
			ci = ctrlInfo{cw: 129, ch: 16, base: 16}
		case "color":
			ci = ctrlInfo{cw: 44, ch: 23, base: 19}
		case "file":
			ci = ctrlInfo{cw: 253, ch: 21, base: 15}
		case "button", "submit", "reset":
			txt := attr(n, "value")
			if txt == "" {
				switch t {
				case "submit":
					txt = "Submit"
				case "reset":
					txt = "Reset"
				}
			}
			ci = ctrlInfo{cw: c.textWidth(st, txt), ch: lineH, base: textBase, text: txt}
		case "date", "datetime-local", "month", "week", "time":
			w := 117.328125
			ch := lineH + 0.328125
			bs := textBase
			if t == "time" {
				w = 96
				ch += 2.671875
				bs += 1.5
			}
			ci = ctrlInfo{cw: w, ch: ch, base: bs}
		default:
			avg, mx := avgMax()
			size := attrInt(n, "size", 20)
			w := avg * float64(size)
			if mx > avg {
				w += mx - avg
			}
			ci = ctrlInfo{cw: w, ch: lineH, base: textBase, text: attr(n, "value")}
			if t == "number" {
				ci.text = attr(n, "value")
			}
		}
	case "textarea", "select", "progress", "meter":
		switch r.ctrl {
		case "textarea":
			avg, _ := avgMax()
			cols := attrInt(n, "cols", 20)
			rows := attrInt(n, "rows", 2)
			ci = ctrlInfo{cw: avg*float64(cols) + 16, ch: lineH * float64(rows), base: textBase, text: textContent(n), rows: rows}
		case "select":
			opts := c.optionNodes(n)
			maxW := 0.0
			for _, o := range opts {
				txt := collapseSpaces(attr(o, "label"))
				if txt == "" {
					txt = collapseSpaces(textContent(o))
				}
				maxW = math.Max(maxW, c.textWidth(st, txt))
			}
			size := attrInt(n, "size", 0)
			multiple := hasAttr(n, "multiple")
			optH := lineH + 2
			if multiple || size > 1 {
				rows := size
				if rows == 0 {
					rows = 4
				}
				ci = ctrlInfo{cw: maxW + 19, ch: float64(rows) * optH, base: float64(rows) * optH, list: true, rows: rows, optH: optH, options: opts}
			} else {
				ci = ctrlInfo{cw: math.Ceil(maxW) + 20, ch: lineH + 2, base: textBase + 1, options: opts}
			}
		case "progress":
			ci = ctrlInfo{cw: 160, ch: 16, base: 16}
		case "meter":
			ci = ctrlInfo{cw: 80, ch: 16, base: 16}
		}
	}
	b.extra = &ci
	return ci
}

// controlSize returns the intrinsic border-box width, height and baseline.
func (c *ctx) controlSize(b *lbox) (float64, float64, float64) {
	c.edges(b, 0)
	ci := c.controlInfo(b)
	return ci.cw + b.hExtra(), ci.ch + b.vExtra(), b.bor[0] + b.pad[0] + ci.base
}

func (c *ctx) layoutControl(b *lbox, in layoutIn) {
	st := b.st
	ci := c.controlInfo(b)
	var cw, chh float64
	switch {
	case in.fixW >= 0:
		cw = math.Max(0, in.fixW-b.hExtra())
	default:
		if wv, ok := c.specW(b, st.Width, in.cbw); ok {
			cw = c.clampW(b, wv, in.cbw)
		} else {
			cw = c.clampW(b, ci.cw, in.cbw)
		}
	}
	autoH := false
	switch {
	case in.fixH >= 0:
		chh = math.Max(0, in.fixH-b.vExtra())
	default:
		if hv, ok := c.specH(b, st.Height, in.cbh); ok {
			chh = c.clampH(b, hv, in.cbh, 0)
		} else {
			chh = c.clampH(b, ci.ch, in.cbh, 0)
			autoH = true
		}
	}
	_ = autoH
	b.w = cw + b.hExtra()
	b.h = chh + b.vExtra()
	b.contentH = chh
	// baseline: single-line controls center their text vertically
	extra := 0.0
	if ci.rows == 0 && !ci.list && chh > ci.ch && b.rep.ctrl != "checkbox" && b.rep.ctrl != "radio" && b.rep.ctrl != "range" && b.rep.ctrl != "progress" && b.rep.ctrl != "meter" {
		extra = (chh - ci.ch) / 2
	}
	b.ctrlBase = b.bor[0] + b.pad[0] + extra + ci.base
	switch b.rep.ctrl {
	case "checkbox", "radio", "range", "progress", "meter", "textarea":
		b.ctrlBase = b.h
	case "select":
		if ci.list {
			b.ctrlBase = b.h - 3
		}
	}
	b.mtopOut, b.mbotOut = b.mar[0], b.mar[2]
	b.through = false
	b.hasBase = false
	if b.kind == kBlock && !b.floated && !b.oof {
		c.horizontalMargins(b, in, b.w)
	}
}

// fieldsetLegend lays out the rendered legend of a fieldset and reserves space for it.
func (c *ctx) fieldsetLegend(b *lbox, cw float64) {
	b.legend = nil
	if b.n == nil || b.n.Data != "fieldset" {
		return
	}
	if b.legendBase >= 0 {
		b.pad[0] = b.legendBase
	} else {
		b.legendBase = b.pad[0]
	}
	var lg *lbox
	for _, k := range b.kids {
		if k.isInFlowBlock() && k.n != nil && k.n.Data == "legend" {
			lg = k
			break
		}
		if k.isInFlowBlock() {
			break
		}
	}
	if lg == nil {
		return
	}
	c.edges(lg, cw)
	in := layoutIn{item: true, cbw: cw, cbh: -1, fixW: -1, fixH: -1, shrink: true, availW: cw, bfc: &bfcState{}}
	lg.hasLast = false
	c.layoutBox(lg, in)
	outer := lg.h + lg.mar[0] + lg.mar[2]
	extra := math.Max(0, outer-b.bor[0])
	b.legendPad = extra
	b.pad[0] += extra
	lg.x = lg.mar[3]
	lg.y = lg.mar[0] - b.bor[0] - b.pad[0]
	lg.lp = b
	b.legend = lg
}
