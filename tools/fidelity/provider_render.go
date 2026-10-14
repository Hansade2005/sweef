package main

import (
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/net/html"

	snet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/raster"
	"github.com/sweefbrowser/sweef/internal/render"
)

// renderProvider runs internal/render directly on the fetched page.
type renderProvider struct{}

func (renderProvider) Name() string { return "render" }

type httpEnv struct {
	mu    sync.Mutex
	cache map[string]image.Image
}

func (e *httpEnv) Image(u string) image.Image {
	e.mu.Lock()
	defer e.mu.Unlock()
	if im, ok := e.cache[u]; ok {
		return im
	}
	var data []byte
	if strings.HasPrefix(u, "data:") {
		i := strings.IndexByte(u, ',')
		if i > 0 {
			meta, body := u[5:i], u[i+1:]
			if strings.Contains(meta, ";base64") {
				data, _ = base64.StdEncoding.DecodeString(body)
			} else if s, err := neturl.PathUnescape(body); err == nil {
				data = []byte(s)
			}
		}
	} else if loopbackRE.MatchString(u) {
		if resp, err := http.Get(u); err == nil {
			data, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
	}
	var im image.Image
	if len(data) > 0 {
		im, _, _ = image.Decode(strings.NewReader(string(data)))
	}
	e.cache[u] = im
	return im
}

// SVGSource lets layout resolve <img src=*.svg>, <use href=x.svg#id> and
// <image href=x.svg> through the same fetch.
func (e *httpEnv) SVGSource(u string) (string, bool) {
	base := strings.ToLower(u)
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if !strings.HasSuffix(base, ".svg") || !strings.HasPrefix(u, "http") {
		return "", false
	}
	body, err := fetch(u)
	if err != nil || !strings.Contains(body, "<svg") {
		return "", false
	}
	return body, true
}

func (e *httpEnv) Bitmap(*html.Node) image.Image                    { return nil }
func (e *httpEnv) IntrinsicSize(*html.Node) (w, h float64, ok bool) { return 0, 0, false }
func (e *httpEnv) Focused() *html.Node                              { return nil }

// loopbackRE accepts only http(s) URLs on the local fixture server: the tool renders local test pages and must
// not be turned into a fetcher of arbitrary hosts by page content.
var loopbackRE = regexp.MustCompile(`^https?://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]+)?(/|$)`)

func fetch(u string) (string, error) {
	if !loopbackRE.MatchString(u) {
		return "", fmt.Errorf("fidelity: refusing non-loopback URL %q", u)
	}
	resp, err := http.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func attrOf(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

// pageSheets collects style sheets in document order.
func pageSheets(root *html.Node, base string) []render.Sheet {
	var sheets []render.Sheet
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "style":
				var sb strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						sb.WriteString(c.Data)
					}
				}
				sheets = append(sheets, render.Sheet{Text: sb.String(), BaseURL: base, Media: attrOf(n, "media")})
			case "link":
				rel := strings.ToLower(attrOf(n, "rel"))
				if strings.Contains(rel, "stylesheet") && !strings.Contains(rel, "alternate") && attrOf(n, "href") != "" {
					bu, _ := neturl.Parse(base)
					ru, err := neturl.Parse(attrOf(n, "href"))
					if err == nil && bu != nil {
						full := bu.ResolveReference(ru).String()
						if txt, err := fetch(full); err == nil {
							sheets = append(sheets, render.Sheet{Text: txt, BaseURL: full, Media: attrOf(n, "media")})
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return sheets
}

func (renderProvider) Run(url string, w, h int) ([]ElemBox, image.Image, error) {
	src, err := fetch(url)
	if err != nil {
		return nil, nil, err
	}
	doc, err := snet.ParseDocument(src, url)
	if err != nil {
		return nil, nil, err
	}
	res := render.Layout(doc.Root, render.Options{
		Width: float64(w), Height: float64(h), DPR: 1, ColorScheme: "light", BaseURL: url,
		Sheets: pageSheets(doc.Root, url), Env: &httpEnv{cache: map[string]image.Image{}},
	})
	var boxes []ElemBox
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			eb := ElemBox{Tag: strings.ToLower(n.Data)}
			if r, ok := res.ClientRect(n); ok {
				eb.X, eb.Y, eb.W, eb.H, eb.Has = r.X, r.Y, r.W, r.H, true
			}
			boxes = append(boxes, eb)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc.Root)
	_ = fmt.Sprint
	img := raster.Paint(res.List, w, h, 1, raster.PaintEnv{Background: raster.RGBA8(255, 255, 255, 255)})
	return boxes, img, nil
}

func init() { Register(renderProvider{}) }
