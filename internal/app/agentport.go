package app

import (
	"context"
	"errors"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
)

// This file is the narrow, goroutine-safe surface through which an agent host
// (internal/agentbridge) drives the live tabs of the GUI. Everything here may
// be called from any goroutine except the UI loop itself (the blocking calls
// would deadlock it). Engine access is always executed on the tab's worker,
// so it is serialised with painting and with the user's own input, exactly
// like the rest of the app.

// AgentAttacher is implemented by an AgentHost that needs the App (to build a
// Backend over the live tabs). New calls AttachApp once, after the engine
// exists and before the chrome starts loading.
type AgentAttacher interface{ AttachApp(a *App) }

// AgentSidebarWatcher is an optional AgentHost capability: SidebarOpened is
// called on the UI loop whenever the sidebar opens (it must not block).
type AgentSidebarWatcher interface{ SidebarOpened() }

// AgentKeySetter is an optional AgentHost capability: StoreKey saves the API
// key and reports whether it was accepted (agent.keyResult carries the answer).
type AgentKeySetter interface{ StoreKey(key string) error }

// AgentTab is the agent host's view of one tab. The visibility and incognito
// decision is the host's; this only reports facts.
type AgentTab struct {
	ID      string
	URL     string
	Title   string
	Active  bool
	Private bool // an incognito tab (or any tab of an incognito window)
	Loading bool
}

// AgentTabs lists the window's tabs in visual order.
func (a *App) AgentTabs() []AgentTab {
	act := a.model.Active(a.winID)
	var out []AgentTab
	for _, id := range a.model.Order(a.winID) {
		st, ok := a.model.Tab(id)
		if !ok {
			continue
		}
		out = append(out, AgentTab{ID: id, URL: st.URL, Title: st.Title, Active: id == act, Private: st.Incognito || a.cfg.Incognito, Loading: st.Loading})
	}
	return out
}

// AgentTab returns one tab by state id.
func (a *App) AgentTab(id string) (AgentTab, bool) {
	st, ok := a.model.Tab(id)
	if !ok {
		return AgentTab{}, false
	}
	return AgentTab{ID: id, URL: st.URL, Title: st.Title, Active: id == a.model.Active(a.winID), Private: st.Incognito || a.cfg.Incognito, Loading: st.Loading}, true
}

// AgentWindowPrivate reports whether the whole window is incognito.
func (a *App) AgentWindowPrivate() bool { return a.cfg.Incognito }

// AgentSiteRule is the user's per-origin agent visibility for a URL or origin.
func (a *App) AgentSiteRule(urlOrOrigin string) store.Visibility {
	return a.prof.Sites.Agent(urlOrOrigin)
}

// AgentSetSiteRule records the user's answer for an origin. It does the same
// as the sidebar's "allow / block this site" buttons (state is refreshed).
func (a *App) AgentSetSiteRule(origin string, v store.Visibility) {
	a.prof.Sites.SetAgent(origin, v)
	a.post(a.markState)
}

// onLoop runs f on the UI loop and waits for it.
func (a *App) onLoop(ctx context.Context, f func()) error {
	done := make(chan struct{})
	a.post(func() { f(); close(done) })
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-a.quit:
		return errors.New("browser is closing")
	}
}

// AgentRunOnTab runs f on the worker of the page tab with state id tabID,
// then forwards the page's bindings and repaints it if it is the visible tab,
// so the user watches each step land. f gets the engine tab; it must not keep
// it. The returned error is f's, or "tab closed" / a timeout.
func (a *App) AgentRunOnTab(tabID string, f func(t *engine.Tab) error) error {
	e := a.page(tabID)
	if e == nil {
		return errors.New("no such tab")
	}
	_ = a.onLoop(context.Background(), func() { a.agentTouch(tabID) }) // before the step runs: the run is measured from here
	ch := make(chan error, 1)
	if !e.w.do(func() {
		a.observeTab(tabID, e.t) // what the page cost so far, before the step may navigate away
		err := f(e.t)
		a.observeTab(tabID, e.t)
		e.drainBindings()
		if !e.closed.Load() && a.model.Active(a.winID) == tabID {
			e.paint(false)
		}
		ch <- err
	}) {
		return errors.New("tab closed")
	}
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Minute):
		return errors.New("timeout")
	}
}

// AgentEngineID maps a state tab id to the engine's tab id ("" if unknown).
func (a *App) AgentEngineID(tabID string) string {
	if e := a.page(tabID); e != nil {
		return e.t.ID
	}
	return ""
}

// AgentSync waits until everything queued for the tab so far has run
// (navigation, paints), and the UI loop has processed its results.
func (a *App) AgentSync(ctx context.Context, tabID string) error {
	if e := a.page(tabID); e != nil {
		if err := e.call(func() error { return nil }); err != nil {
			return err
		}
	}
	return a.onLoop(ctx, func() {})
}

// AgentNavigate loads raw in the tab through the app's own navigation path
// (HTTPS upgrade, tracker shield, history, security state) and returns when
// the load finished. A failed load renders the usual error page and returns
// its error.
func (a *App) AgentNavigate(ctx context.Context, tabID, raw string) error {
	e := a.page(tabID)
	if e == nil {
		return errors.New("no such tab")
	}
	e.loadErr.Store("")
	if err := a.onLoop(ctx, func() {
		e.agentLoad.Store(true) // the agent never needs images, fonts or media (data.agentLite)
		a.agentTouch(tabID)
		a.loadURL(tabID, raw, modePush, false)
	}); err != nil {
		return err
	}
	if err := a.AgentSync(ctx, tabID); err != nil {
		return err
	}
	if s, _ := e.loadErr.Load().(string); s != "" {
		return errors.New(s)
	}
	return nil
}

