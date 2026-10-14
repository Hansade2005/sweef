package app

// notifications.go: the app side of Notifications, Web Push and background sync (docs/notifications.md,
// docs/push.md, docs/background-sync.md). The engine asks this file four things: what a site may do
// (sitePermission), whether to ask the user (requestPermission), where to show a notification (the sink) and how
// to open or focus a tab for a service worker. The chrome page shows the question as a prompt next to the address
// bar, and the answer is kept in the profile's site settings.

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/notify"
)

// permPrompt is a question to the user that a page's script is waiting on.
type permPrompt struct {
	id     string
	tab    string // state tab id
	origin string
	name   string
	ans    chan string
}

// pwaState is what the app keeps for the web-platform background features.
type pwaApp struct {
	ctx     *engine.BrowserContext
	sink    *notify.Fallback
	prompts []*permPrompt // UI loop only
	seq     int
	mu      sync.Mutex
	badges  map[string]engine.Badge
}

// initPWA connects the normal window's engine context to the app. A private window never gets this: its context
// stays unconfigured, which the engine treats as "notifications and push are unavailable".
func (a *App) initPWA() {
	if a.cfg.Incognito {
		return
	}
	primary := a.cfg.NotifySink
	if primary == nil {
		primary = notify.NewLazy(func() (notify.Sink, error) { return notify.Native("Sweef") })
	}
	fb := notify.NewFallback(primary, a.toastNotification)
	p := &pwaApp{ctx: a.eng.DefaultContext(), sink: fb, badges: map[string]engine.Badge{}}
	a.pw = p
	p.ctx.SetPWA(engine.PWAOptions{
		Sink:         fb,
		Permission:   a.sitePermission,
		Request:      a.requestPermission,
		OpenWindow:   a.pwaOpenWindow,
		FocusTab:     a.pwaFocusTab,
		TabState:     a.pwaTabState,
		DoNotDisturb: func() bool { return a.prof.Settings.Bool("notifications.dnd") },
		OnBadge:      a.onBadge,
		PushServiceFunc: func() string {
			if a.cfg.PushService != "" {
				return a.cfg.PushService
			}
			return a.prof.Settings.Str("push.service")
		},
		Logf: a.logf,
	})
}

// sitePermission is the state of a permission for an origin: "granted", "denied" or "prompt".
func (a *App) sitePermission(origin, name string) string {
	rule := a.prof.Sites.Rule(origin)
	switch name {
	case engine.PermNotifications, engine.PermPush:
		switch rule.Notify {
		case "granted", "denied":
			return rule.Notify
		}
		if a.prof.Settings.Str("notifications.default") == "blocked" {
			return "denied"
		}
		return "prompt"
	case engine.PermClipboardRead:
		if rule.ClipboardRead == "granted" || rule.ClipboardRead == "denied" {
			return rule.ClipboardRead
		}
		return "prompt"
	case engine.PermPeriodicSync:
		if rule.Periodic != "" {
			return rule.Periodic
		}
		// background activity is earned: bookmarked sites (the closest thing to "installed" here) may use it
		for _, b := range a.prof.Bookmarks.All() {
			if !b.Folder && store.OriginOf(b.URL) == origin {
				return "granted"
			}
		}
		return "denied"
	}
	return "prompt"
}

// requestPermission runs on an engine goroutine and returns when the user has answered.
func (a *App) requestPermission(ctx context.Context, r engine.PermissionRequest) string {
	if a.prof.Sites.Embargoed(r.Origin, a.now()) {
		return "denied" // dismissed too often: no prompt for a while
	}
	p := &permPrompt{origin: r.Origin, name: r.Permission, ans: make(chan string, 1)}
	a.post(func() { a.addPrompt(p, r.TabID) })
	select {
	case res := <-p.ans:
		return res
	case <-ctx.Done():
		a.post(func() { a.removePrompt(p) })
		return "prompt"
	case <-a.quit:
		return "prompt"
	}
}

func (a *App) now() time.Time {
	if a.cfg.Now != nil {
		return a.cfg.Now()
	}
	return time.Now()
}

// stateTabOf maps an engine tab id to the state tab id.
func (a *App) stateTabOf(engineID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, e := range a.pages {
		if e.t != nil && e.t.ID == engineID {
			return id
		}
	}
	return ""
}

// ---- prompts (UI loop) ----------------------------------------------------------------------------------------------

