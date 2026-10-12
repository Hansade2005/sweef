package main

import (
	"fmt"
	"image"
	"image/color"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/compositor"
	"github.com/sweefbrowser/sweef/internal/app/omnibox"
	"github.com/sweefbrowser/sweef/internal/app/privacy"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

func synthHistory(n int) (*store.History, []string) {
	rnd := rand.New(rand.NewSource(1))
	words := strings.Fields("alpha beta gamma delta news world sport tech cloud rust golang python recipe travel music film game book science health money weather map mail video photo shop deal review guide how what why")
	h := store.OpenHistory(store.NewMemBackend())
	now := time.Now()
	hosts := make([]string, 5000)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("%s%d.%s", words[rnd.Intn(len(words))], i, []string{"com", "org", "net", "io"}[rnd.Intn(4)])
	}
	for i := 0; i < n; i++ {
		u := "https://" + hosts[rnd.Intn(len(hosts))] + "/" + words[rnd.Intn(len(words))] + "/" + words[rnd.Intn(len(words))] + fmt.Sprint(i)
		t := words[rnd.Intn(len(words))] + " " + words[rnd.Intn(len(words))] + " " + words[rnd.Intn(len(words))]
		h.VisitAt(u, t, rnd.Intn(10) == 0, now.Add(-time.Duration(rnd.Intn(90*24))*time.Hour))
	}
	var qs []string
	for i := 0; i < 60; i++ {
		w := words[rnd.Intn(len(words))]
		qs = append(qs, w[:min(len(w), 2+rnd.Intn(3))])
	}
	qs = append(qs, "zzzz", "qx7", "ww", "mail rust", "photo gu", "1234", "xyzzy", "golang.org")
	return h, qs
}

func suggestBench(r *report) {
	fmt.Println("history + omnibox ...")
	r.f("## History search and omnibox suggestions\n\n")
	r.f("Synthetic history (random hosts, paths, titles, 90 days of visits), searched with %d mixed queries (short prefixes, substrings, multi-term, and queries with no match), 4 passes.\n\n", 68)
	r.f("| entries | history search p50 | p99 | max | omnibox suggest (history+bookmarks+tabs+templates) p50 | p99 | max |\n|---|---|---|---|---|---|---|\n")
	for _, n := range []int{10_000, 100_000} {
		h, qs := synthHistory(n)
		h.Search("warm", 8)
		var hs, om []time.Duration
		src := omnibox.Sources{
			History: func(q string, k int) []omnibox.HistoryHit {
				var out []omnibox.HistoryHit
				for _, e := range h.Search(q, k) {
					out = append(out, omnibox.HistoryHit{URL: e.URL, Title: e.Title, Frecency: e.Frec, Typed: e.Typed})
				}
				return out
			},
			Tabs: func() []omnibox.TabHit {
				return []omnibox.TabHit{{ID: "t1", URL: "https://golang.org/", Title: "Go"}, {ID: "t2", URL: "https://news.test/", Title: "News"}}
			},
		}
		for pass := 0; pass < 4; pass++ {
			for _, q := range qs {
				t0 := time.Now()
				h.Search(q, 8)
				hs = append(hs, time.Since(t0))
				t0 = time.Now()
				omnibox.Suggest(src, q, 8)
				om = append(om, time.Since(t0))
			}
		}
		r.f("| %d | %s | %s | %s | %s | %s | %s |\n", n, ms(percentile(hs, .5)), ms(percentile(hs, .99)), ms(percentile(hs, 1)), ms(percentile(om, .5)), ms(percentile(om, .99)), ms(percentile(om, 1)))
	}
	r.f("\nTargets: history search < 2 ms and omnibox answer < 5 ms. The max column is the honest worst case (a rare one- or two-letter query whose Bloom signature matches many entries).\n\n")
}

func micro(r *report) {
	fmt.Println("micro benchmarks ...")
	r.f("## Compositor micro-benchmarks\n\n")
	r.f("Go `testing.Benchmark` on the compositor alone (1280x800 frame, 720-row viewport, 4000-row page texture). \"allocs/op\" is the steady-state allocation count: it must be 0.\n\n")
	r.f("| operation | time/op | allocs/op |\n|---|---|---|\n")
	mk := func() *compositor.Compositor {
		c := compositor.New(1280, 800, 1)
		c.SetViewport(image.Rect(0, 80, 1280, 800))
		c.SetChrome(solid(1280, 800, color.RGBA{40, 50, 90, 255}))
		c.SetPage(solid(1280, 4000, color.RGBA{250, 250, 250, 255}))
		c.SetDocHeight(4000)
		c.Compose()
		return c
	}
	b1 := testing.Benchmark(func(b *testing.B) {
		c := mk()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			c.ScrollTo((i * 13) % 3000)
			c.Compose()
		}
	})
	r.f("| scroll one step (blit 1280x720 from the page texture) | %s | %d |\n", nsop(b1), b1.AllocsPerOp())
	b2 := testing.Benchmark(func(b *testing.B) {
		c := mk()
		x, y := solid(1280, 800, color.RGBA{40, 50, 90, 255}), solid(1280, 800, color.RGBA{40, 50, 90, 255})
		y.SetRGBA(300, 40, color.RGBA{255, 0, 0, 255})
		b.ReportAllocs()
		cur := x
		for i := 0; i < b.N; i++ {
			if cur == x {
				cur = y
			} else {
				cur = x
			}
			c.SetChrome(cur)
			c.Compose()
		}
	})
	r.f("| new full chrome frame with a 1-pixel change (diff 4 MB + compose the damage) | %s | %d |\n", nsop(b2), b2.AllocsPerOp())
	b3 := testing.Benchmark(func(b *testing.B) {
		c := mk()
		ov := solid(520, 300, color.RGBA{30, 30, 40, 220})
		c.SetOverlay(ov, image.Pt(120, 90), 255)
		c.Compose()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			c.SetOverlay(ov, image.Pt(120, 90), uint8(100+i%150))
			c.Compose()
		}
	})
	r.f("| overlay popover 520x300 re-blended with a new opacity | %s | %d |\n", nsop(b3), b3.AllocsPerOp())

	r.f("\n## Tracker blocklist\n\n")
	bl := privacy.SeedList()
	t0 := time.Now()
	n := bl.Len()
	load := time.Since(t0)
	r.f("The embedded seed list (hand-written seed data, not a complete filter list; no coverage claim) holds %d rules. It is compiled lazily on the first lookup, which took %s here; start-up itself pays nothing.\n\n", n, ms(load))
	r.f("| lookup | time/op | allocs/op |\n|---|---|---|\n")
	for _, c := range []struct{ name, host string }{{"clean host (Bloom filter rejects)", "cdn.static.example.com"}, {"blocked host (Bloom + exact check)", "pixel.stats.doubleclick.net"}} {
		host := c.host
		res := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				bl.Match(host)
			}
		})
		r.f("| %s | %s | %d |\n", c.name, nsop(res), res.AllocsPerOp())
	}
	r.f("\n")
	_ = sort.Strings
}

func nsop(b testing.BenchmarkResult) string {
	ns := float64(b.T.Nanoseconds()) / float64(b.N)
	switch {
	case ns >= 1e6:
		return fmt.Sprintf("%.2f ms", ns/1e6)
	case ns >= 1e3:
		return fmt.Sprintf("%.1f µs", ns/1e3)
	}
	return fmt.Sprintf("%.0f ns", ns)
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return m
}
