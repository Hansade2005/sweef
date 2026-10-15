package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// loop.go - the agent loop.
//
// One run = repeated turns of: build request -> stream response -> (while
// streaming) dispatch every tool_use block the moment its input JSON is
// complete -> wait for the turn's tool calls -> append assistant message and
// ONE user message with all tool results -> maybe compact -> next turn.
//
// Early dispatch: Client.Stream invokes Handler.OnBlockStop at
// content_block_stop, long before message_stop. The turn executor starts the
// tool there. Independent calls run concurrently; calls that touch the same
// tab (or a barrier like tab management) are ordered by emission order.

// ErrBusy is returned by Run/Start while another run is active.
var ErrBusy = errors.New("agent: a run is already active")

// RunStats summarises one run.
type RunStats struct {
	Stop             string  `json:"stop"` // done, end_turn, ask_user, stopped, max_turns, budget, refusal, error
	Turns            int     `json:"turns"`
	Requests         int     `json:"requests"`
	Retries          int     `json:"retries"`
	FirstByteAvgMs   float64 `json:"firstByteAvgMs"`
	TTFTAvgMs        float64 `json:"ttftAvgMs"`
	TTFTP50Ms        float64 `json:"ttftP50Ms"`
	TTFToolAvgMs     float64 `json:"firstToolAvgMs"`
	ToolCalls        int     `json:"toolCalls"`
	ToolAvgMs        float64 `json:"toolAvgMs"`
	ToolTotalMs      int64   `json:"toolTotalMs"`
	Actions          int     `json:"actions"`
	Navigations      int     `json:"navigations"`
	FullSnapshots    int     `json:"fullSnapshots"`
	DeltaSnapshots   int     `json:"deltaSnapshots"`
	Compactions      int     `json:"compactions"`
	Usage            Usage   `json:"usage"`
	CacheHitRate     float64 `json:"cacheHitRate"`
	CostUSD          float64 `json:"costUsd"`
	ElapsedMs        int64   `json:"elapsedMs"`
	ReusedConnection int     `json:"reusedConnections"`
	// DecodeMs is the time spent streaming output (first generated block to
	// message end, summed over requests that produced tokens); OutputTPS is
	// output tokens per second over that time (0 when too short to be meaningful).
	DecodeMs  float64 `json:"decodeMs"`
	OutputTPS float64 `json:"outputTps"`
}

type statsCollector struct {
	mu         sync.Mutex
	s          RunStats
	firstByte  []float64
	ttft       []float64
	firstTool  []float64
	toolMs     []float64
	start      time.Time
	cacheStart CacheStats
	decodeMs   float64
	decodeToks int
}

func (c *statsCollector) request(m *Metrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.s.Requests++
	c.s.Retries += m.Attempts - 1
	c.s.Usage.InputTokens += m.Usage.InputTokens
	c.s.Usage.OutputTokens += m.Usage.OutputTokens
	c.s.Usage.CacheReadTokens += m.Usage.CacheReadTokens
	c.s.Usage.CacheCreationTokens += m.Usage.CacheCreationTokens
	c.s.CostUSD += CostUSD(m.Usage)
	if m.FirstByte > 0 {
		c.firstByte = append(c.firstByte, float64(m.FirstByte.Microseconds())/1000)
	}
	if m.FirstToken > 0 {
		c.ttft = append(c.ttft, float64(m.FirstToken.Microseconds())/1000)
	}
	if m.FirstToolCall > 0 {
		c.firstTool = append(c.firstTool, float64(m.FirstToolCall.Microseconds())/1000)
	}
	if m.ReusedConn {
		c.s.ReusedConnection++
	}
	if m.Usage.OutputTokens > 0 {
		from := m.FirstToken
		if from <= 0 {
			from = m.FirstToolCall
		}
		if from <= 0 {
			from = m.FirstByte
		}
		if d := m.Total - from; from > 0 && d > 0 {
			c.decodeMs += float64(d.Microseconds()) / 1000
			c.decodeToks += m.Usage.OutputTokens
		}
	}
}

