package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ---- unit tests for the pure helpers ------------------------------------------------------

func bigSnapshot(lines int, variant int) string {
	var sb strings.Builder
	sb.WriteString("- banner:\n  - heading \"Catalogue\" [level=1 ref=e1]\n")
	for i := 0; i < lines; i++ {
		switch i % 4 {
		case 0:
			fmt.Fprintf(&sb, "- link \"Product %d\" [ref=e%d]:\n  - /url: /p/%d\n", i, i+10, i)
		case 1:
			fmt.Fprintf(&sb, "- paragraph: Product %d is a durable widget with plenty of marketing text attached to it\n", i)
		case 2:
			fmt.Fprintf(&sb, "- button \"Add %d to cart\" [ref=e%d]\n", i, i+10)
		default:
			fmt.Fprintf(&sb, "- text: $%d.99\n", i)
		}
	}
	fmt.Fprintf(&sb, "- status: variant=%d\n", variant)
	return sb.String()
}

func TestDeltaText(t *testing.T) {
	a := "- a [ref=e1]\n- b [ref=e2]\n- c [ref=e3]\n- d [ref=e4]\n"
	if d := DeltaText(a, a); d != noChangeText || !IsDelta(d) {
		t.Fatal(d)
	}
	b := "- a [ref=e1]\n- b [ref=e2]\n- c2 [ref=e3]\n- d [ref=e4]\n- e [ref=e5]\n"
	d := DeltaText(a, b)
	want := []string{"@ - b [ref=e2]", "- - c [ref=e3]", "+ - c2 [ref=e3]", "@ - d [ref=e4]", "+ - e [ref=e5]"}
	for _, w := range want {
		if !strings.Contains(d, w+"\n") {
			t.Errorf("delta lacks %q:\n%s", w, d)
		}
	}
	if strings.Contains(d, "- a [ref=e1]") {
		t.Errorf("unchanged lines must be omitted:\n%s", d)
	}
	// applying the delta mentally: removed + added lines reconstruct b
	big1, big2 := bigSnapshot(2000, 1), bigSnapshot(2000, 2)
	dd := DeltaText(big1, big2)
	if len(dd) > 400 {
		t.Fatalf("a one-line change in a 3000-line tree should give a tiny delta, got %d bytes", len(dd))
	}
	// total rewrite falls back gracefully (no panic, bounded memory)
	x := strings.Repeat("- x\n", 3000)
	y := strings.Repeat("- y\n", 3000)
	if out := DeltaText(x, y); !strings.Contains(out, "+ - y") {
		t.Fatal("rewrite delta")
	}
}

func TestTruncateSnapshotKeepsInteractiveFirst(t *testing.T) {
	s := bigSnapshot(4000, 0)
	out, cut := TruncateSnapshot(s, 3000)
	if !cut || EstimateTokens(out) > 3200 {
		t.Fatalf("cut=%v tokens=%d", cut, EstimateTokens(out))
	}
	if !strings.Contains(out, "[snapshot truncated") || !strings.Contains(out, "heading \"Catalogue\"") {
		t.Fatal("notice/header missing")
	}
	// interactive lines come first: of the kept lines, refs dominate and are in document order
	var refs, plain int
	last := -1
	for _, l := range strings.Split(out, "\n") {
		if hasRef(l) {
			refs++
			var n int
			if i := strings.Index(l, "ref=e"); i >= 0 {
				fmt.Sscanf(l[i:], "ref=e%d", &n)
				if n < last {
					t.Fatalf("document order broken: e%d after e%d", n, last)
				}
				last = n
			}
		} else {
			plain++
		}
	}
	if refs < plain {
		t.Fatalf("interactive elements must be prioritised: %d refs vs %d plain", refs, plain)
	}
	small, cut := TruncateSnapshot("- a [ref=e1]\n", 3000)
	if cut || small != "- a [ref=e1]\n" {
		t.Fatal("small snapshots are untouched")
	}
	long := "- paragraph: " + strings.Repeat("w", 5000) + "\n- button \"Go\" [ref=e1]\n"
	out, _ = TruncateSnapshot(long, 200)
	if len(out) > 2000 || !strings.Contains(out, `button "Go"`) {
		t.Fatalf("long line must be clipped and the button kept: %d", len(out))
	}
}

func TestParseRefs(t *testing.T) {
	m := map[string]refInfo{}
	parseRefs(`- link "Home \"x\"" [ref=e2]:
  - /url: /home
- textbox "Pass" [type=password ref=e7]
- button [ref=e9]
+ - button "New" [ref=e12]
- paragraph: no ref here
`, m)
	if m["e2"].Name != `Home "x"` || m["e2"].Href != "/home" || m["e2"].Role != "link" {
		t.Fatalf("%+v", m["e2"])
	}
	if m["e7"].Role != "textbox" || !strings.Contains(m["e7"].Extra, "password") {
		t.Fatalf("%+v", m["e7"])
	}
	if m["e9"].label() != "button" || m["e12"].Name != "New" || len(m) != 4 {
		t.Fatalf("%+v", m)
	}
}

