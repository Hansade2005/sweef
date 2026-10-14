package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// RunCLI implements `sweef agent "<task>"`: a headless agent run against an
// in-process engine. args excludes the "agent" word itself. The API key comes
// from ANTHROPIC_API_KEY (never from flags, so it cannot end up in shell
// history or process listings); ANTHROPIC_BASE_URL overrides the API origin.
// It returns the process exit code.
func RunCLI(args []string, in io.Reader, out io.Writer) int {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(out)
	model := fs.String("model", DefaultModel, "model id")
	startURL := fs.String("url", "", "open this URL before starting")
	maxTurns := fs.Int("max-turns", 0, "maximum model turns (default 40)")
	maxCost := fs.Float64("max-cost", 0, "maximum estimated cost in USD for the run (default 0.50)")
	effort := fs.String("effort", "", "output_config.effort: low|medium|high (default low)")
	yes := fs.Bool("yes", false, "auto-approve confirmations (payments, messages, cross-site moves). Use with care")
	verbose := fs.Bool("v", false, "print status events and debug lines")
	thoughts := fs.Bool("thoughts", false, "request and print thought summaries")
	noThink := fs.Bool("no-thinking", false, "disable thinking (valid at effort <= high)")
	jsonOut := fs.Bool("json", false, "machine-readable: print only one JSON object {answer,text,stop,stats} on stdout (for benchmarks)")
	baseURL := fs.String("base-url", "", "API base URL (default $ANTHROPIC_BASE_URL or https://api.anthropic.com)")
	fs.Usage = func() {
		fmt.Fprintf(out, "usage: sweef agent [flags] \"<task>\"\n\nRuns the browser agent headlessly. Needs ANTHROPIC_API_KEY.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	task := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if task == "" {
		fs.Usage()
		return 2
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		fmt.Fprintln(out, "sweef agent: ANTHROPIC_API_KEY is not set")
		return 2
	}
	realOut := out
	if *jsonOut {
		out = io.Discard // human-readable progress is suppressed; the JSON object goes to realOut
	}
	cfg := Config{APIKey: key, Model: *model, Effort: *effort, MaxTurns: *maxTurns, MaxCostUSD: *maxCost, ThoughtSummaries: *thoughts}
	cfg.BaseURL = *baseURL
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("ANTHROPIC_BASE_URL")
	}
	if *noThink {
		cfg.Thinking = ThinkingDisabled
	}
	if *verbose {
		cfg.Logf = func(f string, a ...any) { fmt.Fprintf(out, "[debug] "+f+"\n", a...) }
	}

	eng := engine.New(engine.DefaultOptions())
	defer eng.Close()
	be := NewRPCBackend(eng)
	inR := bufio.NewReader(in)
	var outMu sync.Mutex
	be.ConfirmFunc = func(ctx context.Context, r ConfirmRequest) (bool, error) {
		if *yes {
			return true, nil
		}
		outMu.Lock()
		fmt.Fprintf(out, "\n? Approve (%s): %s [%s]\n  allow? [y/N] ", r.Kind, r.Summary, r.Detail)
		outMu.Unlock()
		line, err := inR.ReadString('\n')
		if err != nil && line == "" {
			return false, nil
		}
		l := strings.ToLower(strings.TrimSpace(line))
		return l == "y" || l == "yes", nil
	}
	if _, err := be.OpenTab(*startURL); err != nil {
		fmt.Fprintf(out, "warning: could not load %s: %v\n", *startURL, err)
	}

	ag, err := New(cfg, be)
	if err != nil {
		fmt.Fprintf(out, "sweef agent: %v\n", err)
		return 2
	}
	defer ag.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() { // best effort: have the TLS connection ready before the first request
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if d, err := ag.Prewarm(pctx); err == nil && *verbose {
			outMu.Lock()
			fmt.Fprintf(out, "[debug] connection prewarmed in %v\n", d.Round(time.Millisecond))
			outMu.Unlock()
		}
	}()

	var final RunStats
	exit := 0
	midline := false
	var textBuf strings.Builder
	var doneText string
	st, _ := ag.Run(ctx, task, func(e Event) {
		outMu.Lock()
		defer outMu.Unlock()
		switch e.Kind {
		case EventText:
			textBuf.WriteString(e.Text)
			fmt.Fprint(out, e.Text)
			midline = !strings.HasSuffix(e.Text, "\n")
		case EventThought:
			if *thoughts {
				fmt.Fprint(out, e.Text)
			}
		case EventAction:
			if midline {
				fmt.Fprintln(out)
				midline = false
			}
			fmt.Fprintf(out, "  > %s\n", e.Text)
		case EventStatus:
			if *verbose {
				fmt.Fprintf(out, "  . %s\n", e.Text)
			}
		case EventConfirm:
			// prompting happens in ConfirmFunc
		case EventDone:
			doneText = e.Text
			if midline {
				fmt.Fprintln(out)
				midline = false
			}
			if e.Text != "" && e.Stats != nil && e.Stats.Stop != "end_turn" {
				fmt.Fprintf(out, "\n%s\n", e.Text)
			}
		case EventError:
			if midline {
				fmt.Fprintln(out)
				midline = false
			}
			fmt.Fprintf(out, "\nerror: %s\n", e.Text)
			exit = 1
		}
	})
	final = st
	if *jsonOut {
		b, _ := json.Marshal(struct {
			Answer string   `json:"answer"` // the done-tool summary (what the user reads)
			Text   string   `json:"text"`   // all assistant text
			Stop   string   `json:"stop"`
			Stats  RunStats `json:"stats"`
		}{doneText, textBuf.String(), final.Stop, final})
		fmt.Fprintln(realOut, string(b))
		return exit
	}
	printStats(out, final)
	return exit
}

func printStats(out io.Writer, s RunStats) {
	fmt.Fprintf(out, "\n--- run stats (%s) ---\n", s.Stop)
	fmt.Fprintf(out, "turns %d, requests %d, retries %d, reused connections %d\n", s.Turns, s.Requests, s.Retries, s.ReusedConnection)
	fmt.Fprintf(out, "time to first byte avg %.0f ms, time to first token avg %.0f ms (p50 %.0f), first tool call avg %.0f ms\n",
		s.FirstByteAvgMs, s.TTFTAvgMs, s.TTFTP50Ms, s.TTFToolAvgMs)
	fmt.Fprintf(out, "tool calls %d, avg latency %.1f ms, total %d ms; %d steps, %d navigations; snapshots: %d full, %d delta; compactions %d\n",
		s.ToolCalls, s.ToolAvgMs, s.ToolTotalMs, s.Actions, s.Navigations, s.FullSnapshots, s.DeltaSnapshots, s.Compactions)
	fmt.Fprintf(out, "tokens: input %d, cache read %d, cache write %d, output %d; cache hit rate %.1f%%\n",
		s.Usage.InputTokens, s.Usage.CacheReadTokens, s.Usage.CacheCreationTokens, s.Usage.OutputTokens, s.CacheHitRate*100)
	fmt.Fprintf(out, "estimated cost $%.4f, elapsed %.1f s\n", s.CostUSD, float64(s.ElapsedMs)/1000)
}
