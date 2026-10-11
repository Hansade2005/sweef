package render

import (
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

func attr(n *html.Node, k string) string {
	if n == nil {
		return ""
	}
	for i := range n.Attr {
		if n.Attr[i].Key == k {
			return n.Attr[i].Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, k string) bool {
	if n == nil {
		return false
	}
	for i := range n.Attr {
		if n.Attr[i].Key == k {
			return true
		}
	}
	return false
}

func (c *ctx) resolveURL(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "data:") {
		return ref
	}
	base, err := url.Parse(c.opts.BaseURL)
	if err != nil || c.opts.BaseURL == "" {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(r).String()
}

type counterScope struct{ names []string }

func (c *ctx) pushCounter(name string, v int) {
	c.counters[name] = append(c.counters[name], v)
}

func (c *ctx) applyCounters(st *style.Computed, isListItem bool, listValue int, hasValue bool, scope *counterScope) {
	for _, op := range st.CounterReset {
		c.pushCounter(op.Name, op.Val)
		scope.names = append(scope.names, op.Name)
	}
	incr := st.CounterIncr
	if isListItem {
		has := false
		for _, op := range incr {
			if op.Name == "list-item" {
				has = true
			}
		}
		if hasValue {
			if len(c.counters["list-item"]) == 0 {
				c.pushCounter("list-item", 0)
				scope.names = append(scope.names, "list-item")
			}
			c.counters["list-item"][len(c.counters["list-item"])-1] = listValue - 1
		}
		if !has {
			incr = append(append([]style.CounterOp(nil), incr...), style.CounterOp{Name: "list-item", Val: 1})
		}
	}
	for _, op := range incr {
		if len(c.counters[op.Name]) == 0 {
			c.pushCounter(op.Name, 0)
			scope.names = append(scope.names, op.Name)
		}
		s := c.counters[op.Name]
		s[len(s)-1] += op.Val
	}
	for _, op := range st.CounterSet {
		if len(c.counters[op.Name]) == 0 {
			c.pushCounter(op.Name, 0)
			scope.names = append(scope.names, op.Name)
		}
		s := c.counters[op.Name]
		s[len(s)-1] = op.Val
	}
}

func (c *ctx) popScope(s *counterScope) {
	for i := len(s.names) - 1; i >= 0; i-- {
		n := s.names[i]
		c.counters[n] = c.counters[n][:len(c.counters[n])-1]
	}
}

func isReplacedTag(n *html.Node) bool {
	switch n.Data {
	case "img", "video", "canvas", "iframe", "embed", "audio", "input", "select", "textarea", "progress", "meter":
		return n.Namespace == ""
	case "svg":
		return true
	case "object":
		return hasAttr(n, "data")
	}
	return false
}

func classifyDisplay(d string) (kind boxKind, inner int) {
	switch d {
	case "block", "flow-root":
		return kBlock, iFlow
	case "list-item":
		return kBlock, iFlow
	case "inline":
		return kInline, iFlow
	case "inline-block":
		return kAtomic, iFlow
	case "flex", "-webkit-box":
		return kBlock, iFlex
	case "inline-flex", "-webkit-inline-box":
		return kAtomic, iFlex
	case "grid":
		return kBlock, iGrid
	case "inline-grid":
		return kAtomic, iGrid
	case "table":
		return kBlock, iTable
	case "inline-table":
		return kAtomic, iTable
	case "table-row":
		return kBlock, iRow
	case "table-row-group", "table-header-group", "table-footer-group":
		return kBlock, iRowGroup
	case "table-cell":
		return kBlock, iCell
	case "table-caption":
		return kBlock, iCaption
	case "table-column-group":
		return kBlock, iColGroup
	case "table-column":
		return kBlock, iCol
	case "ruby", "ruby-text":
		return kInline, iFlow
	}
	return kInline, iFlow
}

// buildNode converts a DOM node to layout boxes appended to out.
func (c *ctx) buildNode(n *html.Node, pst *style.Computed, out *[]*lbox) {
	switch n.Type {
	case html.TextNode:
		if n.Data == "" {
			return
		}
		b := newBox(n, pst, kText)
		b.text = n.Data
		b.inlineLv = true
		*out = append(*out, b)
	case html.ElementNode:
		c.buildElement(n, pst, out)
	}
}

func (c *ctx) buildElement(n *html.Node, pst *style.Computed, out *[]*lbox) {
	st := c.eng.Compute(n, pst, "")
	st = c.svgFilterRefs(n, st)
	c.styles[n] = st
	if st.Display == "none" {
		return
	}
	if st.ContentVisibility == "auto" {
		st = c.cvDecide(n, st)
	}
	if st.CVSkipped() {
		c.cvDepth++
		defer func() { c.cvDepth-- }()
	}
	var scope counterScope
	defer c.popScope(&scope)
	isLI := st.Display == "list-item"
	listVal, hasVal := 0, false
	if isLI && n.Data == "li" {
		if v := attr(n, "value"); v != "" {
			if k, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				listVal, hasVal = k, true
			}
		}
	}
	switch n.Data {
	case "ol", "ul", "menu", "dir":
		start := 1
		if v := attr(n, "start"); v != "" {
			if k, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				start = k
			}
		}
		hasReset := false
		for _, op := range st.CounterReset {
			if op.Name == "list-item" {
				hasReset = true
			}
		}
		if !hasReset {
			if hasAttr(n, "reversed") && n.Data == "ol" {
				cnt := 0
				for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
					if ch.Type == html.ElementNode && ch.Data == "li" {
						cnt++
					}
				}
				if attr(n, "start") == "" {
					start = cnt
				}
				c.pushCounter("list-item", start+1)
			} else {
				c.pushCounter("list-item", start-1)
			}
			scope.names = append(scope.names, "list-item")
		}
	}
	c.applyCounters(st, isLI, listVal, hasVal, &scope)
	if isLI && len(c.counters["list-item"]) > 0 && n.Parent != nil && n.Parent.Data == "ol" && hasAttr(n.Parent, "reversed") {
		// reversed lists count down
		s := c.counters["list-item"]
		s[len(s)-1] -= 2
	}

	if st.Display == "contents" {
		c.buildChildrenInto(n, st, out, nil)
		return
	}
	kind, inner := classifyDisplay(st.Display)
	b := newBox(n, st, kind)
	b.inner = inner
	b.floated = st.Float != "none"
	b.oof = st.Position == "absolute" || st.Position == "fixed"
	if isReplacedTag(n) {
		if c.setupReplaced(b) {
			if b.kind == kInline {
				b.kind = kAtomic
			}
			b.inner = iReplaced
			if b.rep.ctrl != "" || b.rep.kind == "select" || b.rep.kind == "textarea" || b.rep.kind == "progress" || b.rep.kind == "meter" || b.rep.kind == "input" {
				b.inner = iControl
			}
			b.inlineLv = b.kind == kAtomic || b.kind == kInline
			if b.floated || b.oof {
				b.inlineLv = false
			}
			c.finishCommon(b)
			*out = append(*out, b)
			return
		}
	}
	if n.Data == "br" {
		b.kind = kBreak
		b.inlineLv = true
		c.finishCommon(b)
		*out = append(*out, b)
		return
	}
	if n.Data == "wbr" {
		t := newBox(n, st, kText)
		t.text = "​"
		t.inlineLv = true
		*out = append(*out, t)
		return
	}
	b.inlineLv = (b.kind == kInline || b.kind == kAtomic) && !b.floated && !b.oof
	c.markVertical(b, pst)
	if n.Data == "button" {
		b.isBtn = true
	}
	if isLI {
		c.addMarker(b, st)
	}
	var kids []*lbox
	if c.cv != nil && c.cv.fast && st.CVSkipped() {
		// sizing pass of a content-visibility:auto page: skipped contents are
		// not built at all (the final pass builds them for geometry queries)
	} else {
		if bf := c.makePseudo(n, st, "before"); bf != nil {
			kids = append(kids, bf...)
		}
		c.buildChildrenInto(n, st, &kids, nil)
		if af := c.makePseudo(n, st, "after"); af != nil {
			kids = append(kids, af...)
		}
	}
	for _, k := range kids {
		b.addKid(k)
	}
	if kind == kBlock || st.Display == "inline-block" || st.Display == "list-item" {
		c.applyFirstLetter(b, n, st)
	}
	if st.CVSkipped() {
		// skipped contents are laid out (geometry queries see their normal flow
		// positions) but do not paint, hit-test or size their container
		b.cvSkip = true
	}
	if n.Data == "details" && !hasAttr(n, "open") {
		seenSummary := false
		for _, k := range b.kids {
			if k.n != nil && k.n.Data == "summary" && !seenSummary {
				seenSummary = true
				continue
			}
			if k.kind == kBlock || k.kind == kAtomic {
				k.hiddenFlow = true
			}
		}
		// loose inline content of a closed details is not rendered either
		var keep []*lbox
		seenS := false
		for _, k := range b.kids {
			if k.n != nil && k.n.Data == "summary" && !seenS && k.pseudo == "" {
				seenS = true
				keep = append(keep, k)
				continue
			}
			if k.kind == kBlock || k.kind == kAtomic || k.oof || k.floated {
				keep = append(keep, k)
			}
		}
		b.kids = keep
	}
	c.finishCommon(b)
	c.fixupChildren(b)
	if c.quirks && (n.Data == "body" || n.Data == "td" || n.Data == "th") {
		var first, last *lbox
		for _, k := range b.kids {
			if k.kind == kBlock && !k.floated && !k.oof && !k.hiddenFlow {
				if first == nil {
					first = k
				}
				last = k
			}
		}
		if first != nil && first.st.QuirkMarginTop {
			first.quirkMT = true
		}
		if last != nil && last.st.QuirkMarginBottom {
			last.quirkMB = true
		}
	}
	if (kind == kBlock || st.Display == "inline-block") && b.inner == iFlow {
		if fls := c.eng.Compute(n, st, "first-line"); fls != nil {
			c.setFirstLine(b, fls)
		}
	}
	if b.kind == kInline && !b.floated && !b.oof {
		// block-in-inline splitting
		if pieces := c.splitInline(b); pieces != nil {
			*out = append(*out, pieces...)
			return
		}
	}
	*out = append(*out, b)
}

