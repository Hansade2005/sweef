package render

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func bigPage(n int) string {
	var sb strings.Builder
	sb.WriteString(`<!doctype html><html><head><style>
body{font:14px Arial;margin:0} .card{border:1px solid #ccc;margin:4px;padding:6px;border-radius:4px;background:#fafafa}
.card h3{margin:0 0 4px;font-size:15px} .card p{margin:0;color:#444} .row{display:flex;gap:8px} .row>div{flex:1}
ul.l li{padding:2px 0} a{color:#06c} table{border-collapse:collapse;width:100%} td{border:1px solid #ddd;padding:2px 4px}
</style></head><body>`)
	for i := 0; i < n/10; i++ {
		fmt.Fprintf(&sb, `<div class="card"><h3>Item %d title text</h3><p>Some <b>bold</b> and <a href="#">linked</a> text that wraps around for the card number %d in the list.</p><div class="row"><div>left</div><div>right <span>x</span></div></div></div>`, i, i)
	}
	sb.WriteString(`<table>`)
	for i := 0; i < n/20; i++ {
		fmt.Fprintf(&sb, `<tr><td>%d</td><td>name %d</td><td>value</td></tr>`, i, i)
	}
	sb.WriteString(`</table></body></html>`)
	return sb.String()
}

func countEls(n *html.Node) int {
	c := 0
	if n.Type == html.ElementNode {
		c = 1
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c += countEls(ch)
	}
	return c
}

func TestPerf5000(t *testing.T) {
	src := bigPage(5000)
	doc, _ := html.Parse(strings.NewReader(src))
	var sheets []Sheet
	collectSheets(doc, &sheets)
	t.Logf("elements: %d", countEls(doc))
	best := time.Hour
	for i := 0; i < 5; i++ {
		st := time.Now()
		res := Layout(doc, Options{Width: 1280, Height: 800, Sheets: sheets})
		d := time.Since(st)
		if d < best {
			best = d
		}
		_ = res
	}
	t.Logf("layout best of 5: %v", best)
	if best > 100*time.Millisecond && !testing.Short() {
		t.Errorf("layout took %v, want < 100ms", best)
	}
}

func TestConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			doc, _ := html.Parse(strings.NewReader(bigPage(300 + g*10)))
			var sheets []Sheet
			collectSheets(doc, &sheets)
			for i := 0; i < 3; i++ {
				Layout(doc, Options{Width: 1280, Height: 800, Sheets: sheets})
			}
		}(g)
	}
	wg.Wait()
}
