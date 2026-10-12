package app

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/app/state"
)

func TestSmokeOpenAndShoot(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitFor("title", func() bool { tab, ok := h.app.activeTab(); return ok && tab.Title == "Home Page" })
	h.idle()
	p := h.shot("smoke.png")
	t.Log("shot:", p)
	var st state.Tab
	st, _ = h.app.activeTab()
	t.Logf("tab: %+v", st)
	t.Log(h.app.Stats().Format())
}
