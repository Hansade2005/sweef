package app

import (
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/headless"
	"github.com/sweefbrowser/sweef/internal/shell"
)

// BenchmarkChromeKeystroke measures the end-to-end cost of one omnibox
// keystroke with the real chrome page (ui.FS): event -> chrome JS -> chrome
// repaint -> compose -> Present.
//
//	go test ./internal/app -run xxx -bench ChromeKeystroke -benchtime 100x -cpuprofile cpu.out
func BenchmarkChromeKeystroke(b *testing.B)   { benchKeystroke(b, 1) }
func BenchmarkChromeKeystroke2x(b *testing.B) { benchKeystroke(b, 2) }

// BenchmarkChromeTyping is the sustained case: letters typed one after the
// other into a non-empty omnibox (the dropdown stays open, only its rows and the
// text change). BenchmarkChromeKeystroke alternates a letter and Backspace, which
// opens and closes the dropdown on every other key.
func BenchmarkChromeTyping(b *testing.B)   { benchTyping(b, 1) }
func BenchmarkChromeTyping2x(b *testing.B) { benchTyping(b, 2) }

func benchTyping(b *testing.B, scale float64) {
	srv := pageServer(b)
	cfg := Config{InitialURLs: []string{srv.URL + "/two"}}
	cfg.ChromeFS = realUIFS()
	h := newHarness(b, hopts{w: 1280, h: 800, scale: scale, cfg: cfg})
	h.waitTitle("Page Two")
	h.waitUIReady()
	h.key("ctrl+l")
	h.typeText("sw") // the dropdown is open from here on
	h.idle()
	b.ResetTimer()
	ds := make([]float64, 0, b.N)
	for i := 0; i < b.N; i++ {
		if i%16 == 15 {
			b.StopTimer()
			h.key("ctrl+a")
			h.typeText("sw")
			b.StartTimer()
		}
		b.StartTimer()
		d, ok := h.win.PushAndMeasure(shell.Event{Kind: shell.EvText, Text: string(rune('a' + i%26))}, 3*time.Second)
		b.StopTimer()
		if ok {
			ds = append(ds, float64(d.Microseconds())/1000)
		}
		h.idle()
	}
	b.ReportMetric(pctF(ds, 0.5), "p50-ms")
	b.ReportMetric(pctF(ds, 0.99), "p99-ms")
}

func benchKeystroke(b *testing.B, scale float64) {
	srv := pageServer(b)
	cfg := Config{InitialURLs: []string{srv.URL + "/two"}}
	cfg.ChromeFS = realUIFS()
	h := newHarness(b, hopts{w: 1280, h: 800, scale: scale, cfg: cfg})
	h.waitTitle("Page Two")
	h.waitUIReady()
	h.key("ctrl+l")
	h.idle()
	b.ResetTimer()
	paints0 := h.app.stats.chromePaints.Load()
	var tot time.Duration
	ds := make([]float64, 0, b.N)
	for i := 0; i < b.N; i++ {
		ev := shell.Event{Kind: shell.EvText, Text: "a"}
		if i%2 == 1 {
			ev = headless.KeyEvents("Backspace")[0]
		}
		b.StartTimer()
		d, ok := h.win.PushAndMeasure(ev, 3*time.Second)
		b.StopTimer()
		if ok {
			tot += d
			ds = append(ds, float64(d.Microseconds())/1000)
		}
		h.idle()
	}
	b.ReportMetric(float64(h.app.stats.chromePaints.Load()-paints0)/float64(b.N), "paints/key")
	b.ReportMetric(pctF(ds, 0.5), "p50-ms")
	b.ReportMetric(pctF(ds, 0.99), "p99-ms")
}

func pctF(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	return c[int(p*float64(len(c)-1))]
}