func (a *App) addPrompt(p *permPrompt, engineTab string) {
	if a.pw == nil {
		p.ans <- "prompt"
		return
	}
	p.tab = a.stateTabOf(engineTab)
	if p.tab == "" {
		p.ans <- "prompt"
		return
	}
	a.pw.seq++
	p.id = "perm" + itoa(a.pw.seq)
	for _, o := range a.pw.prompts { // one question per site and permission at a time
		if o.origin == p.origin && o.name == p.name && o.tab == p.tab {
			p.ans <- "prompt"
			return
		}
	}
	a.pw.prompts = append(a.pw.prompts, p)
	a.markState()
}

func (a *App) removePrompt(p *permPrompt) {
	if a.pw == nil {
		return
	}
	out := a.pw.prompts[:0]
	for _, o := range a.pw.prompts {
		if o != p {
			out = append(out, o)
		}
	}
	a.pw.prompts = out
	a.markState()
}

// dropPromptsOfTab ends the questions of a tab that is closing or navigated to another site.
func (a *App) dropPromptsOfTab(tab string) {
	if a.pw == nil {
		return
	}
	for _, p := range append([]*permPrompt(nil), a.pw.prompts...) {
		if p.tab == tab {
			a.removePrompt(p)
			p.ans <- "prompt"
		}
	}
}

// hasPrompt reports whether a permission question is waiting in the tab.
func (a *App) hasPrompt(tab string) bool {
	if a.pw == nil {
		return false
	}
	for _, p := range a.pw.prompts {
		if p.tab == tab {
			return true
		}
	}
	return false
}

// prunePrompts drops the questions that no longer apply (the tab is gone or shows another site now).
func (a *App) prunePrompts() {
	if a.pw == nil {
		return
	}
	for _, p := range append([]*permPrompt(nil), a.pw.prompts...) {
		t, ok := a.model.Tab(p.tab)
		if !ok || store.OriginOf(t.URL) != p.origin {
			a.removePrompt(p)
			p.ans <- "prompt"
		}
	}
}

// answerPrompt applies the user's choice: allow and block are remembered, "Not now" only counts toward the embargo.
func (a *App) answerPrompt(id, answer string) {
	if a.pw == nil {
		return
	}
	var p *permPrompt
	for _, o := range a.pw.prompts {
		if o.id == id {
			p = o
		}
	}
	if p == nil {
		return
	}
	res := "prompt"
	set := a.prof.Sites.SetNotify
	if p.name == engine.PermClipboardRead {
		set = a.prof.Sites.SetClipboardRead
	}
	switch answer {
	case "allow":
		set(p.origin, "granted")
		res = "granted"
	case "block":
		set(p.origin, "denied")
		res = "denied"
	default:
		if a.prof.Sites.NoteDismissed(p.origin, a.now()) {
			a.sendChrome(mToast, map[string]any{"text": "Not asking " + hostOf(p.origin) + " again for a week.", "kind": ""})
		}
	}
	a.removePrompt(p)
	p.ans <- res
	a.pw.ctx.PWA().PermissionChanged(p.origin)
	a.markState()
}

func hostOf(origin string) string {
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		return u.Host
	}
	return origin
}

// setSitePermission is the site info entry and the settings page: "granted", "denied" or "" (ask again).
func (a *App) setSitePermission(origin, name, st string) {
	if a.pw == nil || store.OriginOf(origin) == "" {
		return
	}
	o := store.OriginOf(origin)
	switch name {
	case store.PermPeriodicSync:
		a.prof.Sites.SetPeriodic(o, st)
	case store.PermClipboardRead:
		a.prof.Sites.SetClipboardRead(o, st)
	default:
		a.prof.Sites.SetNotify(o, st)
	}
	a.pw.ctx.PWA().PermissionChanged(o)
	a.markState()
}

// permState is the "perm" part of the state snapshot.
func (a *App) permState(act state.Tab, hasAct bool) map[string]any {
	if a.pw == nil {
		return map[string]any{"available": false}
	}
	a.prunePrompts()
	m := map[string]any{"available": true, "dnd": a.prof.Settings.Bool("notifications.dnd"),
		"push": a.pw.ctx.PWA().PushServiceConfigured(), "default": a.prof.Settings.Str("notifications.default")}
	if hasAct {
		origin := store.OriginOf(act.URL)
		if (strings.HasPrefix(act.URL, "https://") || strings.HasPrefix(act.URL, "http://")) && !act.Incognito {
			m["site"] = map[string]any{"origin": origin, "state": permWord(a.prof.Sites.Notify(origin))}
		}
		for _, p := range a.pw.prompts {
			if p.tab == act.ID {
				m["prompt"] = map[string]any{"id": p.id, "origin": p.origin, "host": hostOf(p.origin), "kind": p.name}
				break
			}
		}
		if strings.HasPrefix(act.URL, "sweef://settings") {
			var sites []map[string]any
			for o, r := range a.prof.Sites.WithPermissions() {
				sites = append(sites, map[string]any{"origin": o, "state": permWord(r.Notify), "embargo": r.NotifyEmbargo > a.now().Unix(), "periodic": permWord(r.Periodic)})
			}
			sort.Slice(sites, func(i, j int) bool { return sites[i]["origin"].(string) < sites[j]["origin"].(string) })
			m["sites"] = sites
		}
	}
	a.pw.mu.Lock()
	if len(a.pw.badges) > 0 {
		b := map[string]any{}
		for o, v := range a.pw.badges {
			if v.Count > 0 {
				b[o] = v.Count
			} else {
				b[o] = -1
			}
		}
		m["badges"] = b
	}
	a.pw.mu.Unlock()
	return m
}

