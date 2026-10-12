package app

import (
	"github.com/sweefbrowser/sweef/internal/app/privacy"
	"github.com/sweefbrowser/sweef/internal/app/store"
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

func (a *App) defaultVisibility() store.Visibility {
	return store.Visibility(a.prof.Settings.Str("agent.visibilityDefault"))
}
