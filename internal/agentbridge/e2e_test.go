package agentbridge

import (
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

const homePage = `<!doctype html><html><head><title>Shop Home</title></head><body style="margin:0;font:18px sans-serif">
<h1>Welcome to the shop</h1>
<a id="p" href="/products" style="display:block;height:60px;background:#f2c94c">Browse products</a>
<p>Opening hours: 9 to 5</p></body></html>`

const productsPage = `<!doctype html><html><head><title>Products</title></head><body style="margin:0;font:18px sans-serif">
<h1>Products</h1><p id="price">Blue kettle costs 42 dollars</p>
<button id="b" style="display:block;height:50px;width:200px">Place order</button>
<button id="n" style="display:block;height:50px;width:200px">Add note</button>
<script>document.getElementById('b').onclick=function(){window.ordered=true;document.title='Ordered'};
document.getElementById('n').onclick=function(){document.title='Noted'};</script>
</body></html>`

func shopSite(t *testing.T) *pageSite {
	return newSite(t, map[string]string{"/": homePage, "/products": productsPage})
}

func TestEndToEndAgentDrivesRealTab(t *testing.T) {
	site := shopSite(t)
	fake := newFakeAPI(t, scripted(
		toolTurn("Let me look at the page.", tc{"r1", "read_page", `{}`}),
		dyn(func(body []byte) (string, []tc) {
			ref := findRef(body, "link", "Browse products")
			if ref == "" {
				t.Errorf("no ref for the link in:\n%s", toolResultText(body))
			}
			return "", []tc{{"a1", "act", actJSON(`[{"op":"click","ref":"` + ref + `"},{"op":"extract"}]`)}}
		}),
		textTurn(60*time.Millisecond, "The kettle ", "costs **42 dollars**."),
	))
	e := newEnv(t, fake, envOpts{initial: []string{site.URL() + "/"}, allow: []string{site.URL()}})
	e.openSidebar()
	e.send("how much is the kettle", "agent")

	done := e.waitTerminal()
	if done["kind"] != "done" {
		t.Fatalf("run ended with %v; feed: %s", done, e.feed())
	}
	// the real tab navigated
	if u := e.activeURL(); u != site.URL()+"/products" {
		t.Fatalf("active tab at %s, want /products", u)
	}
	e.waitFor("tab title", func() bool { return e.activeTitle() == "Products" })
	// events reached the chrome page in the documented shapes
	if got := e.find("text"); len(got) == 0 || !strings.Contains(joinText(got), "costs **42 dollars**") {
		t.Fatalf("text events: %v", got)
	}
	var steps []map[string]any
	for _, ev := range e.find("action") {
		steps = append(steps, ev)
	}
	sawClick, sawOK := false, false
	for _, ev := range steps {
		if l, _ := ev["label"].(string); strings.HasPrefix(l, "Clicking") {
			sawClick = true
			if ev["id"] == nil || ev["status"] == nil {
				t.Fatalf("action lacks id/status: %v", ev)
			}
			if ev["status"] == "ok" {
				sawOK = true
				if ms, ok := ev["ms"].(float64); !ok || ms < 0 {
					t.Fatalf("finished action without ms: %v", ev)
				}
			}
		}
	}
	if !sawClick || !sawOK {
		t.Fatalf("click action never completed: %v", steps)
	}
	// real timing numbers reach the latency strip
	if done["ttft"] == nil || done["tps"] == nil || done["ms"] == nil {
		t.Fatalf("done lacks timing: %v", done)
	}
	if ttft := done["ttft"].(float64); ttft <= 0 || ttft > 30000 {
		t.Fatalf("ttft %v", ttft)
	}
	if done["ms"].(float64) < 50 {
		t.Fatalf("ms %v", done["ms"])
	}
	if src, _ := done["sources"].([]any); len(src) == 0 {
		t.Fatalf("done carries no sources: %v", done)
	}
	e.waitFor("latency strip", func() bool {
		var txt string
		return e.tryJS(`document.getElementById("sklat").className.indexOf("hide")<0 ? document.getElementById("sklat").textContent : ""`, &txt) && strings.Contains(txt, " s") || strings.Contains(txt, "ms")
	})
	if !strings.Contains(e.feed(), "42 dollars") {
		t.Fatalf("answer not rendered in the sidebar: %s", e.feed())
	}
	e.waitRunEnd()
	e.waitFor("idle state", func() bool { return e.agentState() == "idle" })
	// the model never saw the API key and the chrome state never carries it
	var state string
	e.js(`JSON.stringify(SW.state)`, &state)
	if strings.Contains(state, testKey) {
		t.Fatal("API key leaked into the chrome state")
	}
	if !strings.Contains(state, `"hasKey":true`) {
		t.Fatalf("hasKey missing from state.agent: %s", state)
	}
	for i, b := range fake.requests() {
		if strings.Contains(string(b), testKey) {
			t.Fatalf("request %d body contains the key", i+1)
		}
	}
	fake.mu.Lock()
	keys := append([]string(nil), fake.keys...)
	fake.mu.Unlock()
	for _, k := range keys {
		if k != testKey {
			t.Fatalf("x-api-key header %q", k)
		}
	}
	_ = store.VisAllowed
}

func joinText(evs []map[string]any) string {
	var sb strings.Builder
	for _, e := range evs {
		if s, ok := e["text"].(string); ok {
			sb.WriteString(s)
		}
	}
	return sb.String()
}

func clickOrderTurns(final string) []scriptTurn {
	return []scriptTurn{
		toolTurn("", tc{"r1", "read_page", `{}`}),
		dyn(func(body []byte) (string, []tc) {
			ref := findRef(body, "button", "Place order")
			return "", []tc{{"a1", "act", actJSON(`[{"op":"click","ref":"` + ref + `"}]`)}}
		}),
		textTurn(0, final),
	}
}

func TestConfirmCardApproveAndDeny(t *testing.T) {
	for _, approve := range []bool{true, false} {
		name := "deny"
		if approve {
			name = "approve"
		}
		t.Run(name, func(t *testing.T) {
			site := shopSite(t)
			fake := newFakeAPI(t, scripted(clickOrderTurns("all done")...))
			e := newEnv(t, fake, envOpts{initial: []string{site.URL() + "/products"}, allow: []string{site.URL()}})
			e.openSidebar()
			e.send("order the kettle", "agent")

			cf := e.waitKind("confirm")
			for _, k := range []string{"id", "title", "risk", "action"} {
				if cf[k] == nil {
					t.Fatalf("confirm lacks %q: %v", k, cf)
				}
			}
			if cf["risk"] != "high" || !strings.Contains(cf["title"].(string), "Place order") {
				t.Fatalf("confirm %v", cf)
			}
			e.waitFor("waiting state", func() bool { return e.agentState() == "waiting" })
			e.waitFor("confirm card in the sidebar", func() bool {
				var n float64
				return e.tryJS(`document.querySelectorAll("#skfeed .cf").length`, &n) && n == 1
			})
			// nothing happened to the page yet
			var ordered bool
			e.pageEval(`!!window.ordered`, &ordered)
			if ordered {
				t.Fatal("the risky click ran before the user answered")
			}
			btn := ".btn.primary"
			if !approve {
				btn = ".ca .btn:not(.primary)"
			}
			e.js(`document.querySelector("#skfeed .cf `+btn+`").click(); 1`, nil)
			done := e.waitTerminal()
			if done["kind"] != "done" {
				t.Fatalf("run: %v", done)
			}
			e.pageEval(`!!window.ordered`, &ordered)
			if ordered != approve {
				reqs := fake.requests()
				t.Logf("last tool results: %s", toolResultText(reqs[len(reqs)-1]))
				t.Logf("events: %v", e.events())
				t.Fatalf("ordered=%v with approve=%v", ordered, approve)
			}
			// the action row reflects the decision
			var last map[string]any
			for _, ev := range e.find("action") {
				if strings.HasPrefix(ev["label"].(string), "Clicking") {
					last = ev
				}
			}
			if last == nil {
				t.Fatal("no click action row")
			}
			if approve && last["status"] != "ok" {
				t.Fatalf("approved click row: %v", last)
			}
			if !approve && (last["status"] != "error" || !strings.Contains(last["detail"].(string), "declined")) {
				t.Fatalf("declined click row: %v", last)
			}
			reqs := fake.requests()
			res := toolResultText(reqs[len(reqs)-1])
			if !approve && !strings.Contains(res, "declined") {
				t.Fatalf("the model was not told about the decline: %s", res)
			}
			e.waitFor("run state back to idle", func() bool { return e.agentState() == "idle" })
		})
	}
}
