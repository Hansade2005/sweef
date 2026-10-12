package main

import (
	"fmt"
	"time"
)

// before holds the chrome latencies measured before the incremental paint work:
// this same program run on the engine/raster/render/style code of commit fd6cd8f
// on this machine (a git worktree of the parent commit; only the benchmark
// program and its test helpers are newer). Milliseconds.
var before = map[string][2]float64{
	"alt1x":    {84.57, 108.81},
	"typing1x": {81.25, 116.76},
	"alt2x":    {201.07, 261.14},
	"typing2x": {197.05, 260.39},
	"switch1x": {10.42, 18.71},
	"switch2x": {38.06, 54.09},
	"wheel1x":  {2.94, 3.54},
}

// measured collects single figures from the other sections.
var measured = map[string]float64{}

func msf(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func chromeRepaintSection(r *report) {
	r.f("### Chrome repaint: before and after\n\n")
	r.f("Target (task brief, `docs/gui-contract.md`): typical chrome updates <= 8 ms p50 / <= 16 ms p99 at 1x and about <= 16 ms at 2x. \"Before\" is the code at commit fd6cd8f (every chrome mutation did a full style recalculation, layout, display list and a full-window raster); \"after\" is this run.\n\n")
	r.f("| chrome interaction (event to Present) | before p50 | before p99 | after p50 | after p99 | target |\n|---|---|---|---|---|---|\n")
	row := func(name, key, target string) {
		ds := chromeKeys[key]
		b := before[key]
		bs := func(v float64) string {
			if v == 0 {
				return "n/a"
			}
			return fmtMS(v)
		}
		r.f("| %s | %s | %s | %s | %s | %s |\n", name, bs(b[0]), bs(b[1]), ms(percentile(ds, 0.5)), ms(percentile(ds, 0.99)), target)
	}
	row("omnibox keystroke, 1x (letter / Backspace: dropdown opens and closes)", "alt1x", "8 / 16 ms")
	row("omnibox typing, 1x (dropdown open)", "typing1x", "8 / 16 ms")
	row("omnibox keystroke, 2x", "alt2x", "16 ms")
	row("omnibox typing, 2x", "typing2x", "16 ms")
	row("tab switch, 1x", "switch1x", "-")
	row("tab switch, 2x", "switch2x", "-")
	r.f("\n")
	r.f("Other figures from this run against the same baseline: first frame with an empty profile %s -> %s (warm %s -> %s), RSS idle with one tab %.1f -> %.1f MiB, with ten tabs %.1f -> %.1f MiB (the chrome keeps a copy of its last paint and a snapshot, and the shadow / gradient / stylesheet caches are bounded).\n\n",
		fmtMS(401.29), fmtMS(measured["empty profile (no bytecode cache)"]), fmtMS(311.86), fmtMS(measured["second launch (bytecode cache warm)"]), 94.3, measured["rss1"], 153.5, measured["rss10"])
	t1 := percentile(chromeKeys["typing1x"], 0.5)
	t99 := percentile(chromeKeys["typing1x"], 0.99)
	t2 := percentile(chromeKeys["typing2x"], 0.5)
	switch {
	case msf(t1) <= 8 && msf(t99) <= 16 && msf(t2) <= 16:
		r.f("**The targets are met for sustained typing** (1x p50 %s, p99 %s; 2x p50 %s).\n\n", ms(t1), ms(t99), ms(t2))
	default:
		r.f("**The targets are not met yet**: sustained typing at 1x is p50 %s (target 8 ms) / p99 %s (target 16 ms), at 2x p50 %s (target 16 ms). What is left is mostly not painting. Measured by the app during this run (frame timing table above): the chrome page's JavaScript input handler p50 %.2f ms (QuickJS interpreter, the page's own code), style + layout p50 %.2f ms and raster of the damage p50 %.2f ms per repaint (the omnibox's suggestions arrive as a second update, so a keystroke pays style + layout twice, and the second pays no raster when the rows did not change). The next step is incremental style and layout (the engine recomputes styles and the whole box tree for every DOM mutation), which this change does not do.\n\n",
			ms(t1), ms(t99), ms(t2), measured["chrome: input handler (JS) p50"], measured["chrome: style + layout p50"], measured["chrome: raster p50"])
	}
	r.f("What changed (all of it produces bit-identical pixels to a full paint; `internal/engine/paint_golden_test.go` pins 1x/2x full paints of every UI scenario to hashes recorded before the change and compares each incremental repaint with a full paint): the engine compares the display list of the new paint with the previous one (content + enclosing clip/layer context per op, Myers diff) and repaints only the rows the difference can reach, starting from a cached snapshot of the part of the page that did not change; box-shadow coverage, gradient tiles and parsed stylesheets are cached; the box blur, bilinear image copy, rectangle fills and clip rectangles got fast paths that produce the same bytes; the SVG cascade is indexed.\n\n")
}

func fmtMS(v float64) string { return fmt.Sprintf("%.2f ms", v) }
