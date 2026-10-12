package app

import (
	"github.com/sweefbrowser/sweef/internal/app/privacy"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

func (a *App) privacySettings() privacy.Settings {
	s := a.prof.Settings
	return privacy.Settings{
		BlockTrackers:          s.Bool("blockTrackers"),
		HTTPSUpgrade:           s.Bool("httpsUpgrade"),
		BlockThirdPartyCookies: s.Bool("blockThirdPartyCookies"),
		SendGPC:                s.Bool("sendGPC"),
		TrimReferrer:           s.Bool("trimReferrer"),
	}
}

func (a *App) defaultVisibility() store.Visibility {
	return store.Visibility(a.prof.Settings.Str("agentDefaultVisibility"))
}
