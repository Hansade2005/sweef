package raster

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/render"
)

// genItem builds one random top-level item: a leaf op or a balanced group.
type listGen struct {
	rng   *rand.Rand
	w, h  float64
	face  *font.Face
	bound *render.Rect // children of a group are drawn inside it (a layer far from its clip is a separate, bailing case)
}

func (g *listGen) rect() render.Rect {
	bx, by, bw, bh := 0.0, 0.0, g.w, g.h
	if g.bound != nil {
		bx, by, bw, bh = g.bound.X, g.bound.Y, g.bound.W, g.bound.H
	}
	w, h := 4+g.rng.Float64()*math.Max(1, bw*0.6), 4+g.rng.Float64()*math.Max(1, bh*0.25)
	w, h = math.Min(w, bw), math.Min(h, bh)
	r := render.Rect{X: bx + g.rng.Float64()*(bw-w), Y: by + g.rng.Float64()*(bh-h), W: w, H: h}
	if g.rng.Intn(2) == 0 { // pixel-aligned half of the time
		r = render.Rect{X: math.Round(r.X), Y: math.Round(r.Y), W: math.Round(r.W), H: math.Round(r.H)}
	}
	return r
}

func (g *listGen) col(solid bool) render.Color {
	a := uint8(255)
	if !solid && g.rng.Intn(2) == 0 {
		a = uint8(30 + g.rng.Intn(200))
	}
	return render.Color{R: uint8(g.rng.Intn(256)), G: uint8(g.rng.Intn(256)), B: uint8(g.rng.Intn(256)), A: a}
}

func (g *listGen) radii() render.Radii {
	if g.rng.Intn(2) == 0 {
		return render.Radii{}
	}
	return rad(float64(2 + g.rng.Intn(14)))
}

func (g *listGen) leaf() render.Op {
	switch g.rng.Intn(8) {
	case 0, 1:
		return render.FillRect{Rect: g.rect(), Color: g.col(false), Radii: g.radii()}
	case 2:
		return render.BoxShadow{Rect: g.rect(), Radii: g.radii(), DX: float64(g.rng.Intn(9) - 4), DY: float64(g.rng.Intn(9) - 4), Blur: float64(g.rng.Intn(18)), Spread: float64(g.rng.Intn(7) - 3), Color: g.col(false), Inset: g.rng.Intn(3) == 0}
	case 3:
		st := []render.GradientStop{{Pos: 0, Color: g.col(false)}, {Pos: 1, Color: g.col(false)}}
		gr := render.Gradient{Rect: g.rect(), Kind: []string{"linear", "radial"}[g.rng.Intn(2)], Radii: g.radii(), Stops: st, Angle: float64(g.rng.Intn(360))}
		gr.CX, gr.CY, gr.RX, gr.RY = gr.Rect.W/2, gr.Rect.H/2, gr.Rect.W/2, gr.Rect.H/2
		if g.rng.Intn(3) == 0 {
			gr.Tile = &render.Tile{Origin: [2]float64{gr.Rect.X, gr.Rect.Y}, Size: [2]float64{gr.Rect.W, gr.Rect.H}, Step: [2]float64{gr.Rect.W, gr.Rect.H}}
		}
		return gr
	case 4:
		return render.Border{Rect: g.rect(), Widths: [4]float64{1, 2, 1, 3}, Colors: [4]render.Color{g.col(true), g.col(true), g.col(true), g.col(true)}, Styles: [4]string{"solid", "solid", "dashed", "solid"}, Radii: g.radii()}
	case 5:
		if g.face == nil {
			return render.FillRect{Rect: g.rect(), Color: g.col(true)}
		}
		s := []string{"Hello", "git rebase", "AÅgy_", "tax brackets 2026"}[g.rng.Intn(4)]
		t := textRun(g.face, s, g.rng.Float64()*g.w*0.7, 20+g.rng.Float64()*(g.h-30), float64(10+g.rng.Intn(14)), g.col(true))
		if g.rng.Intn(4) == 0 {
			t.Shadows = []render.TextShadow{{DX: 1, DY: 2, Blur: float64(g.rng.Intn(5)), Color: g.col(false)}}
		}
		if g.rng.Intn(4) == 0 {
			t.Underline = true
		}
		return t
	case 6:
		return render.Image{Img: testImage(), Dst: g.rect(), Smooth: g.rng.Intn(2) == 0, Radii: g.radii()}
	}
	return render.FillRect{Rect: g.rect(), Color: g.col(true)}
}

func (g *listGen) item(depth int) []render.Op {
	if depth < 3 && g.rng.Intn(4) == 0 {
		var open, closeOp render.Op
		saved := g.bound
		defer func() { g.bound = saved }()
		switch g.rng.Intn(4) {
		case 0:
			open, closeOp = render.PushClip{Rect: g.rect(), Radii: g.radii()}, render.PopClip{}
		case 1:
			var fl []render.Filter
			if g.rng.Intn(2) == 0 {
				fl = []render.Filter{{Name: "blur", Args: []float64{float64(1 + g.rng.Intn(4))}}}
			}
			open, closeOp = render.PushLayer{Opacity: 0.4 + g.rng.Float64()*0.6, Filter: fl, Bounds: g.rect()}, render.PopLayer{}
		case 2:
			a := 0.0
			if g.rng.Intn(5) == 0 {
				a = (g.rng.Float64() - 0.5) * 0.2
			}
			open, closeOp = render.PushTransform{M: [6]float64{1 + 0.1*g.rng.Float64(), math.Sin(a), -math.Sin(a), 1, float64(g.rng.Intn(20)), float64(g.rng.Intn(20))}}, render.PopTransform{}
		default:
			open, closeOp = render.PushLayer{Opacity: 1, Blend: "multiply", Bounds: g.rect()}, render.PopLayer{}
		}
		switch o := open.(type) {
		case render.PushClip:
			r := o.Rect
			g.bound = &r
		case render.PushLayer:
			r := o.Bounds
			g.bound = &r
		}
		out := []render.Op{open}
		for n := 1 + g.rng.Intn(4); n > 0; n-- {
			out = append(out, g.item(depth+1)...)
		}
		return append(out, closeOp)
	}
	return []render.Op{g.leaf()}
}

