package main

import "github.com/sweefbrowser/sweef/internal/ui"

// The chrome UI (tab strip, omnibox, sidebar, internal pages) is embedded in
// the binary; internal/app only sees it as an fs.FS.
func init() { guiAssets = ui.FS }
