package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// exec.go - tool execution, early dispatch and the dependency rules between
// concurrent tool calls.

type toolOut struct {
	parts []*part
	isErr bool
}

func textOut(s string) *toolOut {
	return &toolOut{parts: []*part{{kind: partText, text: s}}}
}

func errOut(s string) *toolOut {
	return &toolOut{isErr: true, parts: []*part{{kind: partText, text: s}}}
}

type call struct {
	id, name string
	input    []byte
	inputOK  bool
	write    bool   // mutates browser state or baselines
	scope    string // tab id, "" = current tab, "*" = barrier
	deps     []*call
	done     chan struct{}
	out      *toolOut
	dur      time.Duration
}

// conflicts says whether two calls must not overlap. Two pure reads never
// conflict; otherwise they conflict unless both name different explicit tabs.
func conflicts(a, b *call) bool {
	if !a.write && !b.write {
		return false
	}
	if a.scope == "*" || b.scope == "*" || a.scope == "" || b.scope == "" {
		return true
	}
	return a.scope == b.scope
}

// classify derives write/scope for a call from its (possibly invalid) input.
func classify(name string, input []byte) (write bool, scope string) {
	var probe struct {
		Tab    string `json:"tab"`
		Action string `json:"action"`
	}
	_ = json.Unmarshal(input, &probe)
	switch name {
	case toolAct, toolReadPage:
		return true, probe.Tab
	case toolFind:
		return false, probe.Tab
	case toolTabs:
		if probe.Action == "list" {
			return false, "*"
		}
		return true, "*"
	}
	return true, "*" // ask_user, done and anything unknown: barrier
}

// turnExec runs the tool calls of one model turn.
type turnExec struct {
	a   *Agent
	rs  *runState
	ctx context.Context

	mu    sync.Mutex
	calls []*call
	wg    sync.WaitGroup
}

func newTurnExec(a *Agent, rs *runState, ctx context.Context) *turnExec {
	return &turnExec{a: a, rs: rs, ctx: ctx}
}

// dispatch starts a completed tool_use block immediately (early dispatch).
// It is invoked from the stream goroutine and must not block.
func (te *turnExec) dispatch(b Block) {
	c := &call{id: b.ID, name: b.Name, input: append([]byte(nil), b.Input...), inputOK: b.InputOK, done: make(chan struct{})}
	c.write, c.scope = classify(b.Name, c.input)
	te.mu.Lock()
	for _, p := range te.calls {
		if conflicts(p, c) {
			c.deps = append(c.deps, p)
		}
	}
	te.calls = append(te.calls, c)
	te.mu.Unlock()

	te.wg.Add(1)
	go func() {
		defer te.wg.Done()
		defer close(c.done)
		for _, d := range c.deps {
			select {
			case <-d.done:
			case <-te.ctx.Done():
				c.out = errOut("Cancelled: the user stopped the run.")
				return
			}
		}
		t0 := time.Now()
		defer func() {
			if r := recover(); r != nil {
				c.out = errOut(fmt.Sprintf("internal error while running %s: %v", c.name, r))
			}
			c.dur = time.Since(t0)
			te.rs.stats.tool(c.dur)
		}()
		c.out = te.a.execTool(te.ctx, te.rs, c)
	}()
}

// wait blocks until every dispatched call finished.
func (te *turnExec) wait() { te.wg.Wait() }

// execTool validates and runs one call.
func (a *Agent) execTool(ctx context.Context, rs *runState, c *call) *toolOut {
	if !c.inputOK {
		return errOut("The tool input was not valid JSON (the output was probably cut off). Re-send the call with a complete, smaller input.")
	}
	if ctx.Err() != nil {
		return errOut("Cancelled: the user stopped the run.")
	}
	if a.gate.isPaused() {
		return errOut("Not executed: the user took control of the browser. The page may have changed; wait for the hand-back and re-read the page.")
	}
	switch c.name {
	case toolAct:
		return a.execAct(ctx, rs, c)
	case toolTabs:
		return a.execTabs(ctx, rs, c)
	case toolReadPage:
		return a.execReadPage(ctx, rs, c)
	case toolFind:
		return a.execFind(ctx, rs, c)
	case toolAskUser:
		return a.execAsk(ctx, rs, c)
	case toolDone:
		return a.execDone(rs, c)
	}
	return errOut(fmt.Sprintf("Unknown tool %q. Available tools: act, tabs, read_page, find, ask_user, done.", c.name))
}

