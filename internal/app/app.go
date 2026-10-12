// Package app is the browser core behind `sweef gui`: it owns the engine
// tabs (one privileged chrome tab plus one tab per page), the state model,
// the stores, the compositor and the input router, and talks to a native
// window only through shell.Window.
//
// Threading: a single UI loop goroutine owns all mutable app state and runs
// every handler. Each engine tab has a worker goroutine that executes its
// (blocking) engine calls in order; results come back to the loop through
// App.post. The compositor and the model are internally synchronised.
package app

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/compositor"
	"github.com/sweefbrowser/sweef/internal/app/omnibox"
	"github.com/sweefbrowser/sweef/internal/app/privacy"
	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/shell"
)

// Config configures New.
type Config struct {
	// Shell is the native (or headless) window. Required.
	Shell shell.Window
	// ChromeFS holds the chrome UI assets: "chrome/index.html", internal
	// pages such as "newtab/index.html", "settings/index.html". Pass ui.FS.
	// When nil, a tiny built-in placeholder chrome is used.
	ChromeFS fs.FS
	// ChromeEntry is the chrome page inside ChromeFS (default chrome/index.html).
	ChromeEntry string
	// OverlayEntry is the optional transparent overlay chrome page.
	OverlayEntry string
	// Profile is a ready profile. Otherwise ProfileDir is opened (default
	// per-OS dir), or an in-memory profile when Incognito is set.
	Profile    *store.Profile
	ProfileDir string
	Incognito  bool
	// Agent is the agent host (nil = the sidebar reports "not configured").
	Agent AgentHost
	// InitialURLs are opened instead of restoring the session.
	InitialURLs []string
	// NoRestore skips session restore.
	NoRestore bool
	// Engine lets a caller share an engine; the app closes it only if it made it.
	Engine *engine.Engine
	// UserAgent overrides the default UA.
	UserAgent string
	// DownloadsDir overrides the downloads location.
	DownloadsDir string
	// Version is shown on the About page (state.version).
	Version string
	// NoSmoothScroll turns off wheel easing.
	NoSmoothScroll bool
	// Logf receives diagnostics (default: discard).
	Logf func(format string, args ...any)
}

// App is the browser.
type App struct {
	cfg  Config
	win  shell.Window
	eng  *engine.Engine
	ownE bool

	prof     *store.Profile
	incog    *store.Profile // lazily created in-memory profile for incognito tabs
	model    *state.Model
	winID    int
	comp     *compositor.Compositor
	scroll   *compositor.Scroller
	priv     *privacy.Engine
	agent    AgentHost // the gated host used for Send
	agentRaw AgentHost // the host as given: optional capabilities live there

	inbox   chan func()
	quit    chan struct{}
	done    chan struct{}
	pending atomic.Int64 // queued-but-unfinished work (for Idle)
	wg      sync.WaitGroup

	mu       sync.Mutex // guards pages, incogCtx, chrome
	pages    map[string]*etab
	chrome   *etab
	overlay  *etab
	incogCtx *engine.BrowserContext

	// ---- UI-loop-only state ----
	scale               float64
	wLog, hLog          int
	vpLog               rect // viewport hole in logical px as reported by the chrome page
	vpMu                sync.Mutex
	vpShadow            rect // copy of vpLog readable from workers
	haveLayout          bool
	focus               string // "chrome" | "page"
	lastState           map[string]any
	stateDirty          bool
	focusOmniAfterState bool
	animT               *time.Timer
	animOn              atomic.Bool
	lastTick            time.Time
	scrollbarT          time.Time // last scroll activity, for the fading scrollbar
	sbAlpha             uint8
	mouse               mouseState
	cursorShown         shell.Cursor
	cursorBusy          atomic.Bool
	holeBusy            atomic.Bool // the chrome has a scrim/modal over the viewport (set by the chrome worker)
	chromeLoaded        atomic.Bool
	startedAt           time.Time
	firstFrame          atomic.Int64 // unix nanos of the first Present
	lastLayoutAt        time.Time
	pendingAsk          map[string]pendingAgent
	favicons            *faviconCache
	runSeq              int
	dlSeen              map[string]int
	closing             atomic.Bool
	closeOnce           sync.Once
	runErr              error
	stats               runStats
	pageTabHint         string // tab id whose texture is in the compositor
	paintedDocH         int
	ovlOpen             bool
	shownTab            atomic.Value // id of the tab whose texture is on screen
	scaleBitsV          atomic.Uint64
	zoomBitsV           atomic.Uint64
	omniSeq             int
	uiPages             map[*etab]bool // page tabs showing sweef:// pages that said ui.ready
	compatDismissed     map[string]bool
	compatErrs          map[string]int
	pageTypes           pageTypeCache
	agentRun            agentRunState
	lastActivity        atomic.Int64
	trimArmed           atomic.Bool
	tabSeq              int
	fullscreen          bool
	ovl                 overlayState
}

