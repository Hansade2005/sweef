package engine

import (
	"strings"
	"testing"
	"time"
)

func TestFrameAncestorPolicies(t *testing.T) {
	bank := newSite(t, map[string]string{})
	for _, p := range []string{"/deny", "/same", "/none", "/self", "/other", "/mine", "/wild", "/open"} {
		bank.set(p, `<!doctype html><title>`+p+`</title><body>bank `+p)
	}
	bank.setHeader("/deny", "X-Frame-Options", "DENY")
	bank.setHeader("/same", "X-Frame-Options", "SAMEORIGIN")
	bank.setHeader("/none", "Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
	bank.setHeader("/self", "Content-Security-Policy", "frame-ancestors 'self'")
	bank.setHeader("/other", "Content-Security-Policy", "frame-ancestors https://elsewhere.example")
	bank.setHeader("/wild", "Content-Security-Policy", "frame-ancestors http://127.0.0.1:*")
	// CSP wins over a permissive X-Frame-Options? both present: frame-ancestors decides
	bank.setHeader("/open", "X-Frame-Options", "DENY")
	bank.setHeader("/open", "Content-Security-Policy", "frame-ancestors *")
	embedder := newSite(t, map[string]string{})
	var frames []string
	for _, p := range []string{"/deny", "/same", "/none", "/self", "/other", "/wild", "/open"} {
		frames = append(frames, `<iframe src="`+bank.URL+p+`"></iframe>`)
	}
	embedder.set("/", `<!doctype html>`+strings.Join(frames, ""))
	// the bank embedding its own pages
	bank.set("/embed", `<!doctype html><iframe src="`+bank.URL+`/same"></iframe><iframe src="`+bank.URL+`/self"></iframe><iframe src="`+bank.URL+`/deny"></iframe>`)

	e := newEng(t)
	tab, err := e.NewTab(embedder.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	shown := func(f FrameInfo) bool {
		txt := ""
		waitFor(t, "frame to settle", 5*time.Second, func() bool {
			c, _ := f.Tab.Text("body")
			txt = c
			return txt != ""
		})
		return strings.Contains(txt, "bank")
	}
	want := map[string]bool{"/deny": false, "/same": false, "/none": false, "/self": false, "/other": false, "/wild": true, "/open": true}
	fr := tab.Frames()
	for i, p := range []string{"/deny", "/same", "/none", "/self", "/other", "/wild", "/open"} {
		if got := shown(fr[i]); got != want[p] {
			t.Errorf("%s embedded cross-origin: shown=%v want %v", p, got, want[p])
		}
	}
	// same-origin embedding: sameorigin and 'self' are fine, deny is not
	tab2, err := e.NewTab(bank.URL + "/embed")
	if err != nil {
		t.Fatal(err)
	}
	fr2 := tab2.Frames()
	for i, want := range []bool{true, true, false} {
		if got := shown(fr2[i]); got != want {
			t.Errorf("same-origin embed %d: shown=%v want %v", i, got, want)
		}
	}
	// a refused frame did not keep the bank's origin or content
	if u := fr[0].Tab.URL(); u != "about:blank" {
		t.Fatalf("refused frame URL %q", u)
	}
}

func TestFrameSourceMatching(t *testing.T) {
	for _, c := range []struct {
		src      string
		ancestor string
		want     bool
	}{
		{"*", "https://a.example", true},
		{"'none'", "https://a.example", false},
		{"https:", "https://a.example", true},
		{"https:", "http://a.example", false},
		{"*.example.com", "https://x.example.com", true},
		{"*.example.com", "https://example.com", false},
		{"example.com", "https://example.com", true},
		{"example.com", "https://example.com:8443", false},
		{"example.com:*", "https://example.com:8443", true},
		{"https://example.com", "http://example.com", false},
		{"'self'", "https://doc.test", true},
	} {
		if got := ancestorMatches([]string{c.src}, c.ancestor, "https://doc.test"); got != c.want {
			t.Errorf("%s vs %s = %v, want %v", c.src, c.ancestor, got, c.want)
		}
	}
}
