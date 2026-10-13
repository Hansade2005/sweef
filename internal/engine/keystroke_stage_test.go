package engine_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

const omniItemsJS = `[{"type":"history","title":"sweef/sweef: a fast, light browser written in Go","url":"https://github.com/sweefbrowser/sweef"},{"type":"history","title":"GitHub: Let's build from here","url":"https://github.com/"},{"type":"bookmark","title":"Pro Git book","url":"https://git-scm.com/book/en/v2"},{"type":"tab","title":"Lo-fi beats to refactor to","url":"https://www.youtube.com/watch?v=abc","tab":"t3"},{"type":"search","text":"git rebase vs merge"},{"type":"search","text":"git stash pop"}]`

// BenchmarkKeystrokeStages is the per-stage breakdown of one omnibox keystroke
// on the real chrome page, shaped like the app's path: the key goes through
// __swDispatchKey into the focused input (keydown, input, the page's handler),
// the engine paints, then (two-update variant) the suggestions arrive as a
// second state patch and the engine paints again. Reported per key: js-ms (page
// JavaScript incl. any layout it forces), layout-ms (style + layout + display
// list, wherever it ran), raster-ms (incremental raster + snapshot copy),
// layouts (style+layout runs), and the damaged area.
//
//	go test ./internal/engine -run xxx -bench KeystrokeStages -benchtime 300x
func BenchmarkKeystrokeStages(b *testing.B) {
	for _, scale := range []float64{1, 2} {
		for _, two := range []bool{true, false} {
			b.Run(fmt.Sprintf("%gx/updates=%d", scale, map[bool]int{true: 2, false: 1}[two]), func(b *testing.B) {
				s, done := chromeSession(b, scale)
				defer done()
				o := engine.PaintOptions{Width: 1280, Height: 800, DSF: scale, Transparent: true, Incremental: true}
				s.Tab.PaintRGBAOpts(o)
				s.Run(`var u=document.getElementById('url');u.focus();`)
				paint := func() (time.Duration, time.Duration) {
					s.Tab.PaintRGBAOpts(o)
					pi := s.Tab.LastPaintInfo()
					return pi.Layout, pi.Raster
				}
				// the typed text goes "gi" -> "git" on every iteration; the suggestions
				// that arrive afterwards are the same six items (all matching), like
				// the history answers for a prefix the user keeps extending
				sugg := func(i int) string {
					return `__send('omni.suggestions',{id:SW.omniSeq,items:` + omniItemsJS + `});`
				}
				key := func(i int) string {
					return `(function(){var u=document.getElementById('url');u.value='gi';u.setSelectionRange(2,2);__swDispatchKey(__swIdOf(u),"t",{});})()`
				}
				var jsT, layT, rasT, tot time.Duration
				var lays int64
				var area int
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					n0, _ := s.Tab.LayoutStats()
					t0 := time.Now()
					j0 := time.Now()
					s.Tab.Evaluate(key(i))
					jsT += time.Since(j0)
					l, r := paint()
					layT += l
					rasT += r
					area += s.Tab.LastPaintInfo().Rect.Dx() * s.Tab.LastPaintInfo().Rect.Dy()
					if two {
						j0 = time.Now()
						s.Tab.Evaluate(sugg(i))
						jsT += time.Since(j0)
						l, r = paint()
						layT += l
						rasT += r
						area += s.Tab.LastPaintInfo().Rect.Dx() * s.Tab.LastPaintInfo().Rect.Dy()
					}
					tot += time.Since(t0)
					n1, _ := s.Tab.LayoutStats()
					lays += n1 - n0
				}
				f := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 / float64(b.N) }
				b.ReportMetric(f(tot), "total-ms")
				b.ReportMetric(f(jsT), "js-ms")
				b.ReportMetric(f(layT), "layout-ms")
				b.ReportMetric(f(rasT), "raster-ms")
				b.ReportMetric(float64(lays)/float64(b.N), "layouts")
				b.ReportMetric(float64(area)/float64(b.N), "px-copied")
			})
		}
	}
}
