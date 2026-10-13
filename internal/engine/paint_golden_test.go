package engine_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/raster"
	"github.com/sweefbrowser/sweef/internal/ui/preview"
)

// The golden test pins the pixels of full-page paints of the real UI pages
// (every internal/ui/preview scenario, 1x and 2x for the chrome page) to hashes
// recorded before the incremental / cached paint paths existed, and checks
// that the incremental painter produces exactly the pixels of a full paint at
// every step of a chrome interaction (typing in the omnibox, opening popovers).
//
//	go test ./internal/engine -run TestPaintGolden -update-golden   # re-record (only for intended pixel changes)
var updateGolden = flag.Bool("update-golden", false, "re-record testdata/paint_golden.txt")

// volatile scenarios render clocks / relative times / running animations, so
// their pixels are not reproducible run to run (only the incremental check applies).
var volatile = map[string]bool{"chrome-light-rate-toasts": true, "newtab-light": true, "newtab-dark-typing": true, "history": true}

const goldenFile = "testdata/paint_golden.txt"

func hashPix(img *image.RGBA) string {
	h := sha256.New()
	fmt.Fprintf(h, "%dx%d:", img.Rect.Dx(), img.Rect.Dy())
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		o := img.PixOffset(img.Rect.Min.X, y)
		h.Write(img.Pix[o : o+4*img.Rect.Dx()])
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}

func loadGolden(t *testing.T) map[string]string {
	m := map[string]string{}
	f, err := os.Open(goldenFile)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := sc.Text(); strings.LastIndexByte(l, ' ') > 0 {
			i := strings.LastIndexByte(l, ' ')
			m[l[:i]] = l[i+1:]
		}
	}
	return m
}

// screen mirrors what the GUI compositor does with the images it is given.
type screen struct{ img *image.RGBA }

func (s *screen) apply(part *image.RGBA, w, h int) {
	if s.img == nil || part.Rect.Dx() == w && part.Rect.Dy() == h && part.Rect.Min == (image.Point{}) {
		cp := image.NewRGBA(part.Rect)
		copy(cp.Pix, part.Pix)
		s.img = cp
		return
	}
	for y := part.Rect.Min.Y; y < part.Rect.Max.Y; y++ {
		so := part.PixOffset(part.Rect.Min.X, y)
		do := s.img.PixOffset(part.Rect.Min.X, y)
		copy(s.img.Pix[do:do+4*part.Rect.Dx()], part.Pix[so:so+4*part.Rect.Dx()])
	}
}

func TestPaintGolden(t *testing.T) { runPaintGolden(t) }

// TestPaintGoldenSnapshots runs the same sequences with the prefix snapshot of
// the incremental painter forced on (it normally needs several stable updates).
func TestPaintGoldenSnapshots(t *testing.T) {
	if *updateGolden {
		t.Skip()
	}
	a, b, c := raster.SnapMinOps, raster.SnapStable, raster.SnapMinShare
	raster.SnapMinOps, raster.SnapStable, raster.SnapMinShare = 1, 1, 1000
	defer func() { raster.SnapMinOps, raster.SnapStable, raster.SnapMinShare = a, b, c }()
	used0 := raster.SnapUsed.Load()
	runPaintGolden(t)
	if !t.Failed() && !testing.Short() && raster.SnapUsed.Load() == used0 {
		t.Error("the snapshot path was never used")
	}
}

func runPaintGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	want := loadGolden(t)
	got := map[string]string{}
	visited := map[string]bool{}
	srv := preview.Server()
	defer srv.Close()
	e := preview.Engine()
	defer e.Close()

	typeJS := func(v string) string {
		return `(function(){var u=document.getElementById('url');if(!u)return;u.focus();u.value=` + fmt.Sprintf("%q", v) +
			`;try{u.setSelectionRange(u.value.length,u.value.length)}catch(e){};u.dispatchEvent(new Event('input',{bubbles:true}));})()`
	}
	for _, sc := range preview.Scenarios() {
		if sc.Skip || sc.BackdropFrom != "" {
			continue
		}
		scales := []float64{1}
		chrome := sc.Page == "chrome/index.html"
		if chrome {
			scales = []float64{1, 2}
		}
		for _, scale := range scales {
			sc, scale := sc, scale
			name := fmt.Sprintf("%s@%gx", sc.Name, scale)
			t.Run(name, func(t *testing.T) {
				visited[name] = true
				s, err := preview.Open(e, srv.URL, sc, scale)
				if err != nil {
					t.Fatal(err)
				}
				defer e.CloseTab(s.Tab.ID)
				var scr screen
				step := func(label string) {
					t.Helper()
					base := engine.PaintOptions{Width: sc.W, Height: sc.H, DSF: scale, Transparent: chrome}
					full, ok := s.Tab.PaintRGBAOpts(base)
					if !ok {
						t.Fatal("no paint")
					}
					key := name + " " + label
					h := hashPix(full)
					if !volatile[sc.Name] {
						got[key] = h
					}
					if w, ok := want[key]; ok && !*updateGolden && w != h {
						t.Errorf("%s: full paint changed: golden %s, now %s", key, w, got[key])
					}
					if chrome {
						inc := base
						inc.Incremental = true
						part, ok := s.Tab.PaintRGBAOpts(inc)
						if !ok {
							t.Fatal("no incremental paint")
						}
						scr.apply(part, full.Rect.Dx(), full.Rect.Dy())
						if hashPix(scr.img) != h {
							t.Errorf("%s: incremental paint differs from full paint (damage %v)", key, part.Rect)
							dumpDiff(t, key, full, scr.img)
						}
					}
				}
				step("open")
				if sc.Setup != "" {
					s.Run(sc.Setup)
					s.Settle()
					step("setup")
				}
				if sc.Then != "" {
					s.Run(sc.Then)
					s.Settle()
					step("then")
				}
				if chrome {
					for i, v := range []string{"g", "gi", "git", "git r", "git", "g", ""} {
						s.Run(typeJS(v))
						s.Settle()
						step(fmt.Sprintf("type%d", i))
					}
					if sc.Name == "chrome-omnibox" {
						s.Run(typeJS("git"))
						s.Run(`__send('omni.suggestions',{id:SW.omniSeq,items:[{"type":"search","text":"git rebase"}]});`)
						s.Settle()
						step("suggest-fewer")
						s.Run(`document.getElementById('menubtn').click();`)
						s.Settle()
						step("menu")
					}
				}
			})
		}
	}
	if os.Getenv("GOLDEN_PRINT") != "" {
		// print the hashes of the scenarios that ran (go test -run 'TestPaintGolden/<scenario>'), to add one
		// scenario's lines to the golden file without re-recording the others
		var keys []string
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("GOLDEN %s %s\n", k, got[k])
		}
	}
	if *updateGolden {
		var keys []string
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&sb, "%s %s\n", k, got[k])
		}
		if err := os.WriteFile(goldenFile, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	for k := range want {
		if _, ok := got[k]; !ok && !t.Failed() && visited[k[:strings.LastIndexByte(k, ' ')]] {
			t.Errorf("golden entry %q not produced", k)
		}
	}
}

func dumpDiff(t *testing.T, key string, a, b *image.RGBA) {
	n, minX, minY, maxX, maxY := 0, 1<<30, 1<<30, -1, -1
	for y := 0; y < a.Rect.Dy(); y++ {
		for x := 0; x < a.Rect.Dx(); x++ {
			o := a.PixOffset(x, y)
			if string(a.Pix[o:o+4]) != string(b.Pix[o:o+4]) {
				n++
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	t.Logf("%s: %d differing px in [%d,%d]-[%d,%d]", key, n, minX, minY, maxX, maxY)
}
