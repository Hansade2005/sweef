package engine

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

// fontSite is a test server for web font pages. Every request is logged.
type fontSite struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	log   []string // "path" of every request, in order
	pages map[string]string
	files map[string]fontFile
}

type fontFile struct {
	data   []byte
	ctype  string
	header map[string]string
	status int
	delay  time.Duration
}

func newFontSite(t *testing.T) *fontSite {
	s := &fontSite{t: t, pages: map[string]string{}, files: map[string]fontFile{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.log = append(s.log, r.URL.Path)
		page, isPage := s.pages[r.URL.Path]
		f, isFile := s.files[r.URL.Path]
		s.mu.Unlock()
		switch {
		case isPage:
			ct := "text/html; charset=utf-8"
			if strings.HasSuffix(r.URL.Path, ".css") {
				ct = "text/css"
			}
			w.Header().Set("Content-Type", ct)
			fmt.Fprint(w, page)
		case isFile:
			if f.delay > 0 {
				time.Sleep(f.delay)
			}
			for k, v := range f.header {
				w.Header().Set(k, v)
			}
			if f.ctype != "" {
				w.Header().Set("Content-Type", f.ctype)
			}
			if w.Header().Get("Content-Length") == "" {
				w.Header().Set("Content-Length", strconv.Itoa(len(f.data)))
			}
			if f.status != 0 {
				w.WriteHeader(f.status)
			}
			w.Write(f.data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fontSite) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.log {
		if p == path {
			n++
		}
	}
	return n
}

func (s *fontSite) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

// boxesWidth is the width in px of text set in the Boxes test font (no kerning, no ligatures).
func boxesWidth(text string, size float64, bold bool) float64 {
	w := 0.0
	for _, r := range text {
		a := float64(fonttest.Adv(r))
		if bold {
			a = float64(int(a) * 108 / 100)
		}
		w += a * size / 1000
	}
	return w
}

func spanWidth(t *testing.T, tab *Tab, sel string) float64 {
	t.Helper()
	b, err := tab.Locator(sel).BoundingBox()
	if err != nil {
		t.Fatalf("%s: %v", sel, err)
	}
	return float64(b[2])
}

func within(a, b, tol float64) bool { return a-b <= tol && b-a <= tol }

const fontPage = `<!doctype html><html><head><meta charset="utf-8"><title>f</title>
<link rel="stylesheet" href="/s.css"></head><body style="margin:0">
<span id="s" style="font-family:Boxy,serif;font-size:100px">Hello</span>
</body></html>`

func TestWebFontWOFF2LoadsAndLaysOutAgain(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/boxy.woff2) format("woff2")}`
	s.files["/boxy.woff2"] = fontFile{data: fonttest.BoxesWOFF2, ctype: "font/woff2"}
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(s.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got := spanWidth(t, tab, "#s")
	want := boxesWidth("Hello", 100, false)
	if !within(got, want, 1) {
		t.Fatalf("width with the web font %.1f, want %.1f (requests %v)", got, want, s.requested())
	}
	if s.count("/boxy.woff2") != 1 {
		t.Fatalf("font requests: %v", s.requested())
	}
}

func TestFontsJSAPIBasic(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/boxy.woff2) format("woff2")}`
	s.files["/boxy.woff2"] = fontFile{data: fonttest.BoxesWOFF2, ctype: "font/woff2"}
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(s.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	r := mustEval(t, tab, `typeof document.fonts + ' ' + typeof FontFace + ' ' + document.fonts.size + ' ' + document.fonts.status`)
	t.Log(r)
}

// ---- formats, priority -----------------------------------------------------------

func boxyTTF() []byte { return fonttest.Boxes("Boxy", "Regular", 400, false).SFNT() }

func loadWidth(t *testing.T, s *fontSite, o Options) (*Engine, *Tab, float64) {
	t.Helper()
	e := New(o)
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab(s.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := tab.Locator("#s").Count(); n == 0 {
		_, _ = tab.Locator("body").BoundingBox() // lay the page out
		return e, tab, 0
	}
	return e, tab, spanWidth(t, tab, "#s")
}

func TestWebFontFormats(t *testing.T) {
	ttf := boxyTTF()
	for name, c := range map[string]struct {
		file fontFile
		fmt_ string
		url  string
	}{
		"woff":            {fontFile{data: fonttest.WOFF(ttf, true), ctype: "font/woff"}, `format("woff")`, "/f.woff"},
		"woff stored":     {fontFile{data: fonttest.WOFF(ttf, false), ctype: "application/font-woff"}, `format("woff")`, "/f.woff"},
		"woff2 stored":    {fontFile{data: fonttest.WOFF2(ttf, fonttest.Options{}), ctype: "font/woff2"}, `format("woff2")`, "/f.woff2"},
		"woff2 transform": {fontFile{data: fonttest.WOFF2(ttf, fonttest.Options{Transform: true}), ctype: "font/woff2"}, `format("woff2")`, "/f.woff2"},
		"woff2 brotli":    {fontFile{data: fonttest.BoxesWOFF2, ctype: "font/woff2"}, `format("woff2")`, "/f.woff2"},
		"ttf":             {fontFile{data: ttf, ctype: "font/ttf"}, `format("truetype")`, "/f.ttf"},
		"ttf no hint":     {fontFile{data: ttf, ctype: "application/octet-stream"}, ``, "/f.ttf"},
		"woff2 no ext":    {fontFile{data: fonttest.BoxesWOFF2}, `format("woff2")`, "/font?v=3"},
	} {
		s := newFontSite(t)
		s.pages["/"] = fontPage
		s.pages["/s.css"] = fmt.Sprintf(`@font-face{font-family:Boxy;src:url(%s) %s}`, c.url, c.fmt_)
		s.files[strings.SplitN(c.url, "?", 2)[0]] = c.file
		_, _, got := loadWidth(t, s, DefaultOptions())
		if want := boxesWidth("Hello", 100, false); !within(got, want, 1) {
			t.Errorf("%s: width %.1f, want %.1f (requests %v)", name, got, want, s.requested())
		}
	}
}

func TestWebFontSourceSelection(t *testing.T) {
	ttf := boxyTTF()
	s := newFontSite(t)
	s.pages["/"] = fontPage
	// eot and svg are unusable; the TrueType file is listed before the WOFF2 one but WOFF2 is preferred;
	// tech(color-COLRv1) sources are skipped; the 404 source is skipped after being tried
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:
	  url(/f.eot?#iefix) format("embedded-opentype"),
	  url(/f.svg#x) format("svg"),
	  url(/color.woff2) format("woff2") tech(color-COLRv1),
	  url(/f.ttf) format("truetype"),
	  url(/f.woff) format("woff"),
	  url(/f.woff2) format("woff2")}`
	s.files["/f.ttf"] = fontFile{data: ttf}
	s.files["/f.woff"] = fontFile{data: fonttest.WOFF(ttf, true)}
	s.files["/f.woff2"] = fontFile{data: fonttest.BoxesWOFF2, status: 404}
	_, _, got := loadWidth(t, s, DefaultOptions())
	if want := boxesWidth("Hello", 100, false); !within(got, want, 1) {
		t.Fatalf("width %.1f, want %.1f", got, want)
	}
	reqs := strings.Join(s.requested(), " ")
	for _, never := range []string{"/f.eot", "/f.svg", "/color.woff2", "/f.ttf"} {
		if strings.Contains(reqs, never) {
			t.Errorf("%s must not be requested: %v", never, s.requested())
		}
	}
	if s.count("/f.woff2") != 1 || s.count("/f.woff") != 1 {
		t.Errorf("woff2 is tried first (404), then woff: %v", s.requested())
	}
}

func TestWebFontUnicodeRangeFetchesOnlyNeededSubsets(t *testing.T) {
	ttf := boxyTTF()
	css := `
@font-face{font-family:Boxy;src:url(/latin.woff2) format("woff2");unicode-range:U+0000-00FF}
@font-face{font-family:Boxy;src:url(/cyr.woff2) format("woff2");unicode-range:U+0400-04FF}
@font-face{font-family:Boxy;src:url(/cjk.woff2) format("woff2");unicode-range:U+4E00-9FFF}`
	mk := func() *fontSite {
		s := newFontSite(t)
		s.pages["/s.css"] = css
		for _, n := range []string{"latin", "cyr", "cjk"} {
			s.files["/"+n+".woff2"] = fontFile{data: fonttest.WOFF2(ttf, fonttest.Options{Transform: true})}
		}
		return s
	}
	// Latin text only: one subset
	s := mk()
	s.pages["/"] = fontPage
	_, _, got := loadWidth(t, s, DefaultOptions())
	if want := boxesWidth("Hello", 100, false); !within(got, want, 1) {
		t.Fatalf("latin width %.1f want %.1f", got, want)
	}
	if r := s.requested(); s.count("/latin.woff2") != 1 || s.count("/cyr.woff2") != 0 || s.count("/cjk.woff2") != 0 {
		t.Fatalf("only the Latin subset is needed: %v", r)
	}
	// Latin and Cyrillic text: two subsets; the Cyrillic file also contains Latin letters,
	// which unicode-range keeps from being used
	s = mk()
	s.pages["/"] = strings.Replace(fontPage, "Hello", "HelloЖ", 1)
	_, _, got = loadWidth(t, s, DefaultOptions())
	if want := boxesWidth("Hello", 100, false) + 76; !within(got, want, 1.5) {
		t.Fatalf("latin+cyrillic width %.1f want %.1f (%v)", got, want, s.requested())
	}
	if s.count("/latin.woff2") != 1 || s.count("/cyr.woff2") != 1 || s.count("/cjk.woff2") != 0 {
		t.Fatalf("two subsets: %v", s.requested())
	}
}

func TestWebFontTwoFacesOneFileIsOneRequest(t *testing.T) {
	// Google Fonts serves one variable file for several weights: two rules, one URL
	s := newFontSite(t)
	s.pages["/"] = strings.Replace(fontPage, `>Hello<`, `>Hello <b style="font-family:Boxy">Bold</b><`, 1)
	s.pages["/s.css"] = `@font-face{font-family:Boxy;font-weight:400;src:url(/v.woff2)}
@font-face{font-family:Boxy;font-weight:700;src:url(/v.woff2)}`
	s.files["/v.woff2"] = fontFile{data: fonttest.BoxesWOFF2}
	loadWidth(t, s, DefaultOptions())
	if s.count("/v.woff2") != 1 {
		t.Fatalf("one URL, one request: %v", s.requested())
	}
}

func TestWebFontWeightsPickTheRightFace(t *testing.T) {
	reg := fonttest.Boxes("Boxy", "Regular", 400, false).SFNT()
	bold := fonttest.Boxes("Boxy", "Bold", 700, false).SFNT()
	s := newFontSite(t)
	s.pages["/"] = `<!doctype html><html><head><link rel="stylesheet" href="/s.css"></head><body style="margin:0;font-size:100px;font-family:Boxy">
<span id="r">Hello</span><br><b id="b">Hello</b><br><span id="m" style="font-weight:500">Hello</span></body></html>`
	s.pages["/s.css"] = `@font-face{font-family:Boxy;font-weight:400;src:url(/r.ttf)}
@font-face{font-family:Boxy;font-weight:700;src:url(/b.ttf)}`
	s.files["/r.ttf"] = fontFile{data: reg}
	s.files["/b.ttf"] = fontFile{data: bold}
	_, tab, _ := loadWidth(t, s, DefaultOptions())
	if w, want := spanWidth(t, tab, "#r"), boxesWidth("Hello", 100, false); !within(w, want, 1) {
		t.Errorf("regular %.1f want %.1f", w, want)
	}
	if w, want := spanWidth(t, tab, "#b"), boxesWidth("Hello", 100, true); !within(w, want, 1.5) {
		t.Errorf("bold %.1f want %.1f", w, want)
	}
	if w, want := spanWidth(t, tab, "#m"), boxesWidth("Hello", 100, false); !within(w, want, 1) {
		t.Errorf("weight 500 uses the 400 face: %.1f want %.1f", w, want)
	}
}

func TestVariableWebFontWeights(t *testing.T) {
	vf := fonttest.VarBoxes("Var", true)
	s := newFontSite(t)
	s.pages["/"] = `<!doctype html><html><head><link rel="stylesheet" href="/s.css"></head><body style="margin:0;font-size:100px;font-family:Vf">
<span id="r" style="font-weight:400">mm</span><br><span id="b" style="font-weight:900">mm</span><br><span id="x" style="font-weight:650">mm</span><br><span id="l" style="font-weight:100">mm</span></body></html>`
	s.pages["/s.css"] = `@font-face{font-family:Vf;font-weight:100 900;src:url(/v.woff2) format("woff2")}`
	s.files["/v.woff2"] = fontFile{data: fonttest.WOFF2(vf.SFNT(), fonttest.Options{Transform: true})}
	_, tab, _ := loadWidth(t, s, DefaultOptions())
	base := float64(fonttest.Adv('m'))
	for sel, delta := range map[string]float64{"#r": 0, "#b": 160, "#x": 80, "#l": -100} {
		if w, want := spanWidth(t, tab, sel), 2*(base+delta)/10; !within(w, want, 1.5) {
			t.Errorf("%s: width %.1f, want %.1f", sel, w, want)
		}
	}
	if s.count("/v.woff2") != 1 {
		t.Errorf("requests %v", s.requested())
	}
}

// ---- failure ---------------------------------------------------------------------

func TestWebFontFailuresFallBackWithoutBreakingLayout(t *testing.T) {
	good := boxyTTF()
	for name, f := range map[string]fontFile{
		"404":          {data: []byte("nope"), status: 404},
		"500":          {data: []byte("boom"), status: 500},
		"garbage":      {data: []byte(strings.Repeat("this is not a font ", 100))},
		"html":         {data: []byte("<html><body>login</body></html>"), ctype: "text/html"},
		"empty":        {data: nil},
		"truncated":    {data: good[:len(good)/2]},
		"corrupt woff": {data: append([]byte("wOFF"), make([]byte, 100)...)},
	} {
		s := newFontSite(t)
		s.pages["/"] = fontPage + "<!-- " + name + " -->"
		s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/bad.woff2) format("woff2")}`
		s.files["/bad.woff2"] = f
		// reference: the same page without the web font
		ref := newFontSite(t)
		ref.pages["/"] = fontPage
		ref.pages["/s.css"] = ``
		_, _, want := loadWidth(t, ref, DefaultOptions())
		_, tab, got := loadWidth(t, s, DefaultOptions())
		if got != want {
			t.Errorf("%s: width %.1f, the fallback has %.1f", name, got, want)
		}
		if got <= 0 {
			t.Errorf("%s: no layout", name)
		}
		if txt, _ := tab.Text("#s"); txt != "Hello" {
			t.Errorf("%s: text %q", name, txt)
		}
		if s.count("/bad.woff2") != 1 {
			t.Errorf("%s: requests %v", name, s.requested())
		}
	}
}

func TestWebFontFallsThroughToNextSource(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/a.woff2) format("woff2"),url(/b.woff) format("woff"),url(/c.ttf) format("truetype")}`
	s.files["/a.woff2"] = fontFile{data: []byte("garbage"), status: 200}
	s.files["/b.woff"] = fontFile{data: []byte("missing"), status: 404}
	s.files["/c.ttf"] = fontFile{data: boxyTTF()}
	_, _, got := loadWidth(t, s, DefaultOptions())
	if want := boxesWidth("Hello", 100, false); !within(got, want, 1) {
		t.Fatalf("width %.1f want %.1f (%v)", got, want, s.requested())
	}
	if len(s.requested()) != 5 { // page, css, a, b, c
		t.Fatalf("requests %v", s.requested())
	}
}

func TestWebFontNextFamilyIsUsedWhileAndIfLoadFails(t *testing.T) {
	// font-family: Missing, Boxy — the first is declared but fails; the second still applies
	s := newFontSite(t)
	s.pages["/"] = strings.Replace(fontPage, "Boxy,serif", "Missing,Boxy,serif", 1)
	s.pages["/s.css"] = `@font-face{font-family:Missing;src:url(/m.woff2)}@font-face{font-family:Boxy;src:url(/b.ttf)}`
	s.files["/m.woff2"] = fontFile{status: 404}
	s.files["/b.ttf"] = fontFile{data: boxyTTF()}
	_, _, got := loadWidth(t, s, DefaultOptions())
	if want := boxesWidth("Hello", 100, false); !within(got, want, 1) {
		t.Fatalf("width %.1f want %.1f (%v)", got, want, s.requested())
	}
}

// ---- CORS ------------------------------------------------------------------------

func TestWebFontCrossOriginNeedsCORS(t *testing.T) {
	ttf := boxyTTF()
	for _, c := range []struct {
		name   string
		acao   string
		origin bool
		ok     bool
	}{
		{"no header", "", false, false},
		{"star", "*", false, true},
		{"page origin", "ORIGIN", false, true},
		{"other origin", "https://example.com", false, false},
		{"null", "null", false, false},
	} {
		fonts := newFontSite(t)
		hdr := map[string]string{}
		if c.acao != "" {
			hdr["Access-Control-Allow-Origin"] = c.acao
		}
		fonts.files["/b.ttf"] = fontFile{data: ttf, header: hdr}
		page := newFontSite(t)
		if c.acao == "ORIGIN" {
			fonts.files["/b.ttf"] = fontFile{data: ttf, header: map[string]string{"Access-Control-Allow-Origin": page.srv.URL}}
		}
		page.pages["/"] = fontPage
		page.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(` + fonts.srv.URL + `/b.ttf)}`
		// the origin header the font server saw
		var seen string
		fonts.srv.Config.Handler = wrapHandler(fonts.srv.Config.Handler, func(r *http.Request) { seen = r.Header.Get("Origin") })
		_, tab, got := loadWidth(t, page, DefaultOptions())
		want := boxesWidth("Hello", 100, false)
		if c.ok != within(got, want, 1) {
			t.Errorf("%s: width %.1f (web font width %.1f), allowed=%v\n%s", c.name, got, want, c.ok, consoleText(tab))
		}
		if seen != page.srv.URL {
			t.Errorf("%s: the font request carried Origin %q, want %q", c.name, seen, page.srv.URL)
		}
		if !c.ok && !strings.Contains(consoleText(tab), "CORS") {
			t.Errorf("%s: no console message about CORS", c.name)
		}
	}
}

