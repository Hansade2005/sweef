// Package agentbridge connects the Go agent loop (internal/agent) to the GUI
// (internal/app): Host implements app.AgentHost and its optional capabilities,
// and Backend implements agent.Backend over the app's live tabs.
//
// Threads: the UI loop calls Host.Send/Stop/TakeControl/... and must never be
// blocked; Send starts a goroutine per run. The agent goroutines call Backend,
// which reaches the tabs only through app.Agent* methods (each executes on the
// tab's worker or the UI loop). Events travel agent -> translator goroutine ->
// the channel returned by Send -> the app's reader -> UI loop -> chrome page.
package agentbridge

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/agent"
	"github.com/sweefbrowser/sweef/internal/app"
)

// Options configures a Host. The zero value is a production host.
type Options struct {
	// BaseURL overrides the API origin (default $SWEEF_AGENT_BASE_URL, then
	// https://api.anthropic.com). For a future rate-limited proxy.
	BaseURL string
	// Model overrides agent.DefaultModel.
	Model string
	// Thinking defaults to disabled: the sidebar favours latency.
	Thinking agent.ThinkingMode
	// KeyDir is where the API key file lives when the profile has no
	// directory of its own (private window). "" = memory only.
	KeyDir string
	// NoEnvKey ignores ANTHROPIC_API_KEY (tests).
	NoEnvKey bool
	// Env overrides os.Getenv (tests).
	Env func(string) string
	// ConfirmTimeout is how long a confirm card waits (default 2 min).
	ConfirmTimeout time.Duration
	// Tune lets the caller adjust the agent config before the agent is built
	// (tests point the HTTP client at a fake server, shorten retries ...).
	Tune func(*agent.Config)
	// Logf receives diagnostics. It never sees the key.
	Logf func(string, ...any)
}

// Host is the app.AgentHost over internal/agent.
type Host struct {
	opts  Options
	keys  *KeyStore
	chats chatStore

	ctx    context.Context
	cancel context.CancelFunc

	confirms confirmTable

	mu         sync.Mutex
	a          *app.App
	ag         *agent.Agent
	be         *Backend
	cur        *run
	needReset  bool
	lastWarm   time.Time
	keyCached  string // the key the agent was built with
	hasKey     atomic.Bool
	closed     bool
	keySrc     string
	incognitoW bool
}

var (
	_ app.AgentHost           = (*Host)(nil)
	_ app.AgentInfoer         = (*Host)(nil)
	_ app.AgentResumer        = (*Host)(nil)
	_ app.AgentConfirmer      = (*Host)(nil)
	_ app.AgentKeyClearer     = (*Host)(nil)
	_ app.AgentChats          = (*Host)(nil)
	_ app.AgentAttacher       = (*Host)(nil)
	_ app.AgentSidebarWatcher = (*Host)(nil)
)

// New returns a host. It does not touch the network or the disk until the app
// attaches and the user (or ANTHROPIC_API_KEY) supplies a key.
func New(o Options) *Host {
	if o.Env == nil {
		o.Env = os.Getenv
	}
	if o.BaseURL == "" {
		o.BaseURL = strings.TrimSpace(o.Env("SWEEF_AGENT_BASE_URL"))
	}
	if o.Thinking == "" {
		o.Thinking = agent.ThinkingDisabled
	}
	if o.ConfirmTimeout <= 0 {
		o.ConfirmTimeout = 2 * time.Minute
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	h := &Host{opts: o}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.keys = NewKeyStore(o.KeyDir, o.Env, !o.NoEnvKey)
	h.chats.setDir(o.KeyDir)
	h.refreshKey()
	return h
}

// AttachApp gives the host the app whose tabs it will drive (called by app.New).
func (h *Host) AttachApp(a *app.App) {
	h.mu.Lock()
	h.a = a
	h.be = newBackend(h, a)
	h.mu.Unlock()
	dir := a.Profile().Dir
	if dir == "" {
		dir = h.opts.KeyDir
	}
	h.keys.SetDir(dir)
	// transcripts follow the profile: nothing is written for private windows
	if a.AgentWindowPrivate() || a.Profile().Incognito {
		h.chats.setDir("")
	} else {
		h.chats.setDir(a.Profile().Dir)
	}
	h.refreshKey()
	if a.Profile().Settings.Bool("sidebar.open") {
		h.warm()
	}
}

func (h *Host) appNow() *app.App {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.a
}

func (h *Host) logf(f string, args ...any) { h.opts.Logf(f, args...) }

// refreshKey re-reads the key store and drops the agent if the key changed.
func (h *Host) refreshKey() {
	key, src := h.keys.Get()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keySrc = src
	h.hasKey.Store(key != "")
	if key != h.keyCached {
		if h.ag != nil {
			h.ag.Stop()
			h.ag.Close()
			h.ag = nil
		}
		h.keyCached = key
		h.needReset = false
	}
}

func (h *Host) keyNow() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.keyCached
}