func TestRegistrableDomainAndTrust(t *testing.T) {
	cases := map[string]string{
		"https://www.shop.co.uk/a": "shop.co.uk", "https://a.b.example.com": "example.com",
		"http://127.0.0.1:8080/": "127.0.0.1", "localhost:3000": "localhost", "github.io": "github.io",
	}
	for in, want := range cases {
		if got := registrableDomain(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
	rs := newRunSafety(10, 10)
	rs.seedTrusted("compare prices on https://www.amazon.com and ebay.co.uk please, e.g. now. file index.html")
	rs.taint()
	for _, ok := range []string{"https://smile.amazon.com/x", "https://m.ebay.co.uk"} {
		if cd, _ := rs.crossDomain("https://start.test", ok); cd {
			t.Errorf("%s should be trusted", ok)
		}
	}
	if cd, dom := rs.crossDomain("https://amazon.com", "https://amazon.evil.com/login"); !cd || dom != "evil.com" {
		t.Errorf("lookalike must need approval: %v %s", cd, dom)
	}
}

func TestChargeLimits(t *testing.T) {
	rs := newRunSafety(5, 2)
	if err := rs.charge(3, 1); err != nil {
		t.Fatal(err)
	}
	if err := rs.charge(3, 0); err == nil {
		t.Fatal("action limit")
	}
	if err := rs.charge(1, 2); err == nil {
		t.Fatal("navigation limit")
	}
}

// ---- benchmarks ----------------------------------------------------------------------------------

// historyConvo builds a conversation of n completed browsing turns, each with
// a ~6K-token observation, to measure per-turn costs at realistic sizes.
func historyConvo(n int, withObs bool) *convo {
	cfg := Config{}
	cfg.applyDefaults()
	c := newConvo(cfg.Compact, buildHead(&cfg))
	c.addUserText("Find the cheapest laptop on shop.test and put it in the cart.")
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("toolu_%06d", i)
		c.addAssistant([]Block{{Type: "text", Text: "Clicking next.", Raw: textBlockRaw("Clicking next.")},
			{Type: "tool_use", ID: id, Name: "act", Raw: []byte(fmt.Sprintf(`{"type":"tool_use","id":%q,"name":"act","input":{"steps":[{"op":"click","ref":"e3"},{"op":"type","ref":"e5","text":"laptop"}],"note":"goal: cheapest laptop; page %d"}}`, id, i))}})
		parts := []*part{{kind: partText, text: "ok: 2/2 steps\n1 ok click e3\n2 ok type e5"}}
		if withObs {
			txt := wrapUntrusted("snapshot", "tab-1", fmt.Sprintf("https://shop.test/page/%d", i), bigSnapshot(280, i))
			parts = append(parts, c.newObservation("tab-1", fmt.Sprintf("https://shop.test/page/%d", i), txt, true))
		}
		c.addToolResults([]*toolResult{{id: id, parts: parts}})
	}
	return c
}

func BenchmarkBuildRequest(b *testing.B) {
	for _, n := range []int{5, 20, 40} {
		b.Run(fmt.Sprintf("turns=%d", n), func(b *testing.B) {
			c := historyConvo(n, true)
			tail := "<task_memory>\ngoal\n</task_memory>\n<run_status turn=\"3/40\" current_tab=\"tab-1\"/>"
			b.ReportAllocs()
			body := c.body(tail)
			b.SetBytes(int64(len(body)))
			b.ReportMetric(float64(len(body))/1024, "KiB/req")
			for i := 0; i < b.N; i++ {
				c.lastBP = breakpoint{}
				_ = c.body(tail)
			}
		})
	}
}

func BenchmarkAppendTurn(b *testing.B) {
	// the incremental cost of one more turn: render one tool_result + assistant message
	c := historyConvo(20, true)
	txt := wrapUntrusted("delta", "tab-1", "https://shop.test/p", DeltaText(bigSnapshot(280, 1), bigSnapshot(280, 2)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		id := fmt.Sprintf("toolu_x%d", i)
		c.addAssistant([]Block{{Type: "tool_use", ID: id, Name: "act", Raw: []byte(fmt.Sprintf(`{"type":"tool_use","id":%q,"name":"act","input":{}}`, id))}})
		c.addToolResults([]*toolResult{{id: id, parts: []*part{{kind: partText, text: "ok"}, c.newObservation("tab-1", "u", txt, false)}}})
		if len(c.msgs) > 200 {
			c.msgs = c.msgs[:42]
			c.obs = c.obs[:20]
		}
	}
}

func sseStream(deltas int) []byte {
	var sb strings.Builder
	sb.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"x\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	sb.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	for i := 0; i < deltas; i++ {
		fmt.Fprintf(&sb, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"token%d \\\"quoted\\\" and some more words\"}}\n\n", i)
		if i%50 == 0 {
			sb.WriteString("event: ping\ndata: {\"type\": \"ping\"}\n\n")
		}
	}
	sb.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	sb.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n")
	sb.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return []byte(sb.String())
}

// BenchmarkSSEParseThroughput measures the framing + hot-path delta decode
// (what runs per streamed token), excluding network.
func BenchmarkSSEParseThroughput(b *testing.B) {
	data := sseStream(2000)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	var scratch []byte
	events := 0
	for i := 0; i < b.N; i++ {
		r := newSSEReader(strings.NewReader(string(data)))
		for {
			name, d, err := r.next()
			if err != nil {
				break
			}
			if string(name) == "content_block_delta" {
				if _, _, _, ok := parseDelta(d, &scratch); !ok {
					b.Fatal("fast path failed")
				}
			}
			events++
		}
	}
	b.ReportMetric(float64(events)/float64(b.N), "events/op")
}

// BenchmarkSSEFramingOnly isolates the allocation-free line reader.
func BenchmarkSSEFramingOnly(b *testing.B) {
	data := string(sseStream(2000))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := newSSEReader(strings.NewReader(data))
		for {
			if _, _, err := r.next(); err != nil {
				break
			}
		}
	}
}

