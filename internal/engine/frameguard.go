package engine

// frameguard.go: a document may refuse to be shown in a frame (X-Frame-Options and the
// frame-ancestors directive of Content-Security-Policy). Frames render and take input
// now, so honouring this is what stops a page from putting another site (with the
// person's cookies) under an invisible frame and collecting clicks, and it is what
// sign-in and payment pages ask of the browser.

import (
	"net/url"
	"strings"
)

// frameRefusalDoc is what a frame shows instead of a document that refuses to be framed.
const frameRefusalDoc = `<!doctype html><html><head><meta charset="utf-8"><title>Blocked</title></head>` +
	`<body style="margin:0;padding:12px;font:13px sans-serif;color:#555;background:#f3f3f3">` +
	`<p>This content cannot be shown in a frame.</p></body></html>`

// frameRefusal reports why a document with these headers may not be displayed in a frame
// of t's ancestors ("" = it may). docURL is the document's final URL.
func (t *Tab) frameRefusal(xfo, csp, docURL string) string {
	if t.win.parent == nil {
		return ""
	}
	var ancestors []string
	for p := t.win.parent; p != nil; p = p.win.parent {
		ancestors = append(ancestors, p.origin())
	}
	docOrigin := originOfURL(docURL)
	// frame-ancestors of CSP takes precedence over X-Frame-Options
	if srcs, ok := frameAncestorsOf(csp); ok {
		for _, a := range ancestors {
			if !ancestorMatches(srcs, a, docOrigin) {
				return "it set 'Content-Security-Policy: frame-ancestors " + strings.Join(srcs, " ") + "'"
			}
		}
		return ""
	}
	xfo = strings.ToLower(strings.TrimSpace(xfo))
	if i := strings.IndexByte(xfo, ','); i >= 0 {
		xfo = strings.TrimSpace(xfo[:i])
	}
	switch xfo {
	case "deny":
		return "it set 'X-Frame-Options: deny'"
	case "sameorigin":
		for _, a := range ancestors {
			if !sameOriginStr(a, docOrigin) {
				return "it set 'X-Frame-Options: sameorigin' and an ancestor is on another origin"
			}
		}
	}
	return ""
}

func originOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "null"
	}
	return u.Scheme + "://" + u.Host
}

// frameAncestorsOf extracts the source list of the frame-ancestors directive from one or
// more (comma separated) Content-Security-Policy header values. When several policies
// carry the directive the most restrictive reading is used: the intersection is
// approximated by the first one that has it.
func frameAncestorsOf(csp string) ([]string, bool) {
	for _, pol := range strings.Split(csp, ",") {
		for _, dir := range strings.Split(pol, ";") {
			f := strings.Fields(strings.TrimSpace(dir))
			if len(f) > 0 && strings.EqualFold(f[0], "frame-ancestors") {
				return f[1:], true
			}
		}
	}
	return nil, false
}

// ancestorMatches reports whether the ancestor origin matches one of the source expressions.
func ancestorMatches(srcs []string, ancestor, self string) bool {
	au, err := url.Parse(ancestor)
	if err != nil || au.Host == "" {
		return false // an opaque ancestor matches nothing but...
	}
	for _, s := range srcs {
		ls := strings.ToLower(s)
		switch {
		case ls == "'none'":
			continue
		case ls == "'self'":
			if sameOriginStr(ancestor, self) {
				return true
			}
		case ls == "*":
			if au.Scheme == "http" || au.Scheme == "https" {
				return true
			}
		case strings.HasSuffix(ls, ":") && !strings.Contains(ls, "/"):
			if au.Scheme+":" == ls {
				return true
			}
		default:
			if hostSourceMatches(ls, au) {
				return true
			}
		}
	}
	return false
}

func hostSourceMatches(src string, au *url.URL) bool {
	scheme := ""
	if i := strings.Index(src, "://"); i >= 0 {
		scheme, src = src[:i], src[i+3:]
	}
	if i := strings.IndexByte(src, '/'); i >= 0 {
		src = src[:i]
	}
	host, port := src, ""
	if i := strings.LastIndexByte(src, ':'); i >= 0 {
		host, port = src[:i], src[i+1:]
	}
	if scheme != "" && scheme != au.Scheme {
		return false
	}
	if scheme == "" && au.Scheme != "http" && au.Scheme != "https" {
		return false
	}
	ah := strings.ToLower(au.Hostname())
	switch {
	case host == "*":
	case strings.HasPrefix(host, "*."):
		if !strings.HasSuffix(ah, host[1:]) {
			return false
		}
	default:
		if ah != host {
			return false
		}
	}
	if port != "*" {
		ap := au.Port()
		if ap == "" {
			ap = "80"
			if au.Scheme == "https" {
				ap = "443"
			}
		}
		want := port
		if want == "" { // no port in the source: the scheme's default port only
			want = "80"
			if scheme == "https" || (scheme == "" && au.Scheme == "https") {
				want = "443"
			}
		}
		if ap != want {
			return false
		}
	}
	return true
}
