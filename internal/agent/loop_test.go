package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- scripting helpers --------------------------------------------------------------

type scriptTurn func(s *sw)

func scripted(turns ...scriptTurn) func(n int, s *sw, body []byte) {
	return func(n int, s *sw, body []byte) {
		if n > len(turns) {
			turns[len(turns)-1](s)
			return
		}
		turns[n-1](s)
	}
}

type tc struct{ id, name, input string }

// toolTurn emits an assistant turn with optional text/thinking and tool calls.
func toolTurn(text string, think bool, calls ...tc) scriptTurn {
	return func(s *sw) {
		s.std()
		idx := 0
		if think {
			s.thinking(idx, "I should act", fmt.Sprintf("sig-%d", s.n))
			idx++
		}
		if text != "" {
			s.text(idx, text)
			idx++
		}
		for _, c := range calls {
			s.toolJSON(idx, c.id, c.name, c.input)
			idx++
		}
		s.end("tool_use", 50)
	}
}

func textTurn(text string) scriptTurn {
	return func(s *sw) { s.std(); s.text(0, text); s.end("end_turn", 20) }
}

func doneTurn(summary string) scriptTurn {
	return toolTurn("", false, tc{"toolu_done", "done", fmt.Sprintf(`{"summary":%q,"success":true}`, summary)})
}

func newAgent(t *testing.T, f *fakeAPI, be Backend, mut ...func(*Config)) *Agent {
	t.Helper()
	cfg := f.cfg()
	for _, m := range mut {
		m(&cfg)
	}
	ag, err := New(cfg, be)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ag.Close)
	return ag
}

// toolResultTexts returns the text of every tool_result content part, in order.
func toolResultTexts(p *parsedReq) []string {
	var out []string
	for _, m := range p.Messages {
		for _, b := range m.Content {
			if b["type"] != "tool_result" {
				continue
			}
			if arr, ok := b["content"].([]any); ok {
				for _, e := range arr {
					if mm, ok := e.(map[string]any); ok {
						if s, ok := mm["text"].(string); ok {
							out = append(out, s)
						}
					}
				}
			}
		}
	}
	return out
}

// validateHistory checks the structural rules of the Messages API.
func validateHistory(t *testing.T, p *parsedReq) {
	t.Helper()
	if len(p.Messages) == 0 || p.Messages[0].Role != "user" {
		t.Fatalf("history must start with a user message")
	}
	last := p.Messages[len(p.Messages)-1]
	if last.Role != "user" {
		t.Fatalf("history must end with a user message (no prefill), got %s", last.Role)
	}
	for i, m := range p.Messages {
		if i > 0 && m.Role == p.Messages[i-1].Role {
			t.Fatalf("messages %d and %d have the same role %s", i-1, i, m.Role)
		}
		if len(m.Content) == 0 {
			t.Fatalf("message %d is empty", i)
		}
		if m.Role == "assistant" {
			var ids []string
			for _, b := range m.Content {
				if b["type"] == "text" && strings.TrimSpace(fmt.Sprint(b["text"])) == "" {
					t.Fatalf("assistant message %d has an empty text block", i)
				}
				if b["type"] == "tool_use" {
					ids = append(ids, b["id"].(string))
				}
			}
			if len(ids) > 0 {
				if i+1 >= len(p.Messages) {
					t.Fatalf("tool_use without a following user message")
				}
				next := p.Messages[i+1]
				seenOther := false
				got := map[string]bool{}
				for _, b := range next.Content {
					if b["type"] == "tool_result" {
						if seenOther {
							t.Fatalf("tool_result after other content in message %d", i+1)
						}
						got[b["tool_use_id"].(string)] = true
					} else {
						seenOther = true
					}
				}
				for _, id := range ids {
					if !got[id] {
						t.Fatalf("tool_use %s has no tool_result", id)
					}
				}
			}
		}
	}
}

// bpPositions returns the flat (message, block) positions that carry cache_control.
func bpPositions(p *parsedReq) [][2]int {
	var out [][2]int
	for mi, m := range p.Messages {
		for bi, b := range m.Content {
			if _, ok := b["cache_control"]; ok {
				out = append(out, [2]int{mi, bi})
			}
		}
	}
	return out
}

func stripCC(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k == "cache_control" {
				continue
			}
			out[k] = stripCC(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripCC(e)
		}
		return out
	}
	return v
}

