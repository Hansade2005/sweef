package net

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func mustDoc(t *testing.T, src string) *Doc {
	t.Helper()
	d, err := ParseDocument(src, "https://t.test/")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func firstMatch(t *testing.T, d *Doc, sel string) *html.Node {
	t.Helper()
	n, err := d.queryOne(sel)
	if err != nil {
		t.Fatalf("%s: %v", sel, err)
	}
	return n
}

func TestCSSParseCommentsStringsAtRules(t *testing.T) {
	p := &cssParser{vw: 1280, scheme: "light"}
	p.parseSheet(`
		/* a comment { with braces } */
		@charset "utf-8";
		@import url("x.css") screen;
		.a { color: red; display: none; }
		.b::before { content: "}{ ; display:none"; display: none }
		.c { background: url(data:image/png;base64,AAA/*not a comment*/); visibility: hidden !important }
		@font-face { font-family: X; src: url(x.woff) }
		@keyframes spin { from { opacity: 0 } to { opacity: 1 } }
		@supports (display: grid) { .d { display: none } }
		@media print { .e { display: none } }
		@media screen { .f { display: none } }
		@media (min-width: 600px) { .g { display: none } @media (max-width: 2000px) { .h { display: none } } }
		.i { display: block; .nested { display: none } }
		.j { visibility : HIDDEN ! important ; }
		.k, .l { opacity: 0 }
		bad|selector { display: none }
	`)
	got := map[string]cssDecl{}
	orders := map[string]int{}
	for _, r := range p.rules {
		last := r.sel.parts[len(r.sel.parts)-1]
		key := strings.Join(last.classes, ".")
		got[key] = r.decls[0]
		orders[key] = r.order
	}
	if _, ok := got["a"]; !ok {
		t.Fatal("rule .a missing")
	}
	if len(p.rules) > 0 {
		for _, r := range p.rules {
			if len(r.decls) != 1 {
				t.Fatalf("expected only kept declarations, got %+v", r.decls)
			}
		}
	}
	if d := got["b"]; d.val != "none" {
		t.Fatalf("string with braces broke parsing: %+v", d)
	}
	if d := got["c"]; d.val != "hidden" || !d.important {
		t.Fatalf("url() with ; and !important: %+v", d)
	}
	for _, skipped := range []string{"d", "e"} {
		if _, ok := got[skipped]; ok {
			t.Fatalf("rule .%s should have been skipped", skipped)
		}
	}
	for _, kept := range []string{"f", "g", "h", "i", "j", "k", "l"} {
		if _, ok := got[kept]; !ok {
			t.Fatalf("rule .%s missing", kept)
		}
	}
	if d := got["i"]; d.val != "block" {
		t.Fatalf("nested rule leaked into parent: %+v", d)
	}
	if d := got["j"]; d.val != "hidden" || !d.important {
		t.Fatalf("spaced !important: %+v", d)
	}
	if orders["k"] != orders["l"] {
		t.Fatal("selector list entries must share source order")
	}
	if _, ok := got[""]; ok {
		t.Fatal("invalid selector must drop the rule")
	}
}

func TestCSSSpecificity(t *testing.T) {
	cases := map[string][3]uint32{
		"*":                    {0, 0, 0},
		"li":                   {0, 0, 1},
		"ul li":                {0, 0, 2},
		"ul ol+li":             {0, 0, 3},
		"h1 + *[rel=up]":       {0, 1, 1},
		"ul ol li.red":         {0, 1, 3},
		"li.red.level":         {0, 2, 1},
		"#x34y":                {1, 0, 0},
		"#s12:not(foo)":        {1, 0, 1},
		".foo :is(.bar, #baz)": {1, 1, 0},
		":where(.a, #b) p":     {0, 0, 1},
		"a::before":            {0, 0, 2},
		"a:hover":              {0, 1, 1},
	}
	for sel, want := range cases {
		g, err := compileSelector(sel)
		if err != nil {
			t.Fatalf("%s: %v", sel, err)
		}
		if got := specificity(g[0]); got != pack(want[0], want[1], want[2]) {
			t.Errorf("%s: got %d,%d,%d want %v", sel, got>>20, got>>10&1023, got&1023, want)
		}
	}
}

func TestCSSCascadeOrderAndImportant(t *testing.T) {
	d := mustDoc(t, `<div id="a" class="x y"><p class="x">one</p></div><div class="late">two</div><div class="imp" style="display:block">three</div>`)
	d.SetStylesheets([]string{`
		.x { display: none }
		div { display: block }        /* lower specificity: loses to .x */
		#a { display: flex }          /* id wins */
		p.x { display: inline }
		.late { display: none }
		.late { display: grid }       /* later source order wins */
		.imp { display: none !important } /* beats inline normal */
	`}, 1280, "light")
	if got := d.ComputedDisplay(firstMatch(t, d, "#a")); got != "flex" {
		t.Fatalf("#a display = %q", got)
	}
	if got := d.ComputedDisplay(firstMatch(t, d, "p")); got != "inline" {
		t.Fatalf("p display = %q", got)
	}
	if got := d.ComputedDisplay(firstMatch(t, d, ".late")); got != "grid" {
		t.Fatalf(".late display = %q", got)
	}
	if got := d.ComputedDisplay(firstMatch(t, d, ".imp")); got != "none" {
		t.Fatalf(".imp display = %q (rule !important must beat inline)", got)
	}
	// inline !important beats rule !important
	d2 := mustDoc(t, `<div class="imp" style="display:block !important">x</div>`)
	d2.SetStylesheets([]string{`.imp { display: none !important }`}, 1280, "light")
	if got := d2.ComputedDisplay(firstMatch(t, d2, ".imp")); got != "block" {
		t.Fatalf("inline !important lost: %q", got)
	}
	// no rules: UA defaults
	d3 := mustDoc(t, `<div><span>x</span><p hidden>y</p></div>`)
	if got := d3.ComputedDisplay(firstMatch(t, d3, "div")); got != "block" {
		t.Fatalf("div default = %q", got)
	}
	if got := d3.ComputedDisplay(firstMatch(t, d3, "span")); got != "inline" {
		t.Fatalf("span default = %q", got)
	}
	if got := d3.ComputedDisplay(firstMatch(t, d3, "p")); got != "none" {
		t.Fatalf("[hidden] default = %q", got)
	}
}

func TestCSSMediaQueries(t *testing.T) {
	cases := []struct {
		q      string
		vw     int
		scheme string
		want   bool
	}{
		{"", 1000, "light", true},
		{"screen", 1000, "light", true},
		{"all", 1000, "light", true},
		{"print", 1000, "light", false},
		{"only screen and (min-width: 600px)", 1000, "light", true},
		{"screen and (min-width: 600px)", 500, "light", false},
		{"(max-width: 600px)", 500, "light", true},
		{"(max-width: 600px)", 601, "light", false},
		{"(min-width: 40em)", 640, "light", true},
		{"(min-width: 40em)", 639, "light", false},
		{"(min-width: 600px) and (max-width: 900px)", 750, "light", true},
		{"(min-width: 600px) and (max-width: 900px)", 950, "light", false},
		{"print, screen and (max-width: 600px)", 500, "light", true},
		{"not screen", 500, "light", false},
		{"not print", 500, "light", true},
		{"(prefers-color-scheme: dark)", 500, "light", false},
		{"(prefers-color-scheme: dark)", 500, "dark", true},
		{"(hover: hover)", 500, "light", false},
		{"speech", 500, "light", false},
		{"(width <= 600px)", 600, "light", true},
		{"(400px <= width < 800px)", 799, "light", true},
		{"(400px <= width < 800px)", 800, "light", false},
		{"(orientation: landscape)", 800, "light", true},
	}
	for _, c := range cases {
		if got := evalMediaQuery(c.q, c.vw, c.scheme); got != c.want {
			t.Errorf("%q @ %d/%s = %v, want %v", c.q, c.vw, c.scheme, got, c.want)
		}
	}
	// against a document
	src := `<style>@media (max-width: 600px){ .mobile-only { display:none } }</style><a class="mobile-only" href="m">Mobile</a><a href="d">Desktop</a>`
	d := mustDoc(t, src)
	d.SetStylesheets(d.InlineStyleSheets(), 1280, "light")
	if y, _ := d.AriaSnapshotYAML(); !strings.Contains(y, "Mobile") {
		t.Fatalf("wide viewport should not hide .mobile-only:\n%s", y)
	}
	d.SetStylesheets(d.InlineStyleSheets(), 400, "light")
	if y, _ := d.AriaSnapshotYAML(); strings.Contains(y, "Mobile") || !strings.Contains(y, "Desktop") {
		t.Fatalf("narrow viewport should hide .mobile-only:\n%s", y)
	}
}

func TestCSSVisibilityInheritance(t *testing.T) {
	d := mustDoc(t, `<div class="hide"><p id="p"><em id="e">x</em></p><span id="s" style="visibility: visible">y</span></div><div id="o">z</div>`)
	d.SetStylesheets([]string{`.hide { visibility: hidden }`}, 1280, "light")
	if got := d.ComputedVisibility(firstMatch(t, d, ".hide")); got != "hidden" {
		t.Fatalf(".hide = %q", got)
	}
	for _, id := range []string{"#p", "#e"} {
		if got := d.ComputedVisibility(firstMatch(t, d, id)); got != "hidden" {
			t.Fatalf("%s should inherit hidden, got %q", id, got)
		}
	}
	if got := d.ComputedVisibility(firstMatch(t, d, "#s")); got != "visible" {
		t.Fatalf("#s overrides to visible, got %q", got)
	}
	if got := d.ComputedVisibility(firstMatch(t, d, "#o")); got != "visible" {
		t.Fatalf("#o = %q", got)
	}
	if vis, _ := d.IsVisible(d.idOf(firstMatch(t, d, "#e"))); vis {
		t.Fatal("IsVisible must honour inherited visibility:hidden")
	}
}

func TestCSSSnapshotHonoursStylesheet(t *testing.T) {
	src := `<style>.menu{display:none}</style><ul class="menu"><li><a href=x>Hidden</a></li></ul><a href=y>Shown</a>`
	d := mustDoc(t, src)
	before, err := d.AriaSnapshotYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "Hidden") || !strings.Contains(before, "Shown") {
		t.Fatalf("before SetStylesheets both links must appear:\n%s", before)
	}
	sheets := d.InlineStyleSheets()
	if len(sheets) != 1 || !strings.Contains(sheets[0], ".menu") {
		t.Fatalf("InlineStyleSheets = %q", sheets)
	}
	d.SetStylesheets(sheets, 1280, "light")
	after, err := d.AriaSnapshotYAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, "Hidden") || !strings.Contains(after, "Shown") {
		t.Fatalf("after SetStylesheets only Shown must appear:\n%s", after)
	}
	if n := firstMatch(t, d, ".menu a"); d.effectivelyVisible(n) {
		t.Fatal("effectivelyVisible must see the ancestor's display:none")
	}
	// mutation invalidates the cache: removing the class brings the menu back
	ul := firstMatch(t, d, "ul")
	d.SetAttr(d.idOf(ul), "class", "")
	if y, _ := d.AriaSnapshotYAML(); !strings.Contains(y, "Hidden") {
		t.Fatalf("after removing the class the menu must reappear:\n%s", y)
	}
	// and SetStylesheets again replaces the rules
	d.SetAttr(d.idOf(ul), "class", "menu")
	d.SetStylesheets(nil, 1280, "light")
	if y, _ := d.AriaSnapshotYAML(); !strings.Contains(y, "Hidden") {
		t.Fatalf("empty stylesheet set must show everything:\n%s", y)
	}
}

