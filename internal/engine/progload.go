package engine

// progload.go: the document lifecycle as a state machine that can be driven in
// slices.
//
//	sync scripts (document order) -> async scripts -> defer/module scripts ->
//	DOMContentLoaded -> pump until idle (or the pre-load budget) -> load ->
//	settle pump -> done
//
// Navigate runs it to the end before returning (the headless contract: the page
// is settled when the call returns). NavigateProgressive returns after the
// prepaint step and the window then calls LoadStep between paints and input,
// so a slow script, a late stylesheet-less image or the settle budget never
// holds the first frame. Both drive the same machine, so the final document is
// the same; only who waits differs.

import (
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

type loadStage int

const (
	stSync loadStage = iota // classic scripts without async/defer, in document order
	stAsync
	stDefer // defer and module scripts, after the tree is complete
	stDCL
	stPre // pump until idle or the pre-load budget
	stLoad
	stSettle // pump until idle or the settle budget
	stDone
)

// prepaintExecBudget is how long already-downloaded blocking scripts may run
// before the first frame. Browsers run them before first paint too (they block
// parsing); the budget keeps a slow one from holding the frame.
const prepaintExecBudget = 40 * time.Millisecond

type progLoad struct {
	t           *Tab
	j           *jsVM
	doc         *fnet.Doc
	url         string
	progressive bool

	syncQ, asyncQ, deferQ []fnet.ScriptEl
	stage                 loadStage
	deadline              time.Time // end of the current pump stage
	loaded                bool      // the load event has fired
	wake                  time.Duration
}

func newProgLoad(t *Tab, j *jsVM, doc *fnet.Doc, url string, progressive bool) *progLoad {
	p := &progLoad{t: t, j: j, doc: doc, url: url, progressive: progressive}
	scripts := doc.ScriptElements()
	t.tmark("scripts.start", itoa(len(scripts))+" scripts")
	for _, s := range scripts {
		if !isJavaScriptType(s.Type) || s.NoModule {
			j.executed[s.ID] = true
			continue
		}
		switch {
		case s.Module || s.Defer:
			p.deferQ = append(p.deferQ, s)
		case s.Async:
			p.asyncQ = append(p.asyncQ, s)
		default:
			p.syncQ = append(p.syncQ, s)
		}
	}
	return p
}

// stale reports whether this load no longer belongs to the tab's document (the
// tab navigated, closed, or page code replaced the document).
func (p *progLoad) stale() bool {
	t := p.t
	if t.closed.Load() || t.pl.Load() != p {
		return true
	}
	t.mu.RLock()
	gone := t.js != p.j
	t.mu.RUnlock()
	return gone
}

// scriptReady: an external script is ready when its bytes are already here (an
// inline one always is).
func (p *progLoad) scriptReady(s fnet.ScriptEl) bool {
	if s.Src == "" {
		return true
	}
	abs, err := resolveURL(p.t.URL(), s.Src)
	if err != nil {
		return true // fails fast
	}
	return p.t.resourceReady(abs, KindScript)
}

// prepaint runs the leading blocking scripts that are already downloaded, within
// a small time budget, so the first frame shows what the page's own early code
// makes of the document (class flips, inline layout switches) and not a flash
// of the raw markup. It stops at the first script that is still downloading.
func (p *progLoad) prepaint() {
	end := time.Now().Add(prepaintExecBudget)
	for p.stage == stSync && len(p.syncQ) > 0 && time.Now().Before(end) && !p.stale() {
		s := p.syncQ[0]
		if !p.scriptReady(s) {
			return
		}
		p.syncQ = p.syncQ[1:]
		p.t.runScriptEl(p.j, s)
	}
}

func (p *progLoad) preBudget() time.Duration {
	if p.progressive {
		return p.t.settleBudget() / 4
	}
	return p.t.settleBudget() / 2
}

func (p *progLoad) pumpStage(soft time.Time) (done, navigated bool) {
	nav, fin := p.j.pumpUntil(p.deadline, soft)
	return fin, nav
}

// run drives the load to its end without yielding (headless).
func (p *progLoad) run() {
	for p.step(0) {
		if p.wake > 0 {
			time.Sleep(p.wake)
		}
	}
}

// step advances the load for about slice (0 = until done) and reports whether
// work remains. p.wake is how long the caller may wait before the next step
// (a timer is pending), 0 = go on at once. A unit of work (one script, one
// timer batch) is not interrupted, so a step can overrun slice by that much.
func (p *progLoad) step(slice time.Duration) bool {
	t := p.t
	var soft time.Time
	if slice > 0 {
		soft = time.Now().Add(slice)
	}
	p.wake = 0
	for {
		if p.stale() {
			return false
		}
		switch p.stage {
		case stSync, stAsync, stDefer:
			q := &p.syncQ
			if p.stage == stAsync {
				q = &p.asyncQ
			} else if p.stage == stDefer {
				q = &p.deferQ
			}
			if len(*q) == 0 {
				p.stage++
				if p.stage == stDCL {
					t.tmark("scripts.done", "")
				}
				continue
			}
			s := (*q)[0]
			*q = (*q)[1:]
			t.runScriptEl(p.j, s)
		case stDCL:
			p.j.lifecycle("DOMContentLoaded")
			t.tmark("event.DOMContentLoaded", "")
			p.stage = stPre
			p.deadline = time.Now().Add(p.preBudget())
		case stPre:
			tp := time.Now()
			fin, nav := p.pumpStage(soft)
			if nav {
				return false // page code navigated; the new document fires its own load
			}
			if !fin {
				return p.yield()
			}
			t.tspan("pump.pre-load", "", tp)
			p.stage = stLoad
		case stLoad:
			p.j.lifecycle("load")
			t.tmark("event.load", "")
			p.loaded = true
			if p.progressive {
				t.finishLoad(p.url)
			}
			p.stage = stSettle
			p.deadline = time.Now().Add(t.settleBudget())
		case stSettle:
			tp := time.Now()
			fin, nav := p.pumpStage(soft)
			if nav {
				return false
			}
			if !fin {
				return p.yield()
			}
			t.tspan("pump.settle", "", tp)
			p.stage = stDone
		case stDone:
			if !p.progressive {
				t.finishLoad(p.url)
			}
			t.tmark("load.done", "")
			t.pl.CompareAndSwap(p, nil)
			return false
		}
		if !soft.IsZero() && !time.Now().Before(soft) {
			return true
		}
	}
}

func (p *progLoad) yield() bool {
	p.wake = time.Duration(p.j.wakeMs * float64(time.Millisecond))
	return true
}

// ---- Tab API ----------------------------------------------------------------------

// LoadPending reports whether a progressive load still has work to do.
func (t *Tab) LoadPending() bool { return t.pl.Load() != nil }

// LoadLoaded reports whether the current document's load event has fired (true
// also when no progressive load is pending).
func (t *Tab) LoadLoaded() bool {
	p := t.pl.Load()
	return p == nil || p.loaded
}

// LoadStep advances a pending progressive load for about slice. more is false
// when nothing is pending any more; wait is how long the caller may leave the
// tab alone before the next step (a timer is not due yet), 0 = step again now.
func (t *Tab) LoadStep(slice time.Duration) (more bool, wait time.Duration) {
	t.plMu.Lock()
	defer t.plMu.Unlock()
	p := t.pl.Load()
	if p == nil {
		return false, 0
	}
	p.step(slice)
	np := t.pl.Load()
	if np == p && p.stale() {
		t.pl.CompareAndSwap(p, nil) // the tab closed under it
		np = nil
	}
	// Page code that navigated during the step started a load of its own: still pending.
	if np == p {
		wait = p.wake
	}
	return np != nil, wait
}

// StopLoading abandons the pending part of a progressive load (the stop button):
// what has run stays, the rest of the scripts never run.
func (t *Tab) StopLoading() { t.pl.Store(nil) }

// SetProgressiveNav makes navigations that page code starts (a clicked link,
// location.href) progressive too, for windows that drive LoadStep. Navigate
// stays blocking for explicit callers.
func (t *Tab) SetProgressiveNav(on bool) { t.progMode.Store(on) }

// LoadFinish runs a pending progressive load to its end.
func (t *Tab) LoadFinish() {
	for {
		more, wait := t.LoadStep(0)
		if !more {
			return
		}
		if wait > 0 {
			time.Sleep(wait)
		}
	}
}
