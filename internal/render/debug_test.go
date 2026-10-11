package render

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func collectSheets(n *html.Node, out *[]Sheet) {
	if n.Type == html.ElementNode && n.Data == "style" {
		var sb strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				sb.WriteString(c.Data)
			}
		}
		*out = append(*out, Sheet{Text: sb.String(), Media: attr(n, "media")})
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectSheets(c, out)
	}
}

// TestDump prints the layout of $SWEEF_DUMP (a file) for debugging.
func TestDump(t *testing.T) {
	p := os.Getenv("SWEEF_DUMP")
	if p == "" {
		t.Skip()
	}
	src, _ := os.ReadFile(p)
	doc, _ := html.Parse(strings.NewReader(string(src)))
	var sheets []Sheet
	collectSheets(doc, &sheets)
	w, h := 1280.0, 800.0
	if v := os.Getenv("SWEEF_W"); v != "" {
		fmt.Sscan(v, &w)
	}
	if v := os.Getenv("SWEEF_H"); v != "" {
		fmt.Sscan(v, &h)
	}
	res := Layout(doc, Options{Width: w, Height: h, DPR: 1, Sheets: sheets})
	fmt.Printf("doc %vx%v viewport %v\n", res.Width, res.Height, res.Viewport)
	var refs []struct {
		Tag        string
		X, Y, W, H float64
		Has        bool
	}
	if rp := os.Getenv("SWEEF_REF"); rp != "" {
		raw, _ := os.ReadFile(rp)
		var rd struct {
			Boxes []struct {
				Tag        string
				X, Y, W, H float64
				Has        bool
			}
		}
		json.Unmarshal(raw, &rd)
		refs = rd.Boxes
	}
	idx := 0
	var walk func(n *html.Node, d int)
	walk = func(n *html.Node, d int) {
		if n.Type == html.ElementNode {
			mark := ""
			ref := ""
			if idx < len(refs) {
				rf := refs[idx]
				ref = fmt.Sprintf("  | chrome %.2f %.2f %.2f %.2f", rf.X, rf.Y, rf.W, rf.H)
				r, ok := res.ClientRect(n)
				if rf.Has && (!ok || math.Abs(r.X-rf.X) > 1 || math.Abs(r.Y-rf.Y) > 1 || math.Abs(r.W-rf.W) > 1 || math.Abs(r.H-rf.H) > 1) && rf.W > 0 && rf.H > 0 {
					mark = " !!"
				}
			}
			idx++
			if r, ok := res.ClientRect(n); ok {
				fmt.Printf("%s<%s class=%q> %.2f %.2f %.2f %.2f%s%s\n", strings.Repeat(" ", d), n.Data, attr(n, "class"), r.X, r.Y, r.W, r.H, mark, ref)
			} else {
				fmt.Printf("%s<%s> none%s\n", strings.Repeat(" ", d), n.Data, ref)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, d+1)
		}
	}
	walk(doc, 0)
}
