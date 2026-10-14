package agentbridge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/agent"
	"github.com/sweefbrowser/sweef/internal/app"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
)

const hiddenText = "[hidden by user]"

const askModeMsg = "Ask mode only reads pages. Tell the user to switch the sidebar to Agent mode if they want you to click, type or navigate."

var errHidden = errors.New(hiddenText + " The user has not allowed Sweef to see this page.")

// Backend implements agent.Backend over the live tabs of an app.App. Actions
// run on the engine tab the user is looking at (through the tab's worker), so
// the compositor repaints after every step and the user watches the agent
// work. Every read and action is checked against the user's per-origin
// visibility rule at the moment it happens (not only when the run started),
// and incognito tabs are invisible to the agent unless the whole window is
// private.
//
// The browser verbs themselves (click, type, wait, expect, snapshot ...) are
// agent.RPCBackend's: this type adds tab-id mapping, the visibility gate,
// app-level navigation (HTTPS upgrade, tracker shield, history), visible
// scrolling, the cursor overlay and the confirm cards.
type Backend struct {
	h   *Host
	a   *app.App
	rpc *agent.RPCBackend

	mu     sync.Mutex
	active string // the agent's default tab (state id)
}

var _ agent.Backend = (*Backend)(nil)

func newBackend(h *Host, a *app.App) *Backend {
	b := &Backend{h: h, a: a, rpc: agent.NewRPCBackend(a.Engine())}
	// a frame is read (snapshot, refs) only when its own site is one the person allowed, not just the page embedding it;
	// about:blank / srcdoc / data: frames have no site of their own and follow the page
	a.Engine().SetFrameReadFilter(func(u string) bool { return !isWeb(u) || b.allowedURL(u) })
	return b
}

// ---- tab resolution -------------------------------------------------------------------

func (b *Backend) setActive(id string) {
	b.mu.Lock()
	b.active = id
	b.mu.Unlock()
}

func (b *Backend) activeID() string {
	b.mu.Lock()
	id := b.active
	b.mu.Unlock()
	if id != "" {
		if t, ok := b.a.AgentTab(id); ok && b.tabVisible(t) {
			return id
		}
	}
	for _, t := range b.a.AgentTabs() {
		if t.Active && b.tabVisible(t) {
			return t.ID
		}
	}
	return ""
}

func (b *Backend) resolve(id string) string {
	if id != "" {
		return id
	}
	return b.activeID()
}

// tabVisible: incognito tabs of a normal window do not exist for the agent.
func (b *Backend) tabVisible(t app.AgentTab) bool {
	return !t.Private || b.a.AgentWindowPrivate()
}

// engineURL is the engine's current URL of a tab (a thread-safe getter).
func (b *Backend) engineTab(id string) *engine.Tab {
	eid := b.a.AgentEngineID(id)
	if eid == "" {
		return nil
	}
	t, err := b.a.Engine().Tab(eid)
	if err != nil {
		return nil
	}
	return t
}

func (b *Backend) curURL(id string) string {
	if t := b.engineTab(id); t != nil {
		return t.URL()
	}
	if t, ok := b.a.AgentTab(id); ok {
		return t.URL
	}
	return ""
}

