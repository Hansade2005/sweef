package app

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/shell"
)

func TestDbgCursor(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: 60, Y: float64(vpTop + 150)})
	h.idle()
	t.Log("cursor:", h.win.Cursor(), "vp:", h.app.vpLog, "target:", h.app.target(60, float64(vpTop+150)), "shown", h.app.cursorShown)
}
