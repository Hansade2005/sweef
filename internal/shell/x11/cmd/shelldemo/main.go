//go:build linux

// Command shelldemo animates frames into an X11 shell window and prints the
// achieved Present throughput and latency at a given size, for full-frame
// and partial-damage updates.
//
//	xvfb-run -a -s "-screen 0 1920x1080x24" go run ./internal/shell/x11/cmd/shelldemo
//	go run ./internal/shell/x11/cmd/shelldemo -live     # interactive animation
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"sort"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/x11"
)

func main() {
	var (
		width   = flag.Int("w", 1920, "window width (logical px)")
		height  = flag.Int("h", 1080, "window height (logical px)")
		secs    = flag.Float64("secs", 3, "seconds per measurement")
		live    = flag.Bool("live", false, "animate until the window is closed instead of benchmarking")
		display = flag.String("display", "", "X display (default $DISPLAY)")
	)
	flag.Parse()

	win, err := x11.OpenDisplay(*display, shell.Config{Title: "Sweef shell demo", Width: *width, Height: *height, Dark: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer win.Close()
	w, h, scale := win.Size()
	dw, dh := int(float64(w)*scale+0.5), int(float64(h)*scale+0.5)
	fmt.Printf("window %dx%d logical, scale %.2f, device %dx%d, MIT-SHM=%v\n", w, h, scale, dw, dh, win.UsesSHM())

	// Pre-render a few frames so measurement excludes content generation.
	const nframes = 8
	frames := make([]*image.RGBA, nframes)
	for i := range frames {
		frames[i] = render(dw, dh, i)
	}

	if *live {
		runLive(win, frames, dw, dh)
		return
	}

	full := image.Rectangle{}
	fmt.Println()
	bench(win, "full frame (100% damage)", frames, func(i int) image.Rectangle { return full }, dw, dh, *secs)
	// 10% of the area: a moving rectangle 31.6% x 31.6% of each dimension.
	rw, rh := int(float64(dw)*0.3162), int(float64(dh)*0.3162)
	bench(win, "10% damage rectangle", frames, func(i int) image.Rectangle {
		x := (i * 37) % (dw - rw)
		y := (i * 23) % (dh - rh)
		return image.Rect(x, y, x+rw, y+rh)
	}, dw, dh, *secs)
	// A typical browser update: a caret-sized / hover-sized 1% region.
	sw, sh := dw/10, dh/10
	bench(win, "1% damage rectangle", frames, func(i int) image.Rectangle {
		x := (i * 53) % (dw - sw)
		y := (i * 31) % (dh - sh)
		return image.Rect(x, y, x+sw, y+sh)
	}, dw, dh, *secs)
}

func bench(win *x11.Window, name string, frames []*image.RGBA, dmg func(int) image.Rectangle, dw, dh int, secs float64) {
	// 1) pipelined: Present as fast as it returns (convert + send; waits only
	//    for the server to release the shared segment).
	var calls []time.Duration
	start := time.Now()
	n := 0
	for time.Since(start) < time.Duration(secs*float64(time.Second)) {
		t0 := time.Now()
		win.Present(frames[n%len(frames)], dmg(n))
		calls = append(calls, time.Since(t0))
		n++
	}
	win.Sync()
	elapsed := time.Since(start)
	fps := float64(n) / elapsed.Seconds()
	// 2) synchronous: Present followed by a server round trip, i.e. time
	//    until the X server has the pixels (latency to "on screen" modulo
	//    display refresh, which Xvfb does not have).
	var lat []time.Duration
	start = time.Now()
	m := 0
	for time.Since(start) < time.Duration(secs*float64(time.Second)) {
		t0 := time.Now()
		win.Present(frames[m%len(frames)], dmg(m))
		win.Sync()
		lat = append(lat, time.Since(t0))
		m++
	}
	syncFPS := float64(m) / time.Since(start).Seconds()
	fmt.Printf("%s @ %dx%d\n", name, dw, dh)
	fmt.Printf("  pipelined : %7.1f fps   Present() call  median %-9v p95 %-9v max %v\n", fps, pct(calls, 50), pct(calls, 95), pct(calls, 100))
	fmt.Printf("  synced    : %7.1f fps   Present+roundtrip median %-9v p95 %-9v max %v\n", syncFPS, pct(lat, 50), pct(lat, 95), pct(lat, 100))
}

func pct(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := (len(s) - 1) * p / 100
	return s[i].Round(time.Microsecond)
}

// render makes a colourful frame whose pattern depends on phase.
func render(w, h, phase int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w; x++ {
			row[x*4] = uint8((x + phase*40) * 255 / w)
			row[x*4+1] = uint8(y * 255 / h)
			row[x*4+2] = uint8(((x ^ y) + phase*16) & 0xff)
			row[x*4+3] = 0xff
		}
	}
	return img
}

func runLive(win *x11.Window, frames []*image.RGBA, dw, dh int) {
	tick := time.NewTicker(time.Second / 60)
	defer tick.Stop()
	n, last := 0, time.Now()
	frames0 := 0
	for {
		select {
		case e, ok := <-win.Events():
			if !ok || e.Kind == shell.EvClose {
				return
			}
			switch e.Kind {
			case shell.EvResize:
				fmt.Printf("resize %dx%d scale %.2f\n", e.W, e.H, e.Scale)
			case shell.EvKeyDown:
				fmt.Printf("key %q code %s mods %b repeat=%v\n", e.Key, e.Code, e.Mods, e.Repeat)
			case shell.EvText:
				fmt.Printf("text %q\n", e.Text)
			case shell.EvMouseDown, shell.EvMouseUp:
				fmt.Printf("mouse %d at %.1f,%.1f kind=%d\n", e.Button, e.X, e.Y, e.Kind)
			case shell.EvWheel:
				fmt.Printf("wheel %.1f,%.1f\n", e.DX, e.DY)
			case shell.EvDropFiles:
				fmt.Printf("drop %q\n", e.Files)
			}
		case <-tick.C:
			win.Present(frames[n%len(frames)], image.Rectangle{})
			n++
			frames0++
			if time.Since(last) > 2*time.Second {
				fmt.Printf("%.1f fps\n", float64(frames0)/time.Since(last).Seconds())
				frames0, last = 0, time.Now()
			}
		}
	}
}
