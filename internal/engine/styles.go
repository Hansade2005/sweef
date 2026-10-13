package engine

import (
	"sync"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/pdf"
)

// prefetchConns bounds the sub-resource requests a navigation starts at once. HTTP/2 hosts multiplex them over
// one connection; HTTP/1 hosts get up to this many connections (a browser uses 6 per host).
const prefetchConns = 12

// applyStyles feeds the document's stylesheets (inline <style> plus linked
// sheets, fetched in parallel through the resource cache) into the CSS
// cascade so visibility follows the page's real rules. Scripts referenced by
// the page are prefetched at the same time, so the loader finds them warm.
func (t *Tab) applyStyles(doc *fnet.Doc) {
	links := doc.StylesheetLinks()
	var scriptSrcs []string
	if !t.eng.opts.DisableJS {
		// Data Saver: with a script budget the scripts are read in document order by
		// the loader (a parallel prefetch would spend the budget in a random order),
		// and third-party scripts the policy blocks are not asked for at all.
		pol := t.policy()
		for _, s := range doc.ScriptElements() {
			if s.Src != "" && isJavaScriptType(s.Type) && !s.NoModule {
				if pol.ScriptMaxBytes > 0 {
					continue
				}
				if abs, err := resolveURL(doc.BaseURL(), s.Src); pol.BlockThirdPartyScripts && err == nil && thirdParty(doc.BaseURL(), abs) {
					continue
				}
				scriptSrcs = append(scriptSrcs, s.Src)
			}
		}
	}
	fetched := make([]string, len(links))
	if len(links)+len(scriptSrcs) > 0 && !t.IsOfflineMode() {
		var wg sync.WaitGroup
		sem := make(chan struct{}, prefetchConns) // stylesheets first, then the scripts
		for i, href := range links {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, href string) {
				defer wg.Done()
				defer func() { <-sem }()
				tf := time.Now()
				if body, status, err := t.fetchResourceFromKind(doc, href, KindStyle); err == nil && status < 400 {
					fetched[i] = body
				}
				t.tspan("css.fetch", href, tf)
			}(i, href)
		}
		// Scripts are fetched ahead of the loader but do not hold the first frame: the loader joins the request
		// in flight (or finds the bytes) when it gets to the script.
		go func() {
			for _, src := range scriptSrcs {
				sem <- struct{}{}
				go func(src string) {
					defer func() { <-sem }()
					_, _, _ = t.fetchResourceFromKind(doc, src, KindScript)
				}(src)
			}
		}()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	sheets := doc.InlineStyleSheets()
	for _, f := range fetched {
		if f != "" {
			sheets = append(sheets, f)
		}
	}
	if len(sheets) == 0 {
		return
	}
	w, _, _, _, _ := t.Viewport()
	doc.SetStylesheets(sheets, w, t.ColorSchemeName())
}

// fetchResourceFrom resolves a sub-resource against the document being
// installed (the tab URL may still point at the previous page).
func (t *Tab) fetchResourceFrom(doc *fnet.Doc, ref string) (string, int, error) {
	return t.fetchResourceFromKind(doc, ref, KindOther)
}

func (t *Tab) fetchResourceFromKind(doc *fnet.Doc, ref string, kind DataKind) (string, int, error) {
	abs, err := resolveURL(doc.BaseURL(), ref)
	if err != nil {
		return "", 0, err
	}
	return t.fetchRes(abs, fetchOpts{kind: kind, page: doc.BaseURL(), prefetch: true})
}

// PDF renders the current page as a PDF document (Playwright page.pdf).
func (t *Tab) PDF(o pdf.Options) ([]byte, error) {
	t.settle(0)
	t.mu.RLock()
	d := t.dom
	title, u := t.title, t.URL()
	t.mu.RUnlock()
	if d == nil {
		return nil, ErrNotFound
	}
	if o.Title == "" {
		o.Title = d.Title()
		if o.Title == "" {
			o.Title = title
		}
	}
	if o.URL == "" {
		o.URL = u
	}
	return pdf.Render(d.PDFLines(), o)
}
