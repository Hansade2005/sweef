package engine

import (
	"math"
	"sync"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render"
)

// scroll.go: scrolling of overflow containers (overflow: auto | scroll | hidden elements).
//
// The document keeps one scroll offset per scrolled element. Layout reads them through render.ScrollEnv and
// places the element's content at its scrolled position, so painting, hit testing and every geometry query agree
// with what is on screen. The viewport itself is scrolled by the embedder (the GUI moves a window over the page
// texture); frames scroll as documents of their own (frames_input.go).

// scrollOffsets are the scroll offsets of one document's overflow containers.
type scrollOffsets struct {
	mu sync.Mutex
	m  map[*html.Node][2]float64
}

// ScrollOffset implements render.ScrollEnv.
func (e *tabEnv) ScrollOffset(n *html.Node) (float64, float64) {
	e.scroll.mu.Lock()
	o := e.scroll.m[n]
	e.scroll.mu.Unlock()
	return o[0], o[1]
}

var _ render.ScrollEnv = (*tabEnv)(nil)

// ScrollTarget describes one scroll container.
type ScrollTarget struct {
	ID         int64      // backend id of the element
	X, Y       float64    // how far its content is scrolled, CSS px
	MaxX, MaxY float64    // the scrollable range
	Rect       [4]float64 // border box in document CSS px (x, y, w, h)
	SmoothCSS  bool       // scroll-behavior: smooth
	Hidden     bool       // overflow: hidden (scrollable by script only)
}

// scrollInfoOf reads the scroll state of n from the current layout.
func scrollInfoOf(res *render.Result, d *fnet.Doc, n *html.Node) (ScrollTarget, bool) {
	b := res.Boxes[n]
	if b == nil || b.Style == nil {
		return ScrollTarget{}, false
	}
	tg := ScrollTarget{ID: d.IDOf(n), X: b.ScrollLeft, Y: b.ScrollTop,
		MaxX: math.Max(0, b.ScrollW-b.ClientW), MaxY: math.Max(0, b.ScrollH-b.ClientH),
		Rect:      [4]float64{b.Border.X, b.Border.Y, b.Border.W, b.Border.H},
		SmoothCSS: b.Style.ScrollBehavior == "smooth"}
	return tg, true
}

func userScrollable(ov string) bool { return ov == "auto" || ov == "scroll" }

// cachedLayout returns the last layout the document computed, never starting a new one: the caller is the UI
// thread, which must not wait for style and layout. It is nil while the page is being laid out.
func (t *Tab) cachedResult() (*render.Result, *fnet.Doc) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return nil, nil
	}
	l := d.CachedLayout()
	if l == nil {
		return nil, nil
	}
	ext, _ := l.Ext.(*renderExt)
	if ext == nil {
		return nil, nil
	}
	return ext.res, d
}

// ScrollTargetAt finds the overflow container that a scroll gesture of (dx, dy) at the document point (x, y)
// scrolls: the innermost one under the point that can move in the direction of the gesture. blocked is true
// when the gesture ends at a container that cannot move any further and has overscroll-behavior contain or
// none, so it must not scroll anything else; found is false when no container takes it and the viewport
// should. It uses the last layout and never waits for a new one.
func (t *Tab) ScrollTargetAt(x, y, dx, dy float64) (tg ScrollTarget, found, blocked bool) {
	res, d := t.cachedResult()
	if res == nil || res.Hit == nil {
		return
	}
	n := res.Hit(x, y)
	vertical := math.Abs(dy) >= math.Abs(dx)
	for a := n; a != nil; a = a.Parent {
		if a.Type != html.ElementNode || a.Data == "html" || a.Data == "body" {
			continue
		}
		b := res.Boxes[a]
		if b == nil || b.Style == nil || b.Display == "none" {
			continue
		}
		info, ok := scrollInfoOf(res, d, a)
		if !ok {
			continue
		}
		st := b.Style
		if vertical {
			if !userScrollable(st.OverflowY) || info.MaxY <= 0 {
				continue
			}
			if dy > 0 && info.Y < info.MaxY-0.5 || dy < 0 && info.Y > 0.5 {
				return info, true, false
			}
			if st.OverscrollY != "auto" && st.OverscrollY != "" {
				return ScrollTarget{}, false, true
			}
		} else {
			if !userScrollable(st.OverflowX) || info.MaxX <= 0 {
				continue
			}
			if dx > 0 && info.X < info.MaxX-0.5 || dx < 0 && info.X > 0.5 {
				return info, true, false
			}
			if st.OverscrollX != "auto" && st.OverscrollX != "" {
				return ScrollTarget{}, false, true
			}
		}
	}
	return
}

