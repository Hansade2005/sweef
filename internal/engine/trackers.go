package engine

// trackers.go: the agent-mode tracker blocker. Analytics, ad and tag-manager scripts are invisible to an agent
// but cost it a lot: they download and run hundreds of kilobytes of JavaScript, force layouts to measure the
// page and keep the network busy, so the page settles later (angular.dev ran Google Tag Manager's 535 KB
// script during load; pypi.org's analytics script forced a 1.1 s layout). With Options.BlockTrackers a
// third-party request to a host on the built-in seed list (internal/app/privacy, the same list the desktop
// browser's blocker uses) fails as a network error, the way a content blocker makes it fail. Same-site
// requests and the top-level document are never blocked. The CLI, MCP and agent commands turn it on;
// --allow-trackers turns it off.

import (
	"errors"
	"net/url"

	"github.com/sweefbrowser/sweef/internal/app/privacy"
)

var errTrackerBlocked = errors.New("net::ERR_BLOCKED_BY_CLIENT (tracker blocked; --allow-trackers loads it)")

func (t *Tab) trackerBlocked(raw string) bool {
	if !t.eng.opts.BlockTrackers {
		return false
	}
	ru, err := url.Parse(raw)
	if err != nil || (ru.Scheme != "http" && ru.Scheme != "https" && ru.Scheme != "ws" && ru.Scheme != "wss") || ru.Hostname() == "" {
		return false
	}
	top := t
	if t.isFrame() && t.bc != nil && t.bc.top != nil {
		top = t.bc.top
	}
	if tu, err := url.Parse(top.URL()); err == nil && tu.Hostname() != "" && privacy.SameSite(tu.Hostname(), ru.Hostname()) {
		return false
	}
	_, hit := privacy.SeedList().Match(ru.Hostname())
	return hit
}
