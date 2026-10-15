// Command shotdiff compares two screenshots of the same page (Chromium's and Sweef's) and writes a small
// side-by-side PNG. It prints one JSON line of metrics:
//
//	mae        mean absolute error over RGB, 0-255 (0 = identical)
//	diff_pct   share of pixels whose largest channel difference exceeds -tol
//	ssim       mean SSIM of the luma over 8x8 windows (1 = identical)
//
// Images of different sizes are compared over their common top-left area; the size difference is reported.
//
//	go run ./bench/shotdiff -a chromium.png -b sweef.png -out side.png [-tol 32] [-scale 0.5]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
)

func load(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func rgba(im image.Image) *image.RGBA {
	if r, ok := im.(*image.RGBA); ok {
		return r
	}
	b := im.Bounds()
	r := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(r, r.Bounds(), im, b.Min, draw.Src)
	return r
}

func luma(c []uint8) float64 { return 0.299*float64(c[0]) + 0.587*float64(c[1]) + 0.114*float64(c[2]) }

func main() {
	a := flag.String("a", "", "reference PNG (Chromium)")
	b := flag.String("b", "", "candidate PNG (Sweef)")
	out := flag.String("out", "", "side-by-side PNG to write (optional)")
	tol := flag.Int("tol", 32, "per-channel difference counted as a differing pixel")
	scale := flag.Float64("scale", 0.5, "scale of the side-by-side image")
	flag.Parse()
	ia, err := load(*a)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ib, err := load(*b)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	A, B := rgba(ia), rgba(ib)
	w := min(A.Bounds().Dx(), B.Bounds().Dx())
	h := min(A.Bounds().Dy(), B.Bounds().Dy())
	var sum float64
	diff := 0
	la := make([]float64, w*h)
	lb := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pa := A.Pix[A.PixOffset(x, y):]
			pb := B.Pix[B.PixOffset(x, y):]
			mx := 0
			for c := 0; c < 3; c++ {
				d := int(pa[c]) - int(pb[c])
				if d < 0 {
					d = -d
				}
				sum += float64(d)
				mx = max(mx, d)
			}
			if mx > *tol {
				diff++
			}
			la[y*w+x], lb[y*w+x] = luma(pa), luma(pb)
		}
	}
	n := float64(w * h)
	// SSIM over 8x8 windows
	const c1, c2 = (0.01 * 255) * (0.01 * 255), (0.03 * 255) * (0.03 * 255)
	var ssim float64
	wins := 0
	for y := 0; y+8 <= h; y += 8 {
		for x := 0; x+8 <= w; x += 8 {
			var ma, mb float64
			for j := 0; j < 8; j++ {
				for i := 0; i < 8; i++ {
					ma += la[(y+j)*w+x+i]
					mb += lb[(y+j)*w+x+i]
				}
			}
			ma /= 64
			mb /= 64
			var va, vb, cov float64
			for j := 0; j < 8; j++ {
				for i := 0; i < 8; i++ {
					da, db := la[(y+j)*w+x+i]-ma, lb[(y+j)*w+x+i]-mb
					va += da * da
					vb += db * db
					cov += da * db
				}
			}
			va /= 63
			vb /= 63
			cov /= 63
			ssim += ((2*ma*mb + c1) * (2*cov + c2)) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			wins++
		}
	}
	if wins > 0 {
		ssim /= float64(wins)
	}
	res := map[string]any{
		"mae":      math.Round(sum/(3*n)*100) / 100,
		"diff_pct": math.Round(float64(diff)/n*10000) / 100,
		"ssim":     math.Round(ssim*10000) / 10000,
		"a_size":   []int{A.Bounds().Dx(), A.Bounds().Dy()},
		"b_size":   []int{B.Bounds().Dx(), B.Bounds().Dy()},
	}
	if *out != "" {
		writeSide(*out, A, B, *scale)
	}
	j, _ := json.Marshal(res)
	fmt.Println(string(j))
}

// writeSide writes A | B (scaled, nearest neighbour; a 4 px gap) as a PNG.
func writeSide(p string, A, B *image.RGBA, s float64) {
	sw := func(v int) int { return max(1, int(float64(v)*s)) }
	aw, ah, bw, bh := sw(A.Bounds().Dx()), sw(A.Bounds().Dy()), sw(B.Bounds().Dx()), sw(B.Bounds().Dy())
	out := image.NewRGBA(image.Rect(0, 0, aw+4+bw, max(ah, bh)))
	draw.Draw(out, out.Bounds(), &image.Uniform{color.RGBA{255, 0, 255, 255}}, image.Point{}, draw.Src)
	blit := func(src *image.RGBA, ox, w, h int) {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				sx, sy := int(float64(x)/s), int(float64(y)/s)
				if sx >= src.Bounds().Dx() || sy >= src.Bounds().Dy() {
					continue
				}
				out.Set(ox+x, y, src.At(sx, sy))
			}
		}
	}
	blit(A, 0, aw, ah)
	blit(B, aw+4, bw, bh)
	f, err := os.Create(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer f.Close()
	_ = png.Encode(f, out)
}
