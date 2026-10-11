package raster

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/render"
)

type paintCase struct {
	name string
	html string // body content, absolutely positioned elements
	ops  func() []render.Op
}

func rc(r, g, b uint8) render.Color { return render.Color{R: r, G: g, B: b, A: 255} }

func rad(r float64) render.Radii {
	return render.Radii{{r, r}, {r, r}, {r, r}, {r, r}}
}

func textRun(face *font.Face, s string, x, y, size float64, c render.Color) render.TextRun {
	run := render.TextRun{X: x, Y: y, Size: size, Color: c}
	var pen float64
	for _, r := range s {
		run.Glyphs = append(run.Glyphs, render.Glyph{R: r, X: pen, Face: face})
		pen += face.Advance(r, 2048) * size / 2048
	}
	run.Width = pen
	return run
}

func testImage() image.Image {
	im := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if (x+y)%2 == 0 {
				im.Set(x, y, color.RGBA{220, 60, 40, 255})
			} else {
				im.Set(x, y, color.RGBA{40, 90, 220, 255})
			}
		}
	}
	return im
}

func imageDataURI(im image.Image) string {
	var bb bytes.Buffer
	_ = png.Encode(&bb, im)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(bb.Bytes())
}

func paintCases() []paintCase {
	sans := font.Lookup("DejaVu Sans", font.Style{Weight: 400})
	sansB := font.Lookup("Liberation Sans", font.Style{Weight: 400})
	box := func(x, y, w, h float64, style string) string {
		return fmt.Sprintf(`<div style="position:absolute;left:%gpx;top:%gpx;width:%gpx;height:%gpx;box-sizing:border-box;%s"></div>`, x, y, w, h, style)
	}
	var cases []paintCase
	cases = append(cases, paintCase{"borders_solid",
		box(10, 10, 80, 60, "border:6px solid #c33;background:#fe9;") +
			box(110, 10, 80, 60, "border-style:solid;border-width:2px 8px 4px 12px;border-color:#c33 #3a3 #33c #a3a;background:#eef;") +
			box(210, 10, 80, 60, "border:5px solid #333;border-radius:20px;background:#9fc;") +
			box(10, 90, 80, 60, "border:6px solid #c60;border-radius:30px 5px 30px 5px;") +
			box(110, 90, 80, 60, "border-style:solid;border-width:4px 10px;border-color:#c33 #33c;border-radius:25px;") +
			box(210, 90, 80, 60, "border:1px solid #000;background:#ddd;"),
		func() []render.Op {
			return []render.Op{
				render.FillRect{Rect: render.Rect{X: 10, Y: 10, W: 80, H: 60}, Color: rc(255, 238, 153)},
				render.Border{Rect: render.Rect{X: 10, Y: 10, W: 80, H: 60}, Widths: [4]float64{6, 6, 6, 6}, Colors: [4]render.Color{rc(204, 51, 51), rc(204, 51, 51), rc(204, 51, 51), rc(204, 51, 51)}, Styles: [4]string{"solid", "solid", "solid", "solid"}},
				render.FillRect{Rect: render.Rect{X: 110, Y: 10, W: 80, H: 60}, Color: rc(238, 238, 255)},
				render.Border{Rect: render.Rect{X: 110, Y: 10, W: 80, H: 60}, Widths: [4]float64{2, 8, 4, 12}, Colors: [4]render.Color{rc(204, 51, 51), rc(51, 170, 51), rc(51, 51, 204), rc(170, 51, 170)}, Styles: [4]string{"solid", "solid", "solid", "solid"}},
				render.FillRect{Rect: render.Rect{X: 210, Y: 10, W: 80, H: 60}, Color: rc(153, 255, 204), Radii: rad(20)},
				render.Border{Rect: render.Rect{X: 210, Y: 10, W: 80, H: 60}, Widths: [4]float64{5, 5, 5, 5}, Colors: [4]render.Color{rc(51, 51, 51), rc(51, 51, 51), rc(51, 51, 51), rc(51, 51, 51)}, Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: rad(20)},
				render.Border{Rect: render.Rect{X: 10, Y: 90, W: 80, H: 60}, Widths: [4]float64{6, 6, 6, 6}, Colors: [4]render.Color{rc(204, 102, 0), rc(204, 102, 0), rc(204, 102, 0), rc(204, 102, 0)}, Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: render.Radii{{30, 30}, {5, 5}, {30, 30}, {5, 5}}},
				render.Border{Rect: render.Rect{X: 110, Y: 90, W: 80, H: 60}, Widths: [4]float64{4, 10, 4, 10}, Colors: [4]render.Color{rc(204, 51, 51), rc(51, 51, 204), rc(204, 51, 51), rc(51, 51, 204)}, Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: rad(25)},
				render.FillRect{Rect: render.Rect{X: 210, Y: 90, W: 80, H: 60}, Color: rc(221, 221, 221)},
				render.Border{Rect: render.Rect{X: 210, Y: 90, W: 80, H: 60}, Widths: [4]float64{1, 1, 1, 1}, Colors: [4]render.Color{rc(0, 0, 0), rc(0, 0, 0), rc(0, 0, 0), rc(0, 0, 0)}, Styles: [4]string{"solid", "solid", "solid", "solid"}},
			}
		}})
	styles := []string{"dashed", "dotted", "double", "groove", "ridge", "inset", "outset", "solid"}
	cases = append(cases, paintCase{"borders_styles", func() string {
		var sb strings.Builder
		for i, s := range styles {
			sb.WriteString(box(10+float64(i%4)*72, 10+float64(i/4)*90, 62, 70, "border:6px "+s+" #3366cc;"))
		}
		return sb.String()
	}(), func() []render.Op {
		var ops []render.Op
		for i, s := range styles {
			c := rc(51, 102, 204)
			ops = append(ops, render.Border{Rect: render.Rect{X: 10 + float64(i%4)*72, Y: 10 + float64(i/4)*90, W: 62, H: 70}, Widths: [4]float64{6, 6, 6, 6}, Colors: [4]render.Color{c, c, c, c}, Styles: [4]string{s, s, s, s}})
		}
		return ops
	}})
	cases = append(cases, paintCase{"radii",
		box(10, 10, 130, 80, "background:#69c;border-radius:40px 10px / 20px 30px;") +
			box(160, 10, 130, 80, "background:#c96;border-radius:50%;") +
			box(10, 110, 130, 80, "background:#9c6;border-radius:0 40px 0 40px;") +
			box(160, 110, 130, 80, "background:#c69;border-radius:200px;"),
		func() []render.Op {
			return []render.Op{
				render.FillRect{Rect: render.Rect{X: 10, Y: 10, W: 130, H: 80}, Color: rc(102, 153, 204), Radii: render.Radii{{40, 20}, {10, 30}, {40, 20}, {10, 30}}},
				render.FillRect{Rect: render.Rect{X: 160, Y: 10, W: 130, H: 80}, Color: rc(204, 153, 102), Radii: render.Radii{{65, 40}, {65, 40}, {65, 40}, {65, 40}}},
				render.FillRect{Rect: render.Rect{X: 10, Y: 110, W: 130, H: 80}, Color: rc(153, 204, 102), Radii: render.Radii{{0, 0}, {40, 40}, {0, 0}, {40, 40}}},
				render.FillRect{Rect: render.Rect{X: 160, Y: 110, W: 130, H: 80}, Color: rc(204, 102, 153), Radii: rad(200)},
			}
		}})
	st := func(pos float64, c render.Color) render.GradientStop { return render.GradientStop{Pos: pos, Color: c} }
	cases = append(cases, paintCase{"gradients",
		box(10, 10, 130, 80, "background:linear-gradient(90deg,#f00,#00f);") +
			box(160, 10, 130, 80, "background:linear-gradient(45deg,#fc0 20%,#09f 80%);") +
			box(10, 110, 130, 80, "background:radial-gradient(circle 40px at 65px 40px,#fff,#c00 60%,#006);") +
			box(160, 110, 130, 80, "background:linear-gradient(to bottom,rgba(255,0,0,1),rgba(0,0,255,0)),#fff;"),
		func() []render.Op {
			return []render.Op{
				render.Gradient{Rect: render.Rect{X: 10, Y: 10, W: 130, H: 80}, Kind: "linear", Angle: 90, Stops: []render.GradientStop{st(0, rc(255, 0, 0)), st(1, rc(0, 0, 255))}},
				render.Gradient{Rect: render.Rect{X: 160, Y: 10, W: 130, H: 80}, Kind: "linear", Angle: 45, Stops: []render.GradientStop{st(0.2, rc(255, 204, 0)), st(0.8, rc(0, 153, 255))}},
				render.Gradient{Rect: render.Rect{X: 10, Y: 110, W: 130, H: 80}, Kind: "radial", CX: 65, CY: 40, RX: 40, RY: 40, Stops: []render.GradientStop{st(0, rc(255, 255, 255)), st(0.6, rc(204, 0, 0)), st(1, rc(0, 0, 102))}},
				render.FillRect{Rect: render.Rect{X: 160, Y: 110, W: 130, H: 80}, Color: rc(255, 255, 255)},
				render.Gradient{Rect: render.Rect{X: 160, Y: 110, W: 130, H: 80}, Kind: "linear", Angle: 180, Stops: []render.GradientStop{st(0, rc(255, 0, 0)), st(1, render.Color{R: 0, G: 0, B: 255, A: 0})}},
			}
		}})
	cases = append(cases, paintCase{"box_shadow",
		box(30, 30, 100, 60, "background:#fff;border:1px solid #888;box-shadow:6px 8px 12px 2px rgba(0,0,0,.45);") +
			box(170, 30, 100, 60, "background:#fff;border-radius:16px;box-shadow:0 0 20px rgba(0,60,200,.8);") +
			box(30, 120, 100, 60, "background:#eee;box-shadow:inset 4px 4px 10px rgba(0,0,0,.5);") +
			box(170, 120, 100, 60, "background:#fff;border-radius:20px;box-shadow:8px 8px 0 -4px #c33, inset 0 0 12px #36c;"),
		func() []render.Op {
			r1 := render.Rect{X: 30, Y: 30, W: 100, H: 60}
			r2 := render.Rect{X: 170, Y: 30, W: 100, H: 60}
			r3 := render.Rect{X: 30, Y: 120, W: 100, H: 60}
			r4 := render.Rect{X: 170, Y: 120, W: 100, H: 60}
			a := func(r, g, b uint8, al uint8) render.Color { return render.Color{R: r, G: g, B: b, A: al} }
			return []render.Op{
				render.BoxShadow{Rect: r1, DX: 6, DY: 8, Blur: 12, Spread: 2, Color: a(0, 0, 0, 115)},
				render.FillRect{Rect: r1, Color: rc(255, 255, 255)},
				render.Border{Rect: r1, Widths: [4]float64{1, 1, 1, 1}, Colors: [4]render.Color{rc(136, 136, 136), rc(136, 136, 136), rc(136, 136, 136), rc(136, 136, 136)}, Styles: [4]string{"solid", "solid", "solid", "solid"}},
				render.BoxShadow{Rect: r2, Radii: rad(16), Blur: 20, Color: a(0, 60, 200, 204)},
				render.FillRect{Rect: r2, Color: rc(255, 255, 255), Radii: rad(16)},
				render.FillRect{Rect: r3, Color: rc(238, 238, 238)},
				render.BoxShadow{Rect: r3, DX: 4, DY: 4, Blur: 10, Color: a(0, 0, 0, 128), Inset: true},
				render.BoxShadow{Rect: r4, Radii: rad(20), DX: 8, DY: 8, Spread: -4, Color: rc(204, 51, 51)},
				render.FillRect{Rect: r4, Color: rc(255, 255, 255), Radii: rad(20)},
				render.BoxShadow{Rect: r4, Radii: rad(20), Blur: 12, Color: rc(51, 102, 204), Inset: true},
			}
		}})
	if sans != nil && sansB != nil {
		rm := func(f *font.Face, size float64) (float64, float64) {
			m := f.Metrics(size)
			return float64(int(m.Ascent + 0.5)), float64(int(m.Descent + 0.5))
		}
		txt := func(x, y float64, f string, size float64, extra, s string) (string, float64) {
			return fmt.Sprintf(`<div style="position:absolute;left:%gpx;top:%gpx;font:%gpx '%s';white-space:pre;line-height:normal;%s">%s</div>`, x, y, size, f, extra, s), 0
		}
		h1, _ := txt(10, 10, "DejaVu Sans", 16, "color:#222", "Plain text sample")
		h2, _ := txt(10, 40, "DejaVu Sans", 16, "color:#222;text-decoration:underline", "Underlined text")
		h3, _ := txt(10, 70, "Liberation Sans", 20, "color:#c30;text-shadow:2px 2px 3px rgba(0,0,0,.5)", "Text shadow")
		h4, _ := txt(10, 110, "Liberation Sans", 24, "color:#036;text-decoration:line-through", "Line through")
		h5, _ := txt(10, 150, "DejaVu Sans", 14, "color:#060;text-decoration:overline", "Overline")
		cases = append(cases, paintCase{"text", h1 + h2 + h3 + h4 + h5, func() []render.Op {
			a1, _ := rm(sans, 16)
			a2, _ := rm(sansB, 20)
			a3, _ := rm(sansB, 24)
			a4, _ := rm(sans, 14)
			r1 := textRun(sans, "Plain text sample", 10, 10+a1, 16, rc(34, 34, 34))
			r2 := textRun(sans, "Underlined text", 10, 40+a1, 16, rc(34, 34, 34))
			r2.Underline = true
			r2.DecoColor = rc(34, 34, 34)
			r2.DecoStyle = "solid"
			r3 := textRun(sansB, "Text shadow", 10, 70+a2, 20, rc(204, 51, 0))
			r3.Shadows = []render.TextShadow{{DX: 2, DY: 2, Blur: 3, Color: render.Color{A: 128}}}
			r4 := textRun(sansB, "Line through", 10, 110+a3, 24, rc(0, 51, 102))
			r4.LineThrough = true
			r4.DecoColor = rc(0, 51, 102)
			r4.DecoStyle = "solid"
			r5 := textRun(sans, "Overline", 10, 150+a4, 14, rc(0, 102, 0))
			r5.Overline = true
			r5.DecoColor = rc(0, 102, 0)
			r5.DecoStyle = "solid"
			return []render.Op{r1, r2, r3, r4, r5}
		}})
	}
	img := testImage()
	uri := imageDataURI(img)
	cases = append(cases, paintCase{"images",
		fmt.Sprintf(`<img src="%s" style="position:absolute;left:10px;top:10px;width:80px;height:80px;image-rendering:pixelated">`, uri) +
			fmt.Sprintf(`<img src="%s" style="position:absolute;left:110px;top:10px;width:80px;height:80px;border-radius:20px">`, uri) +
			fmt.Sprintf(`<div style="position:absolute;left:10px;top:110px;width:130px;height:80px;background:url(%s) 0 0/16px 16px repeat;image-rendering:pixelated"></div>`, uri) +
			fmt.Sprintf(`<div style="position:absolute;left:160px;top:110px;width:130px;height:80px;background:url(%s) 5px 5px/24px 24px repeat-x;image-rendering:pixelated"></div>`, uri),
		func() []render.Op {
			return []render.Op{
				render.Image{Img: img, Dst: render.Rect{X: 10, Y: 10, W: 80, H: 80}, Smooth: false},
				render.Image{Img: img, Dst: render.Rect{X: 110, Y: 10, W: 80, H: 80}, Smooth: true, Radii: rad(20)},
				render.Image{Img: img, Dst: render.Rect{X: 10, Y: 110, W: 130, H: 80}, Tile: &render.Tile{Origin: [2]float64{10, 110}, Size: [2]float64{16, 16}, Step: [2]float64{16, 16}, RepeatX: true, RepeatY: true}},
				render.Image{Img: img, Dst: render.Rect{X: 160, Y: 110, W: 130, H: 80}, Tile: &render.Tile{Origin: [2]float64{165, 115}, Size: [2]float64{24, 24}, Step: [2]float64{24, 24}, RepeatX: true}},
			}
		}})
	cases = append(cases, paintCase{"layers",
		box(10, 10, 100, 70, "background:#e33;") +
			`<div style="position:absolute;left:0;top:0;width:300px;height:200px;opacity:.5">` + box(60, 40, 100, 70, "background:#33e;") + box(100, 70, 100, 70, "background:#3c3;") + `</div>` +
			box(200, 10, 80, 60, "background:#fc0;transform:rotate(15deg);transform-origin:0 0;") +
			`<div style="position:absolute;left:0;top:0;width:300px;height:200px;overflow:hidden;clip-path:inset(150px 20px 10px 20px)">` + box(10, 150, 280, 40, "background:#909;") + `</div>`,
		func() []render.Op {
			return []render.Op{
				render.FillRect{Rect: render.Rect{X: 10, Y: 10, W: 100, H: 70}, Color: rc(238, 51, 51)},
				render.PushLayer{Opacity: 0.5, Bounds: render.Rect{X: 0, Y: 0, W: 300, H: 200}},
				render.FillRect{Rect: render.Rect{X: 60, Y: 40, W: 100, H: 70}, Color: rc(51, 51, 238)},
				render.FillRect{Rect: render.Rect{X: 100, Y: 70, W: 100, H: 70}, Color: rc(51, 204, 51)},
				render.PopLayer{},
				render.PushTransform{M: [6]float64{0.9659258262890683, 0.25881904510252074, -0.25881904510252074, 0.9659258262890683, 200, 10}},
				render.FillRect{Rect: render.Rect{X: 0, Y: 0, W: 80, H: 60}, Color: rc(255, 204, 0)},
				render.PopTransform{},
				render.PushClip{Rect: render.Rect{X: 20, Y: 150, W: 260, H: 40}},
				render.FillRect{Rect: render.Rect{X: 10, Y: 150, W: 280, H: 40}, Color: rc(153, 0, 153)},
				render.PopClip{},
			}
		}})
	return cases
}