func (c *statsCollector) tool(d time.Duration) {
	c.mu.Lock()
	c.s.ToolCalls++
	c.s.ToolTotalMs += d.Milliseconds()
	c.toolMs = append(c.toolMs, float64(d.Microseconds())/1000)
	c.mu.Unlock()
}

func (c *statsCollector) obsKind(full bool) {
	c.mu.Lock()
	if full {
		c.s.FullSnapshots++
	} else {
		c.s.DeltaSnapshots++
	}
	c.mu.Unlock()
}

func avg(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t / float64(len(v))
}

func p50(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	w := append([]float64(nil), v...)
	sort.Float64s(w)
	return w[len(w)/2]
}

func (c *statsCollector) snapshot() RunStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.s
	s.FirstByteAvgMs, s.TTFTAvgMs, s.TTFTP50Ms = avg(c.firstByte), avg(c.ttft), p50(c.ttft)
	s.TTFToolAvgMs, s.ToolAvgMs = avg(c.firstTool), avg(c.toolMs)
	s.ElapsedMs = time.Since(c.start).Milliseconds()
	s.DecodeMs = c.decodeMs
	if c.decodeMs >= 50 {
		s.OutputTPS = float64(c.decodeToks) / (c.decodeMs / 1000)
	}
	u := s.Usage
	if d := u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens; d > 0 {
		s.CacheHitRate = float64(u.CacheReadTokens) / float64(d)
	}
	return s
}

// runState is the mutable state of one run, shared by the loop and the tool
// goroutines.
type runState struct {
	id     string
	emit   func(Event)
	safety *runSafety
	stats  *statsCollector

	mu      sync.Mutex
	curTab  string
	refs    map[string]map[string]refInfo
	final   string
	success bool
	stop    string // set when a tool ends the run (done / ask_user)
	turns   int
}

func (rs *runState) event(e Event) {
	e.RunID = rs.id
	rs.emit(e)
}

func (rs *runState) tab() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.curTab
}

func (rs *runState) setTab(id string) {
	rs.mu.Lock()
	rs.curTab = id
	rs.mu.Unlock()
}

func (rs *runState) refInfo(tab, ref string) (refInfo, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	ri, ok := rs.refs[tab][ref]
	return ri, ok
}

func (rs *runState) learnRefs(tab, text string, replace bool) {
	m := map[string]refInfo{}
	parseRefs(text, m)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if replace || rs.refs[tab] == nil {
		rs.refs[tab] = m
		return
	}
	for k, v := range m {
		rs.refs[tab][k] = v
	}
}

func (rs *runState) finish(stop, text string, success bool) {
	rs.mu.Lock()
	if rs.stop == "" {
		rs.stop, rs.final, rs.success = stop, text, success
	}
	rs.mu.Unlock()
}

// ---- take-control gate ----------------------------------------------------------------

type gate struct {
	mu       sync.Mutex
	paused   bool
	took     bool // control was taken since the last wait
	resumeCh chan struct{}
	note     string
}

func newGate() *gate { return &gate{resumeCh: make(chan struct{})} }

func (g *gate) pause() {
	g.mu.Lock()
	if !g.paused {
		g.paused, g.took = true, true
		g.resumeCh = make(chan struct{})
	}
	g.mu.Unlock()
}

func (g *gate) resume(note string) {
	g.mu.Lock()
	if g.paused {
		g.paused = false
		g.note = note
		close(g.resumeCh)
	}
	g.mu.Unlock()
}

func (g *gate) isPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// wait blocks while paused; it reports whether control was taken since the
// previous wait and the user's hand-back note.
func (g *gate) wait(ctx context.Context) (took bool, note string, err error) {
	for {
		g.mu.Lock()
		if !g.paused {
			took, note = g.took, g.note
			g.took, g.note = false, ""
			g.mu.Unlock()
			return took, note, nil
		}
		ch := g.resumeCh
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return false, "", ctx.Err()
		}
	}
}

// ---- Agent ---------------------------------------------------------------------------------