// ScrollTargetOf is the current scroll state of the element with backend id, from the last layout.
func (t *Tab) ScrollTargetOf(id int64) (ScrollTarget, bool) {
	res, d := t.cachedResult()
	if res == nil {
		return ScrollTarget{}, false
	}
	n := d.NodeByID(id)
	if n == nil {
		return ScrollTarget{}, false
	}
	return scrollInfoOf(res, d, n)
}

// SetElementScroll scrolls the element with backend id to (x, y), clamped to its range, and returns the offsets
// that were applied and whether they differ from before. The next layout places the content there. It may wait
// for a layout in progress, so it belongs on the tab's worker.
func (t *Tab) SetElementScroll(id int64, x, y float64) (cx, cy float64, changed bool) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	env := t.ds.env.Load()
	if d == nil || env == nil {
		return 0, 0, false
	}
	n := d.NodeByID(id)
	if n == nil {
		return 0, 0, false
	}
	l := d.Layout(d.LayoutWidth())
	ext, _ := l.Ext.(*renderExt)
	if ext == nil {
		return 0, 0, false
	}
	info, ok := scrollInfoOf(ext.res, d, n)
	if !ok {
		return 0, 0, false
	}
	cx = math.Round(math.Min(math.Max(x, 0), info.MaxX))
	cy = math.Round(math.Min(math.Max(y, 0), info.MaxY))
	env.scroll.mu.Lock()
	if env.scroll.m == nil {
		env.scroll.m = map[*html.Node][2]float64{}
	}
	old := env.scroll.m[n]
	if old[0] == cx && old[1] == cy && info.X == cx && info.Y == cy {
		env.scroll.mu.Unlock()
		return cx, cy, false
	}
	if cx == 0 && cy == 0 {
		delete(env.scroll.m, n)
	} else {
		env.scroll.m[n] = [2]float64{cx, cy}
	}
	env.scroll.mu.Unlock()
	d.TouchLayout()
	// only the container's own rows change on screen
	t.quiet.addRows(info.Rect[1], info.Rect[1]+info.Rect[3])
	return cx, cy, true
}

// NormalizeScroll lays the document out and brings the scroll offsets of its containers back into range (the
// content shrank, a container got smaller). It reports whether anything moved, in which case the next layout
// differs. Worker only.
func (t *Tab) NormalizeScroll() bool {
	env := t.ds.env.Load()
	if env == nil {
		return false
	}
	env.scroll.mu.Lock()
	n := len(env.scroll.m)
	env.scroll.mu.Unlock()
	if n == 0 {
		return false
	}
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return false
	}
	d.Layout(d.LayoutWidth())
	return t.clampScrollOffsets()
}

// clampScrollOffsets brings the stored offsets back into range after a layout (content shrank, a container
// became smaller) and reports whether anything changed; the caller lays out again when it did.
func (t *Tab) clampScrollOffsets() bool {
	res, d := t.cachedResult()
	env := t.ds.env.Load()
	if res == nil || env == nil {
		return false
	}
	env.scroll.mu.Lock()
	defer env.scroll.mu.Unlock()
	changed := false
	for n, o := range env.scroll.m {
		info, ok := scrollInfoOf(res, d, n)
		if !ok {
			continue
		}
		cx, cy := math.Min(o[0], info.MaxX), math.Min(o[1], info.MaxY)
		if cx != o[0] || cy != o[1] {
			if cx <= 0 && cy <= 0 {
				delete(env.scroll.m, n)
			} else {
				env.scroll.m[n] = [2]float64{cx, cy}
			}
			changed = true
		}
	}
	if changed {
		d.TouchLayout()
	}
	return changed
}
