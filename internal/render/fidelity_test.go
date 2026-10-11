package render

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func iou(a, b [4]float64) float64 {
	ix := math.Max(0, math.Min(a[0]+a[2], b[0]+b[2])-math.Max(a[0], b[0]))
	iy := math.Max(0, math.Min(a[1]+a[3], b[1]+b[3])-math.Max(a[1], b[1]))
	in := ix * iy
	u := a[2]*a[3] + b[2]*b[3] - in
	if u <= 0 {
		return 1
	}
	return in / u
}

// TestCachedRefs compares element boxes with cached Chromium references
// (testdata/fidelity/.ref); it does not need a browser.
func TestCachedRefs(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/fidelity/*.html")
	if len(files) == 0 {
		t.Skip("no fixtures")
	}
	var sumIoU float64
	var n, near, total int
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		raw, err := os.ReadFile("../../testdata/fidelity/.ref/" + name + ".1280x800.json")
		if err != nil {
			continue
		}
		var rd struct {
			Boxes []struct {
				X, Y, W, H float64
				Has        bool
			}
		}
		if json.Unmarshal(raw, &rd) != nil {
			continue
		}
		src, _ := os.ReadFile(f)
		if strings.Contains(string(src), "<img") {
			continue // needs an image environment
		}
		doc, _ := html.Parse(strings.NewReader(string(src)))
		var sheets []Sheet
		collectSheets(doc, &sheets)
		res := Layout(doc, Options{Width: 1280, Height: 800, DPR: 1, Sheets: sheets})
		idx := 0
		var fs float64
		var fn int
		var walk func(*html.Node)
		walk = func(x *html.Node) {
			if x.Type == html.ElementNode {
				if idx < len(rd.Boxes) && rd.Boxes[idx].Has {
					rf := rd.Boxes[idx]
					r, _ := res.ClientRect(x)
					v := iou([4]float64{r.X, r.Y, r.W, r.H}, [4]float64{rf.X, rf.Y, rf.W, rf.H})
					fs += v
					fn++
					total++
					if math.Abs(r.X-rf.X) <= 1 && math.Abs(r.Y-rf.Y) <= 1 && math.Abs(r.X+r.W-rf.X-rf.W) <= 1 && math.Abs(r.Y+r.H-rf.Y-rf.H) <= 1 {
						near++
					}
				}
				idx++
			}
			for c := x.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(doc)
		if fn > 0 {
			if m := fs / float64(fn); m < 0.90 {
				t.Errorf("%s: mean IoU %.3f < 0.90", name, m)
			}
			sumIoU += fs / float64(fn)
			n++
		}
	}
	if n == 0 {
		t.Skip("no refs")
	}
	if m := sumIoU / float64(n); m < 0.98 {
		t.Errorf("mean IoU %.3f < 0.98", m)
	}
	if f := float64(near) / float64(total); f < 0.90 {
		t.Errorf("within-1px %.3f < 0.90", f)
	}
}
