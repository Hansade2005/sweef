package engine_test

import (
	"fmt"
	"testing"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/ui/preview"
)

// chromeSession opens the real chrome page with the omnibox dropdown showing.
func chromeSession(b *testing.B, scale float64) (*preview.Session, func()) {
	srv := preview.Server()
	e := preview.Engine()
	var sc preview.Scenario
	for _, s := range preview.Scenarios() {
		if s.Name == "chrome-omnibox" {
			sc = s
		}
	}
	s, err := preview.Open(e, srv.URL, sc, scale)
	if err != nil {
		b.Fatal(err)
	}
	s.Run(sc.Setup)
	s.Settle()
	return s, func() { e.Close(); srv.Close() }
}

// BenchmarkChromePaintFull is one full transparent chrome repaint (layout is cached after the first).
func BenchmarkChromePaintFull(b *testing.B) {
	for _, scale := range []float64{1, 2} {
		b.Run(fmt.Sprintf("%gx", scale), func(b *testing.B) {
			s, done := chromeSession(b, scale)
			defer done()
			o := engine.PaintOptions{Width: 1280, Height: 800, DSF: scale, Transparent: true}
			s.Tab.PaintRGBAOpts(o)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.Tab.PaintRGBAOpts(o)
			}
		})
	}
}

// BenchmarkChromeKeystrokeEngine is the engine-only cost of one omnibox keystroke:
// the input event (page JS), layout and the (incremental) repaint.
func BenchmarkChromeKeystrokeEngine(b *testing.B) {
	for _, scale := range []float64{1, 2} {
		for _, inc := range []bool{false, true} {
			b.Run(fmt.Sprintf("%gx/incremental=%v", scale, inc), func(b *testing.B) {
				s, done := chromeSession(b, scale)
				defer done()
				o := engine.PaintOptions{Width: 1280, Height: 800, DSF: scale, Transparent: true, Incremental: inc}
				s.Tab.PaintRGBAOpts(o)
				js := func(v string) string {
					return `(function(){var u=document.getElementById('url');u.value=` + fmt.Sprintf("%q", v) + `;u.setSelectionRange(u.value.length,u.value.length);u.dispatchEvent(new Event('input',{bubbles:true}));})()`
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					v := "git"
					if i%2 == 1 {
						v = "gi"
					}
					s.Tab.Evaluate(js(v))
					s.Tab.PaintRGBAOpts(o)
				}
			})
		}
	}
}
