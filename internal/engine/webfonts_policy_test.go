package engine

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

// paddedFont is a valid font file (WOFF2) of about n bytes: the Boxes font plus an incompressible table.
func paddedFont(family string, n int, seed int64) []byte {
	f := fonttest.Boxes(family, "Regular", 400, false)
	junk := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(junk)
	f.ExtraTables = map[string][]byte{"JUNK": junk}
	return fonttest.WOFF2(f.SFNT(), fonttest.Options{Transform: true})
}

type policySite struct {
	*fontSite
	sizes map[string]int // path -> bytes served
}

// newPolicySite serves a page whose text uses seven families:
//
//	Small   20 KB woff2         Big    400 KB woff2 (over the 300 KB per-font cap)
//	TTFonly 20 KB truetype      Mid1   250 KB woff2, Mid2 250 KB woff2, Mid3 250 KB woff2 (the page budget is 600 KB)
//	Material Icons (ligatures, 20 KB woff2)
func newPolicySite(t *testing.T) *policySite {
	s := &policySite{fontSite: newFontSite(t), sizes: map[string]int{}}
	add := func(path string, data []byte) {
		s.files[path] = fontFile{data: data, ctype: "font/woff2"}
		s.sizes[path] = len(data)
	}
	add("/small.woff2", paddedFont("Small", 20<<10, 1))
	add("/big.woff2", paddedFont("Big", 400<<10, 2))
	add("/mid1.woff2", paddedFont("Mid1", 250<<10, 3))
	add("/mid2.woff2", paddedFont("Mid2", 250<<10, 4))
	add("/mid3.woff2", paddedFont("Mid3", 250<<10, 5))
	ttf := fonttest.Boxes("TTFonly", "Regular", 400, false)
	ttf.ExtraTables = map[string][]byte{"JUNK": make([]byte, 20<<10)}
	add("/ttfonly.ttf", ttf.SFNT())
	add("/mi.woff2", fonttest.WOFF2(fonttest.Icons("Material Icons").SFNT(), fonttest.Options{Transform: true}))
	s.pages["/s.css"] = `
@font-face{font-family:Small;src:url(/small.woff2) format("woff2")}
@font-face{font-family:Big;src:url(/big.woff2) format("woff2")}
@font-face{font-family:Mid1;src:url(/mid1.woff2) format("woff2")}
@font-face{font-family:Mid2;src:url(/mid2.woff2) format("woff2")}
@font-face{font-family:Mid3;src:url(/mid3.woff2) format("woff2")}
@font-face{font-family:TTFonly;src:url(/ttfonly.ttf) format("truetype")}
@font-face{font-family:'Material Icons';src:url(/mi.woff2) format("woff2")}
@font-face{font-family:Pua;src:url(/small.woff2) format("woff2")}`
	s.pages["/"] = `<!doctype html><html><head><link rel="stylesheet" href="/s.css"></head><body style="margin:0;font-size:20px">
<span id="small" style="font-family:Small">Hello</span>
<span id="big" style="font-family:Big">Hello</span>
<span id="ttf" style="font-family:TTFonly">Hello</span>
<span id="mid1" style="font-family:Mid1">Hello</span>
<span id="mid2" style="font-family:Mid2">Hello</span>
<span id="mid3" style="font-family:Mid3">Hello</span>
<span id="icon" style="font-family:'Material Icons'">arrow_back</span>
</body></html>`
	return s
}

func (s *policySite) run(t *testing.T, pol *DataPolicy) (*BrowserContext, *Tab) {
	e := newDSEngine(t)
	c, tab := ctxTab(t, e, pol)
	nav(t, tab, s.srv.URL+"/")
	for _, id := range []string{"small", "big", "ttf", "mid1", "mid2", "mid3", "icon"} {
		if _, err := tab.Locator("#" + id).BoundingBox(); err != nil {
			t.Fatal(err)
		}
	}
	return c, tab
}

func (s *policySite) served(paths ...string) int {
	n := 0
	for _, p := range paths {
		if s.count(p) > 0 {
			n += s.sizes[p]
		}
	}
	return n
}

func widthOf(t *testing.T, tab *Tab, id string) float64 { return spanWidth(t, tab, "#"+id) }

func TestWebFontPolicyOffLoadsEverythingUsed(t *testing.T) {
	s := newPolicySite(t)
	c, tab := s.run(t, nil)
	for _, p := range []string{"/small.woff2", "/big.woff2", "/ttfonly.ttf", "/mid1.woff2", "/mid2.woff2", "/mid3.woff2", "/mi.woff2"} {
		if s.count(p) != 1 {
			t.Errorf("off: %s requested %d times", p, s.count(p))
		}
	}
	if w := widthOf(t, tab, "small"); !within(w, boxesWidth("Hello", 20, false), 1) {
		t.Errorf("small width %.1f", w)
	}
	st := c.DataStats()
	want := int64(s.served("/small.woff2", "/big.woff2", "/ttfonly.ttf", "/mid1.woff2", "/mid2.woff2", "/mid3.woff2", "/mi.woff2"))
	if got := st.ByKind[KindFont]; got < want || got > want+8*700 {
		t.Errorf("off: font bytes %d, the files are %d", got, want)
	}
	if st.Skipped[KindFont] != 0 {
		t.Errorf("off skipped %v", st.Skipped)
	}
}