func TestCSSHiddenAttributeOverride(t *testing.T) {
	src := `<style>[hidden]{display:none} .show{display:block !important}</style>` +
		`<a hidden class="show" href=a>Forced</a><a hidden href=b>Gone</a><a href=c>Plain</a>`
	d := mustDoc(t, src)
	before, _ := d.AriaSnapshotYAML()
	if strings.Contains(before, "Forced") || strings.Contains(before, "Gone") {
		t.Fatalf("without stylesheets the hidden attribute always prunes:\n%s", before)
	}
	d.SetStylesheets(d.InlineStyleSheets(), 1280, "light")
	after, _ := d.AriaSnapshotYAML()
	if !strings.Contains(after, "Forced") {
		t.Fatalf(".show{display:block !important} must reveal a [hidden] element:\n%s", after)
	}
	if strings.Contains(after, "Gone") || !strings.Contains(after, "Plain") {
		t.Fatalf("plain [hidden] stays hidden, others shown:\n%s", after)
	}
	// aria-hidden and input[type=hidden] are never un-hidden by CSS
	d2 := mustDoc(t, `<style>*{display:block !important}</style><a aria-hidden="true" href=a>A</a><input type=hidden name=tok value=secret>`)
	d2.SetStylesheets(d2.InlineStyleSheets(), 1280, "light")
	if y, _ := d2.AriaSnapshotYAML(); strings.Contains(y, `"A"`) || strings.Contains(y, "secret") {
		t.Fatalf("aria-hidden/input[hidden] leaked:\n%s", y)
	}
}

