package ui

import (
	"net/url"
	"path"
	"strings"
)

// Pages are the internal page hosts. sweef://<page> serves <page>/index.html.
var Pages = map[string]bool{"chrome": true, "newtab": true, "settings": true, "history": true, "downloads": true, "bookmarks": true, "onboarding": true, "error": true}

// Route maps a sweef:// URL to a path inside FS.
//
//	sweef://newtab                  -> newtab/index.html
//	sweef://settings/               -> settings/index.html
//	sweef://chrome/overlay.html     -> chrome/overlay.html
//	sweef://settings/settings.js    -> settings/settings.js   (page-local asset)
//	sweef://<any page>/shared/x.css -> shared/x.css           (shared assets from any page host)
//	sweef://ui/<path>               -> <path>
//
// Query strings and fragments are ignored (the page reads them itself).
// Paths are cleaned and rooted, so they cannot escape the tree. It returns
// ok=false for unknown hosts.
func Route(u *url.URL) (string, bool) {
	if u == nil || u.Scheme != "sweef" {
		return "", false
	}
	host := strings.ToLower(u.Host)
	p := path.Clean("/" + u.Path)
	switch {
	case host == "ui":
		p = strings.TrimPrefix(p, "/")
		if p == "" {
			return "", false
		}
		return p, true
	case Pages[host]:
		if p == "/" {
			return host + "/index.html", true
		}
		if strings.HasPrefix(p, "/shared/") {
			return p[1:], true
		}
		return host + p, true
	}
	return "", false
}
