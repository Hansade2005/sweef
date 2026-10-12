// Package ui embeds Sweef's own browser UI: the chrome page, the overlay, the
// new tab page, onboarding and the internal pages (settings, history, ...).
// Everything is plain HTML/CSS/JS served from FS; there is no bundler and no
// network access.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var webFS embed.FS

// FS is the UI asset tree. Its root holds chrome/, newtab/, onboarding/,
// settings/, history/, downloads/, bookmarks/, error/ and shared/. The app
// serves it as sweef://chrome/..., sweef://newtab/... and so on (the first
// path segment is the page directory).
var FS fs.FS = mustSub()

func mustSub() fs.FS {
	f, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	return f
}