func (a *Agent) resolveTab(rs *runState, explicit string) (string, error) {
	t := explicit
	if t == "" {
		t = rs.tab()
	}
	if t == "" {
		return "", fmt.Errorf("there is no open tab; use tabs open with a url")
	}
	return t, nil
}

// ---- act ---------------------------------------------------------------------

func stepDesc(s Step) string {
	switch s.Op {
	case OpNavigate:
		return "navigate " + clip(s.URL, 80)
	case OpClick, OpHover, OpSelect:
		return s.Op + " " + s.Ref
	case OpType:
		return "type " + s.Ref
	case OpPress:
		return "press " + s.Key
	case OpExpect:
		return "expect " + s.Kind
	case OpWait:
		return "wait"
	}
	return s.Op
}

func (a *Agent) label(rs *runState, tab string, s Step) string {
	name := func() string {
		if ri, ok := rs.refInfo(tab, s.Ref); ok {
			return "'" + clip(ri.label(), 50) + "'"
		}
		return s.Ref
	}
	switch s.Op {
	case OpNavigate:
		return "Opening " + clip(strings.TrimPrefix(strings.TrimPrefix(normalizeNavURL(s.URL), "https://"), "http://"), 70)
	case OpClick:
		return "Clicking " + name()
	case OpHover:
		return "Hovering over " + name()
	case OpType:
		if ri, ok := rs.refInfo(tab, s.Ref); ok && reSecret.MatchString(ri.Role+" "+ri.Name+" "+ri.Extra) {
			return "Typing into " + name()
		}
		return fmt.Sprintf("Typing '%s' into %s", clip(s.Text, 30), name())
	case OpPress:
		return "Pressing " + s.Key
	case OpScroll:
		d := s.Dir
		if d == "" {
			d = "down"
		}
		return "Scrolling " + d
	case OpWait:
		if s.Text != "" {
			return "Waiting for '" + clip(s.Text, 40) + "'"
		}
		return "Waiting"
	case OpSelect:
		return fmt.Sprintf("Selecting '%s' in %s", clip(s.Value, 30), name())
	case OpBack:
		return "Going back"
	case OpForward:
		return "Going forward"
	case OpExtract:
		if s.Ref != "" {
			return "Reading text of " + name()
		}
		return "Reading the page text"
	case OpScreenshot:
		return "Taking a screenshot"
	case OpExpect:
		return "Checking " + s.Kind
	}
	return s.Op
}

// confirm asks the user and records the decision.
func (a *Agent) confirm(ctx context.Context, rs *runState, kind ConfirmKind, summary, pageURL, detail string) bool {
	req := ConfirmRequest{RunID: rs.id, Kind: kind, Summary: summary, URL: pageURL, Detail: detail}
	rs.event(Event{Kind: EventConfirm, Text: summary, Confirm: &req})
	ok, err := a.be.Confirm(ctx, req)
	if err != nil {
		a.cfg.logf("confirm error: %v", err)
		return false
	}
	return ok
}

const declinedMsg = "The user declined this action. Do not retry it or look for another way to get the same effect; tell the user what you could not do."

