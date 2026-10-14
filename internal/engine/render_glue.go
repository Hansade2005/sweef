package engine

// render_glue.go — connects internal/render (CSS layout), internal/raster
// (pixels) and the live bitmap sources (canvas, WebGL, video) to a Tab.
//
// Doc.Layout is backed by render.Layout, so element boxes, hit-testing,
// getBoundingClientRect, offset*/client*/scroll* and screenshots all come from
// the same pass. SWEEF_RENDERER=flow selects the old schematic layout.

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/raster"
	"github.com/sweefbrowser/sweef/internal/render"
)

func renderEnabled() bool { return os.Getenv("SWEEF_RENDERER") != "flow" }

// tabEnv is the render.Env of one document.
type tabEnv struct {
	t    *Tab
	doc  *fnet.Doc
	mu   sync.Mutex
	imgs map[string]image.Image
	svgs map[string]string
	lz   lazyState // Data Saver: lazy, skipped and forced images (lazy.go)
	// async images (Tab.SetAsyncImages): fetches started by layout and still running
	inflight map[string]bool
	// sty carries computed styles from one layout pass to the next (a document's
	// layout provider runs one pass at a time, under Doc's layout lock)
	sty *render.Session
	// fonts is the document's @font-face state (webfonts.go)
	fonts *webFonts
}

func (e *tabEnv) source(n *html.Node) BitmapSource {
	id := e.doc.IDOf(n)
	if id == 0 {
		return nil
	}
	bitmapMu.RLock()
	defer bitmapMu.RUnlock()
	return bitmapSources[bitmapKey{e.t.ID, e.doc, id}]
}

func (e *tabEnv) Image(u string) image.Image {
	e.mu.Lock()
	im, ok := e.imgs[u]
	e.mu.Unlock()
	if ok {
		return im
	}
	var data []byte
	if strings.HasPrefix(u, "data:") {
		if _, b, err := decodeDataURL(u); err == nil {
			data = b
		}
	} else if !e.t.IsOfflineMode() || strings.HasPrefix(u, "file:") {
		if e.asyncOK(u) {
			e.startAsync(u)
			return nil // not final: the layout runs again when the bytes are in
		}
		var memo bool
		ti := time.Now()
		data, memo = e.loadImageBytes(u)
		e.t.tspan("img.fetch", u, ti)
		if !memo {
			return nil // deferred or withheld by the Data Saver policy: not final, do not remember
		}
	}
	if len(data) > 0 {
		im, _, _ = image.Decode(bytes.NewReader(data))
	}
	e.mu.Lock()
	e.imgs[u] = im
	e.mu.Unlock()
	return im
}

// prefetchDocImages fetches the document's <img> images in parallel before a layout that would otherwise
// ask for them one at a time (the headless commands wait for every image). Windows load images in the
// background instead, and a Data Saver policy decides image by image, so both skip it.
func (e *tabEnv) prefetchDocImages() {
	if e.t.asyncImg.Load() || e.t.policy() != (DataPolicy{}) || e.t.IsOfflineMode() {
		return
	}
	base := e.doc.BaseURL()
	var urls []string
	seen := map[string]bool{}
	walkNodes(e.doc.Root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "img" {
			return
		}
		u := imgURL(base, n)
		if u == "" || seen[u] || strings.HasPrefix(u, "data:") {
			return
		}
		seen[u] = true
		e.mu.Lock()
		_, have := e.imgs[u]
		e.mu.Unlock()
		if !have {
			urls = append(urls, u)
		}
	})
	if len(urls) > 1 {
		e.prefetch(urls)
	}
}

// asyncOK: the window shows the page while it loads (Tab.SetAsyncImages) and no
// Data Saver policy is in force (its gates decide synchronously, see lazy.go).
func (e *tabEnv) asyncOK(u string) bool {
	return e.t.asyncImg.Load() && e.t.policy() == (DataPolicy{}) && !strings.HasPrefix(u, "file:")
}

