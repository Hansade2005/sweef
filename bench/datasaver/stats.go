package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Agg is median/min/max of a sample.
type Agg struct {
	Median, Min, Max float64
	N                int
}

func aggOf(xs []float64) Agg {
	if len(xs) == 0 {
		return Agg{}
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	a := Agg{Min: s[0], Max: s[len(s)-1], N: len(s)}
	if len(s)%2 == 1 {
		a.Median = s[len(s)/2]
	} else {
		a.Median = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return a
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t / float64(len(xs))
}

// PlanBytes is "100 MB" in pages-per-100-MB: decimal megabytes, as data plans are sold.
const PlanBytes = 100e6

func pagesPer100MB(meanBytes float64) float64 {
	if meanBytes <= 0 {
		return 0
	}
	return PlanBytes / meanBytes
}

func (v Visit) Total() int64 { return v.WireIn + v.WireOut }

// index is a lookup over the cells.
type index struct {
	res   *Results
	cells map[string][]Cell // site|mode -> cells sorted by run
}

func newIndex(r *Results) *index {
	ix := &index{res: r, cells: map[string][]Cell{}}
	for _, c := range r.Cells {
		k := c.Site + "|" + c.Mode
		ix.cells[k] = append(ix.cells[k], c)
	}
	for k := range ix.cells {
		cs := ix.cells[k]
		sort.Slice(cs, func(i, j int) bool { return cs[i].Run < cs[j].Run })
	}
	return ix
}

func (ix *index) get(site, mode string) []Cell { return ix.cells[site+"|"+mode] }

// Exclusion explains why a site is not in the comparison.
type Exclusion struct {
	Site    string
	Reasons []string
}

// siteVerdict decides inclusion. A site is included only if EVERY executed mode has all runs for it and every one of
// them is OK (cold and warm). One failure anywhere excludes the site everywhere, so all modes cover the same pages.
func (ix *index) siteVerdict(site string) (bool, []string) {
	var reasons []string
	for _, m := range ix.res.Modes {
		cs := ix.get(site, m)
		if len(cs) < ix.res.Runs {
			reasons = append(reasons, fmt.Sprintf("%s: only %d of %d runs completed", m, len(cs), ix.res.Runs))
			continue
		}
		for _, c := range cs {
			if !c.Cold.OK {
				reasons = append(reasons, fmt.Sprintf("%s run %d cold: %s", m, c.Run, c.Cold.Fail))
			} else if c.Warm != nil && !c.Warm.OK {
				reasons = append(reasons, fmt.Sprintf("%s run %d warm: %s", m, c.Run, c.Warm.Fail))
			} else if c.Warm == nil {
				reasons = append(reasons, fmt.Sprintf("%s run %d: no warm visit", m, c.Run))
			}
		}
	}
	return len(reasons) == 0, dedupe(reasons)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Included returns the sites in the comparison, in list order, and the exclusions.
func (ix *index) Included() ([]Site, []Exclusion) {
	var inc []Site
	var exc []Exclusion
	for _, s := range ix.res.Sites {
		ok, why := ix.siteVerdict(s.URL)
		if ok {
			inc = append(inc, s)
		} else {
			exc = append(exc, Exclusion{s.URL, why})
		}
	}
	return inc, exc
}

// series extracts a per-run sample for (site, mode).
func (ix *index) series(site, mode string, warm bool, f func(Visit) float64) []float64 {
	var xs []float64
	for _, c := range ix.get(site, mode) {
		v := c.Cold
		if warm {
			if c.Warm == nil {
				continue
			}
			v = *c.Warm
		}
		xs = append(xs, f(v))
	}
	return xs
}

// ModeSummary is the per-mode total over the included sites.
type ModeSummary struct {
	Mode          string
	Sites         int
	MeanBytes     Agg // mean bytes per page, one sample per run index (median/min/max over runs)
	Pages100MB    Agg // 100 MB / mean bytes per page, per run index
	SumMedian     float64
	Requests      Agg // mean requests per page, per run index
	ReadyMs       Agg // median-over-sites of ready time, per run index
	WarmMeanBytes Agg
	WarmPages100  Agg
	WarmVsCold    float64 // sum of per-site median warm bytes / sum of per-site median cold bytes
}

func (ix *index) summarize(mode string, inc []Site) ModeSummary {
	ms := ModeSummary{Mode: mode, Sites: len(inc)}
	runs := ix.res.Runs
	var meanB, ppm, req, ready, wMeanB, wPpm []float64
	for r := 1; r <= runs; r++ {
		var b, rq, rd, wb []float64
		for _, s := range inc {
			for _, c := range ix.get(s.URL, mode) {
				if c.Run != r {
					continue
				}
				b = append(b, float64(c.Cold.Total()))
				rq = append(rq, float64(c.Cold.Requests))
				rd = append(rd, c.Cold.ReadyMs)
				if c.Warm != nil {
					wb = append(wb, float64(c.Warm.Total()))
				}
			}
		}
		if len(b) == 0 {
			continue
		}
		meanB = append(meanB, mean(b))
		ppm = append(ppm, pagesPer100MB(mean(b)))
		req = append(req, mean(rq))
		ready = append(ready, aggOf(rd).Median)
		if len(wb) > 0 {
			wMeanB = append(wMeanB, mean(wb))
			wPpm = append(wPpm, pagesPer100MB(mean(wb)))
		}
	}
	ms.MeanBytes, ms.Pages100MB, ms.Requests, ms.ReadyMs = aggOf(meanB), aggOf(ppm), aggOf(req), aggOf(ready)
	ms.WarmMeanBytes, ms.WarmPages100 = aggOf(wMeanB), aggOf(wPpm)
	var sc, sw float64
	for _, s := range inc {
		sc += aggOf(ix.series(s.URL, mode, false, func(v Visit) float64 { return float64(v.Total()) })).Median
		sw += aggOf(ix.series(s.URL, mode, true, func(v Visit) float64 { return float64(v.Total()) })).Median
	}
	ms.SumMedian = sc
	if sc > 0 {
		ms.WarmVsCold = sw / sc
	}
	return ms
}

// Headline gate -------------------------------------------------------------------------------------------------

// Headline is the only place a "% saved" number can come from.
type Headline struct {
	Allowed bool
	Reason  string // why it is withheld (when !Allowed)
	Rows    []HeadlineRow
}

type HeadlineRow struct {
	Label        string
	Base, Mode   string
	SavedPct     Agg // per-run-index saved percentage, median/min/max
	SavedMedian  float64
	BaseSum, Sum float64
}

// headline refuses unless every wanted mode ran and completed the same sites, with every run, and the run is complete.
func (ix *index) headline() Headline {
	r := ix.res
	need := []string{ModeSweefOff, ModeSweefStandard, ModeSweefStrict, ModeChromium}
	var missing []string
	for _, m := range need {
		if !contains(r.Modes, m) {
			why := "was not run"
			if s, ok := r.Skipped[m]; ok {
				why = "was not run: " + s
			}
			missing = append(missing, m+" "+why)
		}
	}
	if len(missing) > 0 {
		return Headline{Reason: "not every mode was measured: " + strings.Join(missing, "; ")}
	}
	if len(r.ModesWanted) > 0 && len(r.Modes) != len(r.ModesWanted) {
		return Headline{Reason: "the executed modes differ from the requested modes"}
	}
	if !r.Complete {
		return Headline{Reason: "the run did not complete (partial or filtered site list)"}
	}
	// Failed cells are recorded and exclude their site everywhere; MISSING cells mean the run never finished them.
	for _, m := range r.Modes {
		for _, s := range r.Sites {
			if n := len(ix.get(s.URL, m)); n != r.Runs {
				return Headline{Reason: fmt.Sprintf("%s has %d of %d runs recorded for %s", m, n, r.Runs, s.URL)}
			}
		}
	}
	inc, _ := ix.Included()
	if len(inc) == 0 {
		return Headline{Reason: "no site completed in every mode"}
	}
	// Same-sites check, stated independently of the exclusion logic: every mode must have every run for every
	// included site, cold and warm, all OK.
	for _, m := range r.Modes {
		for _, s := range inc {
			cs := ix.get(s.URL, m)
			if len(cs) != r.Runs {
				return Headline{Reason: fmt.Sprintf("%s has %d of %d runs for %s", m, len(cs), r.Runs, s.URL)}
			}
			for _, c := range cs {
				if !c.OK() || c.Warm == nil {
					return Headline{Reason: fmt.Sprintf("%s did not complete %s cleanly", m, s.URL)}
				}
			}
		}
	}
	h := Headline{Allowed: true}
	pairs := []struct{ label, base, mode string }{
		{"Sweef standard vs Sweef off", ModeSweefOff, ModeSweefStandard},
		{"Sweef strict vs Sweef off", ModeSweefOff, ModeSweefStrict},
		{"Sweef off vs Chromium", ModeChromium, ModeSweefOff},
		{"Sweef standard vs Chromium", ModeChromium, ModeSweefStandard},
		{"Sweef strict vs Chromium", ModeChromium, ModeSweefStrict},
	}
	if contains(r.Modes, ModeChromiumSD) {
		pairs = append(pairs, struct{ label, base, mode string }{"Chromium with Save-Data header vs Chromium", ModeChromium, ModeChromiumSD})
	}
	for _, p := range pairs {
		row := HeadlineRow{Label: p.label, Base: p.base, Mode: p.mode}
		var per []float64
		for run := 1; run <= r.Runs; run++ {
			var bs, ms float64
			for _, s := range inc {
				for _, c := range ix.get(s.URL, p.base) {
					if c.Run == run {
						bs += float64(c.Cold.Total())
					}
				}
				for _, c := range ix.get(s.URL, p.mode) {
					if c.Run == run {
						ms += float64(c.Cold.Total())
					}
				}
			}
			if bs > 0 {
				per = append(per, 100*(1-ms/bs))
			}
		}
		row.SavedPct = aggOf(per)
		for _, s := range inc {
			row.BaseSum += aggOf(ix.series(s.URL, p.base, false, func(v Visit) float64 { return float64(v.Total()) })).Median
			row.Sum += aggOf(ix.series(s.URL, p.mode, false, func(v Visit) float64 { return float64(v.Total()) })).Median
		}
		if row.BaseSum > 0 {
			row.SavedMedian = 100 * (1 - row.Sum/row.BaseSum)
		}
		h.Rows = append(h.Rows, row)
	}
	return h
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Formatting helpers --------------------------------------------------------------------------------------------

func kb(b float64) string {
	switch {
	case math.Abs(b) >= 1e6:
		return fmt.Sprintf("%.2f MB", b/1e6)
	default:
		return fmt.Sprintf("%.0f KB", b/1e3)
	}
}

func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