func (a *Agent) execAct(ctx context.Context, rs *runState, c *call) *toolOut {
	in, err := parseAct(c.input, a.cfg.MaxStepsPerAct)
	a.setNote(in.Note) // keep the memory even if the steps are bad
	if err != nil {
		return errOut(err.Error())
	}
	tab, err := a.resolveTab(rs, in.Tab)
	if err != nil {
		return errOut(err.Error())
	}
	cur := a.be.URL(tab)
	if isInternalURL(cur) || !a.vis.Allowed(cur) {
		if in.Steps[0].Op != OpNavigate {
			if isInternalURL(cur) {
				return errOut("This tab shows an internal browser page that you cannot use. Start with a navigate step to a website.")
			}
			return errOut(hiddenText + " The user has not allowed you to see this page. Navigate elsewhere, switch tab, or ask the user to allow it.")
		}
	}
	navs := 0
	for i := range in.Steps {
		s := &in.Steps[i]
		s.Tab = tab
		switch s.Op {
		case OpNavigate:
			s.URL = resolveAgainst(cur, s.URL)
			s.URL = normalizeNavURL(s.URL)
			if err := checkNavigable(s.URL); err != nil {
				return errOut(fmt.Sprintf("step %d (navigate): %v", i+1, err))
			}
			navs++
		case OpBack, OpForward:
			navs++
		}
	}
	if err := rs.safety.charge(len(in.Steps), navs); err != nil {
		return errOut(err.Error())
	}
	for _, s := range in.Steps {
		rs.event(Event{Kind: EventAction, Tool: toolAct, Text: a.label(rs, tab, s)})
	}

	var results []StepResult
	var extracts []StepResult
	okAll := true
	var shot []byte
	stoppedByUser := false

	runSeg := func(seg []Step) bool {
		if len(seg) == 0 {
			return true
		}
		res, err := a.be.Act(ctx, seg)
		if err != nil {
			results = append(results, StepResult{Index: len(results) + 1, Op: seg[0].Op, Error: err.Error()})
			return false
		}
		for _, sr := range res.Steps {
			sr.Index = len(results) + 1
			results = append(results, sr)
			if sr.Op == OpExtract && sr.OK {
				extracts = append(extracts, sr)
			}
		}
		return res.OK
	}

	var seg []Step
	for _, s := range in.Steps {
		if risk, target := a.assess(rs, tab, s); risk != nil {
			if !runSeg(seg) {
				okAll = false
				seg = nil
				break
			}
			seg = nil
			summary := a.label(rs, tab, s)
			if risk.Kind == ConfirmCrossDomain {
				summary = fmt.Sprintf("Leave this site and open %s", target)
			}
			if !a.confirm(ctx, rs, risk.Kind, summary, a.be.URL(tab), risk.Reason) {
				results = append(results, StepResult{Index: len(results) + 1, Op: s.Op, Error: "declined by the user"})
				okAll, stoppedByUser = false, true
				seg = nil
				break
			}
			if risk.Kind == ConfirmCrossDomain {
				rs.safety.trust(target)
			}
		}
		if s.Op == OpScreenshot {
			if !runSeg(seg) {
				okAll = false
				seg = nil
				break
			}
			seg = nil
			u := a.be.URL(tab)
			if !a.vis.Allowed(u) {
				results = append(results, StepResult{Index: len(results) + 1, Op: s.Op, Error: hiddenText})
				okAll = false
				break
			}
			png, err := a.be.Screenshot(tab)
			if err != nil {
				results = append(results, StepResult{Index: len(results) + 1, Op: s.Op, Error: err.Error()})
				okAll = false
				break
			}
			shot = png
			results = append(results, StepResult{Index: len(results) + 1, Op: s.Op, OK: true})
			continue
		}
		seg = append(seg, s)
	}
	if okAll {
		okAll = runSeg(seg)
	}
	for _, r := range results {
		if !r.OK {
			okAll = false
		}
	}

	finalURL := a.be.URL(tab)
	hidden := !a.vis.Allowed(finalURL)

	var out toolOut
	out.isErr = !okAll
	var sb strings.Builder
	done := 0
	for _, r := range results {
		if r.OK {
			done++
		}
	}
	if okAll {
		fmt.Fprintf(&sb, "ok: %d/%d steps\n", done, len(in.Steps))
	} else {
		fmt.Fprintf(&sb, "FAILED: %d/%d steps completed\n", done, len(in.Steps))
	}
	for _, r := range results {
		desc := r.Op
		if r.Index-1 < len(in.Steps) {
			desc = stepDesc(in.Steps[r.Index-1])
		}
		if r.OK {
			fmt.Fprintf(&sb, "%d ok %s\n", r.Index, desc)
		} else {
			fmt.Fprintf(&sb, "%d ERR %s: %s\n", r.Index, desc, clip(escapeUntrusted(r.Error), 300))
		}
	}
	if len(results) < len(in.Steps) {
		fmt.Fprintf(&sb, "steps %d-%d were not run\n", len(results)+1, len(in.Steps))
	}
	if stoppedByUser {
		sb.WriteString(declinedMsg + "\n")
	}
	out.parts = append(out.parts, &part{kind: partText, text: strings.TrimRight(sb.String(), "\n")})

	for _, e := range extracts {
		if hidden {
			out.parts = append(out.parts, &part{kind: partText, text: hiddenText})
			continue
		}
		txt := e.Output
		if len(txt) > a.cfg.ExtractMaxChars {
			txt = txt[:a.cfg.ExtractMaxChars] + fmt.Sprintf("\n[truncated at %d of %d characters]", a.cfg.ExtractMaxChars, len(e.Output))
		}
		rs.safety.taint()
		a.cvMu.Lock()
		p := a.cv.newExtract(tab, finalURL, wrapUntrusted("extract", tab, finalURL, txt), "")
		a.cvMu.Unlock()
		out.parts = append(out.parts, p)
	}
	if shot != nil && !hidden {
		a.cvMu.Lock()
		p := a.cv.newImage(tab, finalURL, base64.StdEncoding.EncodeToString(shot))
		a.cvMu.Unlock()
		out.parts = append(out.parts, p)
	}

	if in.Observe != "none" && !stoppedByUser {
		force := in.Observe == "full"
		for _, s := range in.Steps {
			if s.Op == OpNavigate || s.Op == OpBack || s.Op == OpForward {
				force = true
			}
		}
		if p := a.observe(ctx, rs, tab, force); p != nil {
			out.parts = append(out.parts, p)
		}
	}
	return &out
}

