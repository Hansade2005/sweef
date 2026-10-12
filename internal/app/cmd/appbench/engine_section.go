package main

import (
	"fmt"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/ui/preview"
)

// engineBefore are the engine-level figures measured on commit fd6cd8f with
// BenchmarkChromePaintFull and BenchmarkChromeKeystrokeEngine
// (internal/engine/paint_bench_test.go), the same scenario: the real chrome page
// with the omnibox dropdown open. Milliseconds.
var engineBefore = map[string]float64{
	"full1x": 89.31, "full2x": 256.66, "key1x": 106.98, "key2x": 274.86,
}

// engineSection times the engine alone (no window, no compositor) on the real
// chrome page: a full repaint, and one omnibox keystroke (page JavaScript, style
// and layout, repaint).
func engineSection(r *report, n int) {
	fmt.Println("engine repaint ...")
	srv := preview.Server()
	defer srv.Close()
	e := preview.Engine()
	defer e.Close()
	var sc preview.Scenario
	for _, s := range preview.Scenarios() {
		if s.Name == "chrome-omnibox" {
			sc = s
		}
	}
	type res struct{ p50, p99 time.Duration }
	pct := func(ds []time.Duration) res { return res{percentile(ds, 0.5), percentile(ds, 0.99)} }
	out := map[string]res{}
	for _, scale := range []float64{1, 2} {
		s, err := preview.Open(e, srv.URL, sc, scale)
		if err != nil {
			fmt.Println("engine section:", err)
			return
		}
		s.Run(sc.Setup)
		s.Settle()
		full := engine.PaintOptions{Width: 1280, Height: 800, DSF: scale, Transparent: true}
		inc := full
		inc.Incremental = true
		s.Tab.PaintRGBAOpts(full)
		s.Tab.PaintRGBAOpts(inc)
		tag := fmt.Sprintf("%dx", int(scale))
		var fp, kf, ki []time.Duration
		for i := 0; i < n; i++ {
			t0 := time.Now()
			s.Tab.PaintRGBAOpts(full)
			fp = append(fp, time.Since(t0))
		}
		js := func(v string) string {
			return `(function(){var u=document.getElementById('url');u.value=` + fmt.Sprintf("%q", v) + `;u.setSelectionRange(u.value.length,u.value.length);u.dispatchEvent(new Event('input',{bubbles:true}));})()`
		}
		for i := 0; i < n; i++ {
			v := "git"
			if i%2 == 1 {
				v = "gi"
			}
			t0 := time.Now()
			s.Tab.Evaluate(js(v))
			s.Tab.PaintRGBAOpts(full)
			kf = append(kf, time.Since(t0))
		}
		for i := 0; i < n; i++ {
			v := "git"
			if i%2 == 1 {
				v = "gi"
			}
			t0 := time.Now()
			s.Tab.Evaluate(js(v))
			s.Tab.PaintRGBAOpts(inc)
			ki = append(ki, time.Since(t0))
		}
		out["full"+tag], out["keyfull"+tag], out["keyinc"+tag] = pct(fp), pct(kf), pct(ki)
		e.CloseTab(s.Tab.ID)
	}
	r.f("### Engine alone on the chrome page\n\n")
	r.f("The real chrome page with the omnibox dropdown open, painted through the engine without a window (`internal/ui/preview`), %d samples. \"Keystroke\" is the page's input handler (JavaScript) plus style, layout and repaint. Before = commit fd6cd8f.\n\n", n)
	r.f("| | before p50 | after p50 | after p99 |\n|---|---|---|---|\n")
	row := func(name, before, key string) {
		b := "n/a"
		if v, ok := engineBefore[before]; ok {
			b = fmtMS(v)
		}
		x := out[key]
		r.f("| %s | %s | %s | %s |\n", name, b, ms(x.p50), ms(x.p99))
	}
	row("full repaint, 1x (resize, first paint, scale change)", "full1x", "full1x")
	row("full repaint, 2x", "full2x", "full2x")
	row("omnibox keystroke, 1x, full repaint after it", "key1x", "keyfull1x")
	row("omnibox keystroke, 2x, full repaint after it", "key2x", "keyfull2x")
	row("omnibox keystroke, 1x, incremental repaint", "-", "keyinc1x")
	row("omnibox keystroke, 2x, incremental repaint", "-", "keyinc2x")
	r.f("\n")
}