func wrapHandler(h http.Handler, f func(r *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f(r)
		h.ServeHTTP(w, r)
	})
}

func TestWebFontSameOriginNeedsNoHeader(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.ttf)}`
	s.files["/b.ttf"] = fontFile{data: boxyTTF()}
	var origin = "unset"
	s.srv.Config.Handler = wrapHandler(s.srv.Config.Handler, func(r *http.Request) {
		if r.URL.Path == "/b.ttf" {
			origin = r.Header.Get("Origin")
		}
	})
	_, _, got := loadWidth(t, s, DefaultOptions())
	if !within(got, boxesWidth("Hello", 100, false), 1) {
		t.Fatalf("width %.1f", got)
	}
	if origin != "" {
		t.Fatalf("a same-origin font request has no Origin header: %q", origin)
	}
}

func TestWebFontDataURL(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(data:font/woff2;base64,` + base64.StdEncoding.EncodeToString(fonttest.BoxesWOFF2) + `) format("woff2")}`
	_, _, got := loadWidth(t, s, DefaultOptions())
	if !within(got, boxesWidth("Hello", 100, false), 1) {
		t.Fatalf("width %.1f", got)
	}
	if s.requested()[len(s.requested())-1] != "/s.css" {
		t.Fatalf("a data URL font needs no request: %v", s.requested())
	}
}