// assess applies the content-based risk rules and the cross-domain rule to a
// step. It returns the risk and, for cross-domain moves, the target domain.
func (a *Agent) assess(rs *runState, tab string, s Step) (*Risk, string) {
	cur := a.be.URL(tab)
	ri, known := rs.refInfo(tab, s.Ref)
	if r := classifyStep(s, ri, known, cur); r != nil {
		return r, ""
	}
	var target string
	switch s.Op {
	case OpNavigate:
		target = s.URL
	case OpClick:
		if known && ri.Href != "" {
			if base, err := url.Parse(cur); err == nil {
				if ref, err := url.Parse(ri.Href); err == nil {
					target = base.ResolveReference(ref).String()
				}
			}
		}
	}
	if target != "" {
		if cd, dom := rs.safety.crossDomain(cur, target); cd {
			return &Risk{ConfirmCrossDomain, "leaves the current site for " + dom + " after reading page content"}, dom
		}
	}
	return nil, ""
}

// observe takes a fresh snapshot of tab and returns the conversation part:
// a full snapshot the first time (or when forced / the page changed / the
// baseline was elided), a delta against what the model already holds
// otherwise. It never returns page content for pages the user hid.
func (a *Agent) observe(ctx context.Context, rs *runState, tab string, forceFull bool) *part {
	pageURL := a.be.URL(tab)
	if !a.vis.Allowed(pageURL) {
		return &part{kind: partText, text: hiddenText}
	}
	raw, err := a.be.Snapshot(tab, false)
	if err != nil {
		return &part{kind: partText, text: "Could not read the page: " + escapeUntrusted(clip(err.Error(), 200))}
	}
	pageURL = a.be.URL(tab)
	if !a.vis.Allowed(pageURL) { // navigated to a hidden page while reading
		return &part{kind: partText, text: hiddenText}
	}
	trunc, _ := TruncateSnapshot(raw, a.cfg.SnapshotMaxTokens)

	a.cvMu.Lock()
	bl := a.cv.baselineFor(tab)
	full := forceFull || !bl.valid || bl.url != pageURL
	text := trunc
	if !full {
		if d := DeltaText(bl.text, trunc); len(d)*10 <= len(trunc)*6 {
			text = d
		} else {
			full = true
		}
	}
	bl.text, bl.url, bl.valid = trunc, pageURL, true
	kind := "snapshot"
	if !full {
		kind = "delta"
	}
	p := a.cv.newObservation(tab, pageURL, wrapUntrusted(kind, tab, pageURL, text), full)
	a.cvMu.Unlock()

	rs.learnRefs(tab, text, full)
	rs.safety.taint()
	rs.stats.obsKind(full)
	return p
}

// ---- tabs ---------------------------------------------------------------------