type rect struct{ X, Y, W, H float64 }

func (r rect) empty() bool { return r.W <= 0 || r.H <= 0 }

type pendingAgent struct {
	req  agentRequest
	mode string
	tab  string
}

const defaultChromeEntry = "chrome/index.html"

// New builds the app. The window must already exist. Engine start-up and the
// chrome page load happen asynchronously on worker goroutines; Run drives
// the UI loop and draws the first frame as soon as the chrome is painted.
func New(cfg Config) (*App, error) {
	if cfg.Shell == nil {
		return nil, errors.New("app: Config.Shell is required")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.ChromeEntry == "" {
		cfg.ChromeEntry = defaultChromeEntry
	}
	a := &App{cfg: cfg, win: cfg.Shell, startedAt: time.Now(),
		inbox: make(chan func(), 4096), quit: make(chan struct{}), done: make(chan struct{}),
		pages: map[string]*etab{}, uiPages: map[*etab]bool{}, compatDismissed: map[string]bool{}, compatErrs: map[string]int{}, pendingAsk: map[string]pendingAgent{}, focus: "page", dlSeen: map[string]int{}}

	// profile
	switch {
	case cfg.Profile != nil:
		a.prof = cfg.Profile
	case cfg.Incognito:
		a.prof = store.OpenIncognito()
	default:
		p, err := store.OpenProfile(cfg.ProfileDir)
		if err != nil {
			return nil, fmt.Errorf("app: profile: %w", err)
		}
		a.prof = p
	}
	if cfg.DownloadsDir != "" {
		a.prof.Downloads.Dir = cfg.DownloadsDir
	}
	a.prof.Downloads.OnUpdate = func(it store.DownloadItem) {
		a.post(func() { a.broadcast(mDownloadsUpd, dlJSON(it)) })
	}
	a.prof.Sites.Default = store.Visibility(a.prof.Settings.Str("agent.visibilityDefault"))

	// window geometry
	lw, lh, sc := a.win.Size()
	if sc <= 0 {
		sc = 1
	}
	a.scale, a.wLog, a.hLog = sc, lw, lh
	a.scaleBitsV.Store(math.Float64bits(sc))
	a.zoomBitsV.Store(math.Float64bits(float64(a.prof.Settings.Int("zoom.default")) / 100))
	a.model = state.New()
	a.winID = a.model.AddWindow(lw, lh, sc, cfg.Incognito)
	a.model.Bookmarked = a.prof.Bookmarks.IsBookmarked
	a.syncSettingsToModel()
	a.comp = compositor.New(dev(lw, sc), dev(lh, sc), sc)
	a.scroll = compositor.NewScroller()
	a.scroll.Smooth = !cfg.NoSmoothScroll
	a.priv = privacy.NewEngine(a.privacySettings())
	a.priv.Exceptions = func(origin string) bool { return a.prof.Sites.Rule(origin).NoBlock }
	a.prof.Settings.OnChange(func(k string, v any) { a.post(func() { a.onSetting(k, v) }) })
	a.favicons = newFaviconCache(a.prof)
	if cfg.Agent != nil {
		a.agentRaw = cfg.Agent
		a.agent = &Gate{Inner: cfg.Agent, Vis: func(o string) store.Visibility { return a.prof.Sites.Agent(o) }}
	}

	// engine
	if cfg.Engine != nil {
		a.eng = cfg.Engine
	} else {
		o := engine.DefaultOptions()
		o.MaxRSSMiB = 4096
		o.ViewportWidth, o.ViewportHeight = lw, lh
		o.SettleMs = 300
		o.DownloadsDir = cfg.DownloadsDir
		o.DefaultUserAgent = userAgent(cfg.UserAgent)
		if a.prof.Dir != "" {
			o.CacheDir = filepath.Join(a.prof.Dir, "cache")
		} else {
			o.CacheDir = "off"
		}
		a.eng = engine.New(o)
		a.ownE = true
	}
	if at, ok := cfg.Agent.(AgentAttacher); ok {
		at.AttachApp(a)
	}
	a.startChrome()
	return a, nil
}

func userAgent(custom string) string {
	if custom != "" {
		return custom
	}
	plat := "X11; Linux x86_64"
	switch runtime.GOOS {
	case "windows":
		plat = "Windows NT 10.0; Win64; x64"
	case "darwin":
		plat = "Macintosh; Intel Mac OS X 10_15_7"
	}
	return "Mozilla/5.0 (" + plat + ") AppleWebKit/537.36 (KHTML, like Gecko) Safari/537.36 Sweef/0.1"
}

func dev(v int, scale float64) int { return int(float64(v)*scale + 0.5) }

// Model exposes the state model (read-mostly; tests and the PM use it).
func (a *App) Model() *state.Model { return a.model }

// Profile returns the active profile.
func (a *App) Profile() *store.Profile { return a.prof }

// Engine returns the engine.
func (a *App) Engine() *engine.Engine { return a.eng }

// Compositor returns the compositor (stats, tests).
func (a *App) Compositor() *compositor.Compositor { return a.comp }

// WindowID is the id of the single window.
func (a *App) WindowID() int { return a.winID }

// post queues f on the UI loop. It never blocks the caller for long and is
// safe from any goroutine.
func (a *App) post(f func()) {
	a.pending.Add(1)
	select {
	case a.inbox <- f:
	case <-a.quit:
		a.pending.Add(-1)
	}
}

// Idle reports whether nothing is queued, loading or animating.
func (a *App) Idle() bool {
	if a.pending.Load() != 0 || a.animOn.Load() {
		return false
	}
	for _, id := range a.model.Order(a.winID) {
		if t, ok := a.model.Tab(id); ok && t.Loading {
			return false
		}
	}
	return a.chromeLoaded.Load()
}

// WaitIdle blocks until the app has been Idle continuously for quiet, or
// timeout passes.
func (a *App) WaitIdle(quiet, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	var since time.Time
	for time.Now().Before(deadline) {
		if a.Idle() {
			if since.IsZero() {
				since = time.Now()
			} else if time.Since(since) >= quiet {
				return true
			}
		} else {
			since = time.Time{}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// Run drives the UI loop until the window closes, ctx is cancelled or Close
// is called.
func (a *App) Run(ctx context.Context) error {
	defer close(a.done)
	a.post(a.start)
	a.armDiscard()
	for {
		select {
		case ev, ok := <-a.win.Events():
			if !ok {
				return a.shutdown()
			}
			quit := false
			a.safely(func() { quit = a.handleEvent(ev) })
			if quit {
				return a.shutdown()
			}
		case f := <-a.inbox:
			a.safely(f)
			a.pending.Add(-1)
		case <-a.animC():
			a.tick()
		case <-ctx.Done():
			return a.shutdown()
		case <-a.quit:
			return a.shutdown()
		}
		a.flush()
	}
}

// safely runs a posted function; a panic in one handler is reported, not fatal.
func (a *App) safely(f func()) {
	defer func() {
		if r := recover(); r != nil {
			a.logf("panic in UI loop: %v", r)
			if PanicHook != nil {
				PanicHook(r)
			}
		}
	}()
	f()
}

func (a *App) animC() <-chan time.Time {
	if a.animT == nil {
		return nil
	}
	return a.animT.C
}

// flush runs after every loop iteration: push pending state to the chrome
// page, then compose and present whatever changed.
func (a *App) flush() {
	if a.stateDirty {
		a.pushState()
	}
	a.present()
	a.armTrim()
}

// armTrim returns freed memory to the OS once the browser has been quiet for a
// moment: painting allocates large short-lived surfaces, and without this the
// resident size stays at the busy peak. One timer, re-armed by activity, no
// periodic wakeups.
func (a *App) armTrim() {
	a.lastActivity.Store(time.Now().UnixNano())
	if !a.trimArmed.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(trimDelay, a.trimIfQuiet)
}

const trimDelay = time.Second

func (a *App) trimIfQuiet() {
	a.trimArmed.Store(false)
	if a.closing.Load() {
		return
	}
	if idle := time.Since(time.Unix(0, a.lastActivity.Load())); idle < trimDelay-50*time.Millisecond {
		a.trimArmed.Store(true)
		time.AfterFunc(trimDelay-idle, a.trimIfQuiet)
		return
	}
	if a.pending.Load() == 0 && !a.animOn.Load() {
		debug.FreeOSMemory() // runs a GC, then scavenges
	}
}

func (a *App) present() {
	if !a.comp.Dirty() {
		return
	}
	frame, dmg := a.comp.Compose()
	if dmg.Empty() {
		return
	}
	a.win.Present(frame, dmg)
	a.stats.notePresent(dmg)
	if a.firstFrame.Load() == 0 && a.stats.chromePaints.Load() > 0 {
		// the first frame that actually contains the chrome, not the blank initial one
		a.firstFrame.Store(time.Now().UnixNano())
	}
}

// FirstFrameAfter returns the time from New to the first presented frame
// (0 until it happens).
func (a *App) FirstFrameAfter() time.Duration {
	ns := a.firstFrame.Load()
	if ns == 0 {
		return 0
	}
	return time.Unix(0, ns).Sub(a.startedAt)
}

// Close stops the app and releases the engine and profile. It is safe to
// call from any goroutine, more than once.
func (a *App) Close() {
	a.requestClose()
	select {
	case <-a.done:
	case <-time.After(10 * time.Second):
	}
}

// requestClose starts shutdown without waiting (safe from the UI loop).
func (a *App) requestClose() {
	a.closeOnce.Do(func() {
		a.closing.Store(true)
		close(a.quit)
	})
}

func (a *App) shutdown() error {
	a.closing.Store(true)
	if c, ok := a.agentRaw.(interface{ Close() }); ok {
		c.Close() // cancel the agent run before the tabs it drives go away
	}
	a.prof.Session.Stop() // flush the debounced save first, then mark the exit clean
	a.saveSession(true)
	if a.animT != nil {
		a.animT.Stop()
	}
	a.mu.Lock()
	var all []*etab
	for _, e := range a.pages {
		all = append(all, e)
	}
	if a.chrome != nil {
		all = append(all, a.chrome)
	}
	if a.overlay != nil {
		all = append(all, a.overlay)
	}
	a.pages = map[string]*etab{}
	a.mu.Unlock()
	if a.ownE {
		a.eng.Bus() // keep reference alive until workers stop
	}
	var wg sync.WaitGroup
	for _, e := range all {
		wg.Add(1)
		go func(e *etab) { defer wg.Done(); e.w.stop() }(e)
	}
	waitTimeout(&wg, 5*time.Second)
	a.saveCookies()
	if a.ownE {
		_ = a.eng.Close()
	}
	a.wg.Wait()
	if a.incog != nil {
		_ = a.incog.Close()
	}
	_ = a.prof.Close()
	a.win.Close()
	return nil
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	c := make(chan struct{})
	go func() { wg.Wait(); close(c) }()
	select {
	case <-c:
	case <-time.After(d):
	}
}

// start runs once on the UI loop: open the first tabs.
func (a *App) start() {
	if a.restoreOrOpen() {
		return
	}
}

// profileFor returns the profile a tab's history/downloads belong to.
func (a *App) profileFor(incognito bool) *store.Profile {
	if !incognito {
		return a.prof
	}
	if a.incog == nil {
		a.incog = store.OpenIncognito()
		a.incog.Downloads.Dir = a.prof.Downloads.Dir
		a.incog.Downloads.OnUpdate = a.prof.Downloads.OnUpdate
	}
	return a.incog
}

func (a *App) logf(f string, args ...any) { a.cfg.Logf(f, args...) }

func (a *App) saveCookies() {
	if a.prof.Incognito || a.prof.Dir == "" || !a.ownE {
		return
	}
	p := filepath.Join(a.prof.Dir, "cookies.json")
	if err := a.eng.DefaultContext().SaveState(p); err == nil {
		_ = os.Chmod(p, 0o600)
	}
}

func (a *App) loadCookies() {
	if a.prof.Incognito || a.prof.Dir == "" {
		return
	}
	p := filepath.Join(a.prof.Dir, "cookies.json")
	if _, err := os.Stat(p); err == nil {
		_ = a.eng.DefaultContext().LoadState(p)
	}
}

func (a *App) engineSearch() omnibox.SearchEngine {
	return omnibox.EngineByID(a.prof.Settings.Str("search.engine"))
}

// logicalToDevice converts logical coordinates to device pixels.
func (a *App) l2d(v float64) int { return int(v*a.scale + 0.5) }

func (a *App) vpDevice() image.Rectangle {
	return image.Rect(a.l2d(a.vpLog.X), a.l2d(a.vpLog.Y), a.l2d(a.vpLog.X+a.vpLog.W), a.l2d(a.vpLog.Y+a.vpLog.H))
}
