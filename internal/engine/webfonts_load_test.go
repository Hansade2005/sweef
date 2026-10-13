package engine

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

// kernedBoxes is a Boxes font with a kerning pair and an "ff" ligature, so that text set in it
// exercises shaping.
func kernedBoxes(family string) *fonttest.Font {
	f := fonttest.Boxes(family, "Regular", 400, false)
	f.ShortLoca = false
	f.Kern = map[[2]int]int{{f.Cmap['A'], f.Cmap['V']}: -90, {f.Cmap['T'], f.Cmap['o']}: -60}
	lig := len(f.Glyphs)
	f.Glyphs = append(f.Glyphs, fonttest.Glyph{Contours: [][]fonttest.Pt{fonttest.Rect(40, 0, 1000, 720)}, Adv: 1040})
	f.Ligatures = []fonttest.Lig{{Comps: []int{f.Cmap['f'], f.Cmap['f']}, To: lig}}
	return f
}

func TestWebFontFOUTAsyncWindow(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2)}`
	s.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2, delay: 400 * time.Millisecond}
	e := New(DefaultOptions())
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetAsyncImages(true) // a window: the page is shown while it loads
	var updates atomic.Int32
	tab.SetUpdateHook(func() { updates.Add(1) })
	t0 := time.Now()
	if err := tab.Navigate(s.srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	first := spanWidth(t, tab, "#s")
	if d := time.Since(t0); d > 350*time.Millisecond {
		t.Fatalf("the first layout waited for the font (%v)", d)
	}
	web := boxesWidth("Hello", 100, false)
	if within(first, web, 1) {
		t.Fatalf("the first layout already has the web font: %.1f", first)
	}
	if first <= 0 {
		t.Fatal("no text painted before the font arrived")
	}
	deadline := time.Now().Add(3 * time.Second)
	got := first
	for time.Now().Before(deadline) && !within(got, web, 1) {
		time.Sleep(20 * time.Millisecond)
		got = spanWidth(t, tab, "#s")
	}
	if !within(got, web, 1) {
		t.Fatalf("after the font arrived: width %.1f, want %.1f", got, web)
	}
	if updates.Load() == 0 {
		t.Fatal("the window was not asked to lay out again when the font arrived")
	}
}

func TestWebFontHeadlessWaitsForFontsWithinTheSettleBudget(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2)}`
	s.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2, delay: 250 * time.Millisecond}
	o := DefaultOptions()
	o.SettleMs = 1500
	_, tab, got := loadWidth(t, s, o)
	if want := boxesWidth("Hello", 100, false); !within(got, want, 1) {
		t.Fatalf("a headless layout must include the web font: %.1f want %.1f", got, want)
	}
	// a font slower than the budget does not hang the command; it lands later
	s2 := newFontSite(t)
	s2.pages["/"] = fontPage
	s2.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2)}`
	s2.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2, delay: 1500 * time.Millisecond}
	o.SettleMs = 300
	t0 := time.Now()
	_, tab, got = loadWidth(t, s2, o)
	if d := time.Since(t0); d > 1300*time.Millisecond {
		t.Fatalf("the slow font held the command for %v", d)
	}
	web := boxesWidth("Hello", 100, false)
	if within(got, web, 1) {
		t.Fatalf("the slow font cannot be in the first layout: %.1f", got)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if within(spanWidth(t, tab, "#s"), web, 1) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the slow font never reached the layout")
}

func TestWebFontRendersLikeTheSameFontInstalled(t *testing.T) {
	sfnt := kernedBoxes("SysBoxy").SFNT()
	if err := font.RegisterWeb("SysBoxy", font.Style{Weight: 400}, sfnt); err != nil { // stands in for an installed font
		t.Fatal(err)
	}
	text := "AVA To office fftoffee Hello World, 0123 &amp; ofi ¡x!"
	mk := func(fam string, css string) []byte {
		s := newFontSite(t)
		s.pages["/"] = `<!doctype html><html><head><link rel="stylesheet" href="/s.css"></head><body style="margin:8px;font-family:` + fam + `,serif;font-size:21.6px;line-height:1.3">