func (a *Agent) tabsText(tabs []TabInfo, cur string) string {
	var sb strings.Builder
	for _, t := range tabs {
		mark := ""
		if t.ID == cur {
			mark = " (current)"
		}
		switch {
		case isInternalURL(t.URL):
			fmt.Fprintf(&sb, "%s%s [internal page]\n", t.ID, mark)
		case !a.vis.Allowed(t.URL):
			fmt.Fprintf(&sb, "%s%s %s\n", t.ID, mark, hiddenText)
		default:
			fmt.Fprintf(&sb, "%s%s %s %q\n", t.ID, mark, clip(t.URL, 200), clip(strings.Join(strings.Fields(t.Title), " "), 80))
		}
	}
	if sb.Len() == 0 {
		return "(no tabs)\n"
	}
	return sb.String()
}

func (a *Agent) execTabs(ctx context.Context, rs *runState, c *call) *toolOut {
	in, err := parseTabs(c.input)
	if err != nil {
		return errOut(err.Error())
	}
	listOut := func(prefix string, extra ...*part) *toolOut {
		txt := wrapUntrusted("tabs", "", "", a.tabsText(a.be.Tabs(), rs.tab()))
		o := &toolOut{parts: []*part{{kind: partText, text: prefix + txt}}}
		o.parts = append(o.parts, extra...)
		return o
	}
	switch in.Action {
	case "list":
		rs.event(Event{Kind: EventAction, Tool: toolTabs, Text: "Listing tabs"})
		return listOut("")
	case "open":
		u := normalizeNavURL(in.URL)
		if err := checkNavigable(u); err != nil {
			return errOut(err.Error())
		}
		if err := rs.safety.charge(1, 1); err != nil {
			return errOut(err.Error())
		}
		if cd, dom := rs.safety.crossDomain(a.be.URL(rs.tab()), u); cd {
			if !a.confirm(ctx, rs, ConfirmCrossDomain, "Open "+dom+" in a new tab", u, "opens another site after reading page content") {
				return errOut(declinedMsg)
			}
			rs.safety.trust(u)
		}
		rs.event(Event{Kind: EventAction, Tool: toolTabs, Text: "Opening a new tab: " + clip(strings.TrimPrefix(u, "https://"), 60)})
		ti, err := a.be.OpenTab(u)
		if ti.ID == "" {
			if err == nil {
				err = fmt.Errorf("could not open a tab")
			}
			return errOut(err.Error())
		}
		a.openedMu.Lock()
		a.opened[ti.ID] = true
		a.openedMu.Unlock()
		rs.setTab(ti.ID)
		prefix := ""
		if err != nil {
			prefix = "The tab opened but loading failed: " + escapeUntrusted(clip(err.Error(), 200)) + "\n"
		}
		o := listOut(prefix, a.observe(ctx, rs, ti.ID, true))
		o.isErr = err != nil
		return o
	case "switch":
		if !a.tabExists(in.ID) {
			return errOut("No such tab " + in.ID + ". Use tabs list.")
		}
		if err := a.be.SwitchTab(in.ID); err != nil {
			return errOut(err.Error())
		}
		rs.setTab(in.ID)
		rs.event(Event{Kind: EventAction, Tool: toolTabs, Text: "Switching tab"})
		return listOut("", a.observe(ctx, rs, in.ID, true))
	case "close":
		a.openedMu.Lock()
		mine := a.opened[in.ID]
		a.openedMu.Unlock()
		if !mine {
			return errOut("You may only close tabs that you opened. Tab " + in.ID + " belongs to the user.")
		}
		rs.event(Event{Kind: EventAction, Tool: toolTabs, Text: "Closing a tab"})
		if err := a.be.CloseTab(in.ID); err != nil {
			return errOut(err.Error())
		}
		a.openedMu.Lock()
		delete(a.opened, in.ID)
		a.openedMu.Unlock()
		a.cvMu.Lock()
		delete(a.cv.baselines, in.ID)
		a.cvMu.Unlock()
		if rs.tab() == in.ID {
			rs.setTab("")
			for _, t := range a.be.Tabs() {
				if t.Active {
					rs.setTab(t.ID)
				}
			}
		}
		return listOut("")
	}
	return errOut("unknown action")
}

func (a *Agent) tabExists(id string) bool {
	for _, t := range a.be.Tabs() {
		if t.ID == id {
			return true
		}
	}
	return false
}

