package engine

// frames_host.go: the host functions behind window proxies, window.open and frames
// (the JavaScript side is frames_prelude.js). A page only ever holds window ids;
// every function checks that the calling document was handed the id it names.

import (
	_ "embed"
	"strings"
)

//go:embed frames_prelude.js
var framesPreludeJS string

func init() {
	registerPrelude("frames", framesPreludeJS)
	registerHostExtension("frames", func(j *jsVM) map[string]hostFn { return (&winHost{j: j}).fns() })
}

type winHost struct{ j *jsVM }

func (h *winHost) reach(id string) *Tab {
	t := h.j.tab
	if t == nil {
		return nil
	}
	if id == t.ID {
		return t
	}
	w := t.eng.lookupWin(id)
	if w == nil || !t.canReach(id) || w.Context() != t.Context() {
		return nil
	}
	return w
}

func idOrNil(t *Tab) any {
	if t == nil {
		return nil
	}
	return t.ID
}

func (h *winHost) fns() map[string]hostFn {
	return map[string]hostFn{
		"winId": func(a []any) (any, error) {
			if h.j.tab == nil {
				return "", nil
			}
			return h.j.tab.ID, nil
		},
		// the relations of the calling window: parent, top, opener, name
		"winInfo": func(a []any) (any, error) {
			t := h.j.tab
			out := map[string]any{"id": t.ID, "top": nil, "parent": nil, "opener": nil, "name": t.WindowName()}
			if p := t.win.parent; p != nil {
				out["parent"] = p.ID
				out["top"] = t.Top().ID
				t.knows(p)
				t.knows(t.Top())
			}
			if o := t.Opener(); o != nil {
				out["opener"] = o.ID
				t.knows(o)
			}
			return out, nil
		},
		// a relation of another window: parent | top | opener (null when none)
		"winRel": func(a []any) (any, error) {
			w := h.reach(argS(a, 0))
			if w == nil {
				return nil, nil
			}
			var r *Tab
			switch argS(a, 1) {
			case "parent":
				r = w.win.parent
			case "top":
				if w.win.parent != nil {
					r = w.Top()
				}
			case "opener":
				r = w.Opener()
			}
			if r != nil {
				h.j.tab.knows(r)
			}
			return idOrNil(r), nil
		},
		"winClosed": func(a []any) (any, error) {
			w := h.reach(argS(a, 0))
			return w == nil || w.WindowClosed(), nil
		},
		"winOrigin": func(a []any) (any, error) {
			w := h.reach(argS(a, 0))
			if w == nil {
				return "", nil
			}
			return w.origin(), nil
		},
		"winName": func(a []any) (any, error) {
			w := h.reach(argS(a, 0))
			if w == nil {
				return "", nil
			}
			return w.WindowName(), nil
		},
		"setWinName": func(a []any) (any, error) { h.j.tab.setWindowName(argS(a, 0)); return nil, nil },
		"winLen": func(a []any) (any, error) {
			w := h.reach(argS(a, 0))
			if w == nil {
				return 0, nil
			}
			return len(w.frameList()), nil
		},
		"winFrameAt": func(a []any) (any, error) {
			w := h.reach(argS(a, 0))
			if w == nil {
				return nil, nil
			}
			i := int(argI(a, 1))
			fl := w.frameList()
			if i < 0 || i >= len(fl) {
				return nil, nil
			}
			h.j.tab.knows(fl[i].child)
			return fl[i].child.ID, nil
		},
		"winPost": func(a []any) (any, error) {
			h.j.tab.postMessageTo(argS(a, 0), argS(a, 1), argS(a, 2))
			return nil, nil
		},
		// window.open(url, target, features): the id of the window it refers to, or null
		"winOpen": func(a []any) (any, error) {
			t := h.j.tab
			w := t.openFromScript(argS(a, 0), argS(a, 1), argS(a, 2))
			if w != nil {
				t.knows(w)
			}
			return idOrNil(w), nil
		},
		"winClose": func(a []any) (any, error) {
			t := h.j.tab
			id := argS(a, 0)
			if id == t.ID {
				return t.closeFromScript(), nil
			}
			w := h.reach(id)
			if w != nil && w.win.scriptOpened && w.win.opener.Load() == t {
				return w.closeFromScript(), nil
			}
			return false, nil
		},
		"winFocus": func(a []any) (any, error) {
			if w := h.reach(argS(a, 0)); w != nil {
				w.focusFromScript()
			}
			return nil, nil
		},
		// location of another window: any window may be navigated, none may be read
		"winNavigate": func(a []any) (any, error) {
			t := h.j.tab
			w := h.reach(argS(a, 0))
			if w == nil || w == t {
				return nil, nil
			}
			abs, err := resolveURL(t.URL(), argS(a, 1))
			if err != nil {
				return nil, err
			}
			if (w == t.win.parent || w == t.Top()) && !t.mayNavigateAncestor() {
				t.recordPopup(PopupEvent{URL: abs, Blocked: true, Reason: "sandbox: top-level navigation not allowed"})
				return nil, nil
			}
			w.queueNavigation(abs, navSpec{}, t)
			return nil, nil
		},
		"frameWinOf": func(a []any) (any, error) {
			t := h.j.tab
			t.mu.RLock()
			d := t.dom
			t.mu.RUnlock()
			if d == nil {
				return nil, nil
			}
			n := d.NodeByID(argI(a, 0))
			if n == nil {
				return nil, nil
			}
			f := t.frameOf(n)
			if f == nil {
				return nil, nil
			}
			t.knows(f.child)
			return f.child.ID, nil
		},
		// anchors and forms with a target other than _self
		"navTarget": func(a []any) (any, error) {
			t := h.j.tab
			rel := argS(a, 2)
			tg := argS(a, 1)
			t.navigateTarget(navSpec{rawURL: argS(a, 0), target: tg, noOpener: anchorNoOpener(tg, rel), noReferrer: relHas(rel, "noreferrer")})
			return true, nil
		},
		"activeNow": func(a []any) (any, error) { return h.j.tab.HasTransientActivation(), nil },
		"sandboxed": func(a []any) (any, error) { return h.j.tab.Sandboxed(), nil },
		"sandboxFlag": func(a []any) (any, error) {
			s := h.j.tab.win.sbx
			if s == nil {
				return true, nil
			}
			switch strings.TrimSpace(argS(a, 0)) {
			case "scripts":
				return s.scripts, nil
			case "popups":
				return s.popups, nil
			case "forms":
				return s.forms, nil
			case "same-origin":
				return s.sameOrigin, nil
			}
			return false, nil
		},
	}
}