func TestWebFontPolicyStandardCaps(t *testing.T) {
	s := newPolicySite(t)
	c, tab := s.run(t, polPtr(SaverStandard))
	// loaded: WOFF2 within the caps, in the order the page asks (the 600 KB page budget fits Small + Mid1 + Mid2 = 520 KB)
	for _, p := range []string{"/small.woff2", "/mid1.woff2", "/mid2.woff2", "/mi.woff2"} {
		if s.count(p) != 1 {
			t.Errorf("standard: %s requested %d times (%v)", p, s.count(p), s.requested())
		}
	}
	// not requested at all: plain TrueType under the WOFF-only rule
	if s.count("/ttfonly.ttf") != 0 {
		t.Errorf("TrueType file fetched under standard: %v", s.requested())
	}
	// Big (400 KB) is over the 300 KB per-font cap and Mid3 would exceed the page budget: requests are
	// made and cut off at the headers
	if s.count("/big.woff2") != 1 || s.count("/mid3.woff2") != 1 {
		t.Errorf("big/mid3 requests: %v", s.requested())
	}
	st := c.DataStats()
	if st.Skipped[KindFont] != 3 {
		t.Errorf("standard skipped %v, want 3 fonts (ttf format, big size, mid3 budget)", st.Skipped)
	}
	if st.AbortedBytes < int64(s.sizes["/big.woff2"]) {
		t.Errorf("aborted bytes %d, the big font is %d", st.AbortedBytes, s.sizes["/big.woff2"])
	}
	loaded := int64(s.served("/small.woff2", "/mid1.woff2", "/mid2.woff2", "/mi.woff2"))
	if got := st.ByKind[KindFont]; got < loaded || got > loaded+50<<10 {
		t.Errorf("font bytes %d for %d bytes of loaded fonts", got, loaded)
	}
	if w := widthOf(t, tab, "big"); within(w, boxesWidth("Hello", 20, false), 0.5) {
		t.Errorf("the skipped font was used")
	}
	skipped := map[string]string{}
	for _, it := range tab.Skipped() {
		if it.Kind == KindFont {
			skipped[it.URL[strings.LastIndex(it.URL, "/"):]] = it.Reason
		}
	}
	want := map[string]string{"/big.woff2": "font-size", "/mid3.woff2": "font-budget", "/ttfonly.ttf": "font-format"}
	for k, v := range want {
		if skipped[k] != v {
			t.Errorf("skip reason of %s: %q, want %q (all: %v)", k, skipped[k], v, skipped)
		}
	}
	// the icon font is loaded and the ligature is an icon (one glyph of the font size)
	if w := widthOf(t, tab, "icon"); !within(w, 20, 1) {
		t.Errorf("icon width %.1f", w)
	}
}

func TestWebFontPolicyStrictKeepsOnlyIconFonts(t *testing.T) {
	s := newPolicySite(t)
	c, tab := s.run(t, polPtr(SaverStrict))
	for _, p := range []string{"/small.woff2", "/big.woff2", "/ttfonly.ttf", "/mid1.woff2", "/mid2.woff2", "/mid3.woff2"} {
		if s.count(p) != 0 {
			t.Errorf("strict: %s requested", p)
		}
	}
	if s.count("/mi.woff2") != 1 {
		t.Errorf("strict: the icon font is kept: %v", s.requested())
	}
	if w := widthOf(t, tab, "icon"); !within(w, 20, 1) {
		t.Errorf("ligature icon width %.1f: the page would show the word", w)
	}
	st := c.DataStats()
	if st.Skipped[KindFont] != 6 {
		t.Errorf("strict skipped %v, want 6 fonts", st.Skipped)
	}
	if st.SkippedEstimate <= 0 {
		t.Errorf("skipped fonts of unknown size must be estimated: %+v", st)
	}
}

