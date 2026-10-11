package engine

import (
	"sync"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/pdf"
)

// applyStyles feeds the document's stylesheets (inline <style> plus linked
// sheets, fetched in parallel through the resource cache) into the CSS
// cascade so visibility follows the page's real rules. Scripts referenced by
// the page are prefetched at the same time, so the loader finds them warm.
func (t *Tab) applyStyles(doc *fnet.Doc) {
	links := doc.StylesheetLinks()
	var scriptSrcs []string
	if !t.eng.opts.DisableJS {
		for _, s := range doc.ScriptElements() {
			if s.Src != "" && isJavaScriptType(s.Type) && !s.NoModule {
				scriptSrcs = append(scriptSrcs, s.Src)
			}
		}
	}
	fetched := make([]string, len(links))
	if len(links)+len(scriptSrcs) > 0 && !t.IsOfflineMode() {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 6) // like a browser's per-host connection limit
		for i, href := range links {
			wg.Add(1)
			go func(i int, href string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if body, status, err := t.fetchResourceFrom(doc, href); err == nil && status < 400 {
					fetched[i] = body
				}
			}(i, href)
		}
		for _, src := range scriptSrcs {
			wg.Add(1)
			go func(src string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				_, _, _ = t.fetchResourceFrom(doc, src)
			}(src)
		}
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
	abs, err := resolveURL(doc.BaseURL(), ref)
	if err != nil {
		return "", 0, err
	}
	return t.fetchResource(abs)
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
