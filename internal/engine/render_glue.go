package engine

import (
	"bytes"
	"image"
	_ "image/gif"  // image decoders for <img> and CSS images
	_ "image/jpeg" //
	_ "image/png"  //
	"os"
	"strings"
	"sync"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render"
	_ "golang.org/x/image/bmp"  //
	_ "golang.org/x/image/webp" //
)

// render_glue.go connects the render pipeline (internal/render) to tabs:
// it installs the pipeline as the layout engine, gives every document an
// image loader that fetches through the tab, and waits for pending images
// before a screenshot the way a browser's load event waits for them.

// useRenderPipeline selects the pixel pipeline for layout and screenshots.
// SWEEF_RENDERER=schematic restores the old flow layout and wireframe PNG.
var useRenderPipeline = os.Getenv("SWEEF_RENDERER") == "pixel" // default flips once layout lands

func init() {
	if useRenderPipeline {
		render.Install()
	}
}

// imageDecoders lets other packages (SVG) decode formats image.Decode does
// not know; the first decoder that accepts the bytes wins.
var imageDecoders []func(data []byte, contentType, url string) (image.Image, bool)

// RegisterImageDecoder adds a decoder for page images (call from init).
func RegisterImageDecoder(f func(data []byte, contentType, url string) (image.Image, bool)) {
	imageDecoders = append(imageDecoders, f)
}

type imgEntry struct {
	img  image.Image
	done chan struct{}
}

// docImages loads and caches the images one document references.
type docImages struct {
	t  *Tab
	d  *fnet.Doc
	mu sync.Mutex
	m  map[string]*imgEntry
}

func (l *docImages) ResolveURL(ref string) string {
	if u, err := resolveURL(l.d.BaseURL(), strings.TrimSpace(ref)); err == nil {
		return u
	}
	return ref
}

// Image returns the decoded image when it is ready; otherwise it starts the
// fetch (once) and reports not ready. Completion invalidates layout so the
// next frame picks the image up.
func (l *docImages) Image(u string) (image.Image, bool) {
	if u == "" {
		return nil, false
	}
	l.mu.Lock()
	e := l.m[u]
	if e == nil {
		e = &imgEntry{done: make(chan struct{})}
		l.m[u] = e
		l.mu.Unlock()
		go l.load(u, e)
		return nil, false
	}
	l.mu.Unlock()
	select {
	case <-e.done:
		return e.img, e.img != nil
	default:
		return nil, false
	}
}

func (l *docImages) load(u string, e *imgEntry) {
	defer func() {
		close(e.done)
		l.d.InvalidateLayout()
		l.t.renderGen.Add(1)
	}()
	var data []byte
	var ct string
	if strings.HasPrefix(u, "data:") {
		mt, b, err := decodeDataURL(u)
		if err != nil {
			return
		}
		data, ct = b, mt
	} else {
		r, err := l.t.httpForScript("GET", u, map[string]string{"Accept": "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"}, "")
		if err != nil {
			return
		}
		if st, _ := r["status"].(int); st >= 400 {
			return
		}
		body, _ := r["body"].(string)
		data = []byte(body)
		if h, ok := r["headers"].(map[string]any); ok {
			ct, _ = h["content-type"].(string)
		}
	}
	for _, dec := range imageDecoders {
		if img, ok := dec(data, ct, u); ok {
			e.img = img
			return
		}
	}
	if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		e.img = img
	}
}

// wait blocks until every started image load finished or the budget ran
// out. Returns whether anything was pending.
func (l *docImages) wait(budget time.Duration) bool {
	deadline := time.After(budget)
	pending := false
	for {
		l.mu.Lock()
		var next *imgEntry
		for _, e := range l.m {
			select {
			case <-e.done:
			default:
				next = e
			}
			if next != nil {
				break
			}
		}
		l.mu.Unlock()
		if next == nil {
			return pending
		}
		pending = true
		select {
		case <-next.done:
		case <-deadline:
			return pending
		}
	}
}

var (
	loadersMu sync.Mutex
	loadersBy = map[*fnet.Doc]*docImages{}
)

// attachRenderer gives a newly installed document its image loader and
// drops the previous document's.
func (t *Tab) attachRenderer(old, doc *fnet.Doc) {
	t.scrollX, t.scrollY = 0, 0 // caller holds t.mu
	if !useRenderPipeline {
		return
	}
	l := &docImages{t: t, d: doc, m: map[string]*imgEntry{}}
	loadersMu.Lock()
	if old != nil && old != doc {
		delete(loadersBy, old)
		render.SetLoader(old, nil)
	}
	loadersBy[doc] = l
	loadersMu.Unlock()
	render.SetLoader(doc, l)
}

func (t *Tab) detachRenderer(doc *fnet.Doc) {
	if doc == nil {
		return
	}
	loadersMu.Lock()
	delete(loadersBy, doc)
	loadersMu.Unlock()
	render.SetLoader(doc, nil)
}

// renderPixels produces a screenshot through the render pipeline: one
// layout pass to discover images, a bounded wait for them, then the frame.
func (t *Tab) renderPixels(width, height int, dsf float64, full bool) *image.RGBA {
	t.mu.RLock()
	dom := t.dom
	t.mu.RUnlock()
	if dom == nil {
		return image.NewRGBA(image.Rect(0, 0, width, height))
	}
	loadersMu.Lock()
	l := loadersBy[dom]
	loadersMu.Unlock()
	t.mu.RLock()
	sx, sy := t.scrollX, t.scrollY
	t.mu.RUnlock()
	o := render.Options{Width: width, Height: height, DPR: dsf, FullPage: full, ScrollX: sx, ScrollY: sy}
	if l != nil {
		// Layout and paint request every image they meet; wait for those,
		// then repeat in case newly sized images revealed more.
		for i := 0; i < 3; i++ {
			render.Prepare(dom, o)
			if !l.wait(3 * time.Second) {
				break
			}
		}
	}
	return render.Render(dom, o)
}