// startAsync fetches and decodes an image in the background (a few at a time).
// The result lands in the memo and the tab asks its window for a new layout.
func (e *tabEnv) startAsync(u string) {
	e.mu.Lock()
	if e.inflight == nil {
		e.inflight = map[string]bool{}
	}
	if e.inflight[u] {
		e.mu.Unlock()
		return
	}
	e.inflight[u] = true
	e.mu.Unlock()
	t := e.t
	go func() {
		t.imgSem <- struct{}{}
		defer func() { <-t.imgSem }()
		ti := time.Now()
		data, memo := e.loadImageBytes(u)
		t.tspan("img.fetch", u, ti)
		var im image.Image
		var markup string
		if memo && len(data) > 0 {
			im, _, _ = image.Decode(bytes.NewReader(data))
			if im == nil && strings.Contains(strings.ToLower(string(data)), "<svg") {
				markup = string(data)
			}
		}
		e.mu.Lock()
		delete(e.inflight, u)
		if memo {
			e.imgs[u] = im
			if markup != "" {
				e.svgs[u] = markup
			}
		}
		e.mu.Unlock()
		t.imageArrived(e)
	}()
}

func (e *tabEnv) Bitmap(n *html.Node) image.Image {
	if s := e.source(n); s != nil {
		return s.Bitmap()
	}
	if n != nil && n.Data == "iframe" {
		if k := e.t.frameKid(e.doc.IDOf(n)); k != nil {
			if im := k.framePixels(); im != nil {
				return im
			}
		}
	}
	return nil
}

func (e *tabEnv) IntrinsicSize(n *html.Node) (float64, float64, bool) {
	if s := e.source(n); s != nil {
		if w, h := s.Size(); w > 0 && h > 0 {
			return float64(w), float64(h), true
		}
	}
	return 0, 0, false
}

// SVGSource lets layout paint <img src=x.svg> and CSS background SVGs.
func (e *tabEnv) SVGSource(u string) (string, bool) {
	base := strings.ToLower(u)
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if !strings.HasSuffix(base, ".svg") && !strings.HasPrefix(base, "data:image/svg") {
		return "", false
	}
	e.mu.Lock()
	if v, ok := e.svgs[u]; ok {
		e.mu.Unlock()
		return v, v != ""
	}
	_, decoded := e.imgs[u]
	e.mu.Unlock()
	if decoded && !strings.HasPrefix(u, "data:") && e.asyncOK(u) {
		return "", false // the asynchronous fetch finished and the bytes were not an SVG
	}
	var markup string
	if strings.HasPrefix(u, "data:") {
		if _, b, err := decodeDataURL(u); err == nil {
			markup = string(b)
		}
	} else if e.asyncOK(u) {
		e.startAsync(u)
		return "", false
	} else if data, memo := e.loadImageBytes(u); !memo {
		return "", false // deferred or withheld by the Data Saver policy
	} else {
		markup = string(data)
	}
	if !strings.Contains(strings.ToLower(markup), "<svg") {
		markup = ""
	}
	e.mu.Lock()
	e.svgs[u] = markup
	e.mu.Unlock()
	return markup, markup != ""
}

func (e *tabEnv) Focused() *html.Node { return nil }