// canonPrefix serializes messages[0..bp] inclusive (cache_control removed).
func canonPrefix(t *testing.T, body []byte, bp [2]int) string {
	t.Helper()
	var raw struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []any  `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for mi := 0; mi <= bp[0]; mi++ {
		m := raw.Messages[mi]
		c := m.Content
		if mi == bp[0] {
			c = c[:bp[1]+1]
		}
		b, _ := json.Marshal(stripCC(map[string]any{"role": m.Role, "content": c}))
		sb.Write(b)
	}
	return sb.String()
}

func rawField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return string(m[key])
}

// ---- tests ----------------------------------------------------------------------------

func TestRequestShapeRespectsHaikuRules(t *testing.T) {
	f := newFakeAPI(t, scripted(textTurn("hi")))
	ag := newAgent(t, f, newFakeBE())
	if _, _, err := runCollect(t, ag, "say hi"); err != nil {
		t.Fatal(err)
	}
	body := f.requests()[0]
	p := parseReq(t, body)
	if p.Model != "claude-haiku-5-5" || !p.Stream || p.MaxTokens <= 0 {
		t.Fatalf("model/stream/max_tokens: %+v", p)
	}
	for _, banned := range []string{"temperature", "top_p", "top_k"} {
		if _, ok := p.raw[banned]; ok {
			t.Fatalf("%s must not be sent", banned)
		}
	}
	if p.Thinking["type"] != "adaptive" {
		t.Fatalf("thinking: %v", p.Thinking)
	}
	if !strings.Contains(string(p.raw["output_config"]), `"effort":"low"`) {
		t.Fatalf("output_config: %s", p.raw["output_config"])
	}
	validateHistory(t, p)
	if len(p.Tools) != 6 {
		t.Fatalf("tools: %d", len(p.Tools))
	}
	for _, tool := range p.Tools {
		if tool["eager_input_streaming"] != true {
			t.Fatalf("tool %v lacks eager_input_streaming", tool["name"])
		}
	}
}

func TestToolDefinitionsAndPrefixSize(t *testing.T) {
	var arr []map[string]any
	if err := json.Unmarshal(toolsJSON, &arr); err != nil {
		t.Fatal(err)
	}
	prev := ""
	for _, a := range arr {
		n := a["name"].(string)
		if n <= prev {
			t.Fatalf("tools must be sorted by name: %s after %s", n, prev)
		}
		prev = n
		if a["eager_input_streaming"] != true {
			t.Fatalf("%s: eager_input_streaming", n)
		}
		if a["input_schema"].(map[string]any)["type"] != "object" {
			t.Fatalf("%s schema", n)
		}
	}
	if strings.Contains(string(toolsJSON), "cache_control") || !strings.Contains(string(toolsJSONCached), "cache_control") {
		t.Fatal("cache_control placement")
	}
	if strings.Count(string(toolsJSONCached), "cache_control") != 1 || !strings.HasSuffix(string(toolsJSONCached), `"cache_control":{"type":"ephemeral"}}]`) {
		t.Fatal("cache breakpoint must be on the last tool only")
	}
	ag, _ := New(Config{APIKey: "k"}, newFakeBE())
	est := ag.PrefixTokenEstimate()
	t.Logf("static prefix: %d bytes, ~%d tokens (tools %d bytes, system %d bytes)", len(ag.cv.head), est, len(toolsJSON), len(systemPrompt))
	if est < 5000 {
		t.Fatalf("static prefix is ~%d tokens; it must comfortably exceed Haiku's 4096-token minimum cacheable prefix", est)
	}
	// determinism: rebuilding gives the same bytes
	cfg := ag.cfg
	if string(buildHead(&cfg)) != string(ag.cv.head) {
		t.Fatal("head is not deterministic")
	}
	for _, bad := range []string{"time.Now", "rand"} {
		if strings.Contains(systemPrompt, bad) {
			t.Fatalf("system prompt looks dynamic: %s", bad)
		}
	}
}

func TestPrefixByteStableAcrossTurns(t *testing.T) {
	act := func(i int) scriptTurn {
		return toolTurn(fmt.Sprintf("step %d", i), i%2 == 0,
			tc{fmt.Sprintf("toolu_%d", i), "act", actJSON(`[{"op":"click","ref":"e3"},{"op":"type","ref":"e5","text":"q"}]`, fmt.Sprintf(`"note":"goal: test; progress %d"`, i))})
	}
	f := newFakeAPI(t, scripted(act(1), act(2), act(3), act(4), act(5), doneTurn("all done")))
	be := newFakeBE()
	ag := newAgent(t, f, be)
	evs, st, err := runCollect(t, ag, "do the thing on shop.test")
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 6 {
		t.Fatalf("expected 6 requests, got %d", len(reqs))
	}
	head0 := rawField(t, reqs[0], "tools") + rawField(t, reqs[0], "system")
	var prevStable [2]int
	var prevCanon string
	for i, body := range reqs {
		p := parseReq(t, body)
		validateHistory(t, p)
		if h := rawField(t, body, "tools") + rawField(t, body, "system"); h != head0 {
			t.Fatalf("turn %d: tools/system differ from turn 1", i+1)
		}
		if strings.Contains(string(body), "paused") || strings.Count(string(body), "<run_status") != 1 {
			t.Fatalf("turn %d: the volatile tail must appear exactly once", i+1)
		}
		total := strings.Count(string(body), `"cache_control"`)
		bps := bpPositions(p)
		wantMsgBPs := 1
		if i > 0 {
			wantMsgBPs = 2
		}
		if total > 4 || len(bps) != wantMsgBPs || total != 2+wantMsgBPs {
			t.Fatalf("turn %d: %d breakpoints total, %d in messages (want %d)", i+1, total, len(bps), wantMsgBPs)
		}
		stable := bps[len(bps)-1]
		// the volatile tail must come after the last breakpoint, in the last message
		lastMsg := p.Messages[len(p.Messages)-1]
		if stable[0] != len(p.Messages)-1 || stable[1] != len(lastMsg.Content)-2 {
			t.Fatalf("turn %d: last breakpoint %v should precede the tail block (msg %d, %d blocks)", i+1, stable, len(p.Messages)-1, len(lastMsg.Content))
		}
		if !strings.Contains(fmt.Sprint(lastMsg.Content[len(lastMsg.Content)-1]["text"]), "<run_status") {
			t.Fatalf("turn %d: tail block missing", i+1)
		}
		if i > 0 {
			// the earlier breakpoint must be exactly where the previous request wrote its cache
			if bps[0] != prevStable {
				t.Fatalf("turn %d: read breakpoint %v != previous write breakpoint %v", i+1, bps[0], prevStable)
			}
			if got := canonPrefix(t, body, bps[0]); got != prevCanon {
				t.Fatalf("turn %d: prefix before the rolling breakpoint changed between turns\n got: %.300s\nwant: %.300s", i+1, got, prevCanon)
			}
		}
		prevStable = stable
		prevCanon = canonPrefix(t, body, stable)
	}
	// Task memory lives in the last user message only, never in the system prefix.
	if strings.Contains(rawField(t, reqs[5], "system"), "progress 5") || strings.Contains(rawField(t, reqs[5], "tools"), "progress 5") {
		t.Fatal("task memory leaked into the static prefix")
	}
	p6 := parseReq(t, reqs[5])
	lm := p6.Messages[len(p6.Messages)-1]
	if !strings.Contains(fmt.Sprint(lm.Content[len(lm.Content)-1]["text"]), "progress 5") {
		t.Fatal("latest note should be in the tail of the last user message")
	}
	if findEvent(evs, EventDone) == nil || st.Stop != "done" || st.Turns != 6 {
		t.Fatalf("stats %+v", st)
	}
	// stats and cache accounting
	if st.Requests != 6 || st.Usage.CacheReadTokens == 0 || st.CacheHitRate < 0.5 {
		t.Fatalf("cache stats %+v", st)
	}
	if cs := ag.CacheStats(); cs.Requests != 6 || cs.HitRate() <= 0 {
		t.Fatalf("client cache stats %+v", cs)
	}
}

func TestThinkingBlocksReplayedUnchanged(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", true, tc{"t1", "act", actJSON(`[{"op":"click","ref":"e3"}]`)}),
		doneTurn("ok")))
	ag := newAgent(t, f, newFakeBE())
	if _, _, err := runCollect(t, ag, "go"); err != nil {
		t.Fatal(err)
	}
	p := parseReq(t, f.requests()[1])
	am := p.Messages[1]
	if am.Role != "assistant" || am.Content[0]["type"] != "thinking" || am.Content[0]["signature"] != "sig-1" || am.Content[0]["thinking"] != "I should act" {
		t.Fatalf("thinking block must be replayed verbatim first in the assistant turn: %v", am.Content)
	}
}

func TestStripThinkingRecovery(t *testing.T) {
	var rejected atomic.Bool
	f := newFakeAPI(t, nil)
	f.handler = func(n int, s *sw, body []byte) {
		if n == 2 && !rejected.Load() {
			rejected.Store(true)
			s.w.WriteHeader(400)
			fmt.Fprint(s.w, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking blocks in the latest assistant turn cannot be modified"}}`)
			return
		}
		scripted(
			toolTurn("", true, tc{"t1", "act", actJSON(`[{"op":"click","ref":"e3"}]`)}),
			doneTurn("recovered"),
		)(n, s, body)
	}
	ag := newAgent(t, f, newFakeBE())
	_, st, err := runCollect(t, ag, "go")
	if err != nil || st.Stop != "done" {
		t.Fatalf("err=%v stats=%+v", err, st)
	}
	reqs := f.requests()
	last := parseReq(t, reqs[len(reqs)-1])
	for _, m := range last.Messages {
		for _, b := range m.Content {
			if b["type"] == "thinking" {
				t.Fatal("thinking should have been stripped after the 400")
			}
		}
	}
	// a stripped history with thinking still on is rejected again (the turn that owns pending tool results
	// must start with a thinking block): the rest of the run asks for no thinking
	if !strings.Contains(string(reqs[len(reqs)-1]), `"thinking":{"type":"disabled"}`) {
		t.Fatal("thinking should be disabled after stripping it")
	}
}