func (c *ctx) finishCommon(b *lbox) {
	c.byNode[b.n] = append(c.byNode[b.n], b)
	if b.pseudo != "" {
		return
	}
}

func (c *ctx) buildChildrenInto(n *html.Node, st *style.Computed, out *[]*lbox, _ *lbox) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		switch ch.Type {
		case html.TextNode:
			c.buildNode(ch, st, out)
		case html.ElementNode:
			c.buildElement(ch, st, out)
		}
	}
}

// makePseudo builds ::before / ::after boxes.
func (c *ctx) makePseudo(n *html.Node, st *style.Computed, which string) []*lbox {
	pst := c.eng.Compute(n, st, which)
	if pst == nil || !pst.ContentSet || pst.ContentNone || pst.Display == "none" {
		return nil
	}
	var scope counterScope
	defer c.popScope(&scope)
	c.applyCounters(pst, false, 0, false, &scope)
	if pst.Display == "contents" {
		return nil
	}
	kind, inner := classifyDisplay(pst.Display)
	b := newBox(n, pst, kind)
	b.inner = inner
	b.pseudo = which
	b.floated = pst.Float != "none"
	b.oof = pst.Position == "absolute" || pst.Position == "fixed"
	b.inlineLv = (b.kind == kInline || b.kind == kAtomic) && !b.floated && !b.oof
	var sb strings.Builder
	var kids []*lbox
	flush := func() {
		if sb.Len() > 0 {
			t := newBox(n, pst, kText)
			t.text = sb.String()
			t.inlineLv = true
			t.pseudo = which
			kids = append(kids, t)
			sb.Reset()
		}
	}
	for _, it := range pst.Content {
		switch it.K {
		case "str":
			sb.WriteString(it.S)
		case "attr":
			sb.WriteString(attr(n, it.S))
		case "counter":
			sb.WriteString(formatCounter(c.counterValue(it.S), it.Style))
		case "counters":
			vals := c.counters[it.S]
			if len(vals) == 0 {
				sb.WriteString(formatCounter(0, it.Style))
			}
			for i, v := range vals {
				if i > 0 {
					sb.WriteString(it.Sep)
				}
				sb.WriteString(formatCounter(v, it.Style))
			}
		case "open-quote", "close-quote":
			depth := c.quoteDepth
			if it.K == "close-quote" {
				if depth > 0 {
					depth--
					c.quoteDepth--
				}
			} else {
				c.quoteDepth++
			}
			if len(pst.Quotes) > 0 {
				idx := depth
				if idx >= len(pst.Quotes) {
					idx = len(pst.Quotes) - 1
				}
				if it.K == "open-quote" {
					sb.WriteString(pst.Quotes[idx][0])
				} else {
					sb.WriteString(pst.Quotes[idx][1])
				}
			}
		case "no-open-quote":
			c.quoteDepth++
		case "no-close-quote":
			if c.quoteDepth > 0 {
				c.quoteDepth--
			}
		case "url":
			flush()
			if it.S != "" {
				im := newBox(n, c.eng.Anonymous(pst, "inline"), kAtomic)
				im.inner = iReplaced
				im.inlineLv = true
				im.rep = &replacedInfo{kind: "img", url: c.resolveURL(it.S)}
				c.loadImage(im.rep)
				kids = append(kids, im)
			}
		}
	}
	flush()
	for _, k := range kids {
		b.addKid(k)
	}
	c.fixupChildren(b)
	c.pseudoBoxes[n] = append(c.pseudoBoxes[n], b)
	return []*lbox{b}
}

