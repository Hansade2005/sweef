package engine

// lazy.go: images under the Data Saver policy.
//
// Images are fetched during layout (tabEnv.Image). Under a saver policy that
// call is a gate with three outcomes:
//
//   - load: the image is near the viewport (or the person asked for it), and
//     its response is read with a size cap; a Content-Length above the cap
//     aborts the response before any body byte is read;
//   - defer: a lazy image that is far from the viewport. Paint reports where
//     it would be drawn (render.ImageHinter); after every layout pass the
//     engine admits the hinted images within a viewport of the visible area
//     and lays out again. Scrolling (Tab.SetScrollY, the helper's scrollTo,
//     the wheel) repeats the check;
//   - skip: strict mode, or an image over the cap. The <img> node gets
//     data-sweef-lite="skipped" and layout draws a labelled box of its declared
//     size; Tab.LoadSkipped swaps in the real image.

import (
	"errors"
	"image"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/render"
)

const (
	maxLazyRounds   = 12
	unsizedPerRound = 3 // images without a known size admitted per round: they stack at one spot until one loads
)

type imgHint struct {
	url string
	r   render.Rect
}

// lazyState is the per-document image state of a tabEnv.
type lazyState struct {
	mu       sync.Mutex
	admitted map[string]bool
	forced   map[string]bool
	skipped  map[string]bool // urls withheld (strict, or over the cap)
	marked   map[string]bool // urls whose <img> nodes carry the placeholder
	pending  []imgHint
	seen     map[string]bool
	frameOK  map[*html.Node]bool // frames the person tapped
	sheets   map[string]string   // linked stylesheets already read (saver levels)
}

func (l *lazyState) resetHints() {
	l.mu.Lock()
	l.pending = l.pending[:0]
	l.seen = nil
	l.mu.Unlock()
}

// ImageHint implements render.ImageHinter.
func (e *tabEnv) ImageHint(u string, r render.Rect) {
	if !e.t.policy().LazyImages {
		return
	}
	l := &e.lz
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.admitted[u] || l.forced[u] || l.skipped[u] || l.seen[u] {
		return
	}
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	l.seen[u] = true
	l.pending = append(l.pending, imgHint{u, r})
}

// loadImageBytes runs the gate and, if allowed, fetches. memo is false when
// the answer must not be remembered (deferred or skipped: it can change).
func (e *tabEnv) loadImageBytes(u string) (data []byte, memo bool) {
	t := e.t
	pol := t.policy()
	o := fetchOpts{kind: KindImage}
	if pol != (DataPolicy{}) {
		l := &e.lz
		l.mu.Lock()
		forced, adm, skipped := l.forced[u], l.admitted[u], l.skipped[u]
		l.mu.Unlock()
		switch {
		case forced:
			o.forced = true
		case skipped:
			return nil, false
		case pol.NoImages:
			e.skip(u, "strict", t.knownSizeURL(u), -1)
			return nil, false
		case t.suspended():
			return nil, false
		case pol.LazyImages && !adm:
			return nil, false
		default:
			if pol.ImageMaxBytes > 0 {
				o.maxBody = int64(pol.ImageMaxBytes)
			}
		}
	}
	body, status, err := t.fetchRes(u, o)
	var tl errTooLarge
	if errors.As(err, &tl) {
		account := int64(-1) // size unknown: estimate
		if tl.declared >= 0 {
			account = -2 // AbortedBytes already holds it
		}
		e.skip(u, "too-large", account, tl.declared)
		return nil, false
	}
	if err != nil || status >= 400 {
		return nil, true
	}
	return []byte(body), true
}

// skip records an image the policy withheld; markSkipped puts the placeholder in the DOM.
func (e *tabEnv) skip(u, reason string, account, size int64) {
	l := &e.lz
	l.mu.Lock()
	if l.skipped == nil {
		l.skipped = map[string]bool{}
	}
	dup := l.skipped[u]
	l.skipped[u] = true
	l.mu.Unlock()
	if !dup {
		e.t.noteSkipped(KindImage, u, account, size, reason, 0)
	}
}