func TestCSSScreenReaderOnly(t *testing.T) {
	src := `<style>
		.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
		.vh{position:absolute !important;clip-path:inset(50%);width:1px;height:1px;overflow:hidden}
	</style><a class="sr-only" href=s>Skip to content</a><a class="vh" href=v>Visually hidden</a><a href=p>Plain</a>`
	d := mustDoc(t, src)
	d.SetStylesheets(d.InlineStyleSheets(), 1280, "light")
	if !d.IsScreenReaderOnly(firstMatch(t, d, ".sr-only")) || !d.IsScreenReaderOnly(firstMatch(t, d, ".vh")) {
		t.Fatal("sr-only patterns not detected")
	}
	if d.IsScreenReaderOnly(firstMatch(t, d, "a[href=p]")) {
		t.Fatal("plain link flagged as sr-only")
	}
	// behaviour unchanged: still in the snapshot
	if y, _ := d.AriaSnapshotYAML(); !strings.Contains(y, "Skip to content") || !strings.Contains(y, "Visually hidden") {
		t.Fatalf("sr-only content must stay in snapshots:\n%s", y)
	}
}

func TestCSSStylesheetDiscovery(t *testing.T) {
	src := `<html><head>
		<link rel="preload" as="style" href="/pre.css">
		<link rel="stylesheet" href="/a.css">
		<link rel="stylesheet" media="print" href="/print.css">
		<link rel="alternate stylesheet" href="/alt.css">
		<link rel="Stylesheet" href="/b.css" media="screen">
		<link rel="icon" href="/favicon.ico">
		<style>.one{}</style>
		<style media="print">.p{}</style>
		<style media="(max-width: 600px)">.m{display:none}</style>
	</head><body><style>.two{}</style></body></html>`
	d := mustDoc(t, src)
	links := d.StylesheetLinks()
	if strings.Join(links, " ") != "/a.css /b.css" {
		t.Fatalf("StylesheetLinks = %q", links)
	}
	sheets := d.InlineStyleSheets()
	if len(sheets) != 3 || sheets[0] != ".one{}" || sheets[2] != ".two{}" {
		t.Fatalf("InlineStyleSheets = %q", sheets)
	}
	if !strings.HasPrefix(sheets[1], "@media (max-width: 600px)") {
		t.Fatalf("media attribute must be wrapped: %q", sheets[1])
	}
}

