package app

import (
	"net/url"
	"path"
	"strings"

	"github.com/sweefbrowser/sweef/internal/app/omnibox"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

// serveInternal answers sweef:// requests from the UI assets, falling back to
// small built-in pages so every internal URL works even without internal/ui.
func (a *App) serveInternal(e *etab, u *url.URL) (int, string, bool) {
	host := u.Host
	p := u.Path
	rel := host + p
	if p == "" || strings.HasSuffix(p, "/") {
		rel = path.Join(host, p, "index.html")
	}
	rel = path.Clean(rel)
	if strings.Contains(rel, "..") {
		return 400, "bad path", true
	}
	if b, err := a.asset(rel); err == nil {
		return 200, string(b), true
	}
	if b, err := a.asset(path.Join(rel, "index.html")); err == nil && path.Ext(rel) == "" {
		return 200, string(b), true
	}
	if path.Ext(rel) == "" || strings.HasSuffix(rel, "index.html") {
		if html, ok := builtinPage(host, u); ok {
			return 200, html, true
		}
	}
	return 404, "not found: " + u.String(), true
}

func builtinPage(host string, u *url.URL) (string, bool) {
	switch host {
	case "error":
		q := u.Query()
		return builtinErrorHTML(q.Get("code"), q.Get("title"), q.Get("url"), q.Get("msg")), true
	case "newtab":
		return `<!doctype html><html><head><meta charset="utf-8"><title>New Tab</title><style>
body{margin:0;font:16px sans-serif;background:#f4f6fb;color:#233}h1{margin:60px 0 0;text-align:center;font-size:40px;color:#3b4a8a}
p{text-align:center;color:#667}</style></head><body><h1>Sweef</h1><p>Type a search or address in the bar above.</p></body></html>`, true
	case "history", "bookmarks", "downloads", "settings":
		return genericInternalHTML(host), true
	}
	return "", false
}

// genericInternalHTML renders injected data as plain readable HTML.
func genericInternalHTML(host string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + host + `</title><style>
body{font:14px sans-serif;margin:24px;color:#222}h1{font-size:22px}li{margin:4px 0}code{background:#eee;padding:1px 4px}</style></head>
<body><h1>` + host + `</h1><ul id="l"></ul><script>
var d=globalThis.__sweefData||{},l=document.getElementById('l');
function add(t){var li=document.createElement('li');li.textContent=t;l.appendChild(li);}
(d.items||[]).forEach(function(i){add((i.title||i.name||i.url||i.id)+' '+(i.url||''));});
if(d.values){Object.keys(d.values).sort().forEach(function(k){add(k+' = '+JSON.stringify(d.values[k]));});}
</script></body></html>`
}

// prepareInternal injects the data an internal page renders, before it loads.
// It runs on the tab's worker. Pages learn nothing they could not already get
// by asking the browser: this is the user's own data, served to sweef:// only.
func (a *App) prepareInternal(e *etab, raw string) {
	if e.dataScript != "" {
		e.t.RemoveInitScript(e.dataScript)
		e.dataScript = ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "sweef" {
		return
	}
	data := a.internalData(e, u)
	if data == nil {
		return
	}
	e.dataScript = e.t.AddInitScript("globalThis.__sweefData = " + jsJSON(data) + ";")
}

func (a *App) internalData(e *etab, u *url.URL) map[string]any {
	p := a.profileFor(e.incog)
	switch u.Host {
	case "newtab":
		var sites []map[string]string
		for _, h := range p.History.TopSites(8) {
			sites = append(sites, map[string]string{"url": h.URL, "title": h.Title})
		}
		return map[string]any{
			"topSites":  sites,
			"usage":     a.prof.Stats.Snapshot(a.model.TabCount()),
			"bookmarks": a.prof.Bookmarks.Children(store.FolderBar),
			"engine":    a.engineSearch(),
			"engines":   omnibox.Engines,
			"incognito": e.incog,
		}
	case "history":
		return map[string]any{"items": p.History.Recent(500)}
	case "bookmarks":
		return map[string]any{"bar": a.prof.Bookmarks.Children(store.FolderBar), "other": a.prof.Bookmarks.Children(store.FolderOther),
			"items": a.prof.Bookmarks.All()}
	case "downloads":
		return map[string]any{"items": p.Downloads.List()}
	case "settings":
		return map[string]any{"values": a.prof.Settings.All(), "schema": store.Schema, "sites": a.prof.Sites.All(),
			"engines": omnibox.Engines, "usage": a.prof.Stats.Snapshot(a.model.TabCount()),
			"defaultVisibility": a.prof.Sites.Default}
	}
	return nil
}