func TestDeltaObservationsAfterFirstFull(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"t1", "act", actJSON(`[{"op":"click","ref":"e3"}]`)}),
		toolTurn("", false, tc{"t2", "act", actJSON(`[{"op":"click","ref":"e3"}]`)}),
		toolTurn("", false, tc{"t3", "act", actJSON(`[{"op":"click","ref":"e3"}]`, `"observe":"none"`)}),
		toolTurn("", false, tc{"t4", "act", actJSON(`[{"op":"click","ref":"e3"}]`)}),
		doneTurn("ok")))
	ag := newAgent(t, f, newFakeBE())
	_, st, err := runCollect(t, ag, "click add to cart a few times")
	if err != nil {
		t.Fatal(err)
	}
	texts := toolResultTexts(parseReq(t, f.requests()[4]))
	var kinds []string
	var sizes []int
	for _, x := range texts {
		switch {
		case strings.Contains(x, `kind="snapshot"`):
			kinds = append(kinds, "full")
			sizes = append(sizes, len(x))
		case strings.Contains(x, `kind="delta"`):
			kinds = append(kinds, "delta")
			sizes = append(sizes, len(x))
			if !strings.Contains(x, DeltaPrefix) || !strings.Contains(x, "clicks=") {
				t.Fatalf("delta should show the changed status line: %s", x)
			}
		}
	}
	if fmt.Sprint(kinds) != "[full delta delta]" { // t3 used observe:none
		t.Fatalf("observation kinds %v", kinds)
	}
	if sizes[1]*10 > sizes[0] {
		t.Fatalf("delta (%d bytes) should be far smaller than the full snapshot (%d bytes)", sizes[1], sizes[0])
	}
	if st.FullSnapshots != 1 || st.DeltaSnapshots != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestNavigationForcesFullSnapshot(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"t1", "act", actJSON(`[{"op":"click","ref":"e3"}]`)}),
		toolTurn("", false, tc{"t2", "act", actJSON(`[{"op":"navigate","url":"shop.test/other"}]`)}),
		doneTurn("ok")))
	ag := newAgent(t, f, newFakeBE())
	_, st, err := runCollect(t, ag, "browse shop.test")
	if err != nil {
		t.Fatal(err)
	}
	if st.FullSnapshots != 2 || st.DeltaSnapshots != 0 {
		t.Fatalf("a navigation must be answered with a full snapshot: %+v", st)
	}
}

func TestEarlyDispatchStartsToolBeforeMessageStop(t *testing.T) {
	gate := make(chan struct{})
	f := newFakeAPI(t, nil)
	f.handler = func(n int, s *sw, body []byte) {
		if n == 1 {
			s.std()
			s.text(0, "On it. ")
			s.toolJSON(1, "t1", "act", actJSON(`[{"op":"click","ref":"e3"}]`))
			// the model "keeps talking": message_stop is withheld
			s.flushWait(gate)
			s.text(2, "more text after the tool call")
			s.end("tool_use", 99)
			return
		}
		doneTurn("fin")(s)
	}
	be := newFakeBE()
	started := make(chan struct{})
	var once sync.Once
	be.onAct = func(ctx context.Context, steps []Step) { once.Do(func() { close(started) }) }
	ag := newAgent(t, f, be)
	type res struct {
		st  RunStats
		err error
	}
	done := make(chan res, 1)
	go func() {
		st, err := ag.Run(context.Background(), "go", nil)
		done <- res{st, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatal("tool did not start before message_stop (no early dispatch)")
	}
	select {
	case <-done:
		t.Fatal("run finished while the stream was still open")
	default:
	}
	close(gate)
	r := <-done
	if r.err != nil || r.st.Stop != "done" {
		t.Fatalf("%v %+v", r.err, r.st)
	}
}

// flushWait blocks the handler until gate is closed (used to withhold message_stop).
func (s *sw) flushWait(gate chan struct{}) { s.f.Flush(); <-gate }

func TestIndependentReadOnlyToolsRunInParallelAndWritesAreOrdered(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false,
			tc{"f1", "find", `{"query":"Add to cart"}`},
			tc{"f2", "find", `{"query":"Place order"}`}),
		toolTurn("", false,
			tc{"a1", "act", actJSON(`[{"op":"click","ref":"e3"}]`, `"observe":"none"`)},
			tc{"r1", "read_page", `{}`}),
		doneTurn("ok")))
	be := newFakeBE()
	var inside atomic.Int32
	var maxInside atomic.Int32
	var actDone atomic.Bool
	var order []string
	var omu sync.Mutex
	be.onSnap = func(tab string) {
		n := inside.Add(1)
		for {
			m := maxInside.Load()
			if n <= m || maxInside.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		inside.Add(-1)
		omu.Lock()
		order = append(order, fmt.Sprintf("snap(actDone=%v)", actDone.Load()))
		omu.Unlock()
	}
	be.actDelay = 80 * time.Millisecond
	be.onAct = func(ctx context.Context, steps []Step) {
		time.AfterFunc(be.actDelay+20*time.Millisecond, func() { actDone.Store(true) })
	}
	ag := newAgent(t, f, be)
	_, _, err := runCollect(t, ag, "look around shop.test")
	if err != nil {
		t.Fatal(err)
	}
	if maxInside.Load() < 2 {
		t.Fatalf("two find calls should overlap (max concurrent snapshots %d)", maxInside.Load())
	}
	omu.Lock()
	defer omu.Unlock()
	// the last snapshot belongs to read_page, which must wait for the act before it
	if last := order[len(order)-1]; last != "snap(actDone=true)" {
		t.Fatalf("read_page ran before the preceding act finished: %v", order)
	}
}

