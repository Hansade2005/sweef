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
		if body, status, err := e.t.fetchResource(u); err == nil && status < 400 {
			data = []byte(body)
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

func (e *tabEnv) Bitmap(n *html.Node) image.Image {
	if s := e.source(n); s != nil {
		return s.Bitmap()
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
				out = append(out, render.Sheet{Text: sb.String(), BaseURL: base, Media: attrOf(n, "media")})
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
						if body, status, err := e.t.fetchResource(abs); err == nil && status < 400 {
							out = append(out, render.Sheet{Text: body, BaseURL: abs, Media: attrOf(n, "media")})
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
	env := &tabEnv{t: t, doc: doc, imgs: map[string]image.Image{}}
	doc.SetLayoutProvider(func(width int) *fnet.Layout {
		_, vh, dsf, _, _ := t.Viewport()
		if vh <= 0 {
			vh = 720
		}
		if dsf <= 0 {
			dsf = 1
		}
		res := render.Layout(doc.Root, render.Options{
			Width: float64(width), Height: float64(vh), DPR: dsf,
			ColorScheme: t.ColorSchemeName(), BaseURL: doc.BaseURL(),
			Sheets: env.pageSheets(), Env: env,
		})
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
		for _, f := range b.Frags {
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
	return raster.Paint(ext.res.List, width, height, dsf, raster.PaintEnv{
		Bitmap:     ext.env.Bitmap,
		Background: raster.Color{R: 1, G: 1, B: 1, A: 1},
	}), true
}