// Agent runs tasks against a Backend.
type Agent struct {
	cfg  Config
	be   Backend
	cl   *Client
	vis  *visibility
	gate *gate

	cvMu sync.Mutex // guards cv and note
	cv   *convo
	note string

	openedMu sync.Mutex
	opened   map[string]bool // tabs opened by the agent (the only ones it may close)

	runMu   sync.Mutex // held for the duration of a run
	stateMu sync.Mutex // guards cancel / active
	cancel  context.CancelFunc
	active  *runState
	seq     int
}

// New builds an Agent. cfg.APIKey is required.
func New(cfg Config, be Backend) (*Agent, error) {
	cfg.applyDefaults()
	cl, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	a := &Agent{cfg: cfg, be: be, cl: cl, vis: newVisibility(), gate: newGate(), opened: map[string]bool{}}
	a.cv = newConvo(cfg.Compact, buildHead(&a.cfg))
	return a, nil
}

// Close releases idle connections.
func (a *Agent) Close() { a.cl.Close() }

// Prewarm opens the API connection (call when the sidebar opens).
func (a *Agent) Prewarm(ctx context.Context) (time.Duration, error) { return a.cl.Prewarm(ctx) }

// CacheStats returns cumulative prompt-cache statistics.
func (a *Agent) CacheStats() CacheStats { return a.cl.CacheStats() }

// PrefixTokenEstimate estimates the static cache prefix (tools + system).
func (a *Agent) PrefixTokenEstimate() int { return EstimateTokens(string(a.cv.head)) }

// SetPageVisibility records the user's choice for an origin ("https://x.com"
// or a bare host). Not-allowed pages are never sent to the model.
func (a *Agent) SetPageVisibility(origin string, allowed bool) { a.vis.Set(origin, allowed) }

// Reset forgets the conversation (a new chat).
func (a *Agent) Reset() {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	a.cvMu.Lock()
	a.cv.reset()
	a.note = ""
	a.cvMu.Unlock()
}

// Stop aborts the active run (no-op when idle).
func (a *Agent) Stop() {
	a.stateMu.Lock()
	c := a.cancel
	a.stateMu.Unlock()
	if c != nil {
		c()
	}
}

// TakeControl pauses the agent: tool calls that have not started return an
// error, and the loop waits before its next request.
func (a *Agent) TakeControl() {
	a.gate.pause()
	a.stateMu.Lock()
	rs := a.active
	a.stateMu.Unlock()
	if rs != nil {
		rs.event(Event{Kind: EventStatus, Text: "Paused: you have control of the browser"})
	}
}

// Resume hands the browser back. note describes what the user did or wants
// (optional); the agent also re-reads the tab list and re-snapshots pages.
func (a *Agent) Resume(note string) {
	a.gate.resume(note)
}

// Start runs a task asynchronously. Events arrive on the returned channel,
// which is closed after the terminal done/error event.
func (a *Agent) Start(ctx context.Context, text string) (string, <-chan Event, error) {
	if !a.runMu.TryLock() {
		return "", nil, ErrBusy
	}
	ch := make(chan Event, 256)
	a.stateMu.Lock()
	a.seq++
	id := fmt.Sprintf("run-%d", a.seq)
	a.stateMu.Unlock()
	go func() {
		defer close(ch)
		defer a.runMu.Unlock()
		emit := func(e Event) {
			select {
			case ch <- e:
			case <-ctx.Done():
				// the consumer may be gone; terminal events must still try
				select {
				case ch <- e:
				default:
				}
			}
		}
		a.run(ctx, id, text, emit)
	}()
	return id, ch, nil
}

// Run is the synchronous form of Start; emit receives every event.
func (a *Agent) Run(ctx context.Context, text string, emit func(Event)) (RunStats, error) {
	if !a.runMu.TryLock() {
		return RunStats{}, ErrBusy
	}
	defer a.runMu.Unlock()
	a.stateMu.Lock()
	a.seq++
	id := fmt.Sprintf("run-%d", a.seq)
	a.stateMu.Unlock()
	if emit == nil {
		emit = func(Event) {}
	}
	return a.run(ctx, id, text, emit)
}