func TestConfirmPaymentDeclinedAndApproved(t *testing.T) {
	for _, approve := range []bool{false, true} {
		f := newFakeAPI(t, scripted(
			toolTurn("", false, tc{"r", "read_page", `{}`}),
			toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"type","ref":"e5","text":"shoes"},{"op":"click","ref":"e4"},{"op":"expect","kind":"text","value":"Thanks"}]`)}),
			doneTurn("finished")))
		be := newFakeBE()
		be.confirmAnswer = approve
		ag := newAgent(t, f, be)
		evs, _, err := runCollect(t, ag, "buy shoes on shop.test")
		if err != nil {
			t.Fatal(err)
		}
		if len(be.confirms) != 1 || be.confirms[0].Kind != ConfirmPayment || !strings.Contains(be.confirms[0].Summary, "Place order") {
			t.Fatalf("confirms: %+v", be.confirms)
		}
		if findEvent(evs, EventConfirm) == nil {
			t.Fatal("a confirm event must be emitted for the UI")
		}
		acts := be.acts()
		res := toolResultTexts(parseReq(t, f.requests()[2]))
		joined := strings.Join(res, "\n")
		if approve {
			if len(acts) != 2 || len(acts[0]) != 1 || acts[0][0].Op != OpType || len(acts[1]) != 2 || acts[1][0].Op != OpClick {
				t.Fatalf("approved: steps must be split around the confirmation: %+v", acts)
			}
		} else {
			if len(acts) != 1 || len(acts[0]) != 1 {
				t.Fatalf("declined: the risky step must not reach the browser: %+v", acts)
			}
			if !strings.Contains(joined, "declined") {
				t.Fatalf("declined result missing: %s", joined)
			}
			// is_error must be set
			p := parseReq(t, f.requests()[2])
			found := false
			for _, m := range p.Messages {
				for _, b := range m.Content {
					if b["type"] == "tool_result" && b["is_error"] == true {
						found = true
					}
				}
			}
			if !found {
				t.Fatal("declined action must produce an is_error tool_result")
			}
		}
	}
}

func TestCrossDomainNavigationNeedsConfirmAfterUntrustedRead(t *testing.T) {
	run := func(task string, steps string, wantConfirms int, setup func(*fakeBE)) *fakeBE {
		f := newFakeAPI(t, scripted(
			toolTurn("", false, tc{"r", "read_page", `{}`}),
			toolTurn("", false, tc{"a", "act", actJSON(steps)}),
			doneTurn("ok")))
		be := newFakeBE()
		be.confirmAnswer = false
		if setup != nil {
			setup(be)
		}
		ag := newAgent(t, f, be)
		if _, _, err := runCollect(t, ag, task); err != nil {
			t.Fatal(err)
		}
		if len(be.confirms) != wantConfirms {
			t.Fatalf("%s: confirms %+v want %d", steps, be.confirms, wantConfirms)
		}
		return be
	}
	be := run("look at the shop", `[{"op":"navigate","url":"https://evil.example/pay"}]`, 1, nil)
	if be.confirms[0].Kind != ConfirmCrossDomain || len(be.acts()) != 0 {
		t.Fatalf("%+v acts=%v", be.confirms, be.acts())
	}
	// same registrable domain: no prompt
	run("look at the shop", `[{"op":"navigate","url":"https://www.shop.test/cart"}]`, 0, nil)
	// the user named the domain: trusted
	run("open evil.example for me", `[{"op":"navigate","url":"https://evil.example/pay"}]`, 0, nil)
	// a link whose href leaves the site (e6 -> evil.example)
	run("look at the shop", `[{"op":"click","ref":"e6"}]`, 1, nil)
	// no untrusted text read yet: opening the user's destination is fine
	f := newFakeAPI(t, scripted(toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"navigate","url":"https://other.example/"}]`, `"observe":"none"`)}), doneTurn("ok")))
	be2 := newFakeBE()
	be2.confirmAnswer = false
	if _, _, err := runCollect(t, newAgent(t, f, be2), "go there"); err != nil || len(be2.confirms) != 0 {
		t.Fatalf("untainted navigation should not prompt: %v %+v", err, be2.confirms)
	}
}

func TestSensitiveFieldAndMessageRisks(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"r", "read_page", `{}`}),
		toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"type","ref":"e7","text":"hunter2"}]`)}),
		doneTurn("ok")))
	be := newFakeBE()
	be.confirmAnswer = false
	ag := newAgent(t, f, be)
	evs, _, _ := runCollect(t, ag, "log in to shop.test")
	if len(be.confirms) != 1 || be.confirms[0].Kind != ConfirmCredentials {
		t.Fatalf("typing a password must require approval: %+v", be.confirms)
	}
	for _, e := range evs {
		if e.Kind == EventAction && strings.Contains(e.Text, "hunter2") {
			t.Fatalf("secret leaked into an action label: %q", e.Text)
		}
	}
	// classifier unit checks
	cases := []struct {
		s    Step
		ri   refInfo
		want ConfirmKind
	}{
		{Step{Op: OpClick}, refInfo{Role: "button", Name: "Send message"}, ConfirmMessage},
		{Step{Op: OpClick}, refInfo{Role: "button", Name: "Delete account"}, ConfirmDelete},
		{Step{Op: OpClick}, refInfo{Role: "button", Name: "Change password"}, ConfirmCredentials},
		{Step{Op: OpClick}, refInfo{Role: "button", Name: "Pay now"}, ConfirmPayment},
		{Step{Op: OpClick}, refInfo{Role: "link", Name: "Complete purchase"}, ConfirmPayment},
		{Step{Op: OpType, Text: "4111"}, refInfo{Role: "textbox", Name: "Card number"}, ConfirmPayment},
		{Step{Op: OpClick}, refInfo{Role: "textbox", Name: "Post comment box"}, ""},
		{Step{Op: OpClick}, refInfo{Role: "button", Name: "Add to cart"}, ""},
		{Step{Op: OpClick}, refInfo{Role: "link", Name: "Postal code lookup"}, ""},
		{Step{Op: OpClick}, refInfo{Role: "button", Name: "Search"}, ""},
	}
	for _, c := range cases {
		r := classifyStep(c.s, c.ri, true, "https://x.test/")
		got := ConfirmKind("")
		if r != nil {
			got = r.Kind
		}
		if got != c.want {
			t.Errorf("%+v %+v: got %q want %q", c.s, c.ri, got, c.want)
		}
	}
}

func TestInternalAndDangerousURLsAreDenied(t *testing.T) {
	for _, u := range []string{"chrome://settings", "sweef://settings", "about:config", "file:///etc/passwd", "javascript:alert(1)", "data:text/html,hi", "view-source:https://a.test", "https://user:pw@evil.test/", "ftp://x.test", "devtools://devtools/bundled/inspector.html"} {
		if err := checkNavigable(u); err == nil {
			t.Errorf("%q should be denied", u)
		}
	}
	for _, u := range []string{"https://example.com", "example.com/path", "http://127.0.0.1:8080/x", "localhost:3000", "about:blank"} {
		if err := checkNavigable(u); err != nil {
			t.Errorf("%q should be allowed: %v", u, err)
		}
	}
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"navigate","url":"chrome://settings/passwords"}]`)}),
		doneTurn("ok")))
	be := newFakeBE()
	ag := newAgent(t, f, be)
	if _, _, err := runCollect(t, ag, "x"); err != nil {
		t.Fatal(err)
	}
	if len(be.acts()) != 0 {
		t.Fatal("denied navigation reached the browser")
	}
	if !strings.Contains(strings.Join(toolResultTexts(parseReq(t, f.requests()[1])), " "), "not allowed") {
		t.Fatal("model should be told the navigation is not allowed")
	}
	// internal pages are never shown: tabs list masks them, read_page refuses
	be.tabs["tab-1"].url = "sweef://newtab"
	f2 := newFakeAPI(t, scripted(toolTurn("", false, tc{"a", "tabs", `{"action":"list"}`}, tc{"b", "read_page", `{}`}), doneTurn("ok")))
	ag2 := newAgent(t, f2, be)
	if _, _, err := runCollect(t, ag2, "x"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(toolResultTexts(parseReq(t, f2.requests()[1])), "\n")
	if strings.Contains(joined, "sweef://newtab") || !strings.Contains(joined, "[internal page]") {
		t.Fatalf("internal page leaked: %s", joined)
	}
}

