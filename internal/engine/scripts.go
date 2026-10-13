package engine

// scripts.go — page script loading and the document lifecycle.
//
// The lifecycle itself (script order, DOMContentLoaded, load, the settle pump) is
// the state machine in progload.go. Order follows the HTML spec closely enough
// for real sites:
//  1. classic scripts without async/defer run in document order as the tree
//     is walked (inline source, or fetched src);
//  2. async scripts run next, in document order (we have no parallel fetch
//     to race them against);
//  3. defer scripts and type=module scripts run after the tree is complete;
//  4. DOMContentLoaded fires, then the event loop pumps, then load fires;
//  5. the loop pumps again until idle or the settle budget is spent.
// Scripts inserted by page code (createElement('script')) are queued by the
// bridge and executed by the pump, with load/error events on the element.

import (
	"strings"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

func isJavaScriptType(typ string) bool {
	t := strings.ToLower(strings.TrimSpace(typ))
	switch t {
	case "", "text/javascript", "application/javascript", "text/ecmascript", "application/ecmascript",
		"module", "text/jscript", "application/x-javascript", "text/x-javascript":
		return true
	}
	return false
}

func (t *Tab) settleBudget() time.Duration {
	ms := t.eng.opts.SettleMs
	if ms <= 0 {
		ms = 1500
	}
	return time.Duration(ms) * time.Millisecond
}

// runScriptEl fetches (if needed) and executes one <script> element, then
// dispatches load or error on it.
func (t *Tab) runScriptEl(j *jsVM, s fnet.ScriptEl) {
	j.mu.Lock()
	if j.executed[s.ID] || j.closed {
		j.mu.Unlock()
		return
	}
	j.executed[s.ID] = true
	j.mu.Unlock()

	src := s.Source
	name := "<inline script>"
	if s.Src != "" {
		abs, err := resolveURL(t.URL(), s.Src)
		if err != nil {
			t.LogConsole("error", "script: bad src "+s.Src)
			j.dispatch(s.ID, "error", nil)
			return
		}
		name = abs
		body, status, err := t.fetchResourceKind(abs, KindScript)
		if err != nil || status >= 400 {
			msg := "script " + abs + ": "
			if err != nil {
				msg += err.Error()
			} else {
				msg += "HTTP " + itoa(status)
			}
			t.LogConsole("error", msg)
			j.dispatch(s.ID, "error", nil)
			return
		}
		src = body
	}
	_, _ = j.call("__swSetCurrentScript", s.ID)
	j.mu.Lock()
	j.curBase = t.URL()
	if s.Src != "" {
		j.curBase = name
	}
	j.mu.Unlock()
	te := time.Now()
	if s.Module {
		j.prefetchModuleDeps(name, src) // its static imports download in parallel, not one by one from the loader
		_ = j.runModule(src, name)
		j.jobs()
	} else {
		_, _ = j.runCached(src, name)
		j.jobs() // microtask checkpoint: the next script must see what promise callbacks of this one set up
	}
	bc := j.bcState
	t.tspan("script.exec", name+" "+itoa(len(src))+"B bytecode:"+bc, te)
	_, _ = j.call("__swSetCurrentScript", 0)
	if s.Src != "" {
		j.dispatch(s.ID, "load", nil)
	}
}

// drainScripts runs scripts that page code inserted into the document, until the queue is empty or limit
// (zero = none) has passed; the rest stays queued for the next pump.
func (j *jsVM) drainScripts(limit time.Time) {
	for {
		j.mu.Lock()
		if j.closed || len(j.scriptQueue) == 0 || (!limit.IsZero() && !time.Now().Before(limit)) || (!j.scriptCap.IsZero() && !time.Now().Before(j.scriptCap)) {
			j.mu.Unlock()
			return
		}
		id := j.scriptQueue[0]
		j.scriptQueue = j.scriptQueue[1:]
		if j.executed[id] {
			j.mu.Unlock()
			continue
		}
		j.mu.Unlock()
		t := j.tab
		t.mu.RLock()
		d := t.dom
		t.mu.RUnlock()
		if d == nil {
			return
		}
		el := d.ScriptElement(id)
		if el == nil || !isJavaScriptType(el.Type) || el.NoModule {
			j.mu.Lock()
			j.executed[id] = true
			j.mu.Unlock()
			continue
		}
		t.runScriptEl(j, *el)
	}
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Replace(fmtInt(n), " ", "", -1))
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
