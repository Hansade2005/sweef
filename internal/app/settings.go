package app

import (
	"github.com/sweefbrowser/sweef/internal/app/privacy"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
)

func (a *App) privacySettings() privacy.Settings {
	s := a.prof.Settings
	return privacy.Settings{
		BlockTrackers:          s.Str("privacy.trackers") != "off",
		HTTPSUpgrade:           s.Bool("privacy.httpsUpgrade") || s.Str("privacy.trackers") == "strict",
		BlockThirdPartyCookies: s.Str("privacy.trackers") != "off" && s.Bool("privacy.thirdPartyCookies"),
		SendGPC:                s.Bool("privacy.gpc"),
		TrimReferrer:           s.Bool("privacy.trimReferrer"),
	}
}

// applyCookiePolicy maps the "block third-party cookies" setting onto the engine: on = third-party contexts get
// cookies partitioned by top-level site (the default), off = no partitioning.
func (a *App) applyCookiePolicy(c *engine.BrowserContext) {
	if c == nil {
		return
	}
	mode := engine.CookiesAllowed
	if a.privacySettings().BlockThirdPartyCookies {
		mode = engine.CookiesPartitioned
	}
	c.SetPartitionPolicy(engine.PartitionPolicy{Cookies: mode})
}

func (a *App) defaultVisibility() store.Visibility {
	return store.Visibility(a.prof.Settings.Str("agent.visibilityDefault"))
}
