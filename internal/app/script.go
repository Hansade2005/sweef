package app

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/headless"
	"github.com/sweefbrowser/sweef/internal/shell"
)

// Script is a headless test script (`sweef gui --headless --script f.json`).
//
//	{"size":"1280x800","steps":[
//	  {"wait":"idle"}, {"key":"ctrl+l"}, {"type":"example.com"}, {"key":"Enter"},
//	  {"click":[100,20]}, {"wheel":[0,300],"at":[400,400]}, {"shot":"home.png"},
//	  {"expect":{"title":"Example","pixel":[10,10,"#1c2030"]}} ]}
type Script struct {
	Size  string `json:"size,omitempty"`
	Steps []Step `json:"steps"`
}

// Step is one script action; exactly one action field is set.
type Step struct {
	Wait   string    `json:"wait,omitempty"`  // "idle" | "load" | duration
	Sleep  string    `json:"sleep,omitempty"` // duration
	Key    string    `json:"key,omitempty"`   // chord, e.g. "ctrl+t", "Enter"
	Type   string    `json:"type,omitempty"`  // committed text
	Click  []float64 `json:"click,omitempty"` // x,y logical
	Right  []float64 `json:"rightclick,omitempty"`
	Move   []float64 `json:"move,omitempty"`
	Wheel  []float64 `json:"wheel,omitempty"` // dx,dy; position from At (default viewport centre)
	At     []float64 `json:"at,omitempty"`
	Resize []int     `json:"resize,omitempty"` // w,h logical
	Nav    string    `json:"nav,omitempty"`    // navigate the active tab (as if typed)
	NewTab string    `json:"newtab,omitempty"` // open a tab
	Shot   string    `json:"shot,omitempty"`   // file name inside the out dir
	Expect *Expect   `json:"expect,omitempty"`
	Print  string    `json:"print,omitempty"` // "stats"
}

// Expect asserts on app state; a failure aborts the script.
type Expect struct {
	Title    string `json:"title,omitempty"`    // active tab title contains
	URL      string `json:"url,omitempty"`      // active tab URL contains
	Tabs     *int   `json:"tabs,omitempty"`     // number of tabs
	Pixel    []any  `json:"pixel,omitempty"`    // [x, y, "#rrggbb"] on the screen (device px)
	NotPixel []any  `json:"notPixel,omitempty"` // same, must differ
}