func TestHiddenPagesNeverReachTheModel(t *testing.T) {
	const secret = "TOPSECRET-BALANCE-1234"
	be := newFakeBE()
	be.pages = func(tab *fakeTab) string {
		return "- heading \"Bank\" [level=1 ref=e1]\n- paragraph: " + secret + "\n- button \"Transfer\" [ref=e2]\n"
	}
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"r", "read_page", `{}`}, tc{"f", "find", `{"query":"balance"}`}, tc{"s", "tabs", `{"action":"list"}`}),
		toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"extract"},{"op":"screenshot"}]`)}),
		doneTurn("ok")))
	ag := newAgent(t, f, be)
	ag.SetPageVisibility("https://shop.test", false)
	if _, _, err := runCollect(t, ag, "check my balance"); err != nil {
		t.Fatal(err)
	}
	for i, body := range f.requests() {
		if strings.Contains(string(body), secret) || strings.Contains(string(body), "shop.test/") && strings.Contains(string(body), "Page https://shop.test") {
			t.Fatalf("request %d contains hidden page content or its title", i+1)
		}
	}
	joined := strings.Join(toolResultTexts(parseReq(t, f.requests()[2])), "\n")
	if strings.Count(joined, hiddenText) < 3 {
		t.Fatalf("expected [hidden by user] placeholders, got: %s", joined)
	}
	if len(be.acts()) != 0 {
		t.Fatal("the agent must not act on a page the user hid")
	}
	// allowing it again restores access
	ag.SetPageVisibility("https://shop.test", true)
	if !ag.vis.Allowed("https://shop.test/x") {
		t.Fatal("allow should clear the rule")
	}
	if ag.vis.Allowed("chrome://settings") {
		t.Fatal("internal pages are never visible")
	}
	v := newVisibility()
	v.Set("example.com", false)
	if v.Allowed("https://example.com/a") || v.Allowed("http://example.com:8080/") || !v.Allowed("https://other.com") {
		t.Fatal("bare host rule should cover all schemes and ports")
	}
}

func TestUntrustedDelimiterCannotBeClosedByPage(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"extract","ref":"e1"}]`)}),
		doneTurn("ok")))
	be := newFakeBE()
	ag := newAgent(t, f, be)
	if _, _, err := runCollect(t, ag, "read it"); err != nil {
		t.Fatal(err)
	}
	texts := strings.Join(toolResultTexts(parseReq(t, f.requests()[1])), "\n")
	open := strings.Count(texts, "<untrusted_page_content ")
	closeN := strings.Count(texts, "</untrusted_page_content>")
	if open < 2 || open != closeN {
		t.Fatalf("delimiters unbalanced: %d open %d close\n%s", open, closeN, texts)
	}
	if !strings.Contains(texts, "&lt;/untrusted_page_content") {
		t.Fatal("injected closing tag should have been neutralised")
	}
	if got := wrapUntrusted("x", "t", "https://a\"b\n<c>", "body"); !strings.Contains(got, `url="https://a'b (c)"`) {
		t.Fatalf("attribute injection: %q", got)
	}
}

func TestInvalidToolInputGivesErrorResult(t *testing.T) {
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		switch n {
		case 1: // truncated by max_tokens mid-JSON
			s.std()
			s.tool(0, "bad1", "act", `{"steps":[{"op":"click","re`)
			s.end("max_tokens", 4096)
		case 2: // schema-invalid
			toolTurn("", false, tc{"bad2", "act", `{"steps":[{"op":"click"}]}`}, tc{"bad3", "act", `{"steps":[{"op":"click","ref":"#css > div"}]}`}, tc{"bad4", "act", `{"steps":[{"op":"fly"}]}`}, tc{"bad5", "nonexistent", `{}`})(s)
		default:
			doneTurn("ok")(s)
		}
	})
	be := newFakeBE()
	ag := newAgent(t, f, be)
	_, st, err := runCollect(t, ag, "x")
	if err != nil || st.Stop != "done" {
		t.Fatalf("%v %+v", err, st)
	}
	if len(be.acts()) != 0 {
		t.Fatal("invalid input must never reach the browser")
	}
	r2 := parseReq(t, f.requests()[1])
	validateHistory(t, r2)
	if !strings.Contains(strings.Join(toolResultTexts(r2), "\n"), "not valid JSON") {
		t.Fatal("truncated input should produce an is_error result mentioning invalid JSON")
	}
	r3 := parseReq(t, f.requests()[2])
	validateHistory(t, r3)
	txt := strings.Join(toolResultTexts(r3), "\n")
	for _, want := range []string{"ref is required", "not an element ref", "unknown op", "Unknown tool"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in %s", want, txt)
		}
	}
	errs := 0
	for _, m := range r3.Messages {
		for _, b := range m.Content {
			if b["type"] == "tool_result" && b["is_error"] == true {
				errs++
			}
		}
	}
	if errs != 5 { // 1 truncated + 4 invalid
		t.Fatalf("is_error results: %d", errs)
	}
}