func permWord(s string) string {
	switch s {
	case "granted":
		return "allowed"
	case "denied":
		return "blocked"
	}
	return "ask"
}

// ---- notifications in the window, windows and badges ---------------------------------------------------------------

// toastNotification shows a web notification inside the browser when the system has no notification service.
func (a *App) toastNotification(n notify.Notification) {
	acts := []map[string]any{}
	for _, x := range n.Actions {
		acts = append(acts, map[string]any{"id": x.ID, "title": x.Title})
	}
	a.post(func() {
		a.sendChrome(mToast, map[string]any{"kind": "notify", "id": n.ID, "title": n.Title, "text": n.Body, "origin": n.Origin,
			"sticky": n.RequireInteraction, "actions": acts})
	})
}

func (a *App) onNotifyEvent(e notify.Event) {
	if a.pw != nil {
		a.pw.sink.Emit(e)
	}
}

var errAppClosing = errors.New("the browser is closing")

// pwaOpenWindow opens a tab for a service worker (clients.openWindow).
func (a *App) pwaOpenWindow(u string) (*engine.Tab, error) {
	type res struct {
		t   *engine.Tab
		err error
	}
	ch := make(chan res, 1)
	a.post(func() {
		id := a.openTab(openOpts{URL: u, Activate: true})
		if e := a.page(id); e != nil && e.t != nil {
			ch <- res{t: e.t}
			return
		}
		ch <- res{err: errors.New("could not open a tab")}
	})
	select {
	case r := <-ch:
		return r.t, r.err
	case <-a.quit:
		return nil, errAppClosing
	case <-time.After(10 * time.Second):
		return nil, errors.New("timed out opening a tab")
	}
}

// pwaFocusTab makes the tab the visible one (WindowClient.focus, a clicked notification).
func (a *App) pwaFocusTab(t *engine.Tab) {
	a.mu.Lock()
	id := ""
	for k, e := range a.pages {
		if e.t == t {
			id = k
		}
	}
	a.mu.Unlock()
	if id != "" {
		a.post(func() { a.activateTab(id) })
	}
}

func (a *App) pwaTabState(t *engine.Tab) (visible, focused bool) {
	shown, _ := a.shownTab.Load().(string)
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, e := range a.pages {
		if e.t == t {
			vis := k == shown
			return vis, vis
		}
	}
	return false, false
}

func (a *App) onBadge(origin string, b engine.Badge) {
	if a.pw == nil {
		return
	}
	a.pw.mu.Lock()
	if b.Set {
		a.pw.badges[origin] = b
	} else {
		delete(a.pw.badges, origin)
	}
	a.pw.mu.Unlock()
	a.post(a.markState)
}

// Badges returns the app badges sites have set (origin to badge), for shells that can show them.
func (a *App) Badges() map[string]engine.Badge {
	if a.pw == nil {
		return nil
	}
	return a.pw.ctx.PWA().Badges()
}

// SetOnline tells the background features whether the network is reachable (pending background syncs fire on
// reconnecting; pages get online and offline events).
func (a *App) SetOnline(on bool) {
	if a.pw != nil {
		a.pw.ctx.PWA().SetOnline(on)
	}
}

// onPWASetting reacts to the settings that change what the engine does.
func (a *App) onPWASetting(key string) {
	if a.pw == nil {
		return
	}
	switch key {
	case "push.service":
		a.pw.ctx.PWA().PushServiceChanged()
	case "notifications.default":
		a.pw.ctx.PWA().PermissionChanged("")
	}
}

// clearSiteNotifications forgets everything the engine holds for an origin (Clear browsing data).
func (a *App) clearSiteNotifications(origin string) {
	if a.pw != nil {
		a.pw.ctx.PWA().ClearOrigin(origin)
	}
}
