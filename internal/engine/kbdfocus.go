package engine

import (
	"errors"
	"strings"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// Keyboard focus on a page without a script VM. With a VM the prelude owns
// focus (document.activeElement); without one nothing did, so "click a field,
// then type" (the most common agent pattern: Input.mouse or Input.click, then
// Input.keyboard text) typed nowhere. The tab remembers the focused element of
// its current document here instead.

type kbdFocusRec struct {
	doc *fnet.Doc
	id  int64
}

// ErrNoFocus is returned when keys are typed into a script-less page where no
// element has focus.
var ErrNoFocus = errors.New("no element has keyboard focus: click or focus a field first, or pass a selector")

func (t *Tab) hasVM() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.js != nil
}

// setKbdFocus records bid (0: none) as the focused element of the current document.
func (t *Tab) setKbdFocus(bid int64) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if bid == 0 || d == nil {
		t.kbdFocus.Store(nil)
		return
	}
	t.kbdFocus.Store(&kbdFocusRec{doc: d, id: bid})
}

// kbdFocused is the element setKbdFocus recorded, 0 when it belongs to an
// earlier document or has left the tree.
func (t *Tab) kbdFocused() int64 {
	r := t.kbdFocus.Load()
	if r == nil {
		return 0
	}
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil || r.doc != d {
		return 0
	}
	if n := d.NodeByID(r.id); n == nil || !attachedTo(d, n) {
		return 0
	}
	return r.id
}

// focusAfterPointer records the focus a press on bid gives in a browser: the
// element or its nearest focusable ancestor, else nothing.
func (t *Tab) focusAfterPointer(bid int64) int64 {
	var target int64
	_ = t.withDoc(func(d *fnet.Doc) error {
		target = d.FocusableAncestor(bid)
		return nil
	})
	t.setKbdFocus(target)
	return target
}

// keyboardNoVM handles Input.keyboard keys on a script-less page: Tab and
// Shift+Tab move focus, Enter in a text field submits its form, Backspace
// deletes the last character; other keys have no default action here.
func (t *Tab) keyboardNoVM(keys string) error {
	key, mods := splitKeyCombo(strings.TrimSpace(keys))
	shift := mods != nil && mods["shiftKey"] == true
	cur := t.kbdFocused()
	switch {
	case key == "Tab" && (mods == nil || len(mods) == 1 && shift):
		var next int64
		_ = t.withDoc(func(d *fnet.Doc) error { next = d.NextTabStop(cur, shift); return nil })
		if next != 0 {
			t.setKbdFocus(next)
		}
	case key == "Enter" && cur != 0:
		if info := t.infoForBackend(cur); info != nil && strings.EqualFold(info.Name, "input") {
			return t.SubmitFormBackend(cur)
		}
	case key == "Backspace" && cur != 0 && mods == nil:
		return t.withDoc(func(d *fnet.Doc) error {
			v, _, _ := d.GetAttribute(cur, "value")
			if r := []rune(v); len(r) > 0 {
				return d.SetValue(cur, string(r[:len(r)-1]))
			}
			return nil
		})
	}
	return nil
}

// typeNoVM types text into the focused element of a script-less page.
func (t *Tab) typeNoVM(text string, delay time.Duration) error {
	bid := t.kbdFocused()
	if bid == 0 {
		return ErrNoFocus
	}
	info := t.infoForBackend(bid)
	if info != nil && !strings.EqualFold(info.Name, "input") && !strings.EqualFold(info.Name, "textarea") {
		// a contenteditable host (or another focusable element: a browser types nothing there)
		ce := false
		err := t.withDoc(func(d *fnet.Doc) error {
			if v, ok := d.GetAttr(bid, "contenteditable"); ok && !strings.EqualFold(v, "false") {
				ce = true
				d.SetTextContent(bid, d.TextContent(bid)+text)
			}
			return nil
		})
		if ce {
			t.afterInput()
		}
		return err
	}
	for _, r := range text {
		if r == '\n' {
			if info == nil || !strings.EqualFold(info.Name, "textarea") {
				if err := t.keyboardNoVM("Enter"); err != nil {
					return err
				}
				continue
			}
		}
		if err := t.appendValue(bid, string(r)); err != nil {
			return err
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	t.afterInput()
	return nil
}