<p>` + text + `</p><p style="font-size:13px;letter-spacing:0.5px">` + text + `</p><p style="font-size:37px">` + text + `</p></body></html>`
		s.pages["/s.css"] = css
		s.files["/w.ttf"] = fontFile{data: sfnt}
		e := New(DefaultOptions())
		defer e.Close()
		tab, _ := e.NewTab(s.srv.URL + "/")
		img, ok := tab.PaintRGBA(600, 300, 1, false)
		if !ok {
			t.Fatal("no render")
		}
		return append([]byte(nil), img.Pix...)
	}
	web := mk("WebBoxy", `@font-face{font-family:WebBoxy;src:url(/w.ttf)}`)
	sys := mk("SysBoxy", ``)
	fb := mk("NoSuchFont", ``)
	if !bytes.Equal(web, sys) {
		n := 0
		for i := range web {
			if web[i] != sys[i] {
				n++
			}
		}
		t.Fatalf("web font and installed font differ in %d of %d bytes", n, len(web))
	}
	if bytes.Equal(web, fb) {
		t.Fatal("the test does not distinguish the font from the fallback")
	}
}

func TestWebFontIncrementalLayoutStaysCorrect(t *testing.T) {
	SetLayoutCheck(true) // every cached pass is compared with a from-scratch one
	defer SetLayoutCheck(false)
	s := newFontSite(t)
	s.pages["/"] = `<!doctype html><html><head><link rel="stylesheet" href="/s.css"></head><body><p><span id="a" style="display:inline-block;font-family:Boxy,serif;font-size:30px">Hello there</span></p><p id="b">other</p></body></html>`
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2);} p{margin:4px}`
	s.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2, delay: 150 * time.Millisecond}
	e := New(DefaultOptions())
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetAsyncImages(true)
	nav(t, tab, s.srv.URL+"/")
	w0 := spanWidth(t, tab, "#a")
	// change the document while the font is on its way, then again after it arrived
	mustEval(t, tab, `document.getElementById('b').textContent='changed'`)
	_ = spanWidth(t, tab, "#b")
	time.Sleep(600 * time.Millisecond)
	_, _ = tab.Locator("#a").Count()
	w1 := spanWidth(t, tab, "#a")
	if within(w0, w1, 0.5) {
		t.Fatalf("width did not change with the font: %.1f -> %.1f", w0, w1)
	}
	mustEval(t, tab, `document.getElementById('a').style.fontSize='20px'`)
	if w2 := spanWidth(t, tab, "#a"); !(w2 < w1) {
		t.Fatalf("restyle after the font: %.1f -> %.1f", w1, w2)
	}
}

func TestWebFontsAreCachedOnDisk(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2)}`
	s.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2, header: map[string]string{"Cache-Control": "max-age=3600"}}
	e := New(DefaultOptions())
	defer e.Close()
	c := e.NewContext("", ContextOptions{})
	t1, _ := c.NewTab("about:blank")
	nav(t, t1, s.srv.URL+"/")
	if got := spanWidth(t, t1, "#s"); !within(got, boxesWidth("Hello", 100, false), 1) {
		t.Fatalf("first visit width %.1f", got)
	}
	st1 := c.DataStats()
	if st1.ByKind[KindFont] < int64(len(fonttest.BoxesWOFF2)) || st1.ByKind[KindFont] > int64(len(fonttest.BoxesWOFF2))+600 {
		t.Fatalf("font bytes %d for a %d byte file", st1.ByKind[KindFont], len(fonttest.BoxesWOFF2))
	}
	t2, _ := c.NewTab("about:blank")
	nav(t, t2, s.srv.URL+"/")
	if got := spanWidth(t, t2, "#s"); !within(got, boxesWidth("Hello", 100, false), 1) {
		t.Fatalf("second visit width %.1f", got)
	}
	if s.count("/b.woff2") != 1 {
		t.Fatalf("the second visit must come from the cache: %v", s.requested())
	}
	st2 := c.DataStats()
	if st2.ByKind[KindFont] != st1.ByKind[KindFont] {
		t.Fatalf("the cached font cost bytes: %d -> %d", st1.ByKind[KindFont], st2.ByKind[KindFont])
	}
	if st2.CacheHitBytes < int64(len(fonttest.BoxesWOFF2)) {
		t.Fatalf("cache hit bytes %d", st2.CacheHitBytes)
	}
}

func TestIconFontLigaturesRender(t *testing.T) {
	icons := fonttest.Icons("Material Icons")
	s := newFontSite(t)
	s.pages["/"] = `<!doctype html><html><head><link rel="stylesheet" href="/s.css"></head><body style="margin:0;font-size:16px">