func TestWebFontPolicyStrictKeepsFontsThatDrawPrivateUse(t *testing.T) {
	// an icon font with an ordinary name is recognised by the characters that ask for it
	s := newFontSite(t)
	s.pages["/"] = `<!doctype html><html><head><link rel="stylesheet" href="/s.css"></head><body style="margin:0;font-size:20px">
<span id="a" style="font-family:Brand">Hello</span> <span id="b" style="font-family:Glyphs">&#xe900;</span></body></html>`
	s.pages["/s.css"] = `@font-face{font-family:Brand;src:url(/brand.woff2)}@font-face{font-family:Glyphs;src:url(/glyphs.woff2)}`
	ic := fonttest.Icons("Glyphs")
	s.files["/brand.woff2"] = fontFile{data: fonttest.BoxesWOFF2}
	s.files["/glyphs.woff2"] = fontFile{data: fonttest.WOFF2(ic.SFNT(), fonttest.Options{})}
	e := newDSEngine(t)
	_, tab := ctxTab(t, e, polPtr(SaverStrict))
	nav(t, tab, s.srv.URL+"/")
	if w := spanWidth(t, tab, "#b"); !within(w, 20, 1) {
		t.Errorf("private use icon width %.1f", w)
	}
	_, _ = tab.Locator("#a").BoundingBox()
	if s.count("/brand.woff2") != 0 || s.count("/glyphs.woff2") != 1 {
		t.Errorf("requests %v", s.requested())
	}
}

func TestWebFontPolicyKnownSizeOfSkippedFonts(t *testing.T) {
	// a font learned from the cache has a known size when a stricter policy skips it later
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2)}`
	s.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2, header: map[string]string{"Cache-Control": "max-age=3600"}}
	e := newDSEngine(t)
	c := e.NewContext("", ContextOptions{})
	t1, _ := c.NewTab("about:blank")
	nav(t, t1, s.srv.URL+"/")
	_, _ = t1.Locator("#s").BoundingBox()
	if s.count("/b.woff2") != 1 {
		t.Fatalf("first visit: %v", s.requested())
	}
	c.SetDataPolicy(PolicyFor(SaverStrict))
	c.ResetDataStats()
	t2, _ := c.NewTab("about:blank")
	nav(t, t2, s.srv.URL+"/")
	if w := spanWidth(t, t2, "#s"); within(w, boxesWidth("Hello", 100, false), 1) {
		t.Fatalf("strict used the font")
	}
	st := c.DataStats()
	if st.Skipped[KindFont] != 1 || st.SkippedKnownBytes <= int64(len(fonttest.BoxesWOFF2)) || st.SkippedKnownBytes > int64(len(fonttest.BoxesWOFF2))+700 {
		t.Fatalf("skipped %v known %d (the font is %d bytes)", st.Skipped, st.SkippedKnownBytes, len(fonttest.BoxesWOFF2))
	}
	if s.count("/b.woff2") != 1 {
		t.Fatalf("strict visit requested the font: %v", s.requested())
	}
}

func TestWebFontPolicyLoadFullyIgnoresTheCaps(t *testing.T) {
	s := newPolicySite(t)
	e := newDSEngine(t)
	_, tab := ctxTab(t, e, polPtr(SaverStrict))
	nav(t, tab, s.srv.URL+"/")
	_, _ = tab.Locator("#big").BoundingBox()
	if s.count("/big.woff2") != 0 {
		t.Fatal("strict fetched the big font")
	}
	if err := tab.LoadFully(); err != nil {
		t.Fatal(err)
	}
	_, _ = tab.Locator("#big").BoundingBox()
	if s.count("/big.woff2") != 1 {
		t.Fatalf("Load fully must load the fonts: %v", s.requested())
	}
	if w := widthOf(t, tab, "big"); !within(w, boxesWidth("Hello", 20, false), 1) {
		t.Fatalf("big font not applied after Load fully: %.1f", w)
	}
}

func TestIconFamilyNames(t *testing.T) {
	for name, want := range map[string]bool{
		"Material Icons": true, "Material Icons Outlined": true, "Material Symbols Rounded": true, "FontAwesome": true, "Font Awesome 6 Free": true,
		"fa-solid-900": true, "fa": true, "Glyphicons Halflings": true, "icomoon": true, "dashicons": true, "Ionicons": true, "bootstrap-icons": true,
		"codicon": true, "feather": true, "remixicon": true, "Roboto": false, "Open Sans": false, "Inter": false, "Fantasy Sans": false,
		"Famous": false, "Fastidious": false, "Fabulous": false, "Source Sans Pro": false, "Georgia": false, "Symbola": true,
	} {
		if got := isIconFamilyName(name); got != want {
			t.Errorf("isIconFamilyName(%q) = %v", name, got)
		}
	}
}

var _ = fmt.Sprint

func TestWebFontsRefusedInTheBackgroundAreRetried(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2)}`
	s.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2}
	e := newDSEngine(t)
	_, tab := ctxTab(t, e, polPtr(SaverStandard))
	tab.SetBackground(true)
	nav(t, tab, s.srv.URL+"/")
	web := boxesWidth("Hello", 100, false)
	if w := spanWidth(t, tab, "#s"); within(w, web, 1) || s.count("/b.woff2") != 0 {
		t.Fatalf("a background tab fetched a font: %.1f %v", w, s.requested())
	}
	tab.SetBackground(false)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if within(spanWidth(t, tab, "#s"), web, 1) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("the font was not loaded when the tab came back: %v", s.requested())
}
