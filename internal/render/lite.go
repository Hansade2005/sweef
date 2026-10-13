package render

// lite.go: Data Saver support in layout and paint.
//
//   - Placeholders. The host marks an <img> (or a third-party <iframe>) it did
//     not load with data-sweef-lite="skipped" ("frame"). Layout gives it the
//     declared size (or 300x150) and paint draws a labelled box, so the page
//     keeps its shape and the person sees what was withheld and can tap it.
//   - ImageHinter. When an image is not available (the host deferred it), paint
//     tells an Env that implements ImageHinter where the image would have been
//     drawn, which is how the host decides what is near the viewport.

import (
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// ImageHinter is an optional Env extension. ImageHint reports, for an image
// that Env.Image could not supply (returned nil), the document-space rectangle
// where it would be drawn (a zero height means its size is not known yet).
// It is called during Layout, so implementations must be cheap and must not
// call back into layout.
type ImageHinter interface {
	ImageHint(url string, r Rect)
}

func (p *painter) hintImage(u string, r Rect) {
	if h, ok := p.c.env.(ImageHinter); ok && u != "" && !strings.HasPrefix(u, "data:") {
		h.ImageHint(u, r)
	}
}

// setupLite sizes a placeholder from the width/height attributes (CSS sizes
// still apply on top, as for any replaced element).
func setupLite(r *replacedInfo, n *html.Node, kind string) {
	r.lite = kind
	w, okw := parseDim(attr(n, "width"))
	h, okh := parseDim(attr(n, "height"))
	switch {
	case okw && okh && h > 0:
	case okw && w > 0:
		h = w / 2
	case okh && h > 0:
		w = h * 2
	default:
		w, h = 300, 150
	}
	if w <= 0 || h <= 0 {
		w, h = 300, 150
	}
	r.iw, r.ih, r.hasIW, r.hasIH = w, h, true, true
	r.ratio = w / h
}

func liteXMLEsc(s string) string {
	var sb strings.Builder
	for _, c := range s {
		switch c {
		case '&':
			sb.WriteString("&amp;")
		case '<':
			sb.WriteString("&lt;")
		case '>':
			sb.WriteString("&gt;")
		case '"':
			sb.WriteString("&quot;")
		default:
			if c >= 0x20 || c == '\t' {
				sb.WriteRune(c)
			}
		}
	}
	return sb.String()
}

func liteClip(s string, n int) string {
	rs := []rune(strings.Join(strings.Fields(s), " "))
	if len(rs) <= n {
		return string(rs)
	}
	return string(rs[:n-1]) + "…"
}

// liteLabel returns the two lines of a placeholder.
func liteLabel(b *lbox, r *replacedInfo) (string, string) {
	if r.lite == "frame" {
		host := "another site"
		if u, err := url.Parse(strings.TrimSpace(attr(b.n, "src"))); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		return "Embedded content from " + host, "Tap to load"
	}
	if alt := strings.TrimSpace(attr(b.n, "alt")); alt != "" {
		return alt, "Image not loaded. Tap to load"
	}
	return "Image not loaded", "Tap to load"
}

// paintLite draws the labelled placeholder box into cr.
func (p *painter) paintLite(b *lbox, r *replacedInfo, cr Rect) {
	w, h := cr.W, cr.H
	body := `<rect x=".5" y=".5" width="` + f(w-1) + `" height="` + f(h-1) + `" fill="#f1f3f4" stroke="#9aa0a6" stroke-dasharray="4 3"/>`
	l1, l2 := liteLabel(b, r)
	const fs = 13.0
	maxChars := int(w / 7.2)
	switch {
	case w >= 60 && h >= 2*fs+8:
		cy := h / 2
		body += `<text x="` + f(w/2) + `" y="` + f(cy-2) + `" text-anchor="middle" font-family="sans-serif" font-size="` + strconv.Itoa(int(fs)) +
			`" fill="#3c4043">` + liteXMLEsc(liteClip(l1, maxChars)) + `</text>`
		body += `<text x="` + f(w/2) + `" y="` + f(cy+fs+2) + `" text-anchor="middle" font-family="sans-serif" font-size="12" fill="#1a73e8">` +
			liteXMLEsc(liteClip(l2, maxChars)) + `</text>`
	case w >= 60 && h >= fs+6:
		body += `<text x="` + f(w/2) + `" y="` + f(h/2+4) + `" text-anchor="middle" font-family="sans-serif" font-size="12" fill="#1a73e8">` +
			liteXMLEsc(liteClip(l2, maxChars)) + `</text>`
	}
	p.emitCtl(cr, svgWrap(w, h, body), b)
}
