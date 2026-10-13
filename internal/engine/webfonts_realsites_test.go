package engine

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealSitesFontBytes loads real pages (a list of URLs in SWEEF_REAL_SITES, separated by
// spaces; it is skipped without it) in a cold context at each Data Saver level and prints
// what the web fonts cost: the font bytes, the faces declared and loaded and the fonts skipped.
//
//	SWEEF_REAL_SITES="https://go.dev/doc/effective_go https://pypi.org/project/requests" \
//	  go test ./internal/engine -run TestRealSitesFontBytes -v
func TestRealSitesFontBytes(t *testing.T) {
	list := os.Getenv("SWEEF_REAL_SITES")
	if list == "" {
		t.Skip()
	}
	for _, u := range strings.Fields(list) {
		for _, lvl := range []SaverLevel{SaverOff, SaverStandard, SaverStrict} {
			o := DefaultOptions()
			o.SettleMs = 1500
			o.CacheDir = t.TempDir()
			e := New(o)
			c := e.NewContext("", ContextOptions{})
			if lvl != SaverOff {
				c.SetDataPolicy(PolicyFor(lvl))
			}
			tab, _ := c.NewTab("about:blank")
			t0 := time.Now()
			err := tab.Navigate(u, nil)
			_, _ = tab.PaintRGBA(1280, 720, 1, false)
			st := c.DataStats()
			faces := 0
			loaded := 0
			if env := tab.ds.env.Load(); env != nil {
				if set := env.fonts.webSet(); set != nil {
					for _, f := range set.Faces() {
						faces++
						if f.State().String() == "loaded" {
							loaded++
						}
					}
				}
			}
			fmt.Printf("%-55s %-8s err=%v total=%8d font=%7d faces=%d loaded=%d skippedFonts=%d t=%v\n", u, lvl, err, st.WireIn+st.WireOut, st.ByKind[KindFont], faces, loaded, st.Skipped[KindFont], time.Since(t0).Round(time.Millisecond))
			e.Close()
		}
	}
}