// TestCSSWikipediaCost reports the snapshot cost of the real wikipedia page
// with its inline <style> rules applied, next to the stylesheet-free cost.
func TestCSSWikipediaCost(t *testing.T) {
	src, err := os.ReadFile("testdata/wikipedia_go.html")
	if err != nil {
		t.Skip(err)
	}
	d := mustDoc(t, string(src))
	nodes := 0
	var count func(*html.Node)
	count = func(n *html.Node) {
		if n.Type == html.ElementNode {
			nodes++
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			count(c)
		}
	}
	count(d.Root)
	o := SnapshotOptions{MaxChars: 1 << 20}
	timeIt := func(f func()) time.Duration {
		best := time.Hour
		for i := 0; i < 5; i++ {
			d.invalidateStyles()
			t0 := time.Now()
			f()
			if el := time.Since(t0); el < best {
				best = el
			}
		}
		return best
	}
	plain := timeIt(func() { d.AriaSnapshotV2(o) })
	before, _ := d.AriaSnapshotV2(o)
	sheets := d.InlineStyleSheets()
	t0 := time.Now()
	d.SetStylesheets(sheets, 1280, "light")
	parse := time.Since(t0)
	d.cssMu.Lock()
	rules := len(d.css.rules)
	d.cssMu.Unlock()
	styled := timeIt(func() { d.AriaSnapshotV2(o) })
	after, _ := d.AriaSnapshotV2(o)
	t.Logf("wikipedia_go: %d elements, %d inline sheets -> %d kept rules; parse %v; snapshot plain %v, with stylesheets %v; output %d -> %d bytes",
		nodes, len(sheets), rules, parse, plain, styled, len(before), len(after))
	if styled > 200*time.Millisecond && !raceEnabled {
		t.Fatalf("styled snapshot too slow: %v", styled)
	}
}

