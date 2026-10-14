package engine

// coi.go: cross-origin isolation, the way Chromium defines it.
//
// A document is cross-origin isolated (self.crossOriginIsolated === true) when it is a secure context and was
// served with
//
//	Cross-Origin-Opener-Policy: same-origin
//	Cross-Origin-Embedder-Policy: require-corp   (or: credentialless)
//
// Only then do SharedArrayBuffer (sab.go) and its postMessage sharing exist. The embedder policy is enforced:
//
//   - require-corp: a no-CORS cross-origin sub-resource (script, stylesheet, image, media loaded without
//     crossorigin) is blocked unless its response says Cross-Origin-Resource-Policy: cross-origin (same-site for a
//     same-site resource). A response that passes the CORS check (Access-Control-Allow-Origin matching the
//     document) is let through, because the request would have been made in CORS mode.
//   - credentialless: such requests are sent without cookies, and no CORP header is needed.
//   - a nested frame must itself be served with a COEP header (and, for a cross-origin frame under require-corp,
//     with CORP: cross-origin), otherwise it is not displayed.
//   - a Worker started from a COEP document needs the same header on its script (blob: and data: workers inherit).
//
// The engine runs one process per profile with no browsing-context-group switch, so COOP is only read for the
// isolation decision; it does not sever window.opener links.

import (
	"fmt"
	"net/http"
	"strings"
)

type isolation struct {
	coop     string // "same-origin", "same-origin-allow-popups", "unsafe-none" ("" when absent)
	coep     string // "require-corp", "credentialless" or "" (unsafe-none)
	isolated bool
}

// parseTokenHeader returns the token of a structured-header item (parameters such as report-to are dropped).
func parseTokenHeader(v string) string {
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

func parseCOEP(h http.Header) string {
	if h == nil {
		return ""
	}
	switch v := parseTokenHeader(h.Get("Cross-Origin-Embedder-Policy")); v {
	case "require-corp", "credentialless":
		return v
	}
	return ""
}

func parseCOOP(h http.Header) string {
	if h == nil {
		return ""
	}
	return parseTokenHeader(h.Get("Cross-Origin-Opener-Policy"))
}

// iso is the isolation state of the tab's current document (never nil).
func (t *Tab) iso() *isolation {
	if p := t.isoState.Load(); p != nil {
		return p
	}
	return &isolation{}
}

// CrossOriginIsolated reports whether the current document is cross-origin isolated.
func (t *Tab) CrossOriginIsolated() bool { return t.iso().isolated }

// embedderPolicy is the COEP in force for requests this tab makes.
func (t *Tab) embedderPolicy() string { return t.iso().coep }

// setIsolation computes the isolation of the document about to be installed from its response headers.
func (t *Tab) setIsolation(h http.Header, finalURL string) {
	st := &isolation{coop: parseCOOP(h), coep: parseCOEP(h)}
	secure := potentiallyTrustworthy(finalURL)
	switch {
	case h == nil && t.isFrame() && (finalURL == "about:blank" || strings.HasPrefix(finalURL, "about:srcdoc")):
		// an initial about:blank frame inherits what its parent had
		if p := t.bc.parent; p != nil {
			pi := p.iso()
			st.coep, st.isolated = pi.coep, pi.isolated
		}
	case t.isFrame():
		// a frame is isolated when its embedder is, and it has opted in itself
		if p := t.bc.parent; p != nil {
			st.isolated = secure && st.coep != "" && p.iso().isolated
		}
	default:
		st.isolated = secure && st.coop == "same-origin" && st.coep != ""
	}
	t.isoState.Store(st)
}

// inheritIsolation gives the hidden tab of a worker its creator's embedder policy and isolation.
func (t *Tab) inheritIsolation(owner *Tab) {
	if owner == nil {
		return
	}
	o := owner.iso()
	t.isoState.Store(&isolation{coep: o.coep, isolated: o.isolated})
}

// corpOK applies the Cross-Origin-Resource-Policy check of a response to a request from the tab's document.
func corpOK(h http.Header, reqURL, docOrigin, docSite string) bool {
	switch parseTokenHeader(h.Get("Cross-Origin-Resource-Policy")) {
	case "cross-origin":
		return true
	case "same-site":
		return schemefulSite(reqURL) == docSite
	case "same-origin":
		return siteOriginOf(reqURL) == docOrigin
	}
	return false
}

// noCORSKind reports whether requests of this kind are made in no-CORS mode unless the page says otherwise.
func noCORSKind(k DataKind) bool {
	switch k {
	case KindScript, KindStyle, KindImage, KindMedia:
		return true
	}
	return false
}

// coepCrossOrigin reports whether a request URL is cross-origin to the document (data:, blob: and about: are not).
func (t *Tab) coepCrossOrigin(reqURL string) bool {
	so := siteOriginOf(reqURL)
	if so == "" {
		return false
	}
	return so != t.docOrigin()
}

// coepCredentialless is true when a request must be sent without credentials (COEP: credentialless).
func (t *Tab) coepCredentialless(kind DataKind, reqURL string) bool {
	return t.embedderPolicy() == "credentialless" && noCORSKind(kind) && t.coepCrossOrigin(reqURL)
}

// coepBlocks applies Cross-Origin-Embedder-Policy: require-corp to a response and returns an error when the
// resource may not be used by the document.
func (t *Tab) coepBlocks(kind DataKind, reqURL string, h http.Header) error {
	if t.embedderPolicy() != "require-corp" || !noCORSKind(kind) || !t.coepCrossOrigin(reqURL) {
		return nil
	}
	origin := t.docOrigin()
	if acao := strings.TrimSpace(h.Get("Access-Control-Allow-Origin")); acao == "*" || (origin != "" && acao == origin) {
		return nil
	}
	if corpOK(h, reqURL, origin, schemefulSite(t.URL())) {
		return nil
	}
	msg := fmt.Sprintf("Failed to load '%s' (%s): blocked by Cross-Origin-Embedder-Policy: require-corp; the response needs 'Cross-Origin-Resource-Policy: cross-origin'.", reqURL, kind)
	t.LogConsole("error", msg)
	return fmt.Errorf("TypeError: Failed to fetch (net::ERR_BLOCKED_BY_RESPONSE.NotSameOriginAfterDefaultedToSameOriginByCoep)")
}

// frameCOEPBlocked is the embedder-policy check for a nested document: a frame inside a COEP document must carry a
// COEP header too, and under require-corp a cross-origin frame also needs CORP: cross-origin.
func (t *Tab) frameCOEPBlocked(h http.Header, docURL string) (bool, string) {
	p := t.bc.parent
	if p == nil {
		return false, ""
	}
	pe := p.embedderPolicy()
	if pe == "" {
		return false, ""
	}
	if parseCOEP(h) == "" {
		return true, "Cross-Origin-Embedder-Policy (the embedding document has " + pe + ")"
	}
	if pe == "require-corp" && siteOriginOf(docURL) != p.docOrigin() && !corpOK(h, docURL, p.docOrigin(), schemefulSite(p.URL())) {
		return true, "Cross-Origin-Resource-Policy (the embedding document has require-corp)"
	}
	return false, ""
}

func init() {
	registerHostExtension("coi", func(j *jsVM) map[string]hostFn {
		return map[string]hostFn{
			"coi": func(a []any) (any, error) { return j.tab != nil && j.tab.CrossOriginIsolated(), nil },
		}
	})
}
