package app

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/notify"
)

// onUIMessage handles sweef.post(type, payload) from the chrome page, the
// overlay or an internal page (new tab, settings, history, ...). src is the
// engine tab that sent it. Nothing here trusts web content: drainBindings only
// forwards calls made from sweef:// documents.
func (a *App) onUIMessage(src *etab, typ string, raw json.RawMessage) {
	a.stats.uiMessages.Add(1)
	senderTab := ""
	if src != nil && src.kind == "page" {
		senderTab = src.id
	}
	// an empty tab argument means the sender's own tab when a page sent it
	tabFor := func(arg string) string {
		if arg != "" {
			return arg
		}
		if senderTab != "" {
			return senderTab
		}
		return a.model.Active(a.winID)
	}
	switch typ {
	case "ui.ready":
		a.onUIReady(src, decode[struct {
			Page string `json:"page"`
		}](raw).Page)
	case "layout":
		if src.kind != "chrome" {
			return
		}
		p := decode[struct {
			Viewport rect `json:"viewport"`
		}](raw)
		a.setViewport(p.Viewport)
	case "overlay.hit":
		if src.kind != "overlay" {
			return
		}
		a.onOverlayHit(decode[struct {
			Rects []rect `json:"rects"`
		}](raw).Rects)
	case "nav.go":
		p := decode[struct {
			Tab   string `json:"tab"`
			Input string `json:"input"`
		}](raw)
		a.navigateInput(tabFor(p.Tab), p.Input)
	case "nav.back":
		a.traverse(tabFor(decode[tabArg](raw).Tab), -1)
	case "nav.forward":
		a.traverse(tabFor(decode[tabArg](raw).Tab), +1)
	case "nav.reload":
		p := decode[struct {
			Tab  string `json:"tab"`
			Hard bool   `json:"hard"`
		}](raw)
		a.reload(tabFor(p.Tab), p.Hard)
	case "nav.stop":
		a.stopLoading(tabFor(decode[tabArg](raw).Tab))
	case "tab.new":
		p := decode[struct {
			URL       string `json:"url"`
			Incognito bool   `json:"incognito"`
			After     string `json:"after"`
		}](raw)
		if p.After != "" {
			if st, ok := a.model.Tab(p.After); ok {
				a.openTab(openOpts{URL: p.URL, Incognito: p.Incognito || st.Incognito, Activate: true, After: p.After})
				return
			}
		}
		a.newTab(p.URL, p.Incognito, true)
	case "tab.duplicate":
		a.duplicateTab(tabFor(decode[idArg](raw).ID))
	case "tab.closeOthers":
		a.closeOtherTabs(tabFor(decode[idArg](raw).ID))
	case "tab.closeRight":
		a.closeTabsRight(tabFor(decode[idArg](raw).ID))
	case "tab.muteSite":
		a.muteSite(tabFor(decode[idArg](raw).ID))
	case "bookmark.update":
		p := decode[struct{ ID, Title, URL string }](raw)
		if a.prof.Bookmarks.Edit(p.ID, p.Title, p.URL) {
			a.markState()
		}
	case "newtab.site":
		p := decode[struct {
			Key   string `json:"key"`
			Hide  bool   `json:"hide"`
			Title string `json:"title"`
			URL   string `json:"url"`
		}](raw)
		a.setTopSite(p.Key, p.Hide, p.Title, p.URL)
	case "incognito.new":
		a.newTab("", true, true)
	case "tab.close":
		a.closeTab(tabFor(decode[idArg](raw).ID))
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
		a.togglePin(tabFor(decode[idArg](raw).ID))
	case "tab.mute":
		a.toggleMute(tabFor(decode[idArg](raw).ID))
	case "tab.reopen":
		a.reopenClosed()
	case "omni.input":
		a.omniInput(src, decode[omniReq](raw))
	case "bookmark.toggle":
		a.toggleBookmark(tabFor(decode[tabArg](raw).Tab))
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
			Name string `json:"name"`
		}](raw).Name)
	case "agent.send":
		a.agentSend(decode[agentRequest](raw))
	case "agent.stop":
		a.agentStop()
	case "agent.takeControl":
		a.agentTakeControl()
	case "agent.resume":
		a.agentResume()
	case "agent.confirm":
		p := decode[struct {
			RunID    string `json:"runId"`
			ID       string `json:"id"`
			Approved bool   `json:"approved"`
		}](raw)
		if strings.HasPrefix(p.ID, "visibility-") { // our own "may the agent read this site" prompt
			a.setAgentVisibility(strings.TrimPrefix(p.ID, "visibility-"), &p.Approved)
		} else if c, ok := a.agentRaw.(AgentConfirmer); ok {
			c.Confirm(p.RunID, p.ID, p.Approved)
		}
	case "agent.setKey":
		key := strings.TrimSpace(decode[struct {
			Key string `json:"key"`
		}](raw).Key)
		ok := a.agentRaw != nil && key != ""
		if ok {
			if ks, isKS := a.agentRaw.(AgentKeySetter); isKS {
				ok = ks.StoreKey(key) == nil // the host can refuse a key that cannot be right
			} else {
				a.agentRaw.SetKey(key)
			}
		}
		a.sendTo(src, "agent.keyResult", map[string]any{"ok": ok})
		a.markState()
	case "agent.clearKey":
		if c, ok := a.agentRaw.(AgentKeyClearer); ok {
			c.ClearKey()
		}
		a.markState()
	case "agent.chat.new":
		if c, ok := a.agentRaw.(AgentChats); ok {
			c.NewChat()
		}
		a.markState()
	case "agent.chat.open":
		id := decode[idArg](raw).ID
		if c, ok := a.agentRaw.(AgentChats); ok {
			if title, msgs, ok := c.OpenChat(id); ok {
				a.sendChrome("agent.chat.load", map[string]any{"id": id, "title": title, "messages": msgs})
			}
		}
	case "agent.pageVisibility":
		p := decode[struct {
			Origin  string `json:"origin"`
			Allowed *bool  `json:"allowed"`
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
		if p.Width != nil {
			_ = a.prof.Settings.Set("sidebar.width", *p.Width)
		}
		if p.Open != nil {
			_ = a.prof.Settings.Set("sidebar.open", *p.Open)
		}
	case "window.close":
		a.requestClose()
	case "window.fullscreen":
		if a.htmlFS {
			a.leaveHTMLFullscreen(true)
			return
		}
		a.fullscreen = !a.fullscreen
		a.win.SetFullscreen(a.fullscreen)
	case "window.minimize", "window.maximize", "window.drag":
		// shell.Window has no minimize/maximize/move calls; the native frame handles them
	case "find.query":
		a.find(decode[struct {
			Text string `json:"text"`
		}](raw).Text, 0)
	case "find.step":
		d := decode[struct {
			Dir int `json:"dir"`
		}](raw).Dir
		if d == 0 {
			d = 1
		}
		a.find(a.model.Find.Query, d)
	case "find.close":
		a.model.Find = state.Find{}
		a.markState()
	case "clipboard.write":
		_ = a.clip.writeNow([]engine.ClipboardEntry{{Type: "text/plain", Data: []byte(decode[struct {
			Text string `json:"text"`
		}](raw).Text)}})
	case "openExternal":
		a.openExternal(decode[struct {
			URL string `json:"url"`
		}](raw).URL)
	case "compat.dismiss":
		a.compatDismissed[decode[struct {
			Origin string `json:"origin"`
		}](raw).Origin] = true
		a.markState()
	case "cert.proceed":
		a.sendChrome(mToast, map[string]any{"text": "Continuing past certificate errors is not supported yet; use Open in system browser.", "kind": "warn"})
	case "defaultBrowser.set":
		a.sendTo(src, "defaultBrowser.result", map[string]any{"ok": false})
	case "import.start":
		a.sendTo(src, "import.unavailable", map[string]any{"source": decode[struct {
			Source string `json:"source"`
		}](raw).Source})
	case "onboarding.done":
		p := decode[struct {
			Trackers   string `json:"trackers"`
			Visibility string `json:"visibility"`
		}](raw)
		if p.Trackers != "" {
			_ = a.prof.Settings.Set("privacy.trackers", p.Trackers)
		}
		if p.Visibility != "" {
			_ = a.prof.Settings.Set("agent.visibilityDefault", p.Visibility)
		}
		a.finishOnboarding(senderTab)
	case "onboarding.skip":
		a.finishOnboarding(senderTab)
	case "data.query":
		a.dataQuery(src, decode[dataReq](raw))
	case "data.remove":
		p := decode[struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}](raw)
		a.dataRemove(p.Kind, p.ID)
	case "data.clear":
		a.clearBrowsingData()
	case "perm.answer":
		p := decode[struct {
			ID     string `json:"id"`
			Answer string `json:"answer"`
		}](raw)
		a.answerPrompt(p.ID, p.Answer)
	case "perm.set":
		p := decode[struct {
			Origin string `json:"origin"`
			Name   string `json:"name"`
			State  string `json:"state"` // granted | denied | "" (ask again)
		}](raw)
		if p.Name == "" {
			p.Name = store.PermNotifications
		}
		a.setSitePermission(p.Origin, p.Name, p.State)
	case "notify.click":
		a.onNotifyEvent(notify.Event{Kind: notify.Click, ID: decode[idArg](raw).ID})
	case "notify.action":
		p := decode[struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}](raw)
		a.onNotifyEvent(notify.Event{Kind: notify.ActionClick, ID: p.ID, Action: p.Action})
	case "notify.closed":
		p := decode[struct {
			ID   string `json:"id"`
			User bool   `json:"user"`
		}](raw)
		reason := notify.ReasonTimeout
		if p.User {
			reason = notify.ReasonUser
		}
		a.onNotifyEvent(notify.Event{Kind: notify.Closed, ID: p.ID, Reason: reason})
	case "downloads.open":
		if it, ok := a.prof.Downloads.Get(decode[idArg](raw).ID); ok {
			_ = a.win.OpenURL("file://" + it.Path)
		}
	case "downloads.show":
		if it, ok := a.prof.Downloads.Get(decode[idArg](raw).ID); ok {
			dir := it.Path
			if i := strings.LastIndexAny(dir, `/\`); i > 0 {
				dir = dir[:i]
			}
			_ = a.win.OpenURL("file://" + dir)
		}
	case "downloads.start":
		p := decode[struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		}](raw)
		a.startDownload(p.URL, p.Name)
	case "downloads.pause":
		a.prof.Downloads.Pause(decode[idArg](raw).ID)
	case "downloads.resume":
		a.prof.Downloads.Resume(decode[idArg](raw).ID)
	case "downloads.retry":
		a.retryDownload(decode[idArg](raw).ID)
	case "downloads.cancel":
		a.prof.Downloads.Cancel(decode[idArg](raw).ID)
	case "downloads.remove":
		a.prof.Downloads.Remove(decode[idArg](raw).ID)
		a.markState()
	case "downloads.clear":
		a.prof.Downloads.ClearFinished()
		a.markState()
	case "downloads.chooseFolder":
		a.sendChrome(mToast, map[string]any{"text": "Choosing a folder needs a native dialog, which the window shell does not provide yet. Edit downloads.dir in settings.", "kind": "warn"})
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
		if (strings.HasPrefix(typ, "ctx.") || typ == "edit.op" || typ == "omni.pasteAndGo") && a.onCtxMessage(src, typ, raw) {
			return
		}
		if strings.HasPrefix(typ, "devtools.") {
			a.onDevtoolsMessage(src, typ, raw)
			return
		}
		if strings.HasPrefix(typ, "data.") && a.onDataMessage(typ, raw, tabFor("")) {
			return
		}
		if (strings.HasPrefix(typ, "media.") || typ == "site.media") && a.onMediaMessage(typ, raw) {
			return
		}
		a.logf("ui: unknown message %q", typ)
	}
}

// onUIReady answers a page's ui.ready with the current state. Page tabs that
// show a sweef:// page are remembered so they get state patches too.
func (a *App) onUIReady(src *etab, page string) {
	if src == nil {
		return
	}
	if src.kind == "page" {
		a.uiPages[src] = true
	}
	a.sendTo(src, mState, state.Normalize(a.buildState()))
	if src.kind == "chrome" {
		a.stateDirty = true
	}
}

// openExternal hands a URL to the OS, for http(s) and mailto only.
func (a *App) openExternal(raw string) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
		return
	}
	if err := a.win.OpenURL(raw); err != nil {
		a.sendChrome(mToast, map[string]any{"text": "Could not open " + raw, "kind": "error"})
	}
}

// onSetting reacts to a changed setting.
func (a *App) onSetting(key string, v any) {
	switch key {
	case "privacy.trackers", "privacy.gpc", "privacy.httpsUpgrade", "privacy.thirdPartyCookies", "privacy.trimReferrer":
		a.priv.Update(a.privacySettings())
		a.applyCookiePolicy(a.eng.DefaultContext())
		a.mu.Lock()
		ic := a.incogCtx
		a.mu.Unlock()
		a.applyCookiePolicy(ic)
		hdr := a.priv.Headers()
		a.mu.Lock()
		for _, e := range a.pages {
			e.t.SetExtraHeaders(hdr)
		}
		a.mu.Unlock()
	case "agent.visibilityDefault":
		a.prof.Sites.SetDefault(a.defaultVisibility())
	case "sidebar.open":
		if b, ok := v.(bool); ok {
			a.model.Sidebar.Open = b
			a.layoutFallback()
			if w, ok := a.agentRaw.(AgentSidebarWatcher); ok && b {
				w.SidebarOpened() // lets the host warm its API connection
			}
		}
	case "zoom.default":
		a.applyZoom()
	case "data.level", "data.budget", "data.sites", "data.agentLite", "data.showMeter":
		a.onDataSetting(key)
	case "push.service", "notifications.default":
		a.onPWASetting(key)
	}
	a.markState()
}