func TestStopReasons(t *testing.T) {
	t.Run("refusal", func(t *testing.T) {
		f := newFakeAPI(t, func(n int, s *sw, body []byte) {
			s.std()
			s.text(0, "I can't")
			s.raw("message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"}},"usage":{"output_tokens":3}}`)
			s.raw("message_stop", `{"type":"message_stop"}`)
		})
		ag := newAgent(t, f, newFakeBE())
		evs, st, err := runCollect(t, ag, "x")
		e := findEvent(evs, EventError)
		if err == nil || e == nil || !strings.Contains(e.Text, "declined") || st.Stop != "refusal" {
			t.Fatalf("err=%v ev=%v st=%+v", err, e, st)
		}
		if findEvent(evs, EventDone) != nil {
			t.Fatal("no done event after an error")
		}
	})
	t.Run("max_tokens text continues", func(t *testing.T) {
		f := newFakeAPI(t, scripted(
			func(s *sw) { s.std(); s.text(0, "part one "); s.end("max_tokens", 4096) },
			textTurn("part two")))
		ag := newAgent(t, f, newFakeBE())
		_, st, err := runCollect(t, ag, "x")
		if err != nil || st.Stop != "end_turn" || st.Requests != 2 {
			t.Fatalf("%v %+v", err, st)
		}
		p := parseReq(t, f.requests()[1])
		validateHistory(t, p)
		if !strings.Contains(fmt.Sprint(p.Messages[len(p.Messages)-1].Content), "cut off") {
			t.Fatal("continuation prompt missing")
		}
	})
	t.Run("ask_user ends the run and chat continues", func(t *testing.T) {
		f := newFakeAPI(t, scripted(
			toolTurn("", false, tc{"q", "ask_user", `{"question":"Which size?","options":["S","M"]}`}),
			doneTurn("Bought size M")))
		ag := newAgent(t, f, newFakeBE())
		evs, st, err := runCollect(t, ag, "buy shirt")
		d := findEvent(evs, EventDone)
		if err != nil || st.Stop != "ask_user" || d == nil || !strings.Contains(d.Text, "Which size?") || !strings.Contains(d.Text, "S / M") {
			t.Fatalf("%v %+v %+v", err, st, d)
		}
		evs, st, err = runCollect(t, ag, "M please")
		if err != nil || st.Stop != "done" {
			t.Fatalf("%v %+v", err, st)
		}
		p := parseReq(t, f.requests()[1])
		validateHistory(t, p) // tool_result + new user text share one user message
		last := p.Messages[len(p.Messages)-1]
		if last.Content[0]["type"] != "tool_result" || last.Content[len(last.Content)-2]["text"] != "M please" {
			t.Fatalf("answer should follow the tool_result: %v", last.Content)
		}
	})
	t.Run("text-only end_turn", func(t *testing.T) {
		f := newFakeAPI(t, scripted(textTurn("The answer is 42.")))
		ag := newAgent(t, f, newFakeBE())
		evs, st, err := runCollect(t, ag, "x")
		if err != nil || st.Stop != "end_turn" || findEvent(evs, EventText) == nil {
			t.Fatalf("%v %+v", err, st)
		}
		if d := findEvent(evs, EventDone); d == nil || d.Text != "The answer is 42." {
			t.Fatalf("%+v", d)
		}
	})
}

type fixedBackendAsker struct {
	*fakeBE
	answer string
}

func (f fixedBackendAsker) Ask(ctx context.Context, q string, opts []string) (string, error) {
	return f.answer, nil
}

func TestAskUserWithAskerBlocksForAnswer(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"q", "ask_user", `{"question":"Color?"}`}),
		doneTurn("ok")))
	ag := newAgent(t, f, fixedBackendAsker{newFakeBE(), "blue"})
	_, st, err := runCollect(t, ag, "x")
	if err != nil || st.Stop != "done" {
		t.Fatalf("%v %+v", err, st)
	}
	if !strings.Contains(strings.Join(toolResultTexts(parseReq(t, f.requests()[1])), " "), "blue") {
		t.Fatal("answer should be returned as the tool result")
	}
}

func TestMaxTurnsAndBudgets(t *testing.T) {
	loopTurn := toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"click","ref":"e3"}]`, `"observe":"none"`)})
	f := newFakeAPI(t, scripted(loopTurn))
	ag := newAgent(t, f, newFakeBE(), func(c *Config) { c.MaxTurns = 3 })
	evs, st, err := runCollect(t, ag, "x")
	if err == nil || st.Stop != "max_turns" || st.Requests != 3 || findEvent(evs, EventError) == nil {
		t.Fatalf("%v %+v", err, st)
	}
	// history stays valid for the next run
	f.handler = scripted(doneTurn("ok"))
	if _, st, err := runCollect(t, ag, "again"); err != nil || st.Stop != "done" {
		t.Fatalf("%v %+v", err, st)
	}
	validateHistory(t, parseReq(t, f.requests()[3]))

	// cost budget
	f2 := newFakeAPI(t, scripted(loopTurn))
	ag2 := newAgent(t, f2, newFakeBE(), func(c *Config) { c.MaxCostUSD = 0.0001 })
	_, st, err = runCollect(t, ag2, "x")
	if err == nil || st.Stop != "budget" || st.Requests != 1 {
		t.Fatalf("%v %+v", err, st)
	}
	// action budget
	f3 := newFakeAPI(t, scripted(loopTurn))
	ag3 := newAgent(t, f3, newFakeBE(), func(c *Config) { c.MaxActions = 2; c.MaxTurns = 6 })
	_, _, _ = runCollect(t, ag3, "x")
	if !strings.Contains(strings.Join(toolResultTexts(parseReq(t, f3.requests()[3])), " "), "action limit reached") {
		t.Fatal("per-run action limit not enforced")
	}
}

func TestStopMidRunKeepsHistoryValidAndNoLeaks(t *testing.T) {
	base := runtime.NumGoroutine()
	hold := make(chan struct{})
	f := newFakeAPI(t, nil)
	f.handler = func(n int, s *sw, body []byte) {
		if n == 1 {
			s.std()
			s.text(0, "working ")
			s.toolJSON(1, "t1", "act", actJSON(`[{"op":"click","ref":"e3"}]`))
			<-hold // never sends message_stop
			return
		}
		doneTurn("after stop")(s)
	}
	be := newFakeBE()
	started := make(chan struct{})
	release := make(chan struct{})
	be.onAct = func(ctx context.Context, steps []Step) {
		close(started)
		select {
		case <-ctx.Done():
		case <-release:
		}
	}
	be.actDelay = time.Millisecond
	ag := newAgent(t, f, be)
	done := make(chan RunStats, 1)
	var evs []Event
	var mu sync.Mutex
	go func() {
		st, _ := ag.Run(context.Background(), "x", func(e Event) { mu.Lock(); evs = append(evs, e); mu.Unlock() })
		done <- st
	}()
	<-started
	t0 := time.Now()
	ag.Stop()
	var st RunStats
	select {
	case st = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not end the run")
	}
	t.Logf("stop latency %v", time.Since(t0))
	if st.Stop != "stopped" {
		t.Fatalf("%+v", st)
	}
	mu.Lock()
	if d := findEvent(evs, EventDone); d == nil {
		t.Fatal("a terminal event is required")
	}
	mu.Unlock()
	close(hold)
	close(release)
	// a following run works (no half-finished tool_use in history)
	f.handler = scripted(doneTurn("again"))
	if _, st, err := runCollect(t, ag, "next"); err != nil || st.Stop != "done" {
		t.Fatalf("%v %+v", err, st)
	}
	validateHistory(t, parseReq(t, f.requests()[len(f.requests())-1]))
	ag.Close()
	f.srv.Close()
	if n := goroutinesSettle(base + 2); n > base+2 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutine leak: %d > %d\n%s", n, base+2, buf[:runtime.Stack(buf, true)])
	}
}

