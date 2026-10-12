//go:build linux

package x11

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/pixconv"
)

var testDisplay string

// TestMain starts a private Xvfb (never the ambient $DISPLAY) so the tests
// behave the same inside and outside xvfb-run.
func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	path, err := exec.LookPath("Xvfb")
	if err != nil {
		fmt.Fprintln(os.Stderr, "x11 tests: Xvfb not installed, skipping")
		return 0
	}
	for n := 90; n < 190; n++ {
		sock := fmt.Sprintf("/tmp/.X11-unix/X%d", n)
		lock := fmt.Sprintf("/tmp/.X%d-lock", n)
		if exists(sock) || exists(lock) {
			continue
		}
		cmd := exec.Command(path, fmt.Sprintf(":%d", n), "-screen", "0", "1920x1080x24", "-nolisten", "tcp", "-ac", "-noreset")
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "x11 tests: cannot start Xvfb:", err)
			return 0
		}
		defer func() {
			cmd.Process.Kill()
			cmd.Wait()
			os.Remove(sock)
		}()
		ok := false
		for i := 0; i < 100; i++ {
			if exists(sock) {
				if c, err := dialDisplay(fmt.Sprintf(":%d", n)); err == nil {
					c.close()
					ok = true
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "x11 tests: Xvfb did not come up")
			return 1
		}
		testDisplay = fmt.Sprintf(":%d", n)
		os.Unsetenv("XAUTHORITY")
		os.Unsetenv("GDK_SCALE")
		os.Unsetenv("SWEEF_SCALE")
		return m.Run()
	}
	fmt.Fprintln(os.Stderr, "x11 tests: no free display")
	return 1
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

var _ = filepath.Join

// ---- helpers ---------------------------------------------------------------

func openTest(t *testing.T, cfg shell.Config) *Window {
	t.Helper()
	if cfg.Width == 0 {
		cfg.Width, cfg.Height = 640, 480
	}
	w, err := OpenDisplay(testDisplay, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return w
}

func testConn(t *testing.T) *conn {
	t.Helper()
	c, err := dialDisplay(testDisplay)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	return c
}

// next reads events until pred matches (other events are dropped).
func next(t *testing.T, w *Window, pred func(shell.Event) bool) shell.Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-w.Events():
			if !ok {
				t.Fatal("event channel closed")
			}
			if pred(e) {
				return e
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
		}
	}
}

func ofKind(k shell.Kind) func(shell.Event) bool {
	return func(e shell.Event) bool { return e.Kind == k }
}

// expectNone asserts no event matching pred arrives within d.
func expectNone(t *testing.T, w *Window, d time.Duration, pred func(shell.Event) bool) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case e := <-w.Events():
			if pred(e) {
				t.Fatalf("unexpected event %+v", e)
			}
		case <-deadline:
			return
		}
	}
}

// XTEST fake input through a second connection.
type xtest struct {
	c     *conn
	major uint8
	root  uint32
}

func newXtest(t *testing.T) *xtest {
	c := testConn(t)
	ext, err := c.queryExtension("XTEST")
	if err != nil || !ext.present {
		t.Skip("XTEST not available")
	}
	return &xtest{c: c, major: ext.major, root: c.screen.root}
}

func (x *xtest) fake(typ, detail uint8, rx, ry int) {
	b := request(x.major, 2, 36)
	b[4] = typ
	b[5] = detail
	le.PutUint32(b[12:], 0)
	le.PutUint16(b[24:], uint16(int16(rx)))
	le.PutUint16(b[26:], uint16(int16(ry)))
	x.c.send(b, false)
	x.c.sync()
}

func (x *xtest) motion(rx, ry int) { x.fake(6, 0, rx, ry) }
func (x *xtest) button(b uint8, down bool) {
	t := uint8(5)
	if down {
		t = 4
	}
	x.fake(t, b, 0, 0)
}
func (x *xtest) key(kc uint8, down bool) {
	t := uint8(3)
	if down {
		t = 2
	}
	x.fake(t, kc, 0, 0)
}

func (c *conn) geometry(win uint32) (x, y, w, h int) {
	b := request(opGetGeometry, 0, 8)
	le.PutUint32(b[4:], win)
	r, err := c.do(b)
	if err != nil {
		return
	}
	return int(int16(le.Uint16(r[12:]))), int(int16(le.Uint16(r[14:]))), int(le.Uint16(r[16:])), int(le.Uint16(r[18:]))
}

func (w *Window) keycodeFor(ks uint32) uint8 {
	km := w.kb.Load()
	for kc := int(km.min); kc < 256; kc++ {
		if km.sym(uint8(kc), 0) == ks {
			return uint8(kc)
		}
	}
	return 0
}

// grab reads the window back as RGBA.
func grab(t *testing.T, c *conn, win uint32, r image.Rectangle) *image.RGBA {
	t.Helper()
	data, err := c.getImage(win, r.Min.X, r.Min.Y, r.Dx(), r.Dy())
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	pixconv.SwapRB(img.Pix, data[:r.Dx()*r.Dy()*4])
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xff
	}
	return img
}

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i] = uint8(x * 255 / w)
			img.Pix[i+1] = uint8(y * 255 / h)
			img.Pix[i+2] = uint8((x + y) & 0xff)
			img.Pix[i+3] = 0xff
		}
	}
	return img
}

func samePixels(a, b *image.RGBA) (int, int, bool) {
	if a.Rect.Dx() != b.Rect.Dx() || a.Rect.Dy() != b.Rect.Dy() {
		return -1, -1, false
	}
	for y := 0; y < a.Rect.Dy(); y++ {
		for x := 0; x < a.Rect.Dx(); x++ {
			i, j := a.PixOffset(x, y), b.PixOffset(x, y)
			if a.Pix[i] != b.Pix[j] || a.Pix[i+1] != b.Pix[j+1] || a.Pix[i+2] != b.Pix[j+2] {
				return x, y, false
			}
		}
	}
	return 0, 0, true
}
