package app

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

// TestScrollBench measures scrolling a long text page and a media-heavy page on the headless shell. It is a
// measurement, not a gate: it runs only with SWEEF_SCROLLBENCH=1 and prints a table (go test -run ScrollBench -v).
// The method is fixed so numbers from different commits compare: 1920x1080, 1x, a flick of precise wheel events
// (DY=48 every 8 ms, 400 events), then a stretch of 120-px notches every 50 ms, then the same flick again and a
// flick back up; frame times are the gaps between Present calls while events were being sent.
func TestScrollBench(t *testing.T) {
	if os.Getenv("SWEEF_SCROLLBENCH") == "" {
		t.Skip("set SWEEF_SCROLLBENCH=1")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/long", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`<!doctype html><html><head><title>Long</title><style>body{margin:0 auto;max-width:900px;font:16px/1.5 sans-serif}p{margin:0 0 16px}h2{margin:24px 0 8px}</style></head><body>`)
		for i := 0; i < 600; i++ {
			fmt.Fprintf(&b, `<h2>Section %d</h2><p>Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur %d.</p>`, i, i)
		}
		b.WriteString(`</body></html>`)
		fmt.Fprint(w, b.String())
	})
	mux.HandleFunc("/img.png", func(w http.ResponseWriter, r *http.Request) {
		im := image.NewRGBA(image.Rect(0, 0, 640, 360))
		for y := 0; y < 360; y++ {
			for x := 0; x < 640; x++ {
				im.SetRGBA(x, y, color.RGBA{uint8(x / 3), uint8(y / 2), uint8((x + y) / 5), 255})
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, im)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	})
	mux.HandleFunc("/media", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`<!doctype html><html><head><title>Media</title><style>body{margin:0;background:#111;color:#eee;font:16px sans-serif}.g{display:grid;grid-template-columns:repeat(3,1fr);gap:16px;padding:16px}.c{background:#222;border-radius:12px;overflow:hidden;box-shadow:0 2px 8px #000}.c img{width:100%;display:block}.c div{padding:8px}</style></head><body><div class="g">`)
		for i := 0; i < 300; i++ {
			fmt.Fprintf(&b, `<div class="c"><img src="/img.png?i=%d" width="640" height="360"><div>Video title number %d with some description text</div></div>`, i%8, i)
		}
		b.WriteString(`</div></body></html>`)
		fmt.Fprint(w, b.String())
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, pg := range []string{"/long", "/media"} {
		pg := pg
		t.Run(strings.TrimPrefix(pg, "/"), func(t *testing.T) { scrollBenchPage(t, srv.URL+pg) })
	}
}

func scrollBenchPage(t *testing.T, url string) {
	h := newHarness(t, hopts{w: 1920, h: 1080, cfg: Config{InitialURLs: []string{url}}})
	h.waitFor("page", func() bool { return h.tab().Title != "" })
	time.Sleep(1500 * time.Millisecond) // images
	h.idle()
	_, max := h.app.comp.Scroll()
	t.Logf("%s: scroll range %d device px", url, max)
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: 900, Y: 600})
	h.idle()

	run := func(name string, n int, gap time.Duration, ev shell.Event) {
		p0 := h.app.stats.pagePaints.Load()
		blank0 := h.app.comp.Stats().Blank
		frames0 := h.app.comp.Stats().Frames
		t0 := time.Now()
		next := t0
		for i := 0; i < n; i++ {
			h.win.Push(ev)
			next = next.Add(gap)
			if d := time.Until(next); d > 0 {
				time.Sleep(d)
			}
		}
		h.idle()
		el := time.Since(t0)
		times := h.win.PresentTimes()
		var gaps []float64
		var worst float64
		var worstAt time.Duration
		for i := 1; i < len(times); i++ {
			if times[i-1].Before(t0) || times[i].After(t0.Add(time.Duration(n)*gap)) {
				continue
			}
			g := float64(times[i].Sub(times[i-1]).Microseconds()) / 1000
			gaps = append(gaps, g)
			if g > worst {
				worst, worstAt = g, times[i-1].Sub(t0)
			}
		}
		sort.Float64s(gaps)
		pc := func(p float64) float64 {
			if len(gaps) == 0 {
				return 0
			}
			return gaps[int(p*float64(len(gaps)-1))]
		}
		pos, _ := h.app.comp.Scroll()
		st := h.app.comp.Stats()
		pp := st.Paint["page"]
		t.Logf("%-26s worst gap %.0fms after %v", name, worst, worstAt)
		t.Logf("%-26s blank frames %d of %d composed", name, st.Blank-blank0, st.Frames-frames0)
		t.Logf("%-26s events=%d wall=%.0fms frames=%d gap p50=%.1f p90=%.1f p99=%.1f max=%.1f ms | pos=%d pagePaints=%d pagePaint p50=%v p99=%v max=%v | compose p50=%v p99=%v",
			name, n, float64(el.Milliseconds()), len(gaps), pc(.5), pc(.9), pc(.99), pc(1), pos, h.app.stats.pagePaints.Load()-p0, pp.P50, pp.P99, pp.Max, st.Compose.P50, st.Compose.P99)
	}
	run("flick (precise 48px/8ms)", 400, 8*time.Millisecond, shell.Event{Kind: shell.EvWheel, X: 900, Y: 600, DY: 48, Precise: true})
	run("notches (120px/50ms)", 150, 50*time.Millisecond, shell.Event{Kind: shell.EvWheel, X: 900, Y: 600, DY: 120})
	run("flick again (deeper)", 400, 8*time.Millisecond, shell.Event{Kind: shell.EvWheel, X: 900, Y: 600, DY: 48, Precise: true})
	run("up (precise -48px/8ms)", 600, 8*time.Millisecond, shell.Event{Kind: shell.EvWheel, X: 900, Y: 600, DY: -48, Precise: true})
}