func TestTakeControlPausesAndResumesWithNote(t *testing.T) {
	gate := make(chan struct{})
	f := newFakeAPI(t, nil)
	f.handler = func(n int, s *sw, body []byte) {
		if n == 1 {
			s.std()
			s.text(0, "starting")
			s.flushWait(gate)
			s.toolJSON(1, "t1", "act", actJSON(`[{"op":"click","ref":"e4"}]`))
			s.end("tool_use", 5)
			return
		}
		doneTurn("resumed")(s)
	}
	be := newFakeBE()
	ag := newAgent(t, f, be)
	var statuses []string
	var mu sync.Mutex
	done := make(chan RunStats, 1)
	go func() {
		st, _ := ag.Run(context.Background(), "x", func(e Event) {
			if e.Kind == EventStatus {
				mu.Lock()
				statuses = append(statuses, e.Text)
				mu.Unlock()
			}
		})
		done <- st
	}()
	time.Sleep(100 * time.Millisecond)
	ag.TakeControl() // user grabs the wheel while the model is mid-response
	close(gate)
	time.Sleep(300 * time.Millisecond)
	if n := len(f.requests()); n != 1 {
		t.Fatalf("no new request may be sent while paused (got %d)", n)
	}
	select {
	case <-done:
		t.Fatal("run must stay blocked while the user has control")
	default:
	}
	be.mu.Lock()
	be.tabs["tab-1"].url = "https://shop.test/after-user"
	be.mu.Unlock()
	ag.Resume("I logged in for you")
	select {
	case st := <-done:
		if st.Stop != "done" {
			t.Fatalf("%+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not resume")
	}
	if len(be.acts()) != 0 {
		t.Fatal("a tool call decided before the takeover must not run after it")
	}
	p := parseReq(t, f.requests()[1])
	validateHistory(t, p)
	txt := fmt.Sprint(p.Messages[len(p.Messages)-1].Content)
	for _, want := range []string{"took control", "I logged in for you", "after-user", "Not executed"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in %s", want, txt)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) < 3 { // working, paused, resumed
		t.Fatalf("statuses %v", statuses)
	}
}

func TestCompactionIsAppendCompatibleBetweenBoundaries(t *testing.T) {
	var turns []scriptTurn
	for i := 0; i < 11; i++ {
		turns = append(turns, toolTurn("", false, tc{fmt.Sprintf("t%d", i), "act", actJSON(fmt.Sprintf(`[{"op":"navigate","url":"https://shop.test/p%d"}]`, i))}))
	}
	turns = append(turns, doneTurn("ok"))
	f := newFakeAPI(t, scripted(turns...))
	be := newFakeBE()
	ag := newAgent(t, f, be, func(c *Config) {
		c.Compact = CompactConfig{Every: 4, KeepRecentTurns: 1, MinPromptTokens: 1, HardPromptTokens: 1 << 30}
	})
	// make usage report a big prompt so the size gate opens
	_, st, err := runCollect(t, ag, "browse shop.test pages")
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	compatible, incompatible := 0, 0
	stubSeen := false
	for i := 1; i < len(reqs); i++ {
		a, b := parseReq(t, reqs[i-1]), parseReq(t, reqs[i])
		validateHistory(t, b)
		// messages of the previous request (minus its volatile tail) must be a prefix of this one
		la := a.Messages[len(a.Messages)-1]
		na := len(la.Content) - 1 // drop tail block
		prefix := canonPrefix(t, reqs[i-1], [2]int{len(a.Messages) - 1, na - 1})
		cur := canonPrefix(t, reqs[i], [2]int{len(a.Messages) - 1, na - 1})
		if prefix == cur {
			compatible++
		} else {
			incompatible++
		}
		if strings.Contains(string(reqs[i]), "elided; re-read with read_page") {
			stubSeen = true
		}
	}
	t.Logf("requests=%d append-compatible transitions=%d rewritten=%d compactions=%d", len(reqs), compatible, incompatible, st.Compactions)
	if !stubSeen || st.Compactions == 0 {
		t.Fatal("expected superseded snapshots to be replaced by stubs")
	}
	if incompatible != st.Compactions {
		t.Fatalf("history may only be rewritten by compactions: rewritten=%d compactions=%d", incompatible, st.Compactions)
	}
	if max := (len(reqs) + 3) / 4; st.Compactions > max {
		t.Fatalf("compactions %d exceed one per 4 turns (%d)", st.Compactions, max)
	}
	// the newest snapshot is never elided
	lastTexts := toolResultTexts(parseReq(t, reqs[len(reqs)-1]))
	if !strings.Contains(lastTexts[len(lastTexts)-1], "kind=\"snapshot\"") {
		t.Fatalf("the current page snapshot must stay in context: %.200s", lastTexts[len(lastTexts)-1])
	}
	// request prefix (tools/system) is untouched by compaction
	if rawField(t, reqs[0], "system") != rawField(t, reqs[len(reqs)-1], "system") {
		t.Fatal("system prefix changed")
	}
	// the turn after a compaction carries a single message breakpoint (dropped read point)
	_ = be
}

func TestScreenshotIsReturnedAsImageThenElided(t *testing.T) {
	var turns []scriptTurn
	turns = append(turns, toolTurn("", false, tc{"s", "act", actJSON(`[{"op":"screenshot"}]`, `"observe":"none"`)}))
	for i := 0; i < 9; i++ {
		turns = append(turns, toolTurn("", false, tc{fmt.Sprintf("n%d", i), "act", actJSON(`[{"op":"click","ref":"e3"}]`, `"observe":"none"`)}))
	}
	turns = append(turns, doneTurn("ok"))
	f := newFakeAPI(t, scripted(turns...))
	ag := newAgent(t, f, newFakeBE(), func(c *Config) {
		c.Compact = CompactConfig{Every: 3, KeepRecentTurns: 1, MinPromptTokens: 1}
	})
	if _, _, err := runCollect(t, ag, "shot"); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if !strings.Contains(string(reqs[1]), `"type":"image"`) {
		t.Fatal("screenshot should be sent as an image block")
	}
	if strings.Contains(string(reqs[len(reqs)-1]), `"type":"image"`) || !strings.Contains(string(reqs[len(reqs)-1]), "screenshot of") {
		t.Fatal("old screenshots should be elided to a stub")
	}
}

func TestParallelToolResultsInOneUserMessage(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"x1", "find", `{"query":"cart"}`}, tc{"x2", "tabs", `{"action":"list"}`}, tc{"x3", "find", `{"role":"button"}`}),
		doneTurn("ok")))
	ag := newAgent(t, f, newFakeBE())
	if _, _, err := runCollect(t, ag, "x"); err != nil {
		t.Fatal(err)
	}
	p := parseReq(t, f.requests()[1])
	validateHistory(t, p)
	um := p.Messages[2]
	ids := []string{}
	for _, b := range um.Content {
		if b["type"] == "tool_result" {
			ids = append(ids, b["tool_use_id"].(string))
		}
	}
	if strings.Join(ids, ",") != "x1,x2,x3" {
		t.Fatalf("results must be in tool_use order in ONE user message: %v", ids)
	}
}

