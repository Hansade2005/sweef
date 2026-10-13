package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"sort"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/headless"
	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/ui"
)

// openProfile measures only the keystroke that opens the omnibox dropdown
// (the Backspace that closes it is not timed) and writes a CPU profile.
func openProfile(path string, scale float64) {
	site := startSite()
	defer site.Close()
	p := startApp(ui.FS, []string{site.URL + "/red", site.URL + "/green", site.URL + "/a/1", site.URL + "/link"}, scale)
	defer p.stop()
	p.idle()
	time.Sleep(300 * time.Millisecond)
	p.key("ctrl+l")
	p.idle()
	var ds, cs []time.Duration
	f, _ := os.Create(path)
	pprof.StartCPUProfile(f)
	for i := 0; i < 200; i++ {
		d, ok := p.win.PushAndMeasure(shell.Event{Kind: shell.EvText, Text: "a"}, 3*time.Second)
		if ok && i >= 10 {
			ds = append(ds, d)
		}
		p.idle()
		if d, ok := p.win.PushAndMeasure(headless.KeyEvents("Backspace")[0], 3*time.Second); ok && i >= 10 {
			cs = append(cs, d)
		}
		p.idle()
	}
	pprof.StopCPUProfile()
	f.Close()
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	fmt.Printf("open key: n=%d p50 %s p90 %s p99 %s\n", len(ds), ms(ds[len(ds)/2]), ms(ds[len(ds)*9/10]), ms(ds[len(ds)*99/100]))
	sort.Slice(cs, func(i, j int) bool { return cs[i] < cs[j] })
	fmt.Printf("close key: n=%d p50 %s p90 %s p99 %s\n", len(cs), ms(cs[len(cs)/2]), ms(cs[len(cs)*9/10]), ms(cs[len(cs)*99/100]))
	fmt.Print(p.a.Stats().Format())
}
