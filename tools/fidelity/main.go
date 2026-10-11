// Command fidelity scores Sweef's layout and rendering against Chromium.
//
//	go run ./tools/fidelity                       # all fixtures, provider "engine"
//	go run ./tools/fidelity -only flex -v         # one fixture, show worst elements
//	go run ./tools/fidelity -provider render      # layout engine directly (boxes only until a painter is registered)
//	go run ./tools/fidelity -refresh              # re-capture Chromium references
//
// For every fixture page in testdata/fidelity/*.html the tool captures, from
// Chromium via the agent-browser CLI, the border box of every element in
// document order and a screenshot, and caches them under
// testdata/fidelity/.ref so later runs need no browser. A provider (the full
// engine, or the render package alone) produces the same data for Sweef and
// the tool prints per-page and overall scores:
//
//   - IoU of every element's border box (mean), and the share of elements
//     within 1 px / 4 px on all four edges of Chromium's box;
//   - screenshot agreement: share of pixels equal within a tolerance, and
//     the same after a 3x3 blur (which forgives anti-aliasing differences).
//
// Providers register themselves with Register (see providers.go and any
// provider_*.go files added by feature work).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ElemBox is one element's border box in document CSS px.
type ElemBox struct {
	Tag        string
	X, Y, W, H float64
	Has        bool
}

// Provider produces Sweef's boxes and screenshot for a page.
type Provider interface {
	Name() string
	// Run loads url (served over HTTP) at the viewport and returns every
	// element's box in document order plus a screenshot (nil if unsupported).
	Run(url string, w, h int) ([]ElemBox, image.Image, error)
}

var providers = map[string]Provider{}

// Register makes a provider selectable with -provider.
func Register(p Provider) { providers[p.Name()] = p }

type refData struct {
	Boxes []ElemBox `json:"boxes"`
	Hash  string    `json:"hash"`
}

