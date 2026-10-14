package synthetic

import (
	"fmt"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// glyphs is a 5x7 bitmap font for the characters the test pattern prints. Each row is 5 bits, MSB on the left.
var glyphs = map[rune][7]uint8{
	'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2': {0x0E, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1F},
	'3': {0x1F, 0x02, 0x04, 0x02, 0x01, 0x11, 0x0E},
	'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6': {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
	'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
	':': {0x00, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00},
	'.': {0x00, 0x00, 0x00, 0x00, 0x00, 0x0C, 0x0C},
	'F': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'#': {0x0A, 0x0A, 0x1F, 0x0A, 0x1F, 0x0A, 0x0A},
	' ': {},
}

// bars are the classic eight test-card colours (white, yellow, cyan, green, magenta, red, blue, black) as limited
// range Y, U, V.
var bars = func() [8][3]uint8 {
	rgb := [8][3]int{{235, 235, 235}, {235, 235, 16}, {16, 235, 235}, {16, 235, 16}, {235, 16, 235}, {235, 16, 16}, {16, 16, 235}, {16, 16, 16}}
	var out [8][3]uint8
	for i, c := range rgb {
		out[i] = [3]uint8{capture.RGBToY(c[0], c[1], c[2]), capture.RGBToU(c[0], c[1], c[2]), capture.RGBToV(c[0], c[1], c[2])}
	}
	return out
}()

// Render draws test-pattern frame n of a w x h camera running at fps: eight colour bars that move four pixels
// per frame, a frame counter and the stream time (n / fps), so a recording shows at a glance that frames are
// whole, in order and not repeated. The result depends on nothing but its arguments.
func Render(dst *capture.Frame, n int, fps float64) {
	w, h := dst.W, dst.H
	shift := (n * 4) % max(w, 1)
	for y := 0; y < h; y++ {
		row := dst.Y[y*dst.YStride:]
		for x := 0; x < w; x++ {
			row[x] = bars[((x+shift)%w)*8/w][0]
		}
	}
	for y := 0; y < (h+1)/2; y++ {
		ur := dst.U[y*dst.UVStride:]
		vr := dst.V[y*dst.UVStride:]
		for x := 0; x < (w+1)/2; x++ {
			c := bars[((2*x+shift)%w)*8/w]
			ur[x], vr[x] = c[1], c[2]
		}
	}
	t := time.Duration(float64(n) / fps * float64(time.Second))
	txt := fmt.Sprintf("F%06d %02d:%02d:%02d.%03d", n, int(t.Hours()), int(t.Minutes())%60, int(t.Seconds())%60, int(t.Milliseconds())%1000)
	scale := max(1, h/120)
	drawText(dst, 4*scale, 4*scale, scale, txt)
}

func drawText(f *capture.Frame, x0, y0, scale int, s string) {
	cw := 6 * scale
	w, h := len(s)*cw+2*scale, 9*scale+2*scale
	// black plate with a one-glyph margin so the text stays legible over any bar colour
	for y := y0 - scale; y < y0-scale+h && y < f.H; y++ {
		if y < 0 {
			continue
		}
		for x := x0 - scale; x < x0-scale+w && x < f.W; x++ {
			if x < 0 {
				continue
			}
			f.Y[y*f.YStride+x] = 16
			f.U[(y/2)*f.UVStride+x/2] = 128
			f.V[(y/2)*f.UVStride+x/2] = 128
		}
	}
	for i, r := range s {
		g := glyphs[r]
		for gy := 0; gy < 7; gy++ {
			for gx := 0; gx < 5; gx++ {
				if g[gy]&(1<<(4-gx)) == 0 {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						x, y := x0+i*cw+gx*scale+dx, y0+gy*scale+dy
						if x < f.W && y < f.H {
							f.Y[y*f.YStride+x] = 235
						}
					}
				}
			}
		}
	}
}

// Camera is the deterministic test-pattern camera.
type Camera struct {
	info capture.DeviceInfo
	// W, H and FPS are the defaults used when Start gets no constraints.
	W, H int
	FPS  float64
	// Unthrottled delivers frames as fast as the consumer takes them instead of at FPS (tests, benchmarks).
	Unthrottled bool

	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
	err  error
	w, h int
	fps  float64
	used bool
}

// NewCamera makes a camera with the given identity and defaults (640x480 at 30 fps if zero).
func NewCamera(info capture.DeviceInfo, w, h int, fps float64) *Camera {
	if w <= 0 || h <= 0 {
		w, h = 640, 480
	}
	if fps <= 0 {
		fps = 30
	}
	return &Camera{info: info, W: w, H: h, FPS: fps}
}

func (c *Camera) Info() capture.DeviceInfo { return c.info }

// Mode implements capture.Moder.
func (c *Camera) Mode() (int, int, float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w, c.h, c.fps
}

func (c *Camera) Start(vc capture.VideoConstraints) (<-chan *capture.Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used {
		return nil, fmt.Errorf("synthetic: camera already started")
	}
	c.used = true
	w, h, fps := c.W, c.H, c.FPS
	if vc.Width > 0 || vc.Height > 0 {
		if m, ok := capture.ClosestMode(Ladder, vc.Width, vc.Height, vc.FrameRate); ok {
			w, h = m.W, m.H
		}
	}
	if vc.FrameRate > 0 {
		fps = min(vc.FrameRate, 60)
	}
	c.w, c.h, c.fps = w, h, fps
	c.stop, c.done = make(chan struct{}), make(chan struct{})
	out := make(chan *capture.Frame, 2)
	go c.run(out, w, h, fps, c.stop, c.done)
	return out, nil
}

func (c *Camera) run(out chan *capture.Frame, w, h int, fps float64, stop, done chan struct{}) {
	defer close(done)
	defer close(out)
	var tick *time.Ticker
	if !c.Unthrottled {
		tick = time.NewTicker(time.Duration(float64(time.Second) / fps))
		defer tick.Stop()
	}
	for n := 0; ; n++ {
		if tick != nil {
			select {
			case <-tick.C:
			case <-stop:
				return
			}
		}
		f := capture.NewFrame(w, h)
		Render(f, n, fps)
		f.PTS = time.Duration(float64(n) / fps * float64(time.Second))
		if tick != nil {
			select { // a live camera never waits for a slow reader: drop the oldest frame
			case out <- f:
			case <-stop:
				return
			default:
				select {
				case <-out:
				default:
				}
				select {
				case out <- f:
				default:
				}
			}
			continue
		}
		select {
		case out <- f:
		case <-stop:
			return
		}
	}
}

func (c *Camera) Stop() error {
	c.mu.Lock()
	stop, done := c.stop, c.done
	c.stop = nil
	c.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
	return nil
}

func (c *Camera) Err() error { return c.err }

// Ladder is the list of sizes the synthetic camera offers.
var Ladder = func() []capture.Mode {
	var m []capture.Mode
	for _, s := range [][2]int{{160, 120}, {320, 240}, {640, 360}, {640, 480}, {800, 600}, {1280, 720}, {1920, 1080}} {
		m = append(m, capture.Mode{W: s[0], H: s[1], FPSMin: 1, FPSMax: 60})
	}
	return m
}()