func BenchmarkSSEDeltaDecodeEncodingJSON(b *testing.B) {
	// baseline: what the same decode costs with encoding/json
	d := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"token12 \"quoted\" and some more words"}}`)
	b.ReportAllocs()
	var scratch []byte
	b.Run("fast", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			parseDelta(d, &scratch)
		}
	})
	b.Run("encoding/json", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var ev struct {
				Index int `json:"index"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			json.Unmarshal(d, &ev)
		}
	})
}

func BenchmarkCompaction(b *testing.B) {
	// The history is built once; each iteration resets the elision flags (a
	// 40-element loop, negligible) and re-runs the policy, which re-renders
	// every touched tool_result.
	run := func(b *testing.B, hard int) {
		c := historyConvo(40, true)
		c.cfg = CompactConfig{Every: 4, KeepRecentTurns: 2, MinPromptTokens: 1, HardPromptTokens: hard}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, p := range c.obs {
				p.elided = false
			}
			c.lastBoundary = 0
			if !c.maybeCompact(100000) {
				b.Fatal("no compaction")
			}
		}
		b.ReportMetric(float64(c.ElidedTokens)/float64(c.Compactions), "tokens-elided/op")
	}
	b.Run("stage1-40turns", func(b *testing.B) { run(b, 1<<30) })
	b.Run("stage2-40turns", func(b *testing.B) { run(b, 1) })
}

func BenchmarkSnapshotDelta(b *testing.B) {
	a, c := bigSnapshot(1000, 1), bigSnapshot(1000, 2)
	b.SetBytes(int64(len(a)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		DeltaText(a, c)
	}
}

func BenchmarkTruncateSnapshot(b *testing.B) {
	s := bigSnapshot(4000, 1)
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		TruncateSnapshot(s, 6000)
	}
}

func BenchmarkClassifyStep(b *testing.B) {
	ri := refInfo{Role: "button", Name: "Add to cart"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		classifyStep(Step{Op: OpClick}, ri, true, "https://x.test/")
	}
}

// BenchmarkLoopOverheadPerTurn runs complete agent turns against a zero-latency
// fake API and fake browser over loopback HTTP: it measures the agent's own
// per-turn overhead (serialize, POST, parse, dispatch, observe, history).
func BenchmarkLoopOverheadPerTurn(b *testing.B) {
	turn := toolTurn("", false, tc{"a", "act", actJSON(`[{"op":"click","ref":"e3"},{"op":"type","ref":"e5","text":"x"}]`)})
	var srvT testing.TB = b
	f := newFakeAPIB(srvT, func(n int, s *sw, body []byte) {
		if n%10 == 0 {
			doneTurn("ok")(s)
			return
		}
		turn(s)
	})
	be := newFakeBE()
	cfg := f.cfg()
	cfg.MaxTurns = 1000
	cfg.MaxCostUSD = 1e9 // the fake reports ever-growing usage
	ag, err := New(cfg, be)
	if err != nil {
		b.Fatal(err)
	}
	defer ag.Close()
	b.ReportAllocs()
	b.ResetTimer()
	turns := 0
	for i := 0; i < b.N; i++ {
		st, err := ag.Run(contextBackground(), "task", nil)
		if err != nil {
			b.Fatal(err)
		}
		turns += st.Turns
		if i%20 == 19 {
			ag.Reset()
		}
	}
	b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(turns), "us/turn")
}
