package render

import "testing"

// The first layout of a page with container queries keeps its first pass only when a second one could not
// differ: a condition that flips at the measured size, or a container unit, still lays out again.
func TestContainerQueryFirstLayoutPasses(t *testing.T) {
	for _, tc := range []struct {
		name, css string
		w, h      float64
	}{
		{"flips", `@container (min-width: 400px) { .x { height: 50px } }`, 500, 50},
		{"stable", `@container (min-width: 10000px) { .x { height: 50px } } .x { height: 7px }`, 500, 7},
		{"units", `.x { width: 50cqw; height: 3px }`, 250, 3},
	} {
		doc := parseHTML(t, `<div class=c><div class=x id=x></div></div>`)
		sheets := []Sheet{{Text: `body{margin:0} .c{container-type:inline-size;width:500px} ` + tc.css}}
		for _, sess := range []*Session{nil, NewSession()} {
			res := Layout(doc, Options{Width: 800, Height: 600, Session: sess, Sheets: sheets})
			r, ok := res.ClientRect(findByID(doc, "x"))
			if !ok || r.W != tc.w || r.H != tc.h {
				t.Errorf("%s (session %v): .x is %vx%v, want %vx%v", tc.name, sess != nil, r.W, r.H, tc.w, tc.h)
			}
		}
	}
}