// pageSheets lists the document's author stylesheets in document order,
// reading <style> text live (so script-inserted styles count) and linked
// sheets through the resource cache.
func (e *tabEnv) pageSheets() []render.Sheet {
	var out []render.Sheet
	base := e.doc.BaseURL()
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "style":
				var sb strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						sb.WriteString(c.Data)
					}
				}
				out = append(out, e.withImports(render.Sheet{Text: sb.String(), BaseURL: base, Media: attrOf(n, "media")}, 0, map[string]bool{})...)
			case "link":
				rel := strings.ToLower(attrOf(n, "rel"))
				isSheet, alt := false, false
				for _, tok := range strings.Fields(rel) {
					if tok == "stylesheet" {
						isSheet = true
					}
					if tok == "alternate" {
						alt = true
					}
				}
				href := strings.TrimSpace(attrOf(n, "href"))
				if isSheet && !alt && href != "" && attrOf(n, "disabled") == "" {
					if abs, err := resolveURL(base, href); err == nil {
						if body, status, err := e.t.fetchResourceKind(abs, KindStyle); err == nil && status < 400 {
							out = append(out, e.withImports(render.Sheet{Text: body, BaseURL: abs, Media: attrOf(n, "media")}, 0, map[string]bool{abs: true})...)
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(e.doc.Root)
	return out
}

// sheet reads a linked stylesheet. Under the saver levels a sheet is read once
// per document (a relayout does not ask the server again); otherwise every
// layout goes through the cache as before.
func (e *tabEnv) sheet(abs string) (string, bool) {
	keep := e.t.policy().ExtendCache
	if keep {
		e.lz.mu.Lock()
		s, ok := e.lz.sheets[abs]
		e.lz.mu.Unlock()
		if ok {
			return s, s != ""
		}
	}
	body, status, err := e.t.fetchResourceKind(abs, KindStyle)
	if err != nil || status >= 400 {
		body = ""
	}
	if keep {
		e.lz.mu.Lock()
		if e.lz.sheets == nil {
			e.lz.sheets = map[string]string{}
		}
		e.lz.sheets[abs] = body
		e.lz.mu.Unlock()
	}
	return body, body != ""
}

func attrOf(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

// installRender makes doc's layout come from the render engine.
func (t *Tab) installRender(doc *fnet.Doc) {
	if !renderEnabled() {
		return
	}
	env := &tabEnv{t: t, doc: doc, imgs: map[string]image.Image{}, svgs: map[string]string{}}
	if !noStyleCache {
		env.sty = render.NewSession()
	}
	env.fonts = newWebFonts(t, doc)
	if old := t.ds.env.Swap(env); old != nil {
		old.fonts.close()
	}
	doc.SetLayoutProvider(func(width int) *fnet.Layout {
		_, vh, dsf, _, _ := t.Viewport()
		if vh <= 0 {
			vh = 720
		}
		if dsf <= 0 {
			dsf = 1
		}
		var res *render.Result
		tl0 := time.Now()
		defer func() {
			t.layoutN.Add(1)
			t.layoutNs.Add(int64(time.Since(tl0)))
			t.tspan("layout", "", tl0)
		}()
		env.prefetchDocImages()
		fontRounds := 0
		for round := 0; ; round++ {
			env.lz.resetHints()
			sheets := env.pageSheets()
			opts := render.Options{
				Width: float64(width), Height: float64(vh), DPR: dsf,
				ColorScheme: t.ColorSchemeName(), BaseURL: doc.BaseURL(),
				Sheets: sheets, Env: env, Session: env.sty,
				Fonts: env.fonts.sync(sheets, float64(width), float64(vh), dsf, t.ColorSchemeName()),
			}
			fgen := opts.Fonts.Gen()
			res = render.Layout(doc.Root, opts)
			if layoutCheck && env.sty != nil {
				// SWEEF_LAYOUT_CHECK: the cached pass must equal a from-scratch one
				opts.Session = nil
				full := render.Layout(doc.Root, opts)
				if opts.Fonts.Gen() == fgen { // a font finishing in between changes the text
					if d := render.DiffResults(res, full); d != "" {
						panic("SWEEF_LAYOUT_CHECK: incremental layout differs from a full layout: " + d)
					}
				}
			}
			// Without a window the fonts the layout asked for are waited for, so the
			// result does not depend on how fast the network is (webfonts.go)
			if fontRounds < maxFontRounds && !t.asyncImg.Load() && env.fonts.settle(opts.Fonts, fgen) {
				fontRounds++
				round--
				continue
			}
			// Data Saver: admit images that came into range, put placeholders
			// on withheld ones, then lay out again (no-op without a policy)
			if round >= maxLazyRounds || !env.settleLazy(float64(vh)) {
				break
			}
		}
		env.updateFrames(res, float64(vh))
		return layoutFromRender(res, env)
	})
}

type renderExt struct {
	res *render.Result
	env *tabEnv
}

func layoutFromRender(res *render.Result, env *tabEnv) *fnet.Layout {
	l := &fnet.Layout{
		Width: int(math.Ceil(res.Width)), Height: int(math.Ceil(res.Height)),
		Boxes: make(map[*html.Node][4]int, len(res.Boxes)),
		Ext:   &renderExt{res, env},
	}
	for n, b := range res.Boxes {
		if b == nil || b.Display == "none" {
			continue
		}
		r := b.Border
		if rc, ok := res.ClientRect(n); ok {
			r = rc
		}
		l.Boxes[n] = [4]int{int(math.Floor(r.X + 0.5)), int(math.Floor(r.Y + 0.5)), int(math.Floor(r.W + 0.5)), int(math.Floor(r.H + 0.5))}
	}
	if res.Hit != nil {
		l.Hit = res.Hit
	}
	l.Geo = func(n *html.Node) (fnet.Geometry, bool) {
		b := res.Boxes[n]
		if b == nil || b.Display == "none" {
			return fnet.Geometry{}, false
		}
		g := fnet.Geometry{ClientW: b.ClientW, ClientH: b.ClientH, ScrollW: b.ScrollW, ScrollH: b.ScrollH}
		g.Border = [4]float64{b.Border.X, b.Border.Y, b.Border.W, b.Border.H}
		if rc, ok := res.ClientRect(n); ok {
			g.Client = [4]float64{rc.X, rc.Y, rc.W, rc.H}
		}
		// projected through transforms (including 3D chains), one rect per fragment
		for _, f := range res.ClientRects(n) {
			g.Frags = append(g.Frags, [4]float64{f.X, f.Y, f.W, f.H})
		}
		return g, true
	}
	return l
}

// paintPNGPixels renders the current page with the real rasterizer. ok is
// false when the render engine is not active for this document.
func (t *Tab) paintPage(width, height int, dsf float64, full bool) (*image.RGBA, bool) {
	t.mu.RLock()
	doc := t.dom
	t.mu.RUnlock()
	if doc == nil || !renderEnabled() {
		return nil, false
	}
	l := doc.Layout(width)
	ext, _ := l.Ext.(*renderExt)
	if ext == nil {
		return nil, false
	}
	if full {
		height = int(math.Ceil(ext.res.Height))
		if height < 1 {
			height = 1
		}
		if height > 16000 {
			height = 16000
		}
	}
	if height <= 0 {
		height = 720
	}
	list := ext.res.List
	if full {
		// a full-page capture is not scrolled, so it carries no page scrollbar
		kept := make([]render.Op, 0, len(list))
		for _, op := range list {
			switch o := op.(type) {
			case render.Scrollbar:
				if o.Vertical && o.Rect.Y == 0 && o.Rect.X+o.Rect.W >= float64(width)-0.5 {
					continue
				}
				if !o.Vertical && o.Rect.Y+o.Rect.H >= ext.res.Viewport.H-0.5 && o.Rect.X == 0 {
					continue
				}
			case render.ScrollCorner:
				if o.Rect.X+o.Rect.W >= float64(width)-0.5 && o.Rect.Y+o.Rect.H >= ext.res.Viewport.H-0.5 {
					continue
				}
			}
			kept = append(kept, op)
		}
		list = kept
	}
	t.setPaintDSF(dsf)
	return raster.Paint(list, width, height, dsf, raster.PaintEnv{
		Bitmap:     ext.env.Bitmap,
		Background: raster.Color{R: 1, G: 1, B: 1, A: 1},
	}), true
}

// SetLayoutCheck switches the SWEEF_LAYOUT_CHECK comparison on or off (tests).
func SetLayoutCheck(on bool) { layoutCheck = on }

// SWEEF_LAYOUT_CHECK=1 lays every document out a second time from scratch after
// each (cached) layout and panics on the first difference in any box or
// display-list op. SWEEF_NO_STYLE_CACHE=1 turns the cross-pass style cache off.
var (
	layoutCheck  = os.Getenv("SWEEF_LAYOUT_CHECK") != ""
	noStyleCache = os.Getenv("SWEEF_NO_STYLE_CACHE") != ""
)