func main() {
	var (
		dir      = flag.String("pages", "testdata/fidelity", "directory of fixture .html files")
		only     = flag.String("only", "", "only fixtures whose name contains this")
		provName = flag.String("provider", "engine", "engine | render | ...")
		w        = flag.Int("w", 1280, "viewport width")
		h        = flag.Int("h", 800, "viewport height")
		refresh  = flag.Bool("refresh", false, "re-capture Chromium references")
		verbose  = flag.Bool("v", false, "list the worst-matching elements per page")
		outDir   = flag.String("out", "", "write sweef/diff PNGs here (default $TMPDIR/sweef-fidelity)")
		ab       = flag.String("ab", os.Getenv("AB"), "path to agent-browser (only needed when capturing references)")
		chrome   = flag.String("chrome", os.Getenv("CHROME"), "path to a Chrome/Chromium binary")
		urls     = flag.String("urls", "", "comma-separated live URLs to score instead of fixtures (references are not cached)")
		jsonOut  = flag.Bool("json", false, "machine-readable summary")
	)
	flag.Parse()
	prov, ok := providers[*provName]
	if !ok {
		names := []string{}
		for n := range providers {
			names = append(names, n)
		}
		sort.Strings(names)
		fatal("unknown provider %q (have %v)", *provName, names)
	}
	if *outDir == "" {
		*outDir = filepath.Join(os.TempDir(), "sweef-fidelity")
	}
	_ = os.MkdirAll(*outDir, 0o755)

	type page struct{ name, url, file string }
	var pages []page
	var base string
	if *urls != "" {
		for i, u := range strings.Split(*urls, ",") {
			pages = append(pages, page{name: fmt.Sprintf("url%d", i+1), url: strings.TrimSpace(u)})
		}
	} else {
		files, _ := filepath.Glob(filepath.Join(*dir, "*.html"))
		sort.Strings(files)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal("listen: %v", err)
		}
		base = "http://" + ln.Addr().String()
		go http.Serve(ln, http.FileServer(http.Dir(*dir)))
		for _, f := range files {
			name := strings.TrimSuffix(filepath.Base(f), ".html")
			if *only != "" && !strings.Contains(name, *only) {
				continue
			}
			pages = append(pages, page{name: name, url: base + "/" + filepath.Base(f), file: f})
		}
	}
	if len(pages) == 0 {
		fatal("no fixtures in %s", *dir)
	}

	var sumIoU, sumExact, sumClose, sumPix, sumBlur float64
	var nPages int
	type row struct {
		name                          string
		n                             int
		iou, exact, close_, pix, blur float64
		note                          string
	}
	var rows []row
	for _, p := range pages {
		ref, refImg, err := loadRef(p.name, p.file, p.url, *dir, *w, *h, *refresh, *ab, *chrome, *urls != "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: reference: %v\n", p.name, err)
			continue
		}
		got, img, err := prov.Run(p.url, *w, *h)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: provider: %v\n", p.name, err)
			continue
		}
		r := row{name: p.name}
		r.n, r.iou, r.exact, r.close_ = scoreBoxes(ref.Boxes, got, *verbose, p.name)
		if img != nil && refImg != nil {
			r.pix, r.blur = scoreImage(refImg, img, filepath.Join(*outDir, p.name))
		} else {
			r.pix, r.blur = math.NaN(), math.NaN()
		}
		if len(ref.Boxes) != len(got) {
			r.note = fmt.Sprintf("element count differs: chromium %d, sweef %d", len(ref.Boxes), len(got))
		}
		rows = append(rows, r)
		sumIoU += r.iou
		sumExact += r.exact
		sumClose += r.close_
		if !math.IsNaN(r.pix) {
			sumPix += r.pix
			sumBlur += r.blur
		}
		nPages++
	}
	if nPages == 0 {
		fatal("nothing scored")
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(map[string]any{"provider": prov.Name(), "pages": rows, "meanIoU": sumIoU / float64(nPages)}, "", " ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("provider=%s viewport=%dx%d\n\n", prov.Name(), *w, *h)
	fmt.Printf("%-22s %5s %7s %8s %8s %8s %8s  %s\n", "page", "elems", "IoU", "≤1px", "≤4px", "ink", "ink~blur", "")
	for _, r := range rows {
		pix, blur := "    n/a", "    n/a"
		if !math.IsNaN(r.pix) {
			pix, blur = fmt.Sprintf("%6.1f%%", r.pix*100), fmt.Sprintf("%6.1f%%", r.blur*100)
		}
		fmt.Printf("%-22s %5d %7.3f %7.1f%% %7.1f%% %8s %8s  %s\n", r.name, r.n, r.iou, r.exact*100, r.close_*100, pix, blur, r.note)
	}
	pix, blur := "    n/a", "    n/a"
	if sumPix > 0 {
		pix, blur = fmt.Sprintf("%6.1f%%", sumPix/float64(nPages)*100), fmt.Sprintf("%6.1f%%", sumBlur/float64(nPages)*100)
	}
	fmt.Printf("%-22s %5s %7.3f %7.1f%% %7.1f%% %8s %8s\n", "MEAN", "", sumIoU/float64(nPages), sumExact/float64(nPages)*100, sumClose/float64(nPages)*100, pix, blur)
	fmt.Printf("\nimages: %s\n", *outDir)
}

func fatal(f string, a ...any) { fmt.Fprintf(os.Stderr, "fidelity: "+f+"\n", a...); os.Exit(1) }

// ---- reference capture ----------------------------------------------------------

const dumpJS = `JSON.stringify([...document.querySelectorAll("*")].map(e=>{const r=e.getBoundingClientRect();const cs=getComputedStyle(e);return [e.tagName.toLowerCase(),r.x+scrollX,r.y+scrollY,r.width,r.height,cs.display==="none"?0:1]}))`

func loadRef(name, file, url, dir string, w, h int, refresh bool, ab, chrome string, live bool) (*refData, image.Image, error) {
	cacheDir := filepath.Join(dir, ".ref")
	key := fmt.Sprintf("%s.%dx%d", name, w, h)
	hash := ""
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, nil, err
		}
		s := sha256.Sum256(b)
		hash = hex.EncodeToString(s[:8])
	}
	jpath, ppath := filepath.Join(cacheDir, key+".json"), filepath.Join(cacheDir, key+".png")
	if !refresh && !live {
		if b, err := os.ReadFile(jpath); err == nil {
			var rd refData
			if json.Unmarshal(b, &rd) == nil && rd.Hash == hash {
				if img, err := readPNG(ppath); err == nil {
					return &rd, img, nil
				}
			}
		}
	}
	if ab == "" {
		return nil, nil, fmt.Errorf("no cached reference for %s and no -ab/$AB agent-browser to capture one", key)
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	tmpPNG := ppath
	if live {
		tmpPNG = filepath.Join(os.TempDir(), key+".ref.png")
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(ab, append([]string{"--session", "fidelity"}, args...)...)
		if chrome != "" {
			cmd.Env = append(os.Environ(), "AGENT_BROWSER_EXECUTABLE_PATH="+chrome)
		}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if err != nil {
			return out.String(), fmt.Errorf("%v: %s%s", err, out.String(), errb.String())
		}
		return out.String(), nil
	}
	defer run("close")
	if _, err := run("open", "about:blank"); err != nil {
		return nil, nil, err
	}
	if _, err := run("set", "viewport", fmt.Sprint(w), fmt.Sprint(h)); err != nil {
		return nil, nil, err
	}
	if _, err := run("open", url); err != nil {
		return nil, nil, err
	}
	_, _ = run("wait", "--load", "networkidle")
	out, err := run("eval", dumpJS)
	if err != nil {
		return nil, nil, err
	}
	var enc string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &enc); err != nil {
		return nil, nil, fmt.Errorf("eval output: %v: %.200s", err, out)
	}
	var rows [][]any
	if err := json.Unmarshal([]byte(enc), &rows); err != nil {
		return nil, nil, err
	}
	rd := &refData{Hash: hash}
	for _, r := range rows {
		b := ElemBox{Tag: r[0].(string), X: r[1].(float64), Y: r[2].(float64), W: r[3].(float64), H: r[4].(float64)}
		b.Has = r[5].(float64) == 1
		rd.Boxes = append(rd.Boxes, b)
	}
	if _, err := run("screenshot", tmpPNG); err != nil {
		return nil, nil, err
	}
	img, err := readPNG(tmpPNG)
	if err != nil {
		return nil, nil, err
	}
	if !live {
		b, _ := json.Marshal(rd)
		_ = os.WriteFile(jpath, b, 0o644)
	}
	return rd, img, nil
}

