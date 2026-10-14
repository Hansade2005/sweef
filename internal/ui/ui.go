// Package ui embeds Sweef's own browser UI: the chrome page, the overlay, the
// new tab page, onboarding and the internal pages (settings, history, ...).
// Everything is plain HTML/CSS/JS served from FS; there is no bundler and no
// network access.
package ui

import (
	_ "embed"
	"io/fs"

	"github.com/sweefbrowser/sweef/internal/zpack"
)

// The assets under web/ are edited as plain files and shipped deflated: web.zar is an internal/zpack archive of that
// tree (a third of its size), inflated file by file as pages are opened. After editing anything below web/ run
//
//	go generate ./internal/ui
//
// and commit web.zar with it. TestArchiveIsCurrent fails when it is stale.
//
//go:generate go run ../../tools/zpack dir -o web.zar web
//go:embed web.zar
var webZ string

// FS is the UI asset tree. Its root holds chrome/, newtab/, onboarding/,
// settings/, history/, downloads/, bookmarks/, error/ and shared/. The app
// serves it as sweef://chrome/..., sweef://newtab/... and so on (the first
// path segment is the page directory).
var FS fs.FS = &zpack.FS{Z: webZ}
