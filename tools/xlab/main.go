// Command xlab renders an HTML file with the render+raster packages and
// compares it to a reference screenshot (temporary experiment tool).
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"

	"golang.org/x/net/html"

	snet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/raster"
	"github.com/sweefbrowser/sweef/internal/render"
)

func main() {
	file := flag.String("html", "", "html file")
	ref := flag.String("ref", "", "reference png")
	out := flag.String("out", "/tmp/xlab.png", "output png")
	w := flag.Int("w", 1280, "width")
	h := flag.Int("h", 800, "height")
	dpr := flag.Float64("dpr", 1, "dpr")
	crop := flag.String("crop", "", "x,y,w,h region to score")
	pt := flag.String("hit", "", "x,y: print elementFromPoint tag")
	flag.Parse()
	b, _ := os.ReadFile(*file)
	doc, err := snet.ParseDocument(string(b), "file://"+*file)
	if err != nil {
		panic(err)
	}
	var sheets []render.Sheet
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "style" {
			var sb strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					sb.WriteString(c.Data)
				}
			}
			sheets = append(sheets, render.Sheet{Text: sb.String()})
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc.Root)
	res := render.Layout(doc.Root, render.Options{Width: float64(*w), Height: float64(*h), DPR: *dpr, ColorScheme: "light", Sheets: sheets})
	if *pt != "" {
		var x, y float64
		fmt.Sscanf(*pt, "%f,%f", &x, &y)
		if n := res.Hit(x, y); n != nil {
			fmt.Println("hit:", n.Data, attrs(n))
		} else {
			fmt.Println("hit: nil")
		}
	}
	img := raster.Paint(res.List, int(float64(*w)**dpr), int(float64(*h)**dpr), *dpr, raster.PaintEnv{Background: raster.RGBA8(255, 255, 255, 255)})
	f, _ := os.Create(*out)
	png.Encode(f, img)
	f.Close()
	if *ref != "" {
		rf, err := os.Open(*ref)
		if err != nil {
			panic(err)
		}
		r, _ := png.Decode(rf)
		rf.Close()
		x0, y0, x1, y1 := 0, 0, img.Bounds().Dx(), img.Bounds().Dy()
		if *crop != "" {
			var cw, ch int
			fmt.Sscanf(*crop, "%d,%d,%d,%d", &x0, &y0, &cw, &ch)
			x1, y1 = x0+cw, y0+ch
		}
		for _, tol := range []int{8, 24} {
			tot, ok := 0, 0
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					a := color.RGBAModel.Convert(r.At(x, y)).(color.RGBA)
					c := img.RGBAAt(x, y)
					if a == (color.RGBA{255, 255, 255, 255}) && c == a {
						continue
					}
					tot++
					if abs(int(a.R)-int(c.R)) <= tol && abs(int(a.G)-int(c.G)) <= tol && abs(int(a.B)-int(c.B)) <= tol {
						ok++
					}
				}
			}
			fmt.Printf("ink tol%d: %.2f%% (%d px)\n", tol, 100*float64(ok)/float64(max(tot, 1)), tot)
		}
	}
}

func attrs(n *html.Node) string {
	s := ""
	for _, a := range n.Attr {
		s += a.Key + "=" + a.Val + " "
	}
	return s
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

var _ = image.Rect