func TestTabsToolOpenSwitchClose(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"o", "tabs", `{"action":"open","url":"https://shop.test/second"}`}),
		toolTurn("", false, tc{"c1", "tabs", `{"action":"close","id":"tab-1"}`}, tc{"c2", "tabs", `{"action":"close","id":"tab-2"}`}),
		doneTurn("ok")))
	be := newFakeBE()
	ag := newAgent(t, f, be)
	if _, _, err := runCollect(t, ag, "work on shop.test"); err != nil {
		t.Fatal(err)
	}
	r2 := strings.Join(toolResultTexts(parseReq(t, f.requests()[1])), "\n")
	if !strings.Contains(r2, "tab-2 (current)") || !strings.Contains(r2, `kind="snapshot"`) {
		t.Fatalf("open should list tabs and return a full snapshot: %s", r2)
	}
	r3 := strings.Join(toolResultTexts(parseReq(t, f.requests()[2])), "\n")
	if !strings.Contains(r3, "belongs to the user") {
		t.Fatalf("agent must not close the user's tab: %s", r3)
	}
	if len(be.Tabs()) != 1 || be.Tabs()[0].ID != "tab-1" {
		t.Fatalf("tabs: %+v", be.Tabs())
	}
}

func TestStartReturnsEventsAndRejectsConcurrentRuns(t *testing.T) {
	gate := make(chan struct{})
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.std()
		s.text(0, "hello ")
		s.flushWait(gate)
		s.text(1, "world")
		s.end("end_turn", 3)
	})
	ag := newAgent(t, f, newFakeBE())
	id, ch, err := ag.Start(context.Background(), "hi")
	if err != nil || id == "" {
		t.Fatal(err)
	}
	if _, _, err := ag.Start(context.Background(), "again"); err != ErrBusy {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	close(gate)
	var kinds []EventKind
	var text strings.Builder
	for e := range ch {
		if e.RunID != id {
			t.Fatalf("run id %q != %q", e.RunID, id)
		}
		kinds = append(kinds, e.Kind)
		if e.Kind == EventText {
			text.WriteString(e.Text)
		}
	}
	if text.String() != "hello world" || kinds[len(kinds)-1] != EventDone {
		t.Fatalf("%q %v", text.String(), kinds)
	}
}

func TestEventLabelsAreHumanReadable(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"r", "read_page", `{}`}),
		toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"click","ref":"e3"},{"op":"type","ref":"e5","text":"red shoes"},{"op":"press","key":"Enter"},{"op":"scroll"}]`, `"observe":"none"`)}),
		doneTurn("ok")))
	ag := newAgent(t, f, newFakeBE())
	evs, _, err := runCollect(t, ag, "x")
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, e := range evs {
		if e.Kind == EventAction {
			labels = append(labels, e.Text)
		}
	}
	joined := strings.Join(labels, " | ")
	for _, want := range []string{"Clicking 'Add to cart'", "Typing 'red shoes' into 'Search'", "Pressing Enter", "Scrolling down", "Reading the page"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing label %q in %s", want, joined)
		}
	}
}

func TestStepFailureStopsBatchAndReportsRest(t *testing.T) {
	f := newFakeAPI(t, scripted(
		toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"click","ref":"e3"},{"op":"click","ref":"e404"},{"op":"click","ref":"e2"}]`)}),
		doneTurn("ok")))
	ag := newAgent(t, f, newFakeBE())
	if _, _, err := runCollect(t, ag, "x"); err != nil {
		t.Fatal(err)
	}
	txt := strings.Join(toolResultTexts(parseReq(t, f.requests()[1])), "\n")
	if !strings.Contains(txt, "FAILED") || !strings.Contains(txt, "2 ERR click e404") || !strings.Contains(txt, "no node matches") {
		t.Fatalf("%s", txt)
	}
	if !strings.Contains(txt, `kind="delta"`) && !strings.Contains(txt, `kind="snapshot"`) {
		t.Fatal("a failed batch should still carry the observation")
	}
}

func TestHTTP2TLSStreaming(t *testing.T) {
	f := newTLSFakeAPI(t, scripted(textTurn("over h2")))
	cfg := Config{APIKey: "sk-ant-test-key-0123456789", BaseURL: f.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.tr.TLSClientConfig.RootCAs = f.rootCAs()
	msg, m, err := c.Stream(context.Background(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Proto != "HTTP/2.0" || msg.Blocks[0].Text != "over h2" {
		t.Fatalf("proto=%s msg=%+v", m.Proto, msg)
	}
	_, m2, err := c.Stream(context.Background(), []byte(`{}`), nil)
	if err != nil || !m2.ReusedConn {
		t.Fatalf("second request should reuse the connection: %v %+v", err, m2)
	}
}

// TestEarlyDispatchGain measures how much earlier tools start than they would
// if the loop waited for message_stop: the model "decodes" for 80 ms between
// two tool calls and another 80 ms before finishing.
func TestEarlyDispatchGain(t *testing.T) {
	var stopAt atomic.Int64
	f := newFakeAPI(t, nil)
	f.handler = func(n int, s *sw, body []byte) {
		if n > 1 {
			doneTurn("ok")(s)
			return
		}
		s.std()
		s.toolJSON(0, "a", "act", actJSON(`[{"op":"click","ref":"e3"}]`, `"observe":"none"`))
		time.Sleep(80 * time.Millisecond)
		s.toolJSON(1, "b", "find", `{"query":"cart"}`)
		time.Sleep(80 * time.Millisecond)
		stopAt.Store(time.Now().UnixNano())
		s.end("tool_use", 40)
	}
	be := newFakeBE()
	var firstAct atomic.Int64
	be.onAct = func(ctx context.Context, steps []Step) { firstAct.CompareAndSwap(0, time.Now().UnixNano()) }
	ag := newAgent(t, f, be)
	if _, _, err := runCollect(t, ag, "x"); err != nil {
		t.Fatal(err)
	}
	gain := time.Duration(stopAt.Load() - firstAct.Load())
	t.Logf("first tool started %v before message_stop", gain.Round(time.Millisecond))
	if gain < 120*time.Millisecond {
		t.Fatalf("early dispatch gain only %v (expected ~160ms)", gain)
	}
}
