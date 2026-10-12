package app

import (
	"encoding/json"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
)

// startChrome creates the privileged chrome tab and loads the UI in its worker.
func (a *App) startChrome() {
	t, err := a.eng.NewTab("about:blank")
	if err != nil {
		a.logf("chrome tab: %v", err)
		return
	}
	e := a.newEtab("chrome", "chrome", t, false)
	e.vpW.Store(int32(a.wLog))
	e.vpH.Store(int32(a.hLog))
	a.mu.Lock()
	a.chrome = e
	a.mu.Unlock()
	a.wg.Add(1)
	go a.busLoop()
	html, err := a.asset(a.cfg.ChromeEntry)
	if err != nil {
		html = []byte(placeholderChrome)
		a.logf("chrome assets missing (%v): using the built-in placeholder", err)
	}
	entryURL := "sweef://" + strings.TrimSuffix(a.cfg.ChromeEntry, "/index.html") + "/index.html"
	if !strings.HasSuffix(a.cfg.ChromeEntry, "index.html") {
		entryURL = "sweef://" + a.cfg.ChromeEntry
	}
	a.startOverlay()
	e.w.do(func() {
		a.loadCookies()
		e.t.SetViewport(a.wLog, a.hLog, a.scale, false, false)
		e.t.SetDocument(string(html), entryURL)
		e.drainBindings()
		e.pump(40 * time.Millisecond)
		a.post(a.onChromeLoaded)
	})
}

// asset reads a file from the chrome FS.
func (a *App) asset(name string) ([]byte, error) {
	if a.cfg.ChromeFS == nil {
		if name == defaultChromeEntry {
			return []byte(placeholderChrome), nil
		}
		return nil, fs.ErrNotExist
	}
	return fs.ReadFile(a.cfg.ChromeFS, strings.TrimPrefix(path.Clean(name), "/"))
}

func (a *App) onChromeLoaded() {
	a.chromeLoaded.Store(true)
	a.lastState = nil
	a.stateDirty = true
	a.chrome.requestPaint(true)
}

// scaleAtomic is read by workers; the UI loop is the only writer.
func (a *App) scaleAtomic() float64 { return a.scaleBits() }

// sendChrome delivers a message to the chrome page only.
func (a *App) sendChrome(typ string, payload any) { a.sendTo(a.chromeTab(), typ, payload) }

// afterOp is the tail of every chrome/overlay/page-UI worker op: forward posts
// made by the page, give its timers a slice, repaint if the DOM changed.
func (e *etab) afterOp() {
	e.drainBindings()
	e.pump(8 * time.Millisecond)
	e.paint(false)
	if e.kind == "chrome" {
		e.probeHole()
	}
	e.armPump()
}

// probeHole asks the chrome page whether something other than the viewport
// placeholder covers the middle of the viewport (a scrim behind a popover, a
// modal): then pointer events belong to the chrome, not the page.
func (e *etab) probeHole() {
	vp := e.a.vpSnapshot()
	if vp.empty() {
		return
	}
	var busy bool
	js := "__sweefHoleBusy(" + itoa(int(vp.X+vp.W/2)) + "," + itoa(int(vp.Y+vp.H/2)) + ")"
	if e.eval(js, &busy) == nil {
		e.a.holeBusy.Store(busy)
	}
}

// armPump keeps a tab's timers running (animations) while it has any, and
// stops completely when it has none, so an idle browser wakes no goroutine.
func (e *etab) armPump() {
	if e.closed.Load() || e.a.closing.Load() {
		return
	}
	if e.kind == "page" && !e.a.isActive(e) {
		return
	}
	if !e.hasTimers() {
		return
	}
	if !e.pumpArmed.CompareAndSwap(false, true) {
		return
	}
	d := 33 * time.Millisecond
	if e.kind == "page" {
		d = 100 * time.Millisecond
	}
	time.AfterFunc(d, func() {
		e.pumpArmed.Store(false)
		e.w.do(func() {
			e.drainBindings()
			e.pump(4 * time.Millisecond)
			e.paint(false)
			e.armPump()
		})
	})
}

// pushState sends the full state once, then merge patches, to every UI page.
func (a *App) pushState() {
	a.stateDirty = false
	snap := state.Normalize(a.buildState())
	if t, ok := a.activeTab(); ok {
		title := t.Title
		if title == "" {
			title = t.URL
		}
		a.win.SetTitle(strings.TrimSpace(title + " - Sweef"))
	}
	if a.lastState == nil {
		a.broadcast(mState, snap)
	} else if p := state.Diff(a.lastState, snap); p != nil {
		a.broadcast(mStatePatch, p)
	}
	a.lastState = snap
	if a.focusOmniAfterState {
		a.focusOmniAfterState = false
		a.sendChrome("omnibox.focus", map[string]any{"select": true})
		a.focusOmnibox()
	}
	a.stats.statePushes.Add(1)
	a.scheduleSession()
}

func (a *App) markState() { a.stateDirty = true }

// syncSettingsToModel mirrors stored settings into the model snapshot.
func (a *App) syncSettingsToModel() {
	all := a.prof.Settings.All()
	a.model.Settings = all
	if th, ok := all["theme"].(string); ok {
		a.model.Theme = th
	}
	if w, ok := all["sidebar.width"].(int); ok {
		a.model.Sidebar.Width = w
	}
}

func decode[T any](raw json.RawMessage) T {
	var v T
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

type tabArg struct {
	Tab string `json:"tab"`
}
type idArg struct {
	ID string `json:"id"`
}

// tabOrActive resolves a tab argument. An empty id means "the sender's tab"
// when a page tab sent the message (the new tab page does this), else the
// active tab.
func (a *App) tabOrActive(id string) string {
	if id != "" {
		return id
	}
	return a.model.Active(a.winID)
}

func (a *App) activeTab() (state.Tab, bool) {
	id := a.model.Active(a.winID)
	if id == "" {
		return state.Tab{}, false
	}
	return a.model.Tab(id)
}

func (a *App) isActive(e *etab) bool {
	return e.kind != "page" || a.model.Active(a.winID) == e.id
}

func (a *App) page(id string) *etab {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pages[id]
}

func (a *App) chromeTab() *etab {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.chrome
}
