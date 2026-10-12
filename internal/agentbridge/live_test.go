package agentbridge

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveSmoke runs one real task through the whole GUI (real chrome UI, real
// engine tabs, the real Anthropic API) against a local page, and prints the
// timings the sidebar's latency strip shows. It is skipped unless
//
//	SWEEF_LIVE_ANTHROPIC=1 ANTHROPIC_API_KEY=sk-ant-... go test -run TestLiveSmoke -v ./internal/agentbridge
//
// SWEEF_AGENT_BASE_URL may point it at a proxy. The key is read from the
// environment by the host and is never printed.
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("SWEEF_LIVE_ANTHROPIC") != "1" || os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("set SWEEF_LIVE_ANTHROPIC=1 and ANTHROPIC_API_KEY to run against the live API")
	}
	runLive(t)
}

// TestLiveSmokeDryRun drives the same code path as TestLiveSmoke (host built
// from the environment, key from ANTHROPIC_API_KEY, base URL from
// SWEEF_AGENT_BASE_URL) against the fake server, so the harness itself stays
// tested without a key.
func TestLiveSmokeDryRun(t *testing.T) {
	fake := newFakeAPI(t, scripted(
		toolTurn("", tc{"r1", "read_page", `{}`}),
		dyn(func(b []byte) (string, []tc) {
			return "", []tc{{"a1", "act", actJSON(`[{"op":"click","ref":"` + findRef(b, "link", "Browse products") + `"},{"op":"extract"}]`)}}
		}),
		textTurn(30*time.Millisecond, "The blue kettle costs ", "42 dollars."),
	))
	t.Setenv("SWEEF_LIVE_ANTHROPIC", "1")
	t.Setenv("ANTHROPIC_API_KEY", testKey)
	t.Setenv("SWEEF_AGENT_BASE_URL", fake.srv.URL)
	runLive(t)
}

func runLive(t *testing.T) {
	site := shopSite(t)
	e := newEnv(t, nil, envOpts{initial: []string{site.URL() + "/"}, allow: []string{site.URL()}})
	e.openSidebar()
	if !e.host.Info().HasKey {
		t.Fatal("host found no key")
	}
	// the sidebar opening warmed the connection; measure the first message after that
	time.Sleep(300 * time.Millisecond)
	task := "Open the products page of this shop and tell me how much the blue kettle costs. Answer in one sentence."
	t0 := time.Now()
	e.send(task, "agent")
	var done map[string]any
	e.waitFor("live run to finish (up to 90s)", func() bool {
		for _, ev := range e.events() {
			if ev["kind"] == "done" || ev["kind"] == "error" {
				done = ev
				return true
			}
		}
		return time.Since(t0) > 90*time.Second
	})
	if done == nil || done["kind"] != "done" {
		t.Fatalf("live run did not finish: %v\nfeed: %s", done, e.feed())
	}
	wall := time.Since(t0)
	answer := joinText(e.find("text"))
	fmt.Printf("\n--- live smoke ---\n")
	fmt.Printf("task            : %s\n", task)
	fmt.Printf("answer          : %s\n", strings.TrimSpace(answer))
	fmt.Printf("tab now at      : %s (%s)\n", e.activeURL(), e.activeTitle())
	fmt.Printf("wall clock      : %v\n", wall.Round(time.Millisecond))
	fmt.Printf("latency strip   : ttft=%vms  tps=%v  total=%vms\n", done["ttft"], done["tps"], done["ms"])
	fmt.Printf("api ttft (avg)  : %vms   turns=%v tools=%v\n", done["apiTtft"], done["turns"], done["tools"])
	fmt.Printf("tokens in/out   : %v / %v   cache hit %v%%   cost $%v\n", done["tokensIn"], done["tokenOut"], done["cacheHit"], done["costUsd"])
	for _, ev := range e.find("action") {
		if ev["status"] == "ok" || ev["status"] == "error" {
			fmt.Printf("  step %-34v %-6v %vms\n", ev["label"], ev["status"], ev["ms"])
		}
	}
	fmt.Printf("------------------\n")
	if e.activeURL() != site.URL()+"/products" {
		t.Errorf("the agent did not end on the products page: %s", e.activeURL())
	}
	if !strings.Contains(answer, "42") {
		t.Errorf("answer does not mention the price: %q", answer)
	}
	if done["ttft"] == nil || done["ttft"].(float64) <= 0 {
		t.Errorf("no ttft")
	}
}