// TestCSSLargePageCost builds a 10,000-node page with 2,000 rules and checks
// one snapshot stays well under the budget.
func TestCSSLargePageCost(t *testing.T) {
	var sb, css strings.Builder
	sb.WriteString("<html><body>")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&css, ".c%d .k%d > a.l%d:hover { color: red; display: none }\n", i, i%50, i%7)
		if i%4 == 0 {
			fmt.Fprintf(&css, "#id%d { display: none }\n", i*3)
		}
		if i%10 == 0 {
			fmt.Fprintf(&css, "div.w%d span { visibility: hidden }\n", i)
		}
	}
	css.WriteString(".hide { display: none } [data-x] { opacity: 0 }\n")
	for i := 0; i < 2500; i++ {
		fmt.Fprintf(&sb, `<div class="w%d c%d" id="id%d"><span class="k%d">t%d <a class="l%d" href="/%d">link</a></span><p>para %d</p></div>`, i, i%300, i, i%50, i, i%7, i, i)
	}
	sb.WriteString("</body></html>")
	d := mustDoc(t, sb.String())
	d.SetStylesheets([]string{css.String()}, 1280, "light")
	d.cssMu.Lock()
	rules := len(d.css.rules)
	d.cssMu.Unlock()
	o := SnapshotOptions{MaxChars: 1 << 22}
	best := time.Hour
	for i := 0; i < 3; i++ {
		d.invalidateStyles()
		t0 := time.Now()
		if _, err := d.AriaSnapshotV2(o); err != nil {
			t.Fatal(err)
		}
		if el := time.Since(t0); el < best {
			best = el
		}
	}
	t.Logf("10k-node page, %d rules: snapshot %v", rules, best)
	// Measured ~28 ms on Linux and ~105 ms on a loaded macOS runner; a quadratic regression costs seconds, so the
	// bound only has to separate those. The race detector is ~10x slower and is not checked.
	limit := 100 * time.Millisecond
	if runtime.GOOS != "linux" {
		limit = 300 * time.Millisecond
	}
	if best > limit && !raceEnabled {
		t.Fatalf("snapshot too slow: %v (limit %v)", best, limit)
	}
}
