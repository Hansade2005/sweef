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
	a.chromeLoaded = true
	a.lastState = nil
	a.stateDirty = true
	a.chrome.requestPaint(true)
}

// scaleAtomic is read by workers; the UI loop is the only writer.
func (a *App) scaleAtomic() float64 { return a.scaleBits() }

// sendChrome delivers a message to the chrome page (any goroutine).
func (a *App) sendChrome(typ string, payload any) {
	a.mu.Lock()
	targets := []*etab{a.chrome, a.overlay}
	a.mu.Unlock()
	if a.closing.Load() {
		return
	}
	js := "sweef.__recv(" + jsString(typ) + "," + jsJSON(payload) + ")"
	for _, e := range targets {
		if e == nil {
			continue
		}
		e := e
		e.w.do(func() {
			if _, err := e.t.Evaluate(js); err != nil {
				a.logf("%s <- %s: %v", e.kind, typ, err)
			}
			e.afterOp()
		})
	}
}

// afterOp is the tail of every chrome/overlay worker op: forward posts made
// by the page, give its timers a slice, repaint if the DOM changed.
func (e *etab) afterOp() {
	e.drainBindings()
	e.pump(8 * time.Millisecond)
	e.paint(false)
	e.armPump()
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

// pushState sends the full state once, then merge patches.
func (a *App) pushState() {
	a.stateDirty = false
	a.syncSettingsToModel()
	snap := state.Normalize(a.model.Snapshot(a.winID))
	if t, ok := a.activeTab(); ok {
		title := t.Title
		if title == "" {
			title = t.URL
		}
		a.win.SetTitle(strings.TrimSpace(title + " - Sweef"))
	}
	if a.lastState == nil {
		a.sendChrome(mState, snap)
	} else if p := state.Diff(a.lastState, snap); p != nil {
		a.sendChrome(mStatePatch, p)
	}
	a.lastState = snap
	a.stats.statePushes++
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
	if w, ok := all["sidebarWidth"].(int); ok {
		a.model.Sidebar.Width = w
	}
}

// ---- messages from the chrome page --------------------------------------------------------

func decode[T any](raw json.RawMessage) T {
	var v T
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

// onUIMessage handles sweef.post(type, payload) from the chrome page or from
// an internal page (history, settings, ...). Internal pages may use the same
// vocabulary; nothing here trusts web content (see drainBindings).
func (a *App) onUIMessage(src *etab, typ string, raw json.RawMessage) {
	a.stats.uiMessages++
	switch typ {
	case "overlay":
		if src.kind != "overlay" {
			return
		}
		p := decode[struct {
			Rect    rect    `json:"rect"`
			Visible *bool   `json:"visible"`
			Alpha   float64 `json:"alpha"`
		}](raw)
		vis := p.Visible == nil || *p.Visible
		a.onOverlayMsg(p.Rect, vis, p.Alpha)
	case "layout":
		if src.kind != "chrome" {
			return
		}
		p := decode[struct {
			Viewport rect `json:"viewport"`
		}](raw)
		a.setViewport(p.Viewport)
	case "ready":
		a.lastState = nil
		a.stateDirty = true
	case "nav.go":
		p := decode[struct {
			Tab   string `json:"tab"`
			Input string `json:"input"`
		}](raw)
		a.navigateInput(a.tabOrActive(p.Tab), p.Input)
	case "nav.back":
		a.traverse(a.tabOrActive(decode[tabArg](raw).Tab), -1)
	case "nav.forward":
		a.traverse(a.tabOrActive(decode[tabArg](raw).Tab), +1)
	case "nav.reload":
		p := decode[struct {
			Tab  string `json:"tab"`
			Hard bool   `json:"hard"`
		}](raw)
		a.reload(a.tabOrActive(p.Tab), p.Hard)
	case "nav.stop":
		a.stopLoading(a.tabOrActive(decode[tabArg](raw).Tab))
	case "tab.new":
		p := decode[struct {
			URL       string `json:"url"`
			Incognito bool   `json:"incognito"`
		}](raw)
		a.newTab(p.URL, p.Incognito, true)
	case "incognito.new":
		a.newTab("", true, true)
	case "tab.close":
		a.closeTab(a.tabOrActive(decode[idArg](raw).ID))
	case "tab.activate":
		a.activateTab(decode[idArg](raw).ID)
	case "tab.move":
		p := decode[struct {
			ID string `json:"id"`
			To int    `json:"to"`
		}](raw)
		a.model.Move(p.ID, p.To)
		a.markState()
	case "tab.pin":
		a.togglePin(a.tabOrActive(decode[idArg](raw).ID))
	case "tab.mute":
		a.toggleMute(a.tabOrActive(decode[idArg](raw).ID))
	case "tab.reopen":
		a.reopenClosed()
	case "omni.input":
		a.omniInput(decode[struct {
			ID   any    `json:"id"`
			Text string `json:"text"`
		}](raw))
	case "bookmark.toggle":
		a.toggleBookmark(a.tabOrActive(decode[tabArg](raw).Tab))
	case "bookmark.add":
		p := decode[struct{ Title, URL, Folder string }](raw)
		if _, err := a.prof.Bookmarks.Add(p.Folder, p.Title, p.URL); err == nil {
			a.markState()
		}
	case "bookmark.remove":
		a.prof.Bookmarks.Remove(decode[idArg](raw).ID)
		a.markState()
	case "menu.open":
		a.openMenu(decode[struct {
			Name string  `json:"name"`
			X    float64 `json:"x"`
			Y    float64 `json:"y"`
		}](raw))
	case "agent.send":
		p := decode[struct{ Text, Mode string }](raw)
		a.agentSend(p.Text, p.Mode)
	case "agent.stop":
		if a.agent != nil {
			a.agent.Stop()
		}
	case "agent.takeControl":
		if a.agent != nil {
			a.agent.TakeControl()
		}
	case "agent.setKey":
		if a.agent != nil {
			a.agent.SetKey(decode[struct {
				Key string `json:"key"`
			}](raw).Key)
		}
	case "agent.pageVisibility":
		p := decode[struct {
			Origin  string `json:"origin"`
			Allowed bool   `json:"allowed"`
		}](raw)
		a.setAgentVisibility(p.Origin, p.Allowed)
	case "settings.set":
		p := decode[struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		}](raw)
		if err := a.prof.Settings.Set(p.Key, p.Value); err != nil {
			a.sendChrome(mToast, map[string]any{"text": err.Error(), "kind": "error"})
		}
	case "sidebar.set":
		p := decode[struct {
			Open  *bool `json:"open"`
			Width *int  `json:"width"`
		}](raw)
		if p.Width != nil && *p.Width >= 240 && *p.Width <= 720 {
			a.model.Sidebar.Width = *p.Width
			_ = a.prof.Settings.Set("sidebarWidth", *p.Width)
		}
		if p.Open != nil {
			a.setSidebar(*p.Open)
		}
		a.markState()
	case "window.close":
		a.requestClose()
	case "window.minimize", "window.maximize":
		a.logf("%s: shell.Window has no such call; ignored", typ)
	case "find.query":
		a.find(decode[struct {
			Text string `json:"text"`
		}](raw).Text, 1)
	case "find.next":
		a.find(a.model.Find.Query, +1)
	case "find.prev":
		a.find(a.model.Find.Query, -1)
	case "find.close":
		a.model.Find = state.Find{}
		a.markState()
	case "downloads.open":
		if it, ok := a.prof.Downloads.Get(decode[idArg](raw).ID); ok {
			_ = a.win.OpenURL("file://" + it.Path)
		}
	case "downloads.pause":
		a.prof.Downloads.Pause(decode[idArg](raw).ID)
	case "downloads.resume":
		a.prof.Downloads.Resume(decode[idArg](raw).ID)
	case "downloads.cancel":
		a.prof.Downloads.Cancel(decode[idArg](raw).ID)
	case "downloads.remove":
		a.prof.Downloads.Remove(decode[idArg](raw).ID)
	case "history.clear":
		since := decode[struct {
			Since int64 `json:"since"`
		}](raw).Since
		if since > 0 {
			a.prof.History.ClearSince(time.Unix(since, 0))
		} else {
			a.prof.History.Clear()
		}
	case "history.delete":
		a.prof.History.Delete(decode[struct {
			URL string `json:"url"`
		}](raw).URL)
	case "site.privacy":
		p := decode[struct {
			Origin  string `json:"origin"`
			NoBlock bool   `json:"noBlock"`
		}](raw)
		a.prof.Sites.SetPrivacyException(p.Origin, p.NoBlock)
	case "zoom":
		a.zoom(decode[struct {
			Delta int `json:"delta"`
		}](raw).Delta)
	default:
		a.logf("ui: unknown message %q", typ)
	}
}

type tabArg struct {
	Tab string `json:"tab"`
}
type idArg struct {
	ID string `json:"id"`
}

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

// onSetting reacts to a changed setting.
func (a *App) onSetting(key string, v any) {
	switch key {
	case "blockTrackers", "httpsUpgrade", "blockThirdPartyCookies", "sendGPC", "trimReferrer":
		a.priv.Update(a.privacySettings())
		hdr := a.priv.Headers()
		a.mu.Lock()
		for _, e := range a.pages {
			e.t.SetExtraHeaders(hdr)
		}
		a.mu.Unlock()
	case "agentDefaultVisibility":
		a.prof.Sites.SetDefault(a.defaultVisibility())
	case "sidebarOpen":
		if b, ok := v.(bool); ok {
			a.setSidebar(b)
		}
	}
	a.markState()
}

func (a *App) chromeTab() *etab {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.chrome
}