// imgURL is the URL the render engine loads for an <img>: src, else the first srcset candidate.
func imgURL(base string, n *html.Node) string {
	src := strings.TrimSpace(attrVal(n, "src"))
	if src == "" {
		if f := strings.Fields(strings.Split(attrVal(n, "srcset"), ",")[0]); len(f) > 0 {
			src = f[0]
		}
	}
	if src == "" {
		return ""
	}
	abs, err := resolveURL(base, src)
	if err != nil {
		return ""
	}
	return abs
}

func walkNodes(n *html.Node, f func(*html.Node)) {
	f(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkNodes(c, f)
	}
}

// settleLazy runs after every layout pass of the provider. It returns true when it
// changed something that makes another pass necessary.
func (e *tabEnv) settleLazy(vh float64) bool {
	pol := e.t.policy()
	if pol == (DataPolicy{}) {
		return false
	}
	changed := false
	if e.markSkipped() {
		changed = true
	}
	if pol.ClickToLoad3PFrames && e.markFrames() {
		changed = true
	}
	if pol.LazyImages && !e.t.suspended() && e.admitNear(float64(e.t.ds.scrollY.Load()), vh) {
		changed = true
	}
	return changed
}

// markSkipped writes data-sweef-lite="skipped" on every <img> whose URL was withheld.
func (e *tabEnv) markSkipped() bool {
	l := &e.lz
	l.mu.Lock()
	var todo map[string]bool
	for u := range l.skipped {
		if !l.marked[u] {
			if todo == nil {
				todo = map[string]bool{}
			}
			todo[u] = true
		}
	}
	if todo == nil {
		l.mu.Unlock()
		return false
	}
	if l.marked == nil {
		l.marked = map[string]bool{}
	}
	for u := range todo {
		l.marked[u] = true
	}
	l.mu.Unlock()
	base := e.doc.BaseURL()
	changed := false
	walkNodes(e.doc.Root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "img" || attrVal(n, liteMark) != "" {
			return
		}
		if u := imgURL(base, n); u != "" && todo[u] {
			e.doc.SetAttrQuiet(n, liteMark, "skipped")
			e.t.attachSkippedNode(u, e.doc.IDOf(n))
			changed = true
		}
	})
	return changed
}

// attachSkippedNode records the placeholder's node id on the SkippedItem of url.
func (t *Tab) attachSkippedNode(u string, id int64) {
	t.ds.mu.Lock()
	for i := range t.ds.skipped {
		if t.ds.skipped[i].URL == u && t.ds.skipped[i].NodeID == 0 {
			t.ds.skipped[i].NodeID = id
			break
		}
	}
	t.ds.mu.Unlock()
}

// markFrames turns third-party iframes into click-to-load placeholders. The engine
// renders no frame documents at all (a frame is an empty box in every mode), so
// the placeholder labels the embed and lets the person see where it comes from;
// nothing is fetched or saved by it, and it is not counted in the stats.
func (e *tabEnv) markFrames() bool {
	base := e.doc.BaseURL()
	changed := false
	walkNodes(e.doc.Root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "iframe" || attrVal(n, liteMark) != "" {
			return
		}
		src := strings.TrimSpace(attrVal(n, "src"))
		if src == "" || strings.HasPrefix(src, "about:") || strings.HasPrefix(src, "javascript:") || strings.HasPrefix(src, "data:") {
			return
		}
		abs, err := resolveURL(base, src)
		if err != nil || !(strings.HasPrefix(abs, "http://") || strings.HasPrefix(abs, "https://")) || !thirdParty(base, abs) {
			return
		}
		e.lz.mu.Lock()
		ok := e.lz.frameOK[n]
		e.lz.mu.Unlock()
		if ok {
			return
		}
		e.doc.SetAttrQuiet(n, liteMark, "frame")
		e.t.ds.mu.Lock()
		e.t.ds.skipped = append(e.t.ds.skipped, SkippedItem{NodeID: e.doc.IDOf(n), Kind: KindFrame, URL: abs, Bytes: -1, Reason: "frame"})
		e.t.ds.mu.Unlock()
		changed = true
	})
	return changed
}