func TestPaintAgainstChromium(t *testing.T) {
	cases := paintCases()
	dir := "testdata/paint"
	if os.Getenv("PAINT_REFRESH") != "" {
		refreshPaintRefs(t, cases, dir)
	}
	var rows []string
	for _, c := range cases {
		ref, err := loadPNG(filepath.Join(dir, ".ref", c.name+".png"))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got := Paint(c.ops(), 300, 200, 1, PaintEnv{Background: Color{1, 1, 1, 1}})
		if out := os.Getenv("PAINT_OUT"); out != "" {
			_ = os.MkdirAll(out, 0o755)
			f, _ := os.Create(filepath.Join(out, c.name+".png"))
			_ = png.Encode(f, got)
			f.Close()
		}
		within, mae := compareRGBA(got, ref, 8)
		rows = append(rows, fmt.Sprintf("%-16s %6.2f%%  mae=%.2f", c.name, within*100, mae))
		if within < 0.90 {
			t.Errorf("%s: %.2f%% within tolerance", c.name, within*100)
		}
	}
	sort.Strings(rows)
	t.Logf("\npaint fixture      within<=8   MAE\n%s", strings.Join(rows, "\n"))
}

func loadPNG(p string) (*image.NRGBA, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	n := image.NewNRGBA(img.Bounds())
	draw.Draw(n, n.Bounds(), img, img.Bounds().Min, draw.Src)
	return n, nil
}