func (c *ctx) counterValue(name string) int {
	s := c.counters[name]
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

func (c *ctx) addMarker(b *lbox, st *style.Computed) {
	if st.ListStyleType == "none" && st.ListStyleImage.IsNone() {
		return
	}
	// the counter value is evaluated when the li's own counters were applied
	val := c.counterValue("list-item")
	m := newBox(b.n, st, kMarker)
	m.pseudo = "marker"
	m.liIndex = val
	m.markerOutside = st.ListStylePos != "inside"
	b.marker = m
	if !m.markerOutside && st.ListStyleImage.IsNone() {
		// an inside marker is inline content at the start of the item
		var txt string
		t := st.ListStyleType
		switch {
		case strings.HasPrefix(t, "\x00"):
			txt = t[1:]
		case t == "disc":
			txt = "\u2022 "
		case t == "circle":
			txt = "\u25e6 "
		case t == "square":
			txt = "\u25aa "
		case t == "disclosure-closed":
			txt = "\u25b8 "
		case t == "disclosure-open":
			txt = "\u25be "
		default:
			txt = formatCounter(val, t) + ". "
		}
		// the marker's own box has line-height 0 so a fallback font cannot grow the line
		mst := *st
		mst.LineHeight = style.Px(0)
		if strings.HasPrefix(t, "disclosure") {
			// Chromium sizes the disclosure marker at 1.059375em
			mst.LetterSpacing = 0
			nat := c.textWidth(&mst, txt)
			mst.LetterSpacing = (st.FontSize*1.059375 - nat) / float64(len([]rune(txt)))
		}
		mb := newBox(b.n, &mst, kInline)
		mb.inlineLv = true
		mb.pseudo = "marker"
		tb := newBox(b.n, &mst, kText)
		tb.text = txt
		tb.inlineLv = true
		tb.pseudo = "marker"
		mb.addKid(tb)
		b.addKid(mb)
		b.marker = nil
	}
}

// ---- anonymous box fixup -----------------------------------------------------------

func isWS(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' }

func onlyWhitespace(s string) bool {
	for _, r := range s {
		if !isWS(r) {
			return false
		}
	}
	return true
}

func collapsibleWS(st *style.Computed) bool {
	switch st.WhiteSpace {
	case "pre", "pre-wrap", "break-spaces":
		return false
	}
	return true
}

func (b *lbox) isInFlowBlock() bool { return b.kind == kBlock && !b.floated && !b.oof }

// fixupChildren wraps inline runs in anonymous blocks when b has block-level
// children, and applies the display-specific child rules.
func (c *ctx) fixupChildren(b *lbox) {
	switch b.inner {
	case iFlex, iGrid:
		c.fixupItems(b)
		return
	case iTable:
		c.fixupTable(b)
		return
	case iRow, iRowGroup, iColGroup:
		c.fixupTableInternal(b)
		return
	case iReplaced, iControl:
		return
	}
	if b.kind == kInline {
		return // inline boxes keep their children as is; splitting handled by caller
	}
	c.wrapTableInternals(b)
	hasBlock := false
	for _, k := range b.kids {
		if k.isInFlowBlock() {
			hasBlock = true
			break
		}
	}
	if !hasBlock {
		b.ifc = len(b.kids) > 0
		// drop nothing; IFC will collapse whitespace
		return
	}
	var nk []*lbox
	var run []*lbox
	flushRun := func() {
		if len(run) == 0 {
			return
		}
		real := false
		for _, r := range run {
			if r.kind == kText && onlyWhitespace(r.text) && collapsibleWS(r.st) {
				continue
			}
			if r.floated || r.oof {
				real = true // floats/abspos survive inside the run
				continue
			}
			real = true
		}
		if !real {
			run = run[:0]
			return
		}
		// strip whitespace-only text at the ends of a run that has only floats
		onlyOOF := true
		for _, r := range run {
			if !(r.floated || r.oof) && !(r.kind == kText && onlyWhitespace(r.text) && collapsibleWS(r.st)) {
				onlyOOF = false
			}
		}
		if onlyOOF {
			for _, r := range run {
				if r.floated || r.oof {
					nk = append(nk, r)
				}
			}
			run = run[:0]
			return
		}
		an := newBox(nil, c.eng.Anonymous(b.st, "block"), kBlock)
		an.anon = true
		an.ifc = true
		for _, r := range run {
			an.addKid(r)
		}
		nk = append(nk, an)
		run = run[:0]
	}
	for _, k := range b.kids {
		if k.isInFlowBlock() {
			flushRun()
			nk = append(nk, k)
		} else {
			run = append(run, k)
		}
	}
	flushRun()
	b.kids = nil
	for _, k := range nk {
		b.addKid(k)
	}
}

// fixupItems prepares flex/grid container children: each in-flow child is an item.
func (c *ctx) fixupItems(b *lbox) {
	var nk []*lbox
	var run []*lbox
	flush := func() {
		if len(run) == 0 {
			return
		}
		real := false
		for _, r := range run {
			if !(r.kind == kText && onlyWhitespace(r.text) && collapsibleWS(r.st)) {
				real = true
			}
		}
		if real {
			an := newBox(nil, c.eng.Anonymous(b.st, "block"), kBlock)
			an.anon = true
			an.ifc = true
			for _, r := range run {
				an.addKid(r)
			}
			nk = append(nk, an)
		}
		run = run[:0]
	}
	for _, k := range b.kids {
		switch {
		case k.kind == kText:
			run = append(run, k)
		case k.kind == kBreak || k.kind == kMarker:
			run = append(run, k)
		case k.floated || k.oof:
			flush()
			nk = append(nk, k)
		default:
			flush()
			// blockify: items are block-level
			if k.kind == kInline || k.kind == kAtomic {
				wasInline := k.kind == kInline
				k.kind = kBlock
				k.inlineLv = false
				if wasInline && k.inner == iFlow {
					c.fixupChildren(k)
					k.kind = kBlock
				}
			}
			nk = append(nk, k)
		}
	}
	flush()
	b.kids = nil
	for _, k := range nk {
		b.addKid(k)
	}
}

// splitInline splits an inline box around block-level descendants. It returns
// nil when no splitting is needed.
func (c *ctx) splitInline(b *lbox) []*lbox {
	has := false
	for _, k := range b.kids {
		if k.isInFlowBlock() {
			has = true
		}
	}
	if !has {
		return nil
	}
	var out []*lbox
	var cur []*lbox
	var blocks []*lbox
	for _, k := range b.kids {
		if k.isInFlowBlock() {
			blocks = append(blocks, k)
		}
	}
	piece := 0
	mk := func() *lbox {
		nb := *b
		nb.kids = nil
		nb.cont = piece
		nb.split = true
		nb.contBlocks = blocks
		piece++
		for _, k := range cur {
			nb.addKid(k)
		}
		cur = nil
		return &nb
	}
	for _, k := range b.kids {
		if k.isInFlowBlock() {
			if len(cur) > 0 || piece == 0 {
				if len(cur) > 0 {
					out = append(out, mk())
				}
			}
			// blocks keep a reference so that a:hover etc. map; they stay block children
			out = append(out, k)
		} else {
			cur = append(cur, k)
		}
	}
	if len(cur) > 0 {
		p := mk()
		p.lastPart = true
		out = append(out, p)
	} else if piece == 0 {
		p := mk()
		p.lastPart = true
		out = append(out, p)
	} else {
		// last piece empty: mark the previous inline piece as last
		for i := len(out) - 1; i >= 0; i-- {
			if out[i].kind == kInline && out[i].n == b.n {
				out[i].lastPart = true
				break
			}
		}
	}
	// replace the byNode entry
	list := c.byNode[b.n]
	for i, x := range list {
		if x == b {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	for _, o := range out {
		if o.n == b.n && o.kind == kInline {
			list = append(list, o)
		}
	}
	c.byNode[b.n] = list
	return out
}

func formatCounter(v int, sty string) string {
	switch sty {
	case "", "decimal":
		return strconv.Itoa(v)
	case "decimal-leading-zero":
		if v >= 0 && v < 10 {
			return "0" + strconv.Itoa(v)
		}
		return strconv.Itoa(v)
	case "lower-alpha", "lower-latin":
		return alpha(v, 'a')
	case "upper-alpha", "upper-latin":
		return alpha(v, 'A')
	case "lower-roman":
		return strings.ToLower(roman(v))
	case "upper-roman":
		return roman(v)
	case "lower-greek":
		if v >= 1 && v <= 24 {
			return string(rune(0x3b1 + v - 1 + b2i(v > 17)))
		}
		return strconv.Itoa(v)
	case "disc":
		return "•"
	case "circle":
		return "◦"
	case "square":
		return "▪"
	case "none":
		return ""
	}
	return strconv.Itoa(v)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func alpha(v int, base rune) string {
	if v <= 0 {
		return strconv.Itoa(v)
	}
	var out []rune
	for v > 0 {
		v--
		out = append([]rune{base + rune(v%26)}, out...)
		v /= 26
	}
	return string(out)
}

func roman(v int) string {
	if v <= 0 || v >= 4000 {
		return strconv.Itoa(v)
	}
	vals := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
	syms := []string{"M", "CM", "D", "CD", "C", "XC", "L", "XL", "X", "IX", "V", "IV", "I"}
	var sb strings.Builder
	for i, x := range vals {
		for v >= x {
			sb.WriteString(syms[i])
			v -= x
		}
	}
	return sb.String()
}

// wrapTableInternals puts table-internal children of a non-table box into anonymous tables.
func (c *ctx) wrapTableInternals(b *lbox) {
	any := false
	for _, k := range b.kids {
		switch k.inner {
		case iRow, iRowGroup, iCell, iCaption, iColGroup, iCol:
			if k.kind == kBlock && !k.oof && !k.floated {
				any = true
			}
		}
	}
	if !any {
		return
	}
	var nk []*lbox
	var run []*lbox
	flush := func() {
		if len(run) == 0 {
			return
		}
		t := c.anonBox(b, "table", iTable)
		for _, r := range run {
			t.addKid(r)
		}
		c.fixupTable(t)
		nk = append(nk, t)
		run = nil
	}
	for _, k := range b.kids {
		isInt := false
		switch k.inner {
		case iRow, iRowGroup, iCell, iCaption, iColGroup, iCol:
			isInt = k.kind == kBlock && !k.oof && !k.floated
		}
		if isInt {
			run = append(run, k)
			continue
		}
		if len(run) > 0 && k.kind == kText && onlyWhitespace(k.text) {
			continue
		}
		flush()
		nk = append(nk, k)
	}
	flush()
	b.kids = nil
	for _, k := range nk {
		b.addKid(k)
	}
}
