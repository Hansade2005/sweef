package render

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestDbg2(t *testing.T) {
	src := `<!doctype html><html><head><style>td{font-family:Verdana,Geneva,sans-serif;font-size:10pt;color:#828282} .pagetop { font-size: 10pt; line-height:12px }</style></head><body><table border="0" cellpadding="0" cellspacing="0" width="100%" style="padding:2px"><tr><td style="width:18px;padding-right:4px"><a href="#"><img src="x" width="18" height="18" style="border:1px white solid; display:block"></a></td>
<td style="line-height:12pt; height:10px;"><span class="pagetop"><b class="hnname"><a href="#">Hacker News</a></b></span></td></tr></table></body></html>`
	doc, _ := html.Parse(strings.NewReader(src))
	var sheets []Sheet
	collectSheets(doc, &sheets)
	res := Layout(doc, Options{Width: 1280, Height: 800, Sheets: sheets})
	for n, b := range res.Boxes {
		if n.Data == "td" || n.Data == "span" {
			fmt.Println(n.Data, b.Border)
		}
	}
}

func TestDbg3(t *testing.T) {
	src := `<!doctype html><html><head><style>td{font-family:Verdana,Geneva,sans-serif;font-size:10pt}</style></head><body><table cellpadding="0" cellspacing="0"><tr><td style="line-height:12pt; height:10px;"><span style="line-height:12px">Hacker News</span></td></tr></table></body></html>`
	doc, _ := html.Parse(strings.NewReader(src))
	var sheets []Sheet
	collectSheets(doc, &sheets)
	res := Layout(doc, Options{Width: 1280, Height: 800, Sheets: sheets})
	for n, b := range res.Boxes {
		fmt.Println(n.Data, b.Border)
	}
}
