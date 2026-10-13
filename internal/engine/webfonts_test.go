package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
			for k, v := range f.header {
				w.Header().Set(k, v)
			}
			if f.ctype != "" {
				w.Header().Set("Content-Type", f.ctype)
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