func (a *Agent) run(parent context.Context, id, text string, emit func(Event)) (RunStats, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	rs := &runState{id: id, emit: emit, refs: map[string]map[string]refInfo{},
		safety: newRunSafety(a.cfg.MaxActions, a.cfg.MaxNavigations),
		stats:  &statsCollector{start: time.Now()}}
	rs.stats.cacheStart = a.cl.CacheStats()
	a.stateMu.Lock()
	a.cancel, a.active = cancel, rs
	a.stateMu.Unlock()
	defer func() {
		a.stateMu.Lock()
		a.cancel, a.active = nil, nil
		a.stateMu.Unlock()
	}()

	err := a.loop(ctx, rs, text)

	st := rs.stats.snapshot()
	st.Turns = rs.turns
	st.Actions, st.Navigations = rs.safety.counts()
	a.cvMu.Lock()
	st.Compactions = a.cv.Compactions
	a.cvMu.Unlock()
	switch {
	case err == nil:
		rs.mu.Lock()
		st.Stop = rs.stop
		final := rs.final
		rs.mu.Unlock()
		if st.Stop == "" {
			st.Stop = "end_turn"
		}
		rs.event(Event{Kind: EventDone, Text: final, Stats: &st})
	case errors.Is(err, context.Canceled):
		st.Stop = "stopped"
		rs.event(Event{Kind: EventDone, Text: "Stopped.", Stats: &st})
		err = nil
	default:
		st.Stop = stopKind(err)
		rs.event(Event{Kind: EventError, Text: err.Error(), Stats: &st})
	}
	return st, err
}

type stopError struct {
	kind string
	msg  string
}

func (e *stopError) Error() string { return e.msg }

func stopKind(err error) string {
	var se *stopError
	if errors.As(err, &se) {
		return se.kind
	}
	return "error"
}

func (a *Agent) tail(rs *runState) string {
	var sb strings.Builder
	a.cvMu.Lock()
	note := a.note
	a.cvMu.Unlock()
	if note != "" {
		sb.WriteString("<task_memory>\n")
		sb.WriteString(escapeUntrusted(note))
		sb.WriteString("\n</task_memory>\n")
	}
	rs.mu.Lock()
	turns, tab := rs.turns, rs.curTab
	rs.mu.Unlock()
	fmt.Fprintf(&sb, "<run_status turn=\"%d/%d\" current_tab=\"%s\"/>", turns+1, a.cfg.MaxTurns, attrClean.Replace(tab))
	return sb.String()
}

func (a *Agent) setNote(n string) {
	n = strings.TrimSpace(n)
	if n == "" {
		return
	}
	if len(n) > 1500 {
		n = n[:1500]
	}
	a.cvMu.Lock()
	a.note = n
	a.cvMu.Unlock()
}

