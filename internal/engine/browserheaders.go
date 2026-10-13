package engine

// browserheaders.go: the ordinary request headers a browser sends. A server that wants to tell a browser from
// a bare script looks at these (Accept, Accept-Language, Sec-Fetch-*, Referer); sending them is not evasion, it
// is what an HTTP client that renders pages is expected to do. Everything here is truthful: Accept lists only
// the formats the engine decodes, the User-Agent is the one the embedder configured, and no Chromium-only
// headers (Client Hints) are imitated. Page code and callers override any of it with explicit headers.

import (
	"net/http"
	"net/url"
	"strings"
)

// defaultAcceptLanguage matches navigator.language / navigator.languages in the JS prelude.
const defaultAcceptLanguage = "en-US,en;q=0.9"

const (
	acceptDocument = "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8"
	acceptStyle    = "text/css,*/*;q=0.1"
	// formats the engine decodes: PNG, JPEG, GIF, WebP, BMP, TIFF and SVG (no AVIF)
	acceptImage = "image/webp,image/png,image/jpeg,image/gif,image/svg+xml,image/*;q=0.8,*/*;q=0.5"
	acceptAny   = "*/*"
)

// SetAcceptLanguage sets the Accept-Language value of every request the engine makes unless a context
// overrides it (ContextOptions.AcceptLanguage). Embedders call it from their language setting.
func (e *Engine) SetAcceptLanguage(v string) { e.acceptLang.Store(strings.TrimSpace(v)) }

// AcceptLanguage is the value in force for the context.
func (c *BrowserContext) AcceptLanguage() string {
	if c != nil && c.options.AcceptLanguage != "" {
		return c.options.AcceptLanguage
	}
	if c != nil {
		if v, _ := c.eng.acceptLang.Load().(string); v != "" {
			return v
		}
		if c.eng.opts.AcceptLanguage != "" {
			return c.eng.opts.AcceptLanguage
		}
	}
	return defaultAcceptLanguage
}

func acceptFor(kind DataKind) string {
	switch kind {
	case KindDocument, KindFrame:
		return acceptDocument
	case KindStyle:
		return acceptStyle
	case KindImage:
		return acceptImage
	}
	return acceptAny
}

func secFetchDest(kind DataKind) string {
	switch kind {
	case KindDocument:
		return "document"
	case KindFrame:
		return "iframe"
	case KindScript:
		return "script"
	case KindStyle:
		return "style"
	case KindImage:
		return "image"
	case KindFont:
		return "font"
	case KindMedia:
		return "video"
	}
	return "empty"
}

// secFetchSite relates the request to the page that makes it: same-origin, same-site or cross-site.
func secFetchSite(page, target *url.URL) string {
	if page == nil || target == nil {
		return "none"
	}
	if page.Scheme == target.Scheme && strings.EqualFold(page.Host, target.Host) {
		return "same-origin"
	}
	if siteOf(page.Hostname()) == siteOf(target.Hostname()) && page.Scheme == target.Scheme {
		return "same-site"
	}
	return "cross-site"
}

// trustworthy: Sec-Fetch-* are only sent to secure contexts and loopback.
func trustworthy(u *url.URL) bool {
	return u.Scheme == "https" || loopbackHost(u.Hostname())
}

// refererFor is the Referer under the default policy, strict-origin-when-cross-origin: the full URL (without
// fragment and credentials) to the same origin, the origin to others, nothing on a downgrade.
func refererFor(page, target *url.URL) string {
	if page == nil || target == nil || (page.Scheme != "http" && page.Scheme != "https") {
		return ""
	}
	if page.Scheme == "https" && target.Scheme != "https" {
		return ""
	}
	if page.Scheme == target.Scheme && strings.EqualFold(page.Host, target.Host) {
		u := *page
		u.Fragment, u.User = "", nil
		return u.String()
	}
	return page.Scheme + "://" + page.Host + "/"
}

// setBrowserHeaders adds the defaults to h (only the ones not already set). nav marks a top-level navigation
// started by the person (address bar, link), page is the document making a sub-resource request.
func (t *Tab) setBrowserHeaders(h http.Header, target *url.URL, kind DataKind, nav bool) {
	set := func(k, v string) {
		if v != "" && h.Get(k) == "" {
			h.Set(k, v)
		}
	}
	set("Accept", acceptFor(kind))
	set("Accept-Language", t.Context().AcceptLanguage())
	var page *url.URL
	if !nav {
		if pu, err := url.Parse(t.URL()); err == nil && pu.Host != "" {
			page = pu
		}
	}
	if target != nil && trustworthy(target) {
		set("Sec-Fetch-Dest", secFetchDest(kind))
		switch {
		case nav:
			set("Sec-Fetch-Mode", "navigate")
			set("Sec-Fetch-Site", "none")
			set("Sec-Fetch-User", "?1")
		default:
			mode := "no-cors"
			switch kind {
			case KindXHR, KindFont, KindOther:
				mode = "cors"
			}
			site := secFetchSite(page, target)
			if site == "same-origin" && mode == "cors" {
				mode = "same-origin"
			}
			set("Sec-Fetch-Mode", mode)
			set("Sec-Fetch-Site", site)
		}
	}
	if nav {
		set("Upgrade-Insecure-Requests", "1")
	} else {
		set("Referer", refererFor(page, target))
	}
}
