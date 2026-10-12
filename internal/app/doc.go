// How the pieces plug together (see also the package comment in app.go and
// the message list in protocol.go):
//
//	win, _ := auto.Open(shell.Config{Title: "Sweef", Width: 1280, Height: 800}) // internal/shell/auto
//	a, err := app.New(app.Config{
//	    Shell:    win,
//	    ChromeFS: ui.FS,                 // chrome/index.html, chrome/overlay.html, newtab/index.html, ...
//	    Agent:    myAgentHost,           // an app.AgentHost adapting internal/agent (optional)
//	    ProfileDir: "",                  // "" = per-OS default
//	})
//	err = a.Run(ctx)                     // returns when the window closes
//
// internal/app never imports internal/ui, internal/agent or a native shell;
// the caller hands them in. cmd/sweef/gui.go does that wiring through three
// tiny hook files (gui_ui.go, gui_shell.go, gui_agent.go).
//
// Packages:
//
//	state       windows, tabs, history, closed-tab stack; snapshot + merge patches
//	store       history, bookmarks, downloads, settings, session, site rules, usage
//	omnibox     classify input, ranked suggestions, search engines
//	privacy     tracker blocklist, HTTPS upgrade, GPC, referrer trimming, cookie policy
//	compositor  chrome + page texture + overlay -> one RGBA frame, damage, scrolling
//	headless    a shell.Window without a display (tests, screenshots)
package app
