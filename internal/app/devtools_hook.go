package app

// DevToolsOpener is the hook the "Inspect" context-menu item calls. The developer tools implementation replaces it
// (assign in an init function or at start-up); the default tells the person they are not built into this binary.
var DevToolsOpener func(a *App, tabID string)

// openDevTools opens the developer tools for a tab.
func (a *App) openDevTools(tabID string) {
	if DevToolsOpener != nil {
		DevToolsOpener(a, tabID)
		return
	}
	a.sendChrome(mToast, map[string]any{"text": "Developer tools are not available in this build", "kind": "info"})
}