func TestGoogleFontsCSSGetsABrowserUserAgent(t *testing.T) {
	if got := fontCSSUserAgent("fonts.googleapis.com", "Sweef/0.1"); !strings.Contains(got, "Chrome/") || !strings.HasSuffix(got, "Sweef/0.1") {
		t.Fatalf("google fonts user agent: %q", got)
	}
	if got := fontCSSUserAgent("example.com", "Sweef/0.1"); got != "Sweef/0.1" {
		t.Fatalf("other hosts keep the agent: %q", got)
	}
}

func TestWebFontRequestsCarryCookiesOnlyToTheirOwnOrigin(t *testing.T) {
	ttf := boxyTTF()
	fonts := newFontSite(t)
	fonts.files["/b.ttf"] = fontFile{data: ttf, header: map[string]string{"Access-Control-Allow-Origin": "*", "Set-Cookie": "tracker=1; Path=/"}}
	var crossCookie, sameCookie string
	fonts.srv.Config.Handler = wrapHandler(fonts.srv.Config.Handler, func(r *http.Request) { crossCookie = r.Header.Get("Cookie") })
	page := newFontSite(t)
	page.pages["/"] = fontPage
	page.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(` + fonts.srv.URL + `/b.ttf)}`
	page.files["/same.ttf"] = fontFile{data: ttf}
	page.srv.Config.Handler = wrapHandler(page.srv.Config.Handler, func(r *http.Request) {
		if r.URL.Path == "/same.ttf" {
			sameCookie = r.Header.Get("Cookie")
		}
	})
	e := New(DefaultOptions())
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	if err := tab.Context().AddCookies([]map[string]any{{"name": "sid", "value": "42", "domain": "127.0.0.1", "path": "/"}}); err != nil {
		t.Fatal(err)
	}
	nav(t, tab, page.srv.URL+"/")
	if w := spanWidth(t, tab, "#s"); !within(w, boxesWidth("Hello", 100, false), 1) {
		t.Fatalf("cross-origin font not loaded: %.1f", w)
	}
	if crossCookie != "" {
		t.Errorf("a cross-origin font request carried cookies: %q", crossCookie)
	}
	if got := tab.Context().JarRef().ForURL(fonts.srv.URL + "/"); strings.Contains(got, "tracker") {
		t.Errorf("a cookie set by a cross-origin font response was stored: %q", got)
	}
	// same origin: credentials are sent
	page.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/same.ttf)}`
	nav(t, tab, page.srv.URL+"/")
	_ = spanWidth(t, tab, "#s")
	if !strings.Contains(sameCookie, "sid=42") {
		t.Errorf("a same-origin font request must carry the cookies: %q", sameCookie)
	}
}