// admitNear admits the hinted images within a viewport of the visible area
// [y, y+vh] and fetches them (in parallel). It reports whether any was admitted.
func (e *tabEnv) admitNear(y, vh float64) bool {
	if vh <= 0 {
		vh = 720
	}
	lo, hi := y-vh, y+2*vh
	l := &e.lz
	l.mu.Lock()
	var urls []string
	unsized := 0
	for _, h := range l.pending {
		if l.admitted[h.url] || l.forced[h.url] || l.skipped[h.url] {
			continue
		}
		if h.r.Y+h.r.H < lo || h.r.Y > hi {
			continue
		}
		if h.r.H <= 1 {
			if unsized >= unsizedPerRound {
				continue
			}
			unsized++
		}
		urls = append(urls, h.url)
	}
	if len(urls) > 0 && l.admitted == nil {
		l.admitted = map[string]bool{}
	}
	for _, u := range urls {
		l.admitted[u] = true
	}
	l.mu.Unlock()
	if len(urls) == 0 {
		return false
	}
	e.prefetch(urls)
	return true
}

// prefetch loads several images at once (at most six in flight, like a
// browser's per-host limit) so a page does not pay their round trips one by one.
func (e *tabEnv) prefetch(urls []string) {
	if len(urls) == 1 {
		e.Image(urls[0])
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			e.Image(u)
		}(u)
	}
	wg.Wait()
}

// hasAdmissible tells, from the hints of the last layout, whether a scroll to y
// brings a deferred image into range.
func (e *tabEnv) hasAdmissible(y, vh float64) bool {
	if vh <= 0 {
		vh = 720
	}
	lo, hi := y-vh, y+2*vh
	l := &e.lz
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, h := range l.pending {
		if l.admitted[h.url] || l.forced[h.url] || l.skipped[h.url] {
			continue
		}
		if h.r.Y+h.r.H >= lo && h.r.Y <= hi {
			return true
		}
	}
	return false
}

// ---- scroll -----------------------------------------------------------------------------

// SetScrollY tells the engine where the page is scrolled to (CSS px from the top of
// the document). With a lazy-images policy it fetches the images that came into
// range and lays the page out again; the document's MutationGen then changes, so a
// compositor that watches Generation repaints. Cheap when nothing is due.
func (t *Tab) SetScrollY(y int) {
	if y < 0 {
		y = 0
	}
	t.ds.scrollY.Store(int64(y))
	t.lazyReeval()
}

// ScrollY is the last value given to SetScrollY (or moved by the wheel).
func (t *Tab) ScrollY() int { return int(t.ds.scrollY.Load()) }

func (t *Tab) lazyReeval() {
	env := t.ds.env.Load()
	if env == nil || t.closed.Load() {
		return
	}
	if !t.policy().LazyImages || t.suspended() {
		return
	}
	_, vh, _, _, _ := t.Viewport()
	if !env.hasAdmissible(float64(t.ds.scrollY.Load()), float64(vh)) {
		return
	}
	t.mu.RLock()
	doc := t.dom
	t.mu.RUnlock()
	if doc == nil || doc != env.doc {
		return
	}
	doc.Touch()
	doc.Layout(doc.LayoutWidth())
}

// scrollFromEval recognises the helper script's scroll notification so the engine
// follows the app's scroll position without a separate call.
func (t *Tab) scrollFromEval(expr string) {
	const p = "__sweefScrollTo("
	if !strings.HasPrefix(expr, p) || !strings.HasSuffix(expr, ")") {
		return
	}
	if y, err := strconv.Atoi(strings.TrimSpace(expr[len(p) : len(expr)-1])); err == nil {
		t.SetScrollY(y)
	}
}

// ---- tap to load -------------------------------------------------------------------------

// ErrNotSkipped is returned by LoadSkipped for a node that is not a placeholder.
var ErrNotSkipped = errors.New("engine: node is not a Data Saver placeholder")