func (h *Host) agentNow() *agent.Agent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ag
}

// agentFor returns the agent, building it on first use.
func (h *Host) agentFor() (*agent.Agent, *Backend, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, errors.New("closed")
	}
	if h.keyCached == "" {
		return nil, nil, errNoKey
	}
	if h.be == nil {
		return nil, nil, errors.New("the agent is not attached to a browser window")
	}
	if h.ag == nil {
		cfg := agent.Config{APIKey: h.keyCached, BaseURL: h.opts.BaseURL, Model: h.opts.Model, Thinking: h.opts.Thinking}
		cfg.Logf = func(f string, args ...any) { h.logf("agent: "+f, args...) }
		if h.opts.Tune != nil {
			h.opts.Tune(&cfg)
		}
		ag, err := agent.New(cfg, h.be)
		if err != nil {
			return nil, nil, errors.New(redact(err.Error(), h.keyCached))
		}
		h.ag = ag
	}
	return h.ag, h.be, nil
}

var errNoKey = errors.New("No API key yet. Add one in Settings to use the agent.")

// current returns the active run (nil when idle).
func (h *Host) current() *run {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur
}

// runCtx is the context of the active run (the host's when idle).
func (h *Host) runCtx() context.Context {
	if r := h.current(); r != nil {
		return r.ctx
	}
	return h.ctx
}

func (h *Host) noteSource(rawURL, title string) {
	if r := h.current(); r != nil && isWeb(rawURL) {
		r.addSource(rawURL, title)
	}
}

// ---- app.AgentHost ------------------------------------------------------------------------

func oneEvent(ev app.AgentEvent) <-chan app.AgentEvent {
	ch := make(chan app.AgentEvent, 1)
	ch <- ev
	close(ch)
	return ch
}

func errEvent(id, code, msg string, extra map[string]any) app.AgentEvent {
	d := map[string]any{"code": code, "message": msg}
	for k, v := range extra {
		d[k] = v
	}
	return app.AgentEvent{RunID: id, Kind: "error", Text: msg, Data: d}
}

// Send starts a run. It never blocks: every failure comes back as an error
// event on the returned channel.
func (h *Host) Send(text, mode string, pc app.PageContext) <-chan app.AgentEvent {
	ag, be, err := h.agentFor()
	if err != nil {
		code := "agent"
		if errors.Is(err, errNoKey) {
			code = "auth"
		}
		return oneEvent(errEvent("none", code, err.Error(), nil))
	}
	h.mu.Lock()
	if h.cur != nil {
		h.mu.Unlock()
		return oneEvent(errEvent("busy", "busy", "Still working on the previous request. Stop it first or wait for it to finish.", nil))
	}
	ctx, cancel := context.WithCancel(h.ctx)
	r := &run{h: h, mode: normMode(mode), ctx: ctx, cancel: cancel, text: text}
	out := make(chan app.AgentEvent, 256)
	r.tr = newTranslator(h, r, out)
	reset := h.needReset
	h.needReset = false
	h.cur = r
	h.mu.Unlock()

	be.a.AgentDataBegin() // start measuring what this run loads (shown as "Research used ..." when it ends)
	// the page the user is on is the agent's starting tab
	be.setActive("")
	if pc.TabID != "" {
		be.setActive(pc.TabID)
	}
	h.syncAllVis(be)
	h.chats.add("user", text, text)
	go h.warm()
	go h.runLoop(ag, r, reset)
	go r.tr.pump()
	return out
}

func (h *Host) syncAllVis(be *Backend) {
	for _, t := range be.a.AgentTabs() {
		be.syncVis(be.curURL(t.ID))
	}
}