func TestWebFontSetsAreReleasedWithTheirDocument(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = fontPage
	s.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(/b.woff2)}`
	s.pages["/plain"] = `<!doctype html><p>no fonts</p>`
	s.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2}
	before := font.OpenSets()
	e := New(DefaultOptions())
	tab, _ := e.NewTab("about:blank")
	for i := 0; i < 3; i++ {
		nav(t, tab, s.srv.URL+"/")
		_ = spanWidth(t, tab, "#s")
	}
	if n := font.OpenSets() - before; n != 1 {
		t.Fatalf("%d sets open after three navigations of one tab, want 1", n)
	}
	nav(t, tab, s.srv.URL+"/plain")
	_, _ = tab.Locator("p").BoundingBox()
	if n := font.OpenSets() - before; n != 0 {
		t.Fatalf("%d sets open on a page without web fonts", n)
	}
	nav(t, tab, s.srv.URL+"/")
	_ = spanWidth(t, tab, "#s")
	e.Close()
	if n := font.OpenSets() - before; n != 0 {
		t.Fatalf("%d sets left after the engine closed", n)
	}
}

func TestCrossOriginWebFontIsCachedWithVaryOrigin(t *testing.T) {
	fonts := newFontSite(t)
	page := newFontSite(t)
	fonts.files["/b.woff2"] = fontFile{data: fonttest.BoxesWOFF2, header: map[string]string{
		"Access-Control-Allow-Origin": page.srv.URL, "Vary": "Origin", "Cache-Control": "public, max-age=31536000, immutable"}}
	page.pages["/"] = fontPage
	page.pages["/s.css"] = `@font-face{font-family:Boxy;src:url(` + fonts.srv.URL + `/b.woff2) format("woff2")}`
	e := New(DefaultOptions())
	defer e.Close()
	c := e.NewContext("", ContextOptions{})
	for i := 0; i < 2; i++ {
		tab, _ := c.NewTab("about:blank")
		nav(t, tab, page.srv.URL+"/")
		if w := spanWidth(t, tab, "#s"); !within(w, boxesWidth("Hello", 100, false), 1) {
			t.Fatalf("visit %d: width %.1f", i, w)
		}
	}
	if fonts.count("/b.woff2") != 1 {
		t.Fatalf("the second visit must come from the cache: %d requests", fonts.count("/b.woff2"))
	}
}