<span id="i" class="material-icons">arrow_back</span> <span id="j" class="material-icons">check_circle_outline</span> <span id="k" class="material-icons">nonexistent</span></body></html>`
	s.pages["/s.css"] = `@font-face{font-family:'Material Icons';font-style:normal;font-weight:400;src:url(/mi.woff2) format('woff2')}
.material-icons{font-family:'Material Icons';font-weight:normal;font-style:normal;font-size:24px;line-height:1;letter-spacing:normal;text-transform:none;display:inline-block;white-space:nowrap;word-wrap:normal;direction:ltr;-webkit-font-feature-settings:'liga';font-feature-settings:'liga'}`
	s.files["/mi.woff2"] = fontFile{data: fonttest.WOFF2(icons.SFNT(), fonttest.Options{Transform: true})}
	_, tab, _ := loadWidth(t, s, DefaultOptions())
	// the ligature words become one 24px icon each
	if w := spanWidth(t, tab, "#i"); !within(w, 24, 1) {
		t.Errorf("arrow_back is %.1fpx wide, an icon is 24", w)
	}
	if w := spanWidth(t, tab, "#j"); !within(w, 24, 1) {
		t.Errorf("check_circle_outline is %.1fpx wide, an icon is 24", w)
	}
	// a word that is not an icon name stays letters (tiny advances in this font)
	if w := spanWidth(t, tab, "#k"); w > 5 || w < 0 {
		t.Errorf("nonexistent is %.1fpx wide", w)
	}
}

func TestFontsJSAPI(t *testing.T) {
	ttf := boxyTTF()
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/boxy.woff2) format("woff2");font-weight:400}`
	s.files["/boxy.woff2"] = fontFile{data: fonttest.BoxesWOFF2, delay: 60 * time.Millisecond}
	s.files["/dyn.ttf"] = fontFile{data: ttf, delay: 30 * time.Millisecond}
	s.files["/bad.woff2"] = fontFile{data: []byte("junk")}
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(s.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	ev := func(js string) string { return mustEval(t, tab, js) }

	if got := ev(`document.fonts instanceof FontFaceSet && typeof document.fonts.load === 'function' && typeof FontFace === 'function'`); got != "true" {
		t.Fatalf("api shape: %s", got)
	}
	// ready resolves once the fonts the first layout asked for are in
	if got := ev(`(async () => { const s = await document.fonts.ready; return [s === document.fonts, document.fonts.status, document.fonts.check('16px Boxy', 'Hello')].join() })()`); got != `"true,loaded,true"` {
		t.Fatalf("ready: %s", got)
	}
	// FontFace with a URL
	if got := ev(`(async () => {
	  const f = new FontFace('Dyn', 'url(/dyn.ttf)', { weight: '400' });
	  const s0 = f.status;
	  document.fonts.add(f);
	  const events = [];
	  document.fonts.onloadingdone = () => events.push('done');
	  const r = await f.load();
	  return [s0, f.status, r === f, document.fonts.has(f), document.fonts.check('16px Dyn'), f.family, f.weight].join()
	})()`); got != `"unloaded,loaded,true,true,true,Dyn,400"` {
		t.Fatalf("FontFace url: %s", got)
	}
	// text using the script-added font is laid out with it
	mustEval(t, tab, `document.getElementById('s').style.fontFamily = 'Dyn'`)
	if w := spanWidth(t, tab, "#s"); !within(w, boxesWidth("Hello", 100, false), 1) {
		t.Errorf("script-added font not used: %.1f", w)
	}
	// FontFace from an ArrayBuffer
	if got := ev(`(async () => {
	  const buf = await (await fetch('/dyn.ttf')).arrayBuffer();
	  const f = new FontFace('Buf', buf);
	  const loaded = await f.loaded;
	  document.fonts.add(f);
	  return [f.status, loaded === f, document.fonts.check('12px Buf')].join()
	})()`); got != `"loaded,true,true"` {
		t.Fatalf("FontFace buffer: %s", got)
	}
	// a face that cannot be parsed rejects loaded and reports error
	if got := ev(`(async () => {
	  const f = new FontFace('Bad', 'url(/bad.woff2)');
	  let err = '';
	  try { await f.load(); } catch (e) { err = e.name; }
	  return [f.status, err].join()
	})()`); got != `"error,NetworkError"` {
		t.Fatalf("FontFace failure: %s", got)
	}
	// invalid descriptors throw a SyntaxError
	if got := ev(`(() => { try { new FontFace('X', 'url(x)', { weight: 'heavy-ish' }); return 'no throw' } catch (e) { return e.name } })()`); got != `"SyntaxError"` {
		t.Fatalf("descriptor validation: %s", got)
	}
	// load(): resolves with the faces; unknown families resolve with none; bad shorthand rejects
	if got := ev(`(async () => {
	  const a = await document.fonts.load('16px Boxy', 'Hello');
	  const b = await document.fonts.load('16px NoSuchFamily');
	  let c = ''; try { await document.fonts.load('not a font'); } catch (e) { c = e.name }
	  return [a.length, a[0] && a[0].family, b.length, c].join()
	})()`); got != `"1,Boxy,0,SyntaxError"` {
		t.Fatalf("load: %s", got)
	}
	// iteration and size include the faces of the style sheet
	if got := ev(`(() => { const fams = []; document.fonts.forEach(f => fams.push(f.family)); return [document.fonts.size, fams.includes('Boxy'), fams.includes('Dyn')].join() })()`); !strings.HasSuffix(got, `,true,true"`) {
		t.Fatalf("iteration: %s", got)
	}
	// delete and clear
	if got := ev(`(() => { const n = document.fonts.size; document.fonts.clear(); return [n > document.fonts.size, document.fonts.size].join() })()`); got != `"true,1"` {
		t.Fatalf("clear removes the script faces only: %s", got)
	}
}