func readPNG(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// ---- scoring ------------------------------------------------------------------------

func iou(a, b ElemBox) float64 {
	x0, y0 := math.Max(a.X, b.X), math.Max(a.Y, b.Y)
	x1, y1 := math.Min(a.X+a.W, b.X+b.W), math.Min(a.Y+a.H, b.Y+b.H)
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	inter := (x1 - x0) * (y1 - y0)
	return inter / (a.W*a.H + b.W*b.H - inter)
}

func within(a, b ElemBox, tol float64) bool {
	return math.Abs(a.X-b.X) <= tol && math.Abs(a.Y-b.Y) <= tol && math.Abs(a.W-b.W) <= tol && math.Abs(a.H-b.H) <= tol
}

// scoreBoxes compares elements with a box in Chromium, aligned by document
// order (both sides parse the same HTML, so indices agree when scripts do not
// alter the tree; a tag mismatch ends alignment and the rest scores zero).
func scoreBoxes(ref, got []ElemBox, verbose bool, name string) (n int, meanIoU, exact, close_ float64) {
	type bad struct {
		i    int
		iou  float64
		r, g ElemBox
	}
	var worst []bad
	aligned := true
	for i, r := range ref {
		if !r.Has || r.W <= 0 || r.H <= 0 {
			continue
		}
		n++
		var g ElemBox
		if aligned && i < len(got) && got[i].Tag == r.Tag {
			g = got[i]
		} else {
			aligned = false
		}
		v := 0.0
		if g.Has && g.W > 0 && g.H > 0 {
			v = iou(r, g)
			if within(r, g, 1) {
				exact++
			}
			if within(r, g, 4) {
				close_++
			}
		}
		meanIoU += v
		if v < 0.98 {
			worst = append(worst, bad{i, v, r, g})
		}
	}
	if n == 0 {
		return 0, 0, 0, 0
	}
	if verbose && len(worst) > 0 {
		sort.Slice(worst, func(a, b int) bool { return worst[a].iou < worst[b].iou })
		fmt.Printf("-- %s: worst elements (chromium vs sweef: x y w h)\n", name)
		for i, w := range worst {
			if i >= 8 {
				break
			}
			fmt.Printf("   #%d <%s> IoU %.2f  chromium %.1f %.1f %.1f %.1f | sweef %.1f %.1f %.1f %.1f\n", w.i, w.r.Tag, w.iou, w.r.X, w.r.Y, w.r.W, w.r.H, w.g.X, w.g.Y, w.g.W, w.g.H)
		}
	}
	return n, meanIoU / float64(n), exact / float64(n), close_ / float64(n)
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) {
		return r
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

func blur3(in *image.RGBA) *image.RGBA {
	b := in.Bounds()
	out := image.NewRGBA(b)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			var r, g, bl, n int
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if xx < 0 || yy < 0 || xx >= b.Dx() || yy >= b.Dy() {
						continue
					}
					c := in.RGBAAt(xx, yy)
					r += int(c.R)
					g += int(c.G)
					bl += int(c.B)
					n++
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255})
		}
	}
	return out
}