// ---- read_page / find ---------------------------------------------------------------

func (a *Agent) execReadPage(ctx context.Context, rs *runState, c *call) *toolOut {
	var in readPageInput
	if err := json.Unmarshal(c.input, &in); err != nil {
		return errOut("invalid read_page input: " + err.Error())
	}
	tab, err := a.resolveTab(rs, in.Tab)
	if err != nil {
		return errOut(err.Error())
	}
	if u := a.be.URL(tab); isInternalURL(u) {
		return errOut("This tab shows an internal browser page that you cannot read.")
	}
	rs.event(Event{Kind: EventAction, Tool: toolReadPage, Text: "Reading the page"})
	return &toolOut{parts: []*part{a.observe(ctx, rs, tab, in.Mode == "full")}}
}

func (a *Agent) execFind(ctx context.Context, rs *runState, c *call) *toolOut {
	var in findInput
	if err := json.Unmarshal(c.input, &in); err != nil {
		return errOut("invalid find input: " + err.Error())
	}
	if strings.TrimSpace(in.Query) == "" && in.Role == "" {
		return errOut("find needs a query or a role")
	}
	tab, err := a.resolveTab(rs, in.Tab)
	if err != nil {
		return errOut(err.Error())
	}
	u := a.be.URL(tab)
	if isInternalURL(u) {
		return errOut("This tab shows an internal browser page that you cannot search.")
	}
	if !a.vis.Allowed(u) {
		return textOut(hiddenText)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 60 {
		limit = 60
	}
	rs.event(Event{Kind: EventAction, Tool: toolFind, Text: "Looking for '" + clip(in.Query, 40) + "'"})
	raw, err := a.be.Snapshot(tab, false)
	if err != nil {
		return errOut("Could not read the page: " + clip(err.Error(), 200))
	}
	if !a.vis.Allowed(a.be.URL(tab)) {
		return textOut(hiddenText)
	}
	rs.learnRefs(tab, raw, false)
	q := strings.ToLower(strings.TrimSpace(in.Query))
	var rolePat *regexp.Regexp
	if in.Role != "" {
		rolePat = regexp.MustCompile(`^\s*- ` + regexp.QuoteMeta(strings.ToLower(in.Role)) + `\b`)
	}
	var sb strings.Builder
	n, total := 0, 0
	for _, line := range splitLines(raw) {
		if rolePat != nil && !rolePat.MatchString(strings.ToLower(line)) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(line), q) {
			continue
		}
		total++
		if n < limit {
			sb.WriteString(clip(line, maxLineChars))
			sb.WriteByte('\n')
			n++
		}
	}
	if total == 0 {
		sb.WriteString("(no matches)\n")
	} else if total > n {
		fmt.Fprintf(&sb, "[%d more matches not shown; refine the query]\n", total-n)
	}
	rs.safety.taint()
	return textOut(wrapUntrusted("find", tab, u, sb.String()))
}

// ---- ask_user / done ------------------------------------------------------------------

func (a *Agent) execAsk(ctx context.Context, rs *runState, c *call) *toolOut {
	var in askInput
	if err := json.Unmarshal(c.input, &in); err != nil || strings.TrimSpace(in.Question) == "" {
		return errOut("ask_user needs a question")
	}
	rs.event(Event{Kind: EventAction, Tool: toolAskUser, Text: "Asking you a question"})
	if asker, ok := a.be.(UserAsker); ok {
		ans, err := asker.Ask(ctx, in.Question, in.Options)
		if err != nil {
			return errOut("The user could not be asked: " + err.Error())
		}
		return textOut("The user answered: " + ans)
	}
	q := in.Question
	if len(in.Options) > 0 {
		q += "\nOptions: " + strings.Join(in.Options, " / ")
	}
	rs.finish("ask_user", q, true)
	return textOut("The question was shown to the user. This run ends here; their answer arrives as the next message.")
}

func (a *Agent) execDone(rs *runState, c *call) *toolOut {
	var in doneInput
	if err := json.Unmarshal(c.input, &in); err != nil {
		return errOut("invalid done input: " + err.Error())
	}
	ok := in.Success == nil || *in.Success
	rs.finish("done", strings.TrimSpace(in.Summary), ok)
	return textOut("ok")
}