func TestFontsJSEvents(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = ``
	s.files["/dyn.ttf"] = fontFile{data: boxyTTF(), delay: 40 * time.Millisecond}
	e := New(DefaultOptions())
	defer e.Close()
	tab, _ := e.NewTab(s.srv.URL + "/")
	got := mustEval(t, tab, `(async () => {
	  const ev = [];
	  document.fonts.addEventListener('loading', () => ev.push('loading'));
	  document.fonts.addEventListener('loadingdone', e => ev.push('done:' + e.fontfaces.length));
	  const f = new FontFace('Ev', 'url(/dyn.ttf)');
	  document.fonts.add(f);
	  const p = f.load();
	  const ready = document.fonts.ready;
	  await p; await ready;
	  await new Promise(r => setTimeout(r, 50));
	  return ev.join('|') + '/' + document.fonts.status
	})()`)
	if got != `"loading|done:1/loaded"` {
		t.Fatalf("events: %s", got)
	}
}

func TestCanvasTextUsesWebFonts(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = `<!doctype html><html><head><link rel="stylesheet" href="/s.css"></head><body><canvas id="c" width="300" height="100"></canvas></body></html>`
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2)}`
	s.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2}
	e := New(DefaultOptions())
	defer e.Close()
	tab, _ := e.NewTab(s.srv.URL + "/")
	got := mustEval(t, tab, `(async () => {
	  const ctx = document.getElementById('c').getContext('2d');
	  ctx.font = '40px Boxy, serif';
	  const before = ctx.measureText('Hello').width;
	  await document.fonts.load('40px Boxy', 'Hello');
	  ctx.font = '40px Boxy, serif';
	  const after = ctx.measureText('Hello').width;
	  return JSON.stringify([before, after])
	})()`)
	want := boxesWidth("Hello", 40, false)
	var pair []float64
	if err := json.Unmarshal([]byte(strings.Trim(strings.ReplaceAll(got, `\"`, `"`), `"`)), &pair); err != nil || len(pair) != 2 {
		t.Fatalf("result %s", got)
	}
	if within(pair[0], want, 0.5) || !within(pair[1], want, 0.1) {
		t.Fatalf("canvas widths before/after the font: %v, want the second to be %.2f", pair, want)
	}
}