// AgentTraverse goes back (-1) or forward (+1) in the tab's own history.
func (a *App) AgentTraverse(ctx context.Context, tabID string, delta int) error {
	e := a.page(tabID)
	if e == nil {
		return errors.New("no such tab")
	}
	ok := false
	e.loadErr.Store("")
	if err := a.onLoop(ctx, func() {
		if st, found := a.model.Tab(tabID); found {
			ok = (delta < 0 && st.CanBack) || (delta > 0 && st.CanForward)
		}
		if ok {
			a.traverse(tabID, delta)
		}
	}); err != nil {
		return err
	}
	if !ok {
		return errors.New("no history entry in that direction")
	}
	if err := a.AgentSync(ctx, tabID); err != nil {
		return err
	}
	if s, _ := e.loadErr.Load().(string); s != "" {
		return errors.New(s)
	}
	return nil
}

// AgentOpenTab opens and activates a new tab and waits for its load.
func (a *App) AgentOpenTab(ctx context.Context, raw string) (string, error) {
	var id string
	if err := a.onLoop(ctx, func() { id = a.openTab(openOpts{URL: raw, Activate: true, Agent: true}); a.agentTouch(id) }); err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("could not open a tab")
	}
	err := a.AgentSync(ctx, id)
	if err == nil {
		if e := a.page(id); e != nil {
			if s, _ := e.loadErr.Load().(string); s != "" {
				err = errors.New(s)
			}
		}
	}
	return id, err
}

// AgentActivateTab brings a tab to the front.
func (a *App) AgentActivateTab(ctx context.Context, id string) error {
	return a.onLoop(ctx, func() {
		if _, ok := a.model.Tab(id); ok {
			a.activateTab(id)
			a.markState()
		}
	})
}

// AgentCloseTab closes a tab.
func (a *App) AgentCloseTab(ctx context.Context, id string) error {
	return a.onLoop(ctx, func() { a.closeTab(id) })
}

// AgentReveal scrolls the visible page so that document position cssY (CSS
// px) is in the upper third of the viewport, with the usual easing. It is a
// no-op for tabs that are not on screen.
func (a *App) AgentReveal(tabID string, cssY int) {
	a.post(func() {
		if a.model.Active(a.winID) != tabID {
			return
		}
		a.scroll.SetMax(a.scrollMax())
		vp := a.vpDevice()
		target := float64(cssY)*a.pageDSF() - float64(vp.Dy())/3
		if target < 0 {
			target = 0
		}
		a.scroll.ScrollTo(target, true)
		a.scrollbarT = time.Now()
		a.kickScroll()
	})
}

// AgentScrollY is the current scroll offset (CSS px) of a tab.
func (a *App) AgentScrollY(tabID string) int {
	if e := a.page(tabID); e != nil {
		return int(e.scrollCSS.Load())
	}
	return 0
}

// AgentCursor shows the agent's cursor over the page at viewport-relative
// logical px (x,y) with a label, or hides it (show=false). The overlay only
// draws it while its state is "acting".
func (a *App) AgentCursor(show bool, x, y float64, label string, pressed bool) {
	a.post(func() {
		if show {
			a.ovl.cursor = map[string]any{"x": x, "y": y, "label": label, "pressed": pressed}
		} else {
			a.ovl.cursor = nil
		}
		if a.ovl.visible() {
			a.setOverlay(a.ovl.state, a.agentRun.task, a.agentRun.step)
		}
	})
}

// AgentViewport is the page viewport in logical px (what the user sees).
func (a *App) AgentViewport() (w, h float64) {
	a.vpMu.Lock()
	defer a.vpMu.Unlock()
	return a.vpShadow.W, a.vpShadow.H
}

// AgentPageZoom is the page zoom factor (cursor coordinates are in layout px).
func (a *App) AgentPageZoom() float64 { return a.zoomF() }

// AgentRunState lets the host mirror the run into state.agent.state (idle,
// working, waiting, paused, error) and the overlay when the app did not see
// an event for it (e.g. a confirm answered).
func (a *App) AgentRunState(s string) {
	a.post(func() {
		if a.agentRun.state != s {
			a.agentRun.state = s
			a.markState()
		}
	})
}

// AgentRefresh asks for a fresh state push (key presence, chats, model).
func (a *App) AgentRefresh() { a.post(a.markState) }

// AgentToast shows a toast in the chrome.
func (a *App) AgentToast(text, kind string) {
	a.post(func() { a.sendChrome(mToast, map[string]any{"text": text, "kind": kind}) })
}

// AgentOverlayState reports the overlay state (off, acting, control, done,
// stopped) and the agent cursor, read on the UI loop (tests, diagnostics).
func (a *App) AgentOverlayState() (state string, cursor map[string]any) {
	_ = a.onLoop(context.Background(), func() {
		state = a.ovl.state
		if a.ovl.cursor != nil {
			cursor = map[string]any{}
			for k, v := range a.ovl.cursor {
				cursor[k] = v
			}
		}
	})
	return
}

// ChromeEval evaluates js in the chrome page on its worker and decodes the
// JSON result into out (nil to ignore). It exists for tests and diagnostics.
func (a *App) ChromeEval(js string, out any) error {
	c := a.chromeTab()
	if c == nil {
		return errors.New("chrome not started")
	}
	return c.call(func() error { return c.eval(js, out) })
}

// ActivePageEval evaluates js in the visible page tab (tests, diagnostics).
func (a *App) ActivePageEval(js string, out any) error {
	e := a.page(a.model.Active(a.winID))
	if e == nil {
		return errors.New("no page")
	}
	return e.call(func() error { return e.eval(js, out) })
}
