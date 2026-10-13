package main

import (
	"strings"
	"testing"
)

func mkVisit(in, out int64) Visit {
	return Visit{OK: true, Status: 200, WireIn: in, WireOut: out, Requests: 3}
}

func mkResults(modes []string, skipped map[string]string, complete bool) *Results {
	r := &Results{Runs: 3, Modes: modes, ModesWanted: defaultModes, Skipped: skipped, Complete: complete,
		Sites: []Site{{URL: "https://a.example/"}, {URL: "https://b.example/"}}}
	for _, s := range r.Sites {
		for _, m := range modes {
			for run := 1; run <= 3; run++ {
				w := mkVisit(50, 5)
				r.Cells = append(r.Cells, Cell{Site: s.URL, Mode: m, Run: run, Cold: mkVisit(100, 10), Warm: &w})
			}
		}
	}
	return r
}

func TestHeadlineRefusedWhenModesMissing(t *testing.T) {
	r := mkResults([]string{ModeSweefOff, ModeChromium, ModeChromiumSD}, map[string]string{ModeSweefStandard: "no api", ModeSweefStrict: "no api"}, true)
	h := newIndex(r).headline()
	if h.Allowed || !strings.Contains(h.Reason, "sweef-standard") {
		t.Fatalf("headline must be withheld: %+v", h)
	}
}

func TestHeadlineRefusedWhenARunIsMissing(t *testing.T) {
	r := mkResults(defaultModes, nil, true)
	r.Cells = r.Cells[:len(r.Cells)-1] // one mode lost one run on the last site
	h := newIndex(r).headline()
	if h.Allowed {
		t.Fatalf("headline must be withheld when a cell is missing")
	}
}

func TestHeadlineRefusedWhenIncomplete(t *testing.T) {
	r := mkResults(defaultModes, nil, false)
	if newIndex(r).headline().Allowed {
		t.Fatal("incomplete run must not get a headline")
	}
}

func TestOneFailureExcludesSiteEverywhere(t *testing.T) {
	r := mkResults(defaultModes, nil, true)
	for i := range r.Cells {
		c := &r.Cells[i]
		if c.Site == "https://b.example/" && c.Mode == ModeChromium && c.Run == 2 {
			c.Cold.OK, c.Cold.Fail = false, "main document HTTP 403"
		}
	}
	ix := newIndex(r)
	inc, exc := ix.Included()
	if len(inc) != 1 || inc[0].URL != "https://a.example/" || len(exc) != 1 {
		t.Fatalf("inc=%v exc=%v", inc, exc)
	}
	h := ix.headline()
	if !h.Allowed {
		t.Fatalf("remaining sites are complete in every mode, headline should be allowed: %s", h.Reason)
	}
	// every mode is computed over the same single site
	for _, m := range defaultModes {
		if s := ix.summarize(m, inc); s.Sites != 1 {
			t.Fatalf("%s covers %d sites", m, s.Sites)
		}
	}
}

func TestHeadlineMath(t *testing.T) {
	r := mkResults(defaultModes, nil, true)
	for i := range r.Cells {
		if r.Cells[i].Mode == ModeSweefStandard {
			r.Cells[i].Cold = mkVisit(50, 5) // half of 110
		}
	}
	h := newIndex(r).headline()
	if !h.Allowed {
		t.Fatal(h.Reason)
	}
	for _, row := range h.Rows {
		if row.Label == "Sweef standard vs Sweef off" && (row.SavedPct.Median < 49.9 || row.SavedPct.Median > 50.1) {
			t.Fatalf("saved %v", row.SavedPct)
		}
	}
}

func TestPagesPer100MB(t *testing.T) {
	if got := pagesPer100MB(2e6); got != 50 {
		t.Fatalf("got %v", got)
	}
	a := aggOf([]float64{3, 1, 2})
	if a.Median != 2 || a.Min != 1 || a.Max != 3 {
		t.Fatalf("%+v", a)
	}
}

func TestSplitInternal(t *testing.T) {
	tot := Totals{In: 300, Out: 30, Conns: 3, Records: []ConnRecord{
		{Host: "news.example:443", In: 100, Out: 10},
		{Host: "accounts.google.com:443", In: 150, Out: 15},
		{Host: "www.google.com:443", In: 50, Out: 5}, // the page itself asked for www.google.com: stays page traffic
	}}
	page, internal, hosts := splitInternal(tot, map[string]bool{"news.example": true, "www.google.com": true})
	if internal.In != 150 || internal.Out != 15 || page.In != 150 || page.Out != 15 || page.Conns != 2 || len(hosts) != 1 {
		t.Fatalf("page=%+v internal=%+v hosts=%v", page, internal, hosts)
	}
}