func (g *listGen) list(n int) [][]render.Op {
	var items [][]render.Op
	for i := 0; i < n; i++ {
		items = append(items, g.item(0))
	}
	return items
}

func flatten(items [][]render.Op) []render.Op {
	var out []render.Op
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

func samePix(a, b *Surface) (int, int, int, bool) {
	for y := a.R.Y0; y < a.R.Y1; y++ {
		ra, rb := a.Row(y, a.R.X0, a.R.X1), b.Row(y, b.R.X0, b.R.X1)
		if !bytes.Equal(ra, rb) {
			for x := 0; x < len(ra); x++ {
				if ra[x] != rb[x] {
					return x / 4, y, x % 4, false
				}
			}
		}
	}
	return 0, 0, 0, true
}

// TestIncrementalMatchesFullPaint edits random display lists step by step and
// checks that every incremental update leaves exactly the pixels of a full paint.
func TestIncrementalMatchesFullPaint(t *testing.T) {
	face := font.Lookup("Liberation Sans", font.Style{Weight: 400})
	steps, runs := 14, 10
	if n, err := strconv.Atoi(os.Getenv("INCR_FUZZ_RUNS")); err == nil {
		runs = n
	}
	if testing.Short() {
		steps, runs = 6, 3
	}
	nFull, nInc := 0, 0
	defer func() {
		t.Logf("%d incremental updates, %d full repaints", nInc, nFull)
		if (!testing.Short() && nInc < 100) || nInc*3 < nFull {
			t.Errorf("the fuzz test mostly bails out of the incremental path (%d vs %d)", nInc, nFull)
		}
	}()
	for _, dpr := range []float64{1, 2} {
		for _, snap := range []bool{false, true} {
			a, b, c := SnapMinOps, SnapStable, SnapMinShare
			if snap {
				SnapMinOps, SnapStable, SnapMinShare = 1, 1, 1000
			}
			for seed := int64(0); seed < int64(runs); seed++ {
				rng := rand.New(rand.NewSource(seed*7 + int64(dpr)))
				g := &listGen{rng: rng, w: 300, h: 520, face: face}
				items := g.list(25 + rng.Intn(20))
				w, h := int(300*dpr), int(520*dpr)
				env := PaintEnv{}
				if seed%3 == 0 {
					env.Background = Color{R: 1, G: 1, B: 1, A: 1}
				}
				cv := NewCanvas(w, h, dpr, env)
				cv.Paint(flatten(items))
				for step := 0; step < steps; step++ {
					// edit: replace, insert or delete a few items
					for e := 1 + rng.Intn(2); e > 0 && len(items) > 3; e-- {
						i := rng.Intn(len(items))
						switch rng.Intn(4) {
						case 0:
							items[i] = g.item(0)
						case 1:
							items = append(items[:i], append([][]render.Op{g.item(0)}, items[i:]...)...)
						case 2:
							items = append(items[:i], items[i+1:]...)
						default:
							j := rng.Intn(len(items))
							items[i], items[j] = items[j], items[i]
						}
					}
					list := flatten(items)
					if _, full := cv.Update(list); full {
						nFull++
					} else {
						nInc++
					}
					want := Paint(list, 300, 520, dpr, env)
					got := cv.Image()
					for y := 0; y < h; y++ {
						if !bytes.Equal(got.Pix[y*got.Stride:y*got.Stride+4*w], want.Pix[y*want.Stride:y*want.Stride+4*w]) {
							dumpFuzzDiff(t, list, got.Pix[y*got.Stride:], want.Pix[y*want.Stride:], y, w, h, dpr)
							t.Fatalf("dpr=%g snap=%v seed=%d step=%d: row %d differs from a full paint", dpr, snap, seed, step, y)
						}
					}
				}
			}
			SnapMinOps, SnapStable, SnapMinShare = a, b, c
		}
	}
}

func dumpFuzzDiff(t *testing.T, list []render.Op, got, want []uint8, y, w, h int, dpr float64) {
	x := 0
	for x = 0; x < w*4; x++ {
		if got[x] != want[x] {
			break
		}
	}
	x /= 4
	t.Logf("first diff at (%d,%d): got %v want %v", x, y, got[x*4:x*4+4], want[x*4:x*4+4])
	info := Analyze(list, w, h, dpr)
	for i, op := range list {
		b := info.Bounds[i]
		if x >= b.X0 && x < b.X1 && y >= b.Y0 && y < b.Y1 {
			t.Logf("  op %d %T %+v bounds %v", i, op, op, b)
		}
	}
}