func (t *Tab) liteEnv() (*tabEnv, error) {
	env := t.ds.env.Load()
	if env == nil {
		return nil, ErrNotFound
	}
	return env, nil
}

// LoadSkipped fetches the resource behind a placeholder (an <img> or a frame
// marked data-sweef-lite) at full size, ignoring the caps, and swaps it in. nodeID is
// the backend node id of the placeholder element. A frame placeholder is only
// uncovered: the engine renders no frame documents.
func (t *Tab) LoadSkipped(nodeID int64) error {
	env, err := t.liteEnv()
	if err != nil {
		return err
	}
	n := env.doc.NodeByID(nodeID)
	if n == nil || n.Type != html.ElementNode || attrVal(n, liteMark) == "" {
		return ErrNotSkipped
	}
	switch n.Data {
	case "iframe":
		env.lz.mu.Lock()
		if env.lz.frameOK == nil {
			env.lz.frameOK = map[*html.Node]bool{}
		}
		env.lz.frameOK[n] = true
		env.lz.mu.Unlock()
		env.doc.RemoveAttrQuiet(n, liteMark)
		env.doc.Touch()
		return nil
	case "img":
		u := imgURL(env.doc.BaseURL(), n)
		env.forceImage(u)
		env.doc.RemoveAttrQuiet(n, liteMark)
		env.doc.Touch()
		_, ferr := env.loadForced(u)
		env.doc.Layout(env.doc.LayoutWidth())
		return ferr
	}
	return ErrNotSkipped
}

// LoadSkippedImages loads every withheld image on the page and every deferred
// lazy one ("Load images on this page"). It returns how many images it loaded.
func (t *Tab) LoadSkippedImages() (int, error) {
	env, err := t.liteEnv()
	if err != nil {
		return 0, err
	}
	base := env.doc.BaseURL()
	urls := map[string]bool{}
	walkNodes(env.doc.Root, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" && attrVal(n, liteMark) == "skipped" {
			if u := imgURL(base, n); u != "" {
				urls[u] = true
			}
			env.doc.RemoveAttrQuiet(n, liteMark)
		}
	})
	env.lz.mu.Lock()
	for _, h := range env.lz.pending {
		urls[h.url] = true
	}
	env.lz.mu.Unlock()
	list := make([]string, 0, len(urls))
	for u := range urls {
		env.forceImage(u)
		list = append(list, u)
	}
	env.prefetch(list)
	env.doc.Touch()
	env.doc.Layout(env.doc.LayoutWidth())
	return len(list), nil
}

func (e *tabEnv) forceImage(u string) {
	e.lz.mu.Lock()
	if e.lz.forced == nil {
		e.lz.forced = map[string]bool{}
	}
	e.lz.forced[u] = true
	delete(e.lz.skipped, u)
	e.lz.mu.Unlock()
	e.mu.Lock()
	delete(e.imgs, u)
	e.mu.Unlock()
}

// loadForced fetches u as the person asked for it and decodes it into the image memo.
func (e *tabEnv) loadForced(u string) (image.Image, error) {
	im := e.Image(u)
	if im == nil {
		return nil, errors.New("image could not be loaded")
	}
	return im, nil
}

// LoadFully reloads the current page once without any Data Saver policy: the
// document, its images, scripts and media load as if the saver were off, and stay
// that way (lazy scrolling included) until the tab navigates elsewhere.
func (t *Tab) LoadFully() error {
	u := t.URL()
	if u == "" || u == "about:blank" {
		return nil
	}
	t.ds.fullNext.Store(true)
	err := t.Navigate(u, map[string]string{"Cache-Control": "no-cache"})
	t.ds.fullNext.Store(false)
	return err
}

// FullyLoaded reports whether LoadFully is in effect for the current page.
func (t *Tab) FullyLoaded() bool { return t.ds.fullDoc.Load() }

// isLitePlaceholder reports whether node bid is a Data Saver placeholder.
func (t *Tab) isLitePlaceholder(bid int64) bool {
	v, ok, err := t.GetAttributeByBackend(bid, liteMark)
	return err == nil && ok && v != ""
}