func compareRGBA(a *image.RGBA, b *image.NRGBA, tol int) (float64, float64) {
	if a.Bounds().Dx() != b.Bounds().Dx() || a.Bounds().Dy() != b.Bounds().Dy() {
		return 0, 255
	}
	n := a.Bounds().Dx() * a.Bounds().Dy()
	ok := 0
	var sum float64
	for i := 0; i < n; i++ {
		good := true
		for c := 0; c < 3; c++ {
			d := int(a.Pix[i*4+c]) - int(b.Pix[i*4+c])
			if d < 0 {
				d = -d
			}
			sum += float64(d)
			if d > tol {
				good = false
			}
		}
		if good {
			ok++
		}
	}
	return float64(ok) / float64(n), sum / float64(n*3)
}

func refreshPaintRefs(t *testing.T, cases []paintCase, dir string) {
	ab, chrome := os.Getenv("AB"), os.Getenv("CHROME")
	if ab == "" {
		t.Fatal("PAINT_REFRESH needs AB (agent-browser) and CHROME")
	}
	abs, _ := filepath.Abs(dir)
	for _, c := range cases {
		page := filepath.Join(abs, c.name+".html")
		html := `<!doctype html><meta charset=utf-8><body style="margin:0;background:#fff;width:300px;height:200px;overflow:hidden">` + c.html + `</body>`
		_ = os.WriteFile(page, []byte(html), 0o644)
		run := func(args ...string) error {
			cmd := exec.Command(ab, append([]string{"--session", "rpaint"}, args...)...)
			cmd.Env = append(os.Environ(), "AGENT_BROWSER_EXECUTABLE_PATH="+chrome)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Logf("%s %v: %s", ab, args, out)
			}
			return err
		}
		_ = run("set", "viewport", "300", "200")
		_ = run("open", "file://"+page)
		_ = run("screenshot", filepath.Join(abs, ".ref", c.name+".png"))
	}
	_ = run0(ab, chrome)
}

func run0(ab, chrome string) error {
	cmd := exec.Command(ab, "--session", "rpaint", "close")
	cmd.Env = append(os.Environ(), "AGENT_BROWSER_EXECUTABLE_PATH="+chrome)
	return cmd.Run()
}