func isWeb(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// allowedURL: the user allowed the agent to read raw's origin.
func (b *Backend) allowedURL(raw string) bool {
	return isWeb(raw) && b.a.AgentSiteRule(raw) == store.VisAllowed
}

// readable: tab t showing raw may be read and driven.
func (b *Backend) readable(t app.AgentTab, raw string) bool {
	return b.tabVisible(t) && b.allowedURL(raw)
}

func (b *Backend) syncVis(raw string) {
	if ag := b.h.agentNow(); ag != nil {
		if o := store.OriginOf(raw); o != "" && isWeb(raw) {
			ag.SetPageVisibility(o, b.a.AgentSiteRule(raw) == store.VisAllowed)
		}
	}
}

// ensureReadable resolves a tab and, when its origin is still undecided
// ("ask"), puts the question to the user and waits. It returns the state tab.
func (b *Backend) ensureReadable(ctx context.Context, tabID string) (app.AgentTab, string, error) {
	id := b.resolve(tabID)
	t, ok := b.a.AgentTab(id)
	if !ok || !b.tabVisible(t) {
		return app.AgentTab{}, "", errors.New("no such tab")
	}
	cur := b.curURL(id)
	if !isWeb(cur) {
		return t, cur, errors.New("this tab shows a browser page that you cannot use")
	}
	if err := b.ensureOrigin(ctx, cur); err != nil {
		return t, cur, err
	}
	return t, cur, nil
}

// ensureOrigin makes sure the user decided about raw's origin (asking if not).
func (b *Backend) ensureOrigin(ctx context.Context, raw string) error {
	switch b.a.AgentSiteRule(raw) {
	case store.VisAllowed:
		return nil
	case store.VisBlocked:
		return errHidden
	}
	origin := store.OriginOf(raw)
	if origin == "" {
		return errHidden
	}
	r := b.h.current()
	if r == nil {
		return errHidden
	}
	if !r.askVisibility(ctx, b, origin) {
		return errHidden
	}
	return nil
}

// ---- agent.Backend ----------------------------------------------------------------------

// Tabs lists the tabs the agent may know about. Pages it may not read are
// listed by id only; incognito tabs of a normal window are omitted.
func (b *Backend) Tabs() []agent.TabInfo {
	act := b.activeID()
	var out []agent.TabInfo
	for _, t := range b.a.AgentTabs() {
		if !b.tabVisible(t) {
			continue
		}
		u := b.curURL(t.ID)
		ti := agent.TabInfo{ID: t.ID, Active: t.ID == act}
		switch {
		case !isWeb(u):
			ti.URL = u // browser pages are described as such by the agent
		case b.readable(t, u):
			b.syncVis(u)
			ti.URL, ti.Title = u, t.Title
		default:
			b.syncVis(u)
			ti.Title = hiddenText
		}
		out = append(out, ti)
	}
	return out
}

// URL returns the tab's real URL (the agent core needs it to decide what it
// may show the model; it syncs the user's rule for that origin first).
func (b *Backend) URL(tabID string) string {
	id := b.resolve(tabID)
	t, ok := b.a.AgentTab(id)
	if !ok || !b.tabVisible(t) {
		return ""
	}
	u := b.curURL(id)
	b.syncVis(u)
	return u
}

// Title returns the title of a readable tab ("" otherwise).
func (b *Backend) Title(tabID string) string {
	id := b.resolve(tabID)
	t, ok := b.a.AgentTab(id)
	if !ok {
		return ""
	}
	if et := b.engineTab(id); et != nil {
		if u := et.URL(); b.readable(t, u) {
			return et.Title()
		}
	}
	return ""
}

// Snapshot returns the accessibility snapshot of a readable tab.
func (b *Backend) Snapshot(tabID string, delta bool) (string, error) {
	ctx := b.h.runCtx()
	t, _, err := b.ensureReadable(ctx, tabID)
	if err != nil {
		return "", err
	}
	var yaml, title, final string
	err = b.a.AgentRunOnTab(t.ID, func(et *engine.Tab) error {
		if !b.allowedURL(et.URL()) {
			return errHidden
		}
		y, err := b.rpc.Snapshot(et.ID, false)
		if err != nil {
			return err
		}
		final, title = et.URL(), et.Title()
		if !b.allowedURL(final) { // a redirect landed somewhere hidden while we looked
			return errHidden
		}
		yaml = y
		return nil
	})
	if err != nil {
		return "", err
	}
	b.h.noteSource(final, title)
	return yaml, nil
}

// Screenshot returns a PNG of a readable tab.
func (b *Backend) Screenshot(tabID string) ([]byte, error) {
	ctx := b.h.runCtx()
	t, _, err := b.ensureReadable(ctx, tabID)
	if err != nil {
		return nil, err
	}
	r := b.h.current()
	if r != nil {
		r.tr.actStart()
	}
	var png []byte
	t0 := time.Now()
	err = b.a.AgentRunOnTab(t.ID, func(et *engine.Tab) error {
		if !b.allowedURL(et.URL()) {
			return errHidden
		}
		p, err := b.rpc.Screenshot(et.ID)
		png = p
		return err
	})
	if r != nil {
		r.tr.actDone(err == nil, time.Since(t0).Milliseconds(), errText(err))
	}
	return png, err
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// OpenTab opens a new foreground tab.
func (b *Backend) OpenTab(raw string) (agent.TabInfo, error) {
	ctx := b.h.runCtx()
	if r := b.h.current(); r != nil && r.mode == modeAsk {
		return agent.TabInfo{}, errors.New(askModeMsg)
	}
	if err := b.ensureOrigin(ctx, raw); err != nil {
		return agent.TabInfo{}, err
	}
	id, lerr := b.a.AgentOpenTab(ctx, raw)
	if id == "" {
		return agent.TabInfo{}, lerr
	}
	b.setActive(id)
	ti := agent.TabInfo{ID: id, Active: true}
	if u := b.curURL(id); b.allowedURL(u) {
		ti.URL = u
		if t, ok := b.a.AgentTab(id); ok {
			ti.Title = t.Title
		}
	}
	return ti, lerr
}

// SwitchTab brings a tab to the front (the user sees it).
func (b *Backend) SwitchTab(id string) error {
	t, ok := b.a.AgentTab(id)
	if !ok || !b.tabVisible(t) {
		return errors.New("no such tab")
	}
	if err := b.a.AgentActivateTab(b.h.runCtx(), id); err != nil {
		return err
	}
	b.setActive(id)
	return nil
}

// CloseTab closes a tab.
func (b *Backend) CloseTab(id string) error {
	t, ok := b.a.AgentTab(id)
	if !ok || !b.tabVisible(t) {
		return errors.New("no such tab")
	}
	err := b.a.AgentCloseTab(b.h.runCtx(), id)
	b.mu.Lock()
	if b.active == id {
		b.active = ""
	}
	b.mu.Unlock()
	return err
}

// ---- Act ----------------------------------------------------------------------------------------

func readOnlyOp(op string) bool {
	switch op {
	case agent.OpExtract, agent.OpExpect, agent.OpWait, agent.OpScroll, agent.OpScreenshot:
		return true
	}
	return false
}

func pointerOp(op string) bool {
	switch op {
	case agent.OpClick, agent.OpHover, agent.OpType, agent.OpSelect, agent.OpPress:
		return true
	}
	return false
}

// Act runs steps one at a time. Each step is its own worker operation, so the
// page repaints between steps. It stops at the first failing non-optional step.
func (b *Backend) Act(ctx context.Context, steps []agent.Step) (agent.Result, error) {
	t0 := time.Now()
	res := agent.Result{Steps: make([]agent.StepResult, len(steps)), OK: true}
	r := b.h.current()
	failed := false
	lastTab := ""
	for i, s := range steps {
		sr := &res.Steps[i]
		sr.Index, sr.Op = i+1, s.Op
		if failed {
			sr.Error = "not run (an earlier step failed)"
			continue
		}
		if err := ctx.Err(); err != nil {
			sr.Error = err.Error()
			failed = true
			res.OK = false
			if r != nil {
				r.tr.actSkip(1, "stopped")
			}
			continue
		}
		if r != nil {
			r.tr.actStart()
		}
		st := time.Now()
		out, tabID, err := b.step(ctx, r, s)
		if tabID != "" {
			lastTab = tabID
		}
		sr.Ms = time.Since(st).Milliseconds()
		if err != nil {
			sr.Error = redact(err.Error(), b.h.keyNow())
		} else {
			sr.OK, sr.Output = true, out
		}
		if r != nil {
			r.tr.actDone(err == nil, sr.Ms, sr.Error)
		}
		if err != nil && !s.Optional {
			failed = true
			res.OK = false
		}
	}
	if failed && r != nil {
		// rows of steps that never ran
		n := 0
		for _, sr := range res.Steps {
			if !sr.OK && sr.Error == "not run (an earlier step failed)" {
				n++
			}
		}
		r.tr.actSkip(n, "not run")
	}
	b.a.AgentCursor(false, 0, 0, "", false)
	if lastTab != "" {
		if t, ok := b.a.AgentTab(lastTab); ok {
			if u := b.curURL(lastTab); b.readable(t, u) {
				res.URL, res.Title = u, b.Title(lastTab)
			}
		}
	}
	res.TotalMs = time.Since(t0).Milliseconds()
	return res, nil
}

// step runs one step; it returns the extracted text (if any) and the state tab.
func (b *Backend) step(ctx context.Context, r *run, s agent.Step) (string, string, error) {
	id := b.resolve(s.Tab)
	t, ok := b.a.AgentTab(id)
	if !ok || !b.tabVisible(t) {
		return "", "", errors.New("no such tab")
	}
	if r != nil && r.mode == modeAsk && !readOnlyOp(s.Op) {
		return "", id, errors.New(askModeMsg)
	}
	switch s.Op {
	case agent.OpNavigate:
		if !isWeb(s.URL) {
			return "", id, errors.New("only http(s) pages can be opened")
		}
		if err := b.ensureOrigin(ctx, s.URL); err != nil {
			return "", id, err
		}
		err := b.a.AgentNavigate(ctx, id, s.URL)
		return "", id, err
	case agent.OpBack, agent.OpForward:
		cur := b.curURL(id)
		if isWeb(cur) {
			if err := b.ensureOrigin(ctx, cur); err != nil {
				return "", id, err
			}
		}
		d := -1
		if s.Op == agent.OpForward {
			d = 1
		}
		return "", id, b.a.AgentTraverse(ctx, id, d)
	}
	// every other verb needs a readable page
	if _, _, err := b.ensureReadable(ctx, id); err != nil {
		return "", id, err
	}
	if s.Op == agent.OpClick && s.Ref != "" {
		// a link to a site the user hid is not followed (and an undecided one is asked about)
		if href := b.linkHref(id, s.Ref); href != "" && isWeb(href) {
			if err := b.ensureOrigin(ctx, href); err != nil {
				return "", id, errors.New("that link leads to a site Sweef is not allowed to see (" + hiddenText + ")")
			}
		}
	}
	var out string
	var stepErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		stepErr = b.a.AgentRunOnTab(id, func(et *engine.Tab) error {
			if !b.allowedURL(et.URL()) {
				return errHidden
			}
			if pointerOp(s.Op) && s.Ref != "" {
				b.pointAt(id, et, s.Ref, s.Op == agent.OpClick)
			}
			if s.Op == agent.OpScroll {
				b.scrollVisible(id, s)
			}
			s2 := s
			s2.Tab = et.ID
			// The run context is not passed down: Batch.run cannot be
			// interrupted, and returning early would let this worker op
			// overlap the next one on the same tab.
			rr, err := b.rpc.Act(context.Background(), []agent.Step{s2})
			if err != nil {
				return err
			}
			if len(rr.Steps) == 0 || !rr.Steps[0].OK {
				msg := "step failed"
				if len(rr.Steps) > 0 && rr.Steps[0].Error != "" {
					msg = rr.Steps[0].Error
				}
				return errors.New(msg)
			}
			out = rr.Steps[0].Output
			// the step may have navigated (link click, form submit) somewhere hidden
			after := et.URL()
			if !b.allowedURL(after) {
				out = hiddenText
			} else if s.Op == agent.OpExtract {
				b.h.noteSource(after, et.Title())
			}
			return nil
		})
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return "", id, ctx.Err()
	}
	return out, id, stepErr
}

// linkHref resolves the target of a ref'd link ("" if it is not a link).
func (b *Backend) linkHref(tab, ref string) string {
	ref = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(ref), "ref="), "@")
	var out string
	_ = b.a.AgentRunOnTab(tab, func(et *engine.Tab) error {
		info, err := et.ByRef(ref)
		if err != nil || info == nil || info.Href == "" {
			return nil
		}
		base, err := url.Parse(et.URL())
		if err != nil {
			return nil
		}
		if u, err := base.Parse(info.Href); err == nil {
			out = u.String()
		}
		return nil
	})
	return out
}