func (h *Host) runLoop(ag *agent.Agent, r *run, reset bool) {
	var stats agent.RunStats
	var err error
	defer func() {
		if p := recover(); p != nil {
			err = errors.New("the agent crashed unexpectedly")
			h.logf("agent panic: %v", p)
		}
		r.tr.push(item{fin: &finish{stats: stats, err: err}})
		select { // the pump delivers the terminal event, then closes the channel
		case <-r.tr.done:
		case <-h.ctx.Done():
		case <-time.After(10 * time.Second):
		}
		if a := h.appNow(); a != nil {
			a.AgentCursor(false, 0, 0, "", false)
		}
		select {
		case <-r.tr.done: // the transcript is complete only once the pump finished
			h.chats.add("assistant", r.tr.textAll.String(), r.text)
		default:
		}
		h.mu.Lock()
		if h.cur == r {
			h.cur = nil
		}
		h.mu.Unlock()
		r.cancel()
	}()
	if reset {
		ag.Reset()
	}
	stats, err = ag.Run(r.ctx, r.text, r.tr.emit)
}

// Stop cancels the active run.
func (h *Host) Stop() {
	h.mu.Lock()
	r, ag := h.cur, h.ag
	h.mu.Unlock()
	if ag != nil {
		ag.Stop()
	}
	if r != nil {
		r.cancel()
	}
}

// TakeControl pauses the agent; Resume hands the browser back.
func (h *Host) TakeControl() {
	if ag := h.agentNow(); ag != nil && h.current() != nil {
		ag.TakeControl()
	}
}

// Resume continues after TakeControl.
func (h *Host) Resume() {
	if ag := h.agentNow(); ag != nil && h.current() != nil {
		ag.Resume("The user used the browser while you were paused.")
	}
}

// SetKey stores the key (see KeyStore) and rebuilds the agent.
func (h *Host) SetKey(key string) { _ = h.StoreKey(key) }

// StoreKey is SetKey with a result: junk is refused (and a toast says so).
func (h *Host) StoreKey(key string) error {
	key = strings.TrimSpace(key)
	if err := h.keys.Save(key); err != nil {
		if a := h.appNow(); a != nil {
			a.AgentToast(redact(err.Error(), key), "error")
		}
		return errors.New(redact(err.Error(), key))
	}
	h.Stop()
	h.refreshKey()
	if a := h.appNow(); a != nil {
		a.AgentRefresh()
	}
	h.warm()
	return nil
}

// ClearKey forgets the stored key.
func (h *Host) ClearKey() {
	h.Stop()
	h.keys.Clear()
	h.refreshKey()
	if a := h.appNow(); a != nil {
		a.AgentRefresh()
	}
}

// Info feeds state.agent (called on every state push: cheap, no I/O).
func (h *Host) Info() app.AgentInfo {
	model := h.opts.Model
	if model == "" {
		model = agent.DefaultModel
	}
	return app.AgentInfo{HasKey: h.hasKey.Load(), Model: model, Chats: h.chats.list(), Title: h.chats.title()}
}

// Confirm delivers the user's answer to a confirm card.
func (h *Host) Confirm(runID, id string, approved bool) { h.confirms.answer(id, approved) }

// NewChat starts a new conversation.
func (h *Host) NewChat() {
	h.Stop()
	h.chats.newChat()
	h.mu.Lock()
	h.needReset = true
	h.mu.Unlock()
}

// OpenChat shows an old transcript. The model starts without it.
func (h *Host) OpenChat(id string) (string, []map[string]any, bool) {
	title, msgs, ok := h.chats.open(id)
	if ok {
		h.Stop()
		h.mu.Lock()
		h.needReset = true
		h.mu.Unlock()
	}
	return title, msgs, ok
}

// SidebarOpened warms the API connection (dial + TLS + HTTP/2) so the first
// message does not pay for it.
func (h *Host) SidebarOpened() { go h.warm() }

func (h *Host) warm() {
	h.mu.Lock()
	if h.closed || h.keyCached == "" || time.Since(h.lastWarm) < 20*time.Second {
		h.mu.Unlock()
		return
	}
	h.lastWarm = time.Now()
	h.mu.Unlock()
	ag, _, err := h.agentFor()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	_, _ = ag.Prewarm(ctx)
}

// Prewarm exposes warm for tests: it reports the error of the HEAD request.
func (h *Host) Prewarm(ctx context.Context) error {
	ag, _, err := h.agentFor()
	if err != nil {
		return err
	}
	_, err = ag.Prewarm(ctx)
	return err
}

// Close cancels the run in flight and releases the connections. Safe to call
// more than once; the app calls it as the window closes.
func (h *Host) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	ag := h.ag
	h.ag = nil
	h.mu.Unlock()
	h.cancel()
	if ag != nil {
		ag.Stop()
		ag.Close()
	}
}

// KeySource reports where the key comes from ("session", "file", "env" or "").
func (h *Host) KeySource() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.keySrc
}
