package agentbridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/agent"
	"github.com/sweefbrowser/sweef/internal/app"
)

const (
	modeAsk   = "ask"
	modeAgent = "agent"
)

func normMode(m string) string {
	switch strings.ToLower(m) {
	case "agent", "act", "do":
		return modeAgent
	}
	return modeAsk
}

// run is one user request being worked on.
type run struct {
	h      *Host
	mode   string
	ctx    context.Context
	cancel context.CancelFunc
	tr     *translator
	text   string

	srcMu   sync.Mutex
	sources []map[string]any
	seen    map[string]bool
}

func (r *run) addSource(rawURL, title string) {
	r.srcMu.Lock()
	defer r.srcMu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if r.seen[rawURL] || len(r.sources) >= 6 {
		return
	}
	r.seen[rawURL] = true
	r.sources = append(r.sources, map[string]any{"title": title, "url": rawURL})
}

func (r *run) sourceList() []map[string]any {
	r.srcMu.Lock()
	defer r.srcMu.Unlock()
	return append([]map[string]any(nil), r.sources...)
}

// askVisibility puts "may Sweef read <origin>?" to the user through the
// app's own visibility card (answered by agent.confirm with a "visibility-"
// id, which the app turns into a remembered site rule) and waits for the rule
// to change. It reports whether the origin is now allowed.
func (r *run) askVisibility(ctx context.Context, b *Backend, origin string) bool {
	r.tr.do(func(t *translator) {
		t.flushText()
		t.closeRunning("ok")
		t.send("confirm", "Let Sweef read "+origin+"?", map[string]any{
			"id": "visibility-" + origin, "title": "Let Sweef read " + origin + "?", "risk": "low",
			"origin": origin, "action": map[string]any{"type": "visibility", "origin": origin},
		})
		t.state("waiting")
	})
	deadline := time.NewTimer(b.h.opts.ConfirmTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		switch v := b.a.AgentSiteRule(origin); v {
		case "allowed":
			r.tr.do(func(t *translator) { t.state("working") })
			return true
		case "blocked":
			r.tr.do(func(t *translator) { t.state("working") })
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

// ---- confirm table ------------------------------------------------------------------------

type confirmEntry struct {
	id     string
	key    string
	ans    chan bool
	evSeen bool
	waited bool
}

// confirmTable pairs the two halves of a confirmation: the agent emits an
// EventConfirm (turned into a card with an id) and then blocks in
// Backend.Confirm; whichever side arrives first creates the entry.
type confirmTable struct {
	mu   sync.Mutex
	seq  int
	list []*confirmEntry
}

func confirmKey(r agent.ConfirmRequest) string {
	return r.RunID + "\x00" + string(r.Kind) + "\x00" + r.Summary + "\x00" + r.URL + "\x00" + r.Detail
}

func (t *confirmTable) get(r agent.ConfirmRequest, ev bool) *confirmEntry {
	k := confirmKey(r)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.list {
		if e.key == k && ((ev && !e.evSeen) || (!ev && !e.waited)) {
			if ev {
				e.evSeen = true
			} else {
				e.waited = true
			}
			return e
		}
	}
	t.seq++
	e := &confirmEntry{id: fmt.Sprintf("cf%d", t.seq), key: k, ans: make(chan bool, 1), evSeen: ev, waited: !ev}
	t.list = append(t.list, e)
	return e
}

func (t *confirmTable) forEvent(r agent.ConfirmRequest) *confirmEntry { return t.get(r, true) }
func (t *confirmTable) forWait(r agent.ConfirmRequest) *confirmEntry  { return t.get(r, false) }

func (t *confirmTable) drop(e *confirmEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, x := range t.list {
		if x == e {
			t.list = append(t.list[:i], t.list[i+1:]...)
			return
		}
	}
}

// answer delivers the user's decision; it reports whether id was pending.
func (t *confirmTable) answer(id string, ok bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.list {
		if e.id == id {
			select {
			case e.ans <- ok:
			default:
			}
			return true
		}
	}
	return false
}

func riskOf(k agent.ConfirmKind) string {
	switch k {
	case agent.ConfirmPayment, agent.ConfirmCredentials, agent.ConfirmDelete:
		return "high"
	case agent.ConfirmMessage, agent.ConfirmCrossDomain:
		return "medium"
	}
	return "low"
}

// ---- translator ---------------------------------------------------------------------------

type actRow struct {
	id      string
	label   string
	started time.Time
}

type item struct {
	ev  *agent.Event
	fn  func(*translator)
	fin *finish
}

type finish struct {
	stats agent.RunStats
	err   error
}

// translator turns agent events and backend progress into the chrome's
// agent.event messages, on one goroutine so ordering is exactly the order in
// which the agent produced them. Text deltas are coalesced to one message per
// ~40 ms (the first one goes out at once).
type translator struct {
	h    *Host
	r    *run
	in   chan item
	out  chan app.AgentEvent
	done chan struct{}

	runID    string
	start    time.Time
	nextRow  int
	textBuf  strings.Builder
	textAll  strings.Builder
	gotText  bool
	lastText time.Time
	thought  bool
	visible  time.Duration

	actQ     []*actRow // act steps announced, not started
	inflight []*actRow // act steps running
	running  []*actRow // other tool actions running

	finalText string
	doneStats *agent.RunStats
}

func newTranslator(h *Host, r *run, out chan app.AgentEvent) *translator {
	return &translator{h: h, r: r, in: make(chan item, 4096), out: out, done: make(chan struct{}), start: time.Now(), runID: r.fallbackID()}
}

func (r *run) fallbackID() string { return fmt.Sprintf("run-%d", time.Now().UnixNano()) }

// push queues an item; it gives up when the host closes.
func (t *translator) push(it item) {
	select {
	case t.in <- it:
	case <-t.h.ctx.Done():
	}
}

func (t *translator) do(fn func(*translator)) { t.push(item{fn: fn}) }

func (t *translator) emit(e agent.Event) { t.push(item{ev: &e}) }

func (t *translator) actStart() {
	t.do(func(t *translator) {
		if len(t.actQ) == 0 {
			return
		}
		row := t.actQ[0]
		t.actQ = t.actQ[1:]
		row.started = time.Now()
		t.inflight = append(t.inflight, row)
		t.send("action", row.label, map[string]any{"id": row.id, "label": row.label, "status": "running"})
	})
}

func (t *translator) actDone(ok bool, ms int64, errMsg string) {
	t.do(func(t *translator) {
		if len(t.inflight) == 0 {
			return
		}
		row := t.inflight[0]
		t.inflight = t.inflight[1:]
		d := map[string]any{"id": row.id, "label": row.label, "status": "ok", "ms": ms}
		if !ok {
			d["status"] = "error"
			if errMsg != "" {
				d["detail"] = clip(errMsg, 200)
			}
		}
		t.send("action", row.label, d)
	})
}

// actSkip marks the next n announced steps as not run.
func (t *translator) actSkip(n int, why string) {
	t.do(func(t *translator) { t.skip(n, why) })
}

func (t *translator) skip(n int, why string) {
	for ; n > 0 && len(t.actQ) > 0; n-- {
		row := t.actQ[0]
		t.actQ = t.actQ[1:]
		t.send("action", row.label, map[string]any{"id": row.id, "label": row.label, "status": "error", "detail": why})
	}
}

// confirmWaiting / confirmAnswered keep the row of the step that asked in sync.
func (t *translator) confirmWaiting(req agent.ConfirmRequest) {}

func (t *translator) confirmAnswered(req agent.ConfirmRequest, approved bool) {
	t.do(func(t *translator) {
		if !approved {
			for i, row := range t.actQ {
				if row.label == req.Summary || (req.Kind == agent.ConfirmCrossDomain && i == 0) {
					t.actQ = append(t.actQ[:i], t.actQ[i+1:]...)
					t.send("action", row.label, map[string]any{"id": row.id, "label": row.label, "status": "error", "detail": "declined by you"})
					break
				}
			}
		}
		t.state("working")
	})
}

func (t *translator) send(kind, text string, data map[string]any) {
	ev := app.AgentEvent{RunID: t.runID, Kind: kind, Text: text, Data: data}
	select {
	case t.out <- ev:
	case <-t.h.ctx.Done():
	}
}

func (t *translator) state(s string) {
	t.send("status", "", map[string]any{"state": s})
}

func (t *translator) markVisible() {
	if t.visible == 0 {
		t.visible = time.Since(t.start)
		if t.visible <= 0 {
			t.visible = time.Microsecond
		}
	}
}

func (t *translator) flushText() {
	if t.textBuf.Len() == 0 {
		return
	}
	s := t.textBuf.String()
	t.textBuf.Reset()
	t.lastText = time.Now()
	t.send("text", s, nil)
}

func (t *translator) endThought() {
	if t.thought {
		t.thought = false
		t.send("thought", "", map[string]any{"done": true})
	}
}

// closeRunning completes the non-act tool rows ("ok" or "error").
func (t *translator) closeRunning(status string) {
	for _, row := range t.running {
		d := map[string]any{"id": row.id, "label": row.label, "status": status, "ms": time.Since(row.started).Milliseconds()}
		t.send("action", row.label, d)
	}
	t.running = nil
}

func statusState(text string) string {
	switch {
	case strings.HasPrefix(text, "Paused"):
		return "paused"
	}
	return "working"
}

func (t *translator) handle(e agent.Event) {
	if e.RunID != "" && t.runID != e.RunID {
		t.runID = e.RunID
	}
	switch e.Kind {
	case agent.EventText:
		t.markVisible()
		t.endThought()
		t.closeRunning("ok")
		t.textAll.WriteString(e.Text)
		t.textBuf.WriteString(e.Text)
		if !t.gotText {
			t.gotText = true
			t.flushText()
		}
	case agent.EventThought:
		t.markVisible()
		t.flushText()
		t.thought = true
		t.send("thought", e.Text, nil)
	case agent.EventAction:
		t.markVisible()
		t.flushText()
		t.endThought()
		t.nextRow++
		row := &actRow{id: fmt.Sprintf("%s.%d", t.runID, t.nextRow), label: e.Text, started: time.Now()}
		if e.Tool == "act" {
			t.actQ = append(t.actQ, row)
			t.send("action", row.label, map[string]any{"id": row.id, "label": row.label, "status": "pending"})
		} else {
			t.closeRunning("ok")
			t.running = append(t.running, row)
			t.send("action", row.label, map[string]any{"id": row.id, "label": row.label, "status": "running"})
		}
	case agent.EventStatus:
		t.flushText()
		t.send("status", e.Text, map[string]any{"state": statusState(e.Text)})
	case agent.EventConfirm:
		t.markVisible()
		t.flushText()
		t.endThought()
		t.closeRunning("ok")
		if e.Confirm == nil || t.r.mode == modeAsk {
			return // Ask mode never acts, so there is nothing to approve
		}
		req := *e.Confirm
		ce := t.h.confirms.forEvent(req)
		action := map[string]any{"type": string(req.Kind)}
		if req.URL != "" {
			action["page"] = req.URL
		}
		if req.Detail != "" {
			action["why"] = req.Detail
		}
		t.send("confirm", req.Summary, map[string]any{"id": ce.id, "title": req.Summary, "risk": riskOf(req.Kind), "detail": req.Detail, "action": action, "kind": string(req.Kind)})
		t.state("waiting")
	case agent.EventDone:
		t.finalText = e.Text
		t.doneStats = e.Stats
	case agent.EventError:
		// delivered by finish with the typed error
	}
}

// pump runs until finish; it owns every send for the run.
func (t *translator) pump() {
	defer close(t.done)
	defer close(t.out)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	armed := false
	for {
		select {
		case it := <-t.in:
			switch {
			case it.ev != nil:
				t.handle(*it.ev)
			case it.fn != nil:
				it.fn(t)
			case it.fin != nil:
				t.finish(*it.fin)
				return
			}
			if t.textBuf.Len() > 0 && !armed {
				wait := 40*time.Millisecond - time.Since(t.lastText)
				if wait < time.Millisecond {
					wait = time.Millisecond
				}
				timer.Reset(wait)
				armed = true
			}
		case <-timer.C:
			armed = false
			t.flushText()
		case <-t.h.ctx.Done():
			return
		}
	}
}

func (t *translator) finish(f finish) {
	t.flushText()
	t.endThought()
	if f.err == nil {
		t.closeRunning("ok")
	} else {
		t.closeRunning("error")
	}
	t.skip(len(t.actQ), "not run")
	for _, row := range t.inflight {
		t.send("action", row.label, map[string]any{"id": row.id, "label": row.label, "status": "error"})
	}
	t.inflight = nil
	st := f.stats
	key := t.h.keyNow()
	if f.err != nil {
		code, msg, retry := classify(f.err, key)
		d := map[string]any{"code": code, "message": msg}
		if retry > 0 {
			d["retryAfter"] = retry
		}
		t.send("error", msg, d)
		return
	}
	stopped := st.Stop == "stopped"
	if !t.gotText && t.finalText != "" && !stopped {
		t.textAll.WriteString(t.finalText)
		t.send("text", t.finalText, map[string]any{"full": true})
	}
	ttft := float64(t.visible.Microseconds()) / 1000
	if t.visible == 0 {
		ttft = st.TTFTAvgMs
	}
	d := map[string]any{
		"ttft": round1(ttft),
		"tps":  round1(st.OutputTPS),
		"ms":   st.ElapsedMs,
		"stop": st.Stop,
		// diagnostics (the sidebar ignores them)
		"apiTtft":  round1(st.TTFTAvgMs),
		"turns":    st.Turns,
		"tools":    st.ToolCalls,
		"costUsd":  st.CostUSD,
		"cacheHit": round1(st.CacheHitRate * 100),
		"tokensIn": st.Usage.PromptTokens(),
		"tokenOut": st.Usage.OutputTokens,
	}
	if src := t.r.sourceList(); len(src) > 0 {
		d["sources"] = src
	}
	t.send("done", "", d)
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// classify maps a run error to the sidebar's error codes.
func classify(err error, key string) (code, msg string, retryAfter int) {
	msg = redact(err.Error(), key)
	var ae *agent.APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Status == 401 || ae.Status == 403:
			return "auth", msg, 0
		case ae.Status == 429 || ae.Type == "rate_limit_error":
			s := int(math.Ceil(ae.RetryAfter.Seconds()))
			return "rate_limit", msg, s
		case ae.Status >= 500 || ae.Type == "overloaded_error":
			return "unavailable", msg, 0
		}
		return "api", msg, 0
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return "network", msg, 0
	}
	l := strings.ToLower(msg)
	if strings.Contains(l, "connection") || strings.Contains(l, "no such host") || strings.Contains(l, "dial ") || strings.Contains(l, "eof") {
		return "network", msg, 0
	}
	if errors.Is(err, agent.ErrBusy) {
		return "busy", msg, 0
	}
	return "agent", msg, 0
}