// loop is the turn loop.
func (a *Agent) loop(ctx context.Context, rs *runState, text string) error {
	rs.event(Event{Kind: EventStatus, Text: "Working on it"})
	// A takeover left over from before this run must not deadlock it; the
	// hand-back note (and stale-snapshot warning) is still delivered.
	a.gate.resume("")
	// Pin the working tab and seed trust from the user's own words.
	for _, t := range a.be.Tabs() {
		if t.Active {
			rs.setTab(t.ID)
			if !isInternalURL(t.URL) {
				rs.safety.trust(t.URL)
			}
		}
	}
	if rs.tab() == "" {
		if tabs := a.be.Tabs(); len(tabs) > 0 {
			rs.setTab(tabs[len(tabs)-1].ID)
		}
	}
	rs.safety.seedTrusted(text)

	a.cvMu.Lock()
	a.cv.addUserText(text)
	a.cvMu.Unlock()

	recoveredThinking := false
	streamFailures := 0
	lengthContinuations := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if rs.turns >= a.cfg.MaxTurns {
			return &stopError{"max_turns", fmt.Sprintf("Stopped after %d turns (the per-run limit).", a.cfg.MaxTurns)}
		}
		took, note, err := a.gate.wait(ctx)
		if err != nil {
			return err
		}
		if took {
			a.handBack(rs, note)
		}

		tail := a.tail(rs) // takes cvMu itself
		a.cvMu.Lock()
		body := a.cv.body(tail)
		a.cvMu.Unlock()

		te := newTurnExec(a, rs, ctx)
		var turnText strings.Builder
		h := &Handler{
			OnText: func(b []byte) {
				turnText.Write(b)
				rs.event(Event{Kind: EventText, Text: string(b)})
			},
			OnBlockStop: func(b *Block) {
				if b.Type == "tool_use" {
					te.dispatch(*b)
				}
			},
			OnRetry: func(attempt int, wait time.Duration, err error) {
				rs.event(Event{Kind: EventStatus, Text: fmt.Sprintf("Retrying in %.1fs (%s)", wait.Seconds(), shortErr(err))})
			},
		}
		if a.cfg.ThoughtSummaries {
			h.OnThinking = func(b []byte) { rs.event(Event{Kind: EventThought, Text: string(b)}) }
		}
		msg, met, serr := a.cl.Stream(ctx, body, h)
		te.wait()
		if met != nil {
			rs.stats.request(met)
		}

		if serr != nil {
			if ctx.Err() != nil {
				a.settleCancelled(rs, te, msg)
				return ctx.Err()
			}
			var ae *APIError
			if errors.As(serr, &ae) && ae.Status == 400 && strings.Contains(strings.ToLower(ae.Message), "thinking") && !recoveredThinking {
				recoveredThinking = true
				a.cvMu.Lock()
				n := a.cv.stripThinking()
				// and stop asking for thinking in this run: with thinking enabled the API requires the
				// assistant turn that owns the pending tool results to start with a thinking block, so a
				// stripped history was rejected again and the run died (Mode B: 23 of 23 sweef errors had
				// exactly two failed requests)
				noThink := a.cfg
				noThink.Thinking = ThinkingDisabled
				a.cv.head = buildHead(&noThink)
				a.cvMu.Unlock()
				a.cfg.logf("API rejected replayed thinking (%s); stripped %d blocks, thinking off for this run, retrying", ae.Message, n)
				if len(te.calls) == 0 {
					continue
				}
			}
			if len(te.calls) > 0 && streamFailures < 2 {
				// Tools already ran: keep the model informed instead of losing their effects.
				streamFailures++
				a.commitTurn(rs, te, msg)
				continue
			}
			return serr
		}
		streamFailures = 0

		// A completed response: account, then act on stop_reason.
		rs.turns++
		if msg.StopReason == "refusal" {
			if len(te.calls) > 0 {
				a.commitTurn(rs, te, msg)
			}
			reason := "The model declined this request."
			if msg.StopCategory != "" {
				reason += " (" + msg.StopCategory + ")"
			}
			return &stopError{"refusal", reason}
		}
		if msg.StopReason == "pause_turn" {
			a.commitAssistantOnly(msg, "Continue.")
			continue
		}

		hasTools := len(te.calls) > 0
		if !hasTools {
			if msg.StopReason == "max_tokens" && lengthContinuations < 2 {
				lengthContinuations++
				a.commitAssistantOnly(msg, "Your last message was cut off by the output limit. Continue, more briefly, and use tools for actions.")
				continue
			}
			a.commitAssistantOnly(msg, "")
			rs.finish("end_turn", strings.TrimSpace(turnText.String()), true)
			return a.budgetCheck(rs)
		}
		lengthContinuations = 0
		a.commitTurn(rs, te, msg)

		// compaction decision for the NEXT request
		a.cvMu.Lock()
		if a.cv.maybeCompact(met.Usage.PromptTokens()) {
			a.cfg.logf("compacted context at turn %d (prompt %d tokens)", a.cv.turn, met.Usage.PromptTokens())
		}
		a.cvMu.Unlock()

		rs.mu.Lock()
		stopped := rs.stop != ""
		rs.mu.Unlock()
		if stopped {
			return nil
		}
		if err := a.budgetCheck(rs); err != nil {
			return err
		}
	}
}