// LoadScript reads a script file.
func LoadScript(path string) (*Script, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Script
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// ParseSize parses "1280x800".
func ParseSize(s string) (int, int, bool) {
	p := strings.SplitN(strings.ToLower(s), "x", 2)
	if len(p) != 2 {
		return 0, 0, false
	}
	w, e1 := strconv.Atoi(p[0])
	h, e2 := strconv.Atoi(p[1])
	return w, h, e1 == nil && e2 == nil && w > 0 && h > 0
}

// RunScript replays s against a running app through the headless window,
// writing screenshots into outDir.
func RunScript(a *App, win *headless.Window, s *Script, outDir string) error {
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
	}
	settle := func() { a.WaitIdle(120*time.Millisecond, 30*time.Second) }
	settle()
	for i, st := range s.Steps {
		if err := runStep(a, win, st, outDir, settle); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return nil
}

func pt(v []float64) (float64, float64, bool) {
	if len(v) < 2 {
		return 0, 0, false
	}
	return v[0], v[1], true
}

func runStep(a *App, win *headless.Window, st Step, outDir string, settle func()) error {
	push := func(evs ...shell.Event) {
		for _, e := range evs {
			win.Push(e)
		}
	}
	switch {
	case st.Wait != "":
		switch st.Wait {
		case "idle", "load":
			if !a.WaitIdle(150*time.Millisecond, 60*time.Second) {
				return fmt.Errorf("wait idle: timed out")
			}
		default:
			d, err := time.ParseDuration(st.Wait)
			if err != nil {
				return err
			}
			time.Sleep(d)
		}
	case st.Sleep != "":
		d, err := time.ParseDuration(st.Sleep)
		if err != nil {
			return err
		}
		time.Sleep(d)
	case st.Key != "":
		push(headless.KeyEvents(st.Key)...)
		settle()
	case st.Type != "":
		push(headless.TypeEvents(st.Type)...)
		settle()
	case st.Click != nil:
		x, y, ok := pt(st.Click)
		if !ok {
			return fmt.Errorf("click needs [x,y]")
		}
		push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y},
			shell.Event{Kind: shell.EvMouseDown, X: x, Y: y},
			shell.Event{Kind: shell.EvMouseUp, X: x, Y: y})
		settle()
	case st.Right != nil:
		x, y, _ := pt(st.Right)
		push(shell.Event{Kind: shell.EvMouseDown, X: x, Y: y, Button: 2}, shell.Event{Kind: shell.EvMouseUp, X: x, Y: y, Button: 2})
		settle()
	case st.Move != nil:
		x, y, _ := pt(st.Move)
		push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y})
		settle()
	case st.Wheel != nil:
		x, y, ok := pt(st.At)
		if !ok {
			w, h, _ := win.Size()
			x, y = float64(w)/2, float64(h)/2
		}
		dx, dy, _ := pt(st.Wheel)
		push(shell.Event{Kind: shell.EvWheel, X: x, Y: y, DX: dx, DY: dy})
		settle()
	case st.Resize != nil:
		if len(st.Resize) < 2 {
			return fmt.Errorf("resize needs [w,h]")
		}
		_, _, sc := win.Size()
		win.Resize(st.Resize[0], st.Resize[1], sc)
		settle()
	case st.Nav != "":
		a.post(func() { a.navigateInput(a.model.Active(a.winID), st.Nav) })
		settle()
	case st.NewTab != "":
		a.post(func() { a.newTab(st.NewTab, false, true) })
		settle()
	case st.Shot != "":
		settle()
		if outDir == "" {
			return fmt.Errorf("shot needs --out")
		}
		return win.SavePNG(filepath.Join(outDir, st.Shot))
	case st.Print == "stats":
		fmt.Print(a.Stats().Format())
	case st.Expect != nil:
		settle()
		return checkExpect(a, win, st.Expect)
	default:
		return fmt.Errorf("empty step")
	}
	return nil
}

func checkExpect(a *App, win *headless.Window, e *Expect) error {
	t, _ := a.activeTab()
	if e.Title != "" && !strings.Contains(t.Title, e.Title) {
		return fmt.Errorf("title %q does not contain %q", t.Title, e.Title)
	}
	if e.URL != "" && !strings.Contains(t.URL, e.URL) {
		return fmt.Errorf("url %q does not contain %q", t.URL, e.URL)
	}
	if e.Tabs != nil && a.model.TabCount() != *e.Tabs {
		return fmt.Errorf("%d tabs, want %d", a.model.TabCount(), *e.Tabs)
	}
	for k, spec := range [][]any{e.Pixel, e.NotPixel} {
		if len(spec) == 0 {
			continue
		}
		if len(spec) < 3 {
			return fmt.Errorf("pixel needs [x,y,\"#rrggbb\"]")
		}
		x, _ := spec[0].(float64)
		y, _ := spec[1].(float64)
		hexs, _ := spec[2].(string)
		want, err := ParseColor(hexs)
		if err != nil {
			return err
		}
		got := win.Screen().RGBAAt(int(x), int(y))
		match := got.R == want.R && got.G == want.G && got.B == want.B
		if k == 0 && !match {
			return fmt.Errorf("pixel (%d,%d) = #%02x%02x%02x, want %s", int(x), int(y), got.R, got.G, got.B, hexs)
		}
		if k == 1 && match {
			return fmt.Errorf("pixel (%d,%d) is %s, expected something else", int(x), int(y), hexs)
		}
	}
	return nil
}

// ParseColor parses "#rrggbb".
func ParseColor(s string) (color.RGBA, error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return color.RGBA{}, fmt.Errorf("bad colour %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.RGBA{}, err
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, nil
}