func agree(a, b *image.RGBA, tol int) (float64, *image.RGBA) {
	w, h := a.Bounds().Dx(), a.Bounds().Dy()
	diff := image.NewRGBA(image.Rect(0, 0, w, h))
	ok := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var ca, cb color.RGBA
			ca = a.RGBAAt(x, y)
			if x < b.Bounds().Dx() && y < b.Bounds().Dy() {
				cb = b.RGBAAt(x, y)
			} else {
				cb = color.RGBA{255, 0, 255, 255}
			}
			d := maxInt(absInt(int(ca.R)-int(cb.R)), maxInt(absInt(int(ca.G)-int(cb.G)), absInt(int(ca.B)-int(cb.B))))
			if d <= tol {
				ok++
				diff.SetRGBA(x, y, color.RGBA{uint8(255 - (255-int(ca.R))/4), uint8(255 - (255-int(ca.G))/4), uint8(255 - (255-int(ca.B))/4), 255})
			} else {
				diff.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
			}
		}
	}
	return float64(ok) / float64(w*h), diff
}

// ink is the agreement among pixels that are not the page background in
// either image (background = Chromium's most common color), so mostly-white
// pages cannot score well by being blank.
func ink(a, b *image.RGBA, tol int) float64 {
	counts := map[color.RGBA]int{}
	for y := 0; y < a.Bounds().Dy(); y += 2 {
		for x := 0; x < a.Bounds().Dx(); x += 2 {
			counts[a.RGBAAt(x, y)]++
		}
	}
	var bg color.RGBA
	best := -1
	for c, n := range counts {
		if n > best {
			bg, best = c, n
		}
	}
	total, ok := 0, 0
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			ca := a.RGBAAt(x, y)
			cb := color.RGBA{255, 0, 255, 255}
			if x < b.Bounds().Dx() && y < b.Bounds().Dy() {
				cb = b.RGBAAt(x, y)
			}
			if ca == bg && cb == bg {
				continue
			}
			total++
			if maxInt(absInt(int(ca.R)-int(cb.R)), maxInt(absInt(int(ca.G)-int(cb.G)), absInt(int(ca.B)-int(cb.B)))) <= tol {
				ok++
			}
		}
	}
	if total == 0 {
		return 1
	}
	return float64(ok) / float64(total)
}

func scoreImage(ref, got image.Image, outBase string) (pix, blur float64) {
	a, b := toRGBA(ref), toRGBA(got)
	_, diff := agree(a, b, 24)
	pix = ink(a, b, 24)
	blur = ink(blur3(a), blur3(b), 12)
	writePNG(outBase+".sweef.png", b)
	writePNG(outBase+".chromium.png", a)
	writePNG(outBase+".diff.png", diff)
	return
}

func writePNG(p string, img image.Image) {
	f, err := os.Create(p)
	if err != nil {
		return
	}
	defer f.Close()
	_ = png.Encode(f, img)
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