func shortErr(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		if ae.Type != "" && ae.Type != "http_error" {
			return ae.Type
		}
		return fmt.Sprintf("HTTP %d", ae.Status)
	}
	return "network error"
}

func (a *Agent) budgetCheck(rs *runState) error {
	rs.stats.mu.Lock()
	cost, out := rs.stats.s.CostUSD, rs.stats.s.Usage.OutputTokens
	rs.stats.mu.Unlock()
	if cost > a.cfg.MaxCostUSD {
		return &stopError{"budget", fmt.Sprintf("Stopped: this run reached its cost limit ($%.2f).", a.cfg.MaxCostUSD)}
	}
	if out > a.cfg.MaxOutputTokens {
		return &stopError{"budget", "Stopped: this run reached its output token limit."}
	}
	return nil
}

// commitAssistantOnly stores an assistant message that has no tool calls. If
// followUp is non-empty a user message is appended so the next request ends
// on a user turn.
func (a *Agent) commitAssistantOnly(msg *Message, followUp string) {
	a.cvMu.Lock()
	defer a.cvMu.Unlock()
	a.cv.addAssistant(msg.Blocks)
	if followUp != "" {
		a.cv.addUserText(followUp)
	}
}

// commitTurn appends the assistant message and the single user message that
// carries every tool_result (in tool_use order).
func (a *Agent) commitTurn(rs *runState, te *turnExec, msg *Message) {
	byID := map[string]*call{}
	for _, c := range te.calls {
		byID[c.id] = c
	}
	var trs []*toolResult
	for _, b := range msg.Blocks {
		if b.Type != "tool_use" {
			continue
		}
		tr := &toolResult{id: b.ID}
		if c := byID[b.ID]; c != nil && c.out != nil {
			tr.isErr, tr.parts = c.out.isErr, c.out.parts
		} else {
			tr.isErr = true
			tr.parts = []*part{{kind: partText, text: "Tool call was not executed (the response ended before its input was complete)."}}
		}
		trs = append(trs, tr)
	}
	a.cvMu.Lock()
	a.cv.addAssistant(msg.Blocks)
	if len(trs) > 0 {
		a.cv.addToolResults(trs)
	}
	a.cvMu.Unlock()
}

// settleCancelled keeps history valid after a user stop: any tool_use that
// reached the history gets a result.
func (a *Agent) settleCancelled(rs *runState, te *turnExec, msg *Message) {
	if msg == nil {
		return
	}
	hasTool := false
	for _, b := range msg.Blocks {
		if b.Type == "tool_use" {
			hasTool = true
		}
	}
	if !hasTool {
		return
	}
	for _, c := range te.calls {
		if c.out == nil {
			c.out = errOut("Cancelled: the user stopped the run.")
		}
	}
	a.commitTurn(rs, te, msg)
}

// handBack runs when the user returns control: every snapshot the model holds
// may be stale, so baselines are invalidated and the model is told what
// changed.
func (a *Agent) handBack(rs *runState, userNote string) {
	var sb strings.Builder
	sb.WriteString("The user took control of the browser and handed it back. Pages may have changed, tabs may have been opened, closed or switched, so all earlier snapshots are stale: re-read with read_page before acting.")
	tabs := a.be.Tabs()
	var cur string
	for _, t := range tabs {
		if t.Active {
			cur = t.ID
		}
	}
	if cur != "" && !isInternalURL(a.be.URL(cur)) {
		rs.setTab(cur)
	}
	sb.WriteString("\n" + wrapUntrusted("tabs", "", "", a.tabsText(tabs, rs.tab())))
	if n := strings.TrimSpace(userNote); n != "" {
		sb.WriteString("\nThe user adds: " + n)
	}
	a.cvMu.Lock()
	a.cv.invalidateAllBaselines()
	a.cv.addUserText(sb.String())
	a.cvMu.Unlock()
	rs.mu.Lock()
	rs.refs = map[string]map[string]refInfo{}
	rs.mu.Unlock()
	rs.event(Event{Kind: EventStatus, Text: "Resumed"})
}
