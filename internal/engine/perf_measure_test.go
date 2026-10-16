package engine

import "testing"

// performance.measure accepts PerformanceTiming attribute names as marks (React dev builds measure from
// 'navigationStart'; it threw and react.dev showed its client-side error page).
func TestPerformanceMeasureTimingMarks(t *testing.T) {
	e := New(Options{CacheDir: "off"})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetDocument(`<!doctype html><body></body>`, "https://perf.test/")
	if v := mustEval(t, tab, `(function(){ performance.mark('a'); var m = performance.measure('m', 'navigationStart', 'a'); return m.startTime === 0 && m.duration >= 0; })()`); v != "true" {
		t.Fatalf("got %s", v)
	}
}