// pointAt scrolls a ref'd element into view and shows the agent cursor on it
// (worker only).
func (b *Backend) pointAt(tab string, et *engine.Tab, ref string, press bool) {
	ref = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(ref), "ref="), "@")
	rc, ok := et.RectByRef(ref)
	if !ok {
		return
	}
	x := rc[0] + rc[2]/2
	y := rc[1] + rc[3]/2
	_, vh := b.a.AgentViewport()
	z := b.a.AgentPageZoom()
	if vh <= 0 || z <= 0 {
		return
	}
	vhCSS := vh / z
	scroll := float64(b.a.AgentScrollY(tab))
	if y < scroll+24 || y > scroll+vhCSS-24 {
		b.a.AgentReveal(tab, int(y))
		scroll = y - vhCSS/3
		if scroll < 0 {
			scroll = 0
		}
	}
	b.a.AgentCursor(true, x*z, (y-scroll)*z, "", press)
}

// scrollVisible mirrors an agent scroll step onto the on-screen view (the app
// scrolls its own texture; the engine's window.scrollBy alone would be invisible).
func (b *Backend) scrollVisible(tab string, s agent.Step) {
	amt := s.Amount
	if amt <= 0 {
		amt = 600
	}
	if s.Dir == "up" {
		amt = -amt
	}
	_, vh := b.a.AgentViewport()
	z := b.a.AgentPageZoom()
	if z <= 0 {
		z = 1
	}
	target := float64(b.a.AgentScrollY(tab)) + float64(amt)
	if target < 0 {
		target = 0
	}
	// AgentReveal puts the position a third from the top
	b.a.AgentReveal(tab, int(target+vh/z/3))
}

// ---- Confirm --------------------------------------------------------------------------------------

// Confirm blocks until the user answers the confirm card (the agent core has
// already emitted the event), the run is stopped, or the timeout passes.
func (b *Backend) Confirm(ctx context.Context, req agent.ConfirmRequest) (bool, error) {
	r := b.h.current()
	if r == nil {
		return false, errors.New("no active run")
	}
	if r.mode == modeAsk {
		return false, nil // Ask mode only reads: the action is not performed
	}
	e := b.h.confirms.forWait(req)
	defer b.h.confirms.drop(e)
	r.tr.confirmWaiting(req)
	timer := time.NewTimer(b.h.opts.ConfirmTimeout)
	defer timer.Stop()
	var ok bool
	var err error
	select {
	case ok = <-e.ans:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = fmt.Errorf("no answer after %s", b.h.opts.ConfirmTimeout)
	}
	r.tr.confirmAnswered(req, ok)
	return ok, err
}
