// actions.go — the Playwright action surface on Tab: input (mouse/keyboard/
// touch), semantic form ops, waits, storage & attribute getters. All actions
// auto-wait through the Locator model and dispatch synthetic events into the
// QuickJS context so page listeners react like they would in Chromium.

package engine

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// ---- backend-id based helpers ----------------------------------------------

func (t *Tab) withDoc(fn func(*fnet.Doc) error) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return ErrNoTab
	}
	return fn(t.dom)
}

// fire dispatches a DOM event into page JavaScript and reports whether the
// default action may proceed (false when a listener called preventDefault).
// With JS disabled every event is "not prevented".
func (t *Tab) fire(bid int64, typ string, init map[string]any) bool {
	switch typ {
	case "click", "mousedown", "pointerdown", "keydown", "touchstart", "input":
		if t.bc == nil { // the person used this top-level page (a page without scripts has no VM to see it)
			t.Context().noteInteraction(schemefulSite(t.URL()))
		}
	}
	t.mu.RLock()
	j := t.js
	t.mu.RUnlock()
	if j == nil {
		return true
	}
	return j.dispatch(bid, typ, init)
}

// jsCall invokes a prelude helper (e.g. __swDispatchKey) and returns its result.
func (t *Tab) jsCall(fn string, args ...any) (any, bool) {
	t.mu.RLock()
	j := t.js
	t.mu.RUnlock()
	if j == nil {
		return nil, false
	}
	r, err := j.call(fn, args...)
	if err != nil {
		t.LogConsole("error", jsErrorText(err, fn))
		return nil, false
	}
	return r, true
}

// afterAction lets page scripts react (fetch, re-render) before the tool returns.
func (t *Tab) afterAction() { t.settle(t.settleBudget() / 3) }

// ---- attribute / html getters by backend id ---------------------------------

func (t *Tab) GetAttributeByBackend(backendID int64, k string) (string, bool, error) {
	var val string
	var ok bool
	err := t.withDoc(func(d *fnet.Doc) error {
		v, present, err := d.GetAttribute(backendID, k)
		val, ok = v, present
		return err
	})
	return val, ok, err
}

func (t *Tab) SetAttributeByBackend(backendID int64, k, v string) error {
	return t.withDoc(func(d *fnet.Doc) error { return d.SetAttribute(backendID, k, v) })
}

func (t *Tab) OuterHTMLByBackend(backendID int64) string {
	out := ""
	_ = t.withDoc(func(d *fnet.Doc) error {
		if s, err := d.OuterHTML(backendID); err == nil {
			out = s
		}
		return nil
	})
	return out
}

func (t *Tab) InnerHTMLByBackend(backendID int64) string {
	out := ""
	_ = t.withDoc(func(d *fnet.Doc) error {
		if s, err := d.InnerHTML(backendID); err == nil {
			out = s
		}
		return nil
	})
	return out
}

func (t *Tab) TextByBackend(backendID int64) string {
	out := ""
	_ = t.withDoc(func(d *fnet.Doc) error {
		if s, err := d.TextByBackend(backendID); err == nil {
			out = s
		}
		return nil
	})
	return out
}

// AttributesByBackend returns every attribute of a node as a flat map.
func (t *Tab) AttributesByBackend(backendID int64) map[string]string {
	out := map[string]string{}
	_ = t.withDoc(func(d *fnet.Doc) error {
		for _, k := range attrKeysOf(d, backendID) {
			if v, ok, _ := d.GetAttribute(backendID, k); ok {
				out[k] = v
			}
		}
		return nil
	})
	return out
}

// attrKeysOf enumerates attribute names via well-known HTML attributes plus
// anything recorded on the Info text snapshot; cheap and bounded.
func attrKeysOf(d *fnet.Doc, backendID int64) []string {
	candidates := []string{"id", "class", "href", "src", "type", "name", "value",
		"placeholder", "alt", "title", "role", "aria-label", "data-testid",
		"disabled", "checked", "style", "target", "action", "method"}
	var keys []string
	for _, k := range candidates {
		if _, ok, _ := d.GetAttribute(backendID, k); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// ---- core element actions ---------------------------------------------------

// Click resolves sel (Playwright engines supported), scrolls it into view,
// dispatches pointer/mouse/click events into JS, then applies navigation
// semantics: <a href> navigates, submit buttons post their form, labels focus
// their control.
func (t *Tab) Click(sel string) error {
	l := t.Locator(sel)
	// A ref that resolves to something that can never be clicked fails at
	// once instead of waiting for it to become visible.
	if bid, err := l.BackendID(); err == nil {
		if info := t.infoForBackend(bid); info != nil && nonRenderable[strings.ToLower(info.Name)] {
			return fmt.Errorf("<%s> is not a visible element; take a new snapshot and use a listed ref", info.Name)
		}
	} else if strings.Contains(err.Error(), "snapshot") {
		return err // unknown or detached ref: no point waiting
	}
	if err := l.WaitForCondition("visible", 5*time.Second); err != nil {
		// fall back to attached-only click (hidden links still navigate)
		if err2 := l.WaitForCondition("attached", 100*time.Millisecond); err2 != nil {
			return err
		}
	}
	bid, err := l.BackendID()
	if err != nil {
		return err
	}
	return t.clickBackend(bid, sel)
}

var nonRenderable = map[string]bool{"script": true, "style": true, "head": true, "meta": true, "link": true, "title": true, "template": true, "noscript": true}

func (t *Tab) clickBackend(bid int64, sel string) error {
	info := t.infoForBackend(bid)
	// pointer sequence first: page JS may preventDefault or navigate itself
	t.fire(bid, "pointerdown", nil)
	t.fire(bid, "mousedown", nil)
	t.fire(bid, "focus", nil)
	t.fire(bid, "pointerup", nil)
	t.fire(bid, "mouseup", nil)
	proceed := t.fire(bid, "click", nil)
	if !proceed {
		t.afterAction()
		return nil
	}
	if info == nil {
		return nil
	}
	// a tap on a Data Saver placeholder loads what it stands for (and does
	// not also follow the link the image may sit in)
	if t.isLitePlaceholder(bid) {
		defer t.afterAction()
		return t.LoadSkipped(bid)
	}
	defer t.afterAction()
	switch strings.ToLower(info.Name) {
	case "a":
		href := ""
		if h, ok, _ := t.GetAttributeByBackend(bid, "href"); ok {
			href = h
		} else {
			href = info.Href
		}
		if dl, isDL, _ := t.GetAttributeByBackend(bid, "download"); isDL && href != "" && !strings.HasPrefix(href, "javascript:") {
			t.mu.RLock()
			hasJS := t.js != nil
			t.mu.RUnlock()
			if !hasJS || !strings.HasPrefix(href, "blob:") {
				_, err := t.DownloadURL(href, dl)
				return err
			}
			return nil // blob: downloads are saved by the page's default action in JS
		}
		if href != "" && !strings.HasPrefix(href, "javascript:") {
			u, err := resolveURL(t.URL(), href)
			if err != nil {
				return err
			}
			return t.Navigate(u, nil)
		}
	case "label":
		if forr, ok, _ := t.GetAttributeByBackend(bid, "for"); ok && forr != "" {
			return t.Focus("#" + forr)
		}
	case "button", "input":
		typ := strings.ToLower(info.Type)
		// A <button> without a type attribute is a submit button.
		if typ == "submit" || typ == "image" || (typ == "" && strings.EqualFold(info.Name, "button")) {
			inForm := false
			_ = t.withDoc(func(d *fnet.Doc) error { inForm = d.FormOf(bid) != 0; return nil })
			if inForm {
				return t.SubmitFormBackend(bid)
			}
			return nil
		}
		if typ == "checkbox" || typ == "radio" {
			_ = t.withDoc(func(d *fnet.Doc) error { return d.SetChecked(bid, !(info.Checked)) })
			t.fire(bid, "input", nil)
			t.fire(bid, "change", nil)
			return nil
		}
	}
	return nil
}

func (t *Tab) infoForBackend(bid int64) *fnet.Info {
	var info *fnet.Info
	_ = t.withDoc(func(d *fnet.Doc) error {
		info = d.InfoByBackend(bid)
		return nil
	})
	return info
}

// Hover dispatches mouseenter/mouseover on the matched element.
func (t *Tab) Hover(sel string) error {
	bid, err := t.Locator(sel).BackendID()
	if err != nil {
		return err
	}
	t.fire(bid, "pointerover", nil)
	t.fire(bid, "mouseover", nil)
	t.fire(bid, "mouseenter", nil)
	t.afterAction()
	return nil
}

// Focus moves DOM focus to the element (dispatches focus/focusin).
func (t *Tab) Focus(sel string) error {
	bid, err := t.Locator(sel).BackendID()
	if err != nil {
		return err
	}
	if err := t.withDoc(func(d *fnet.Doc) error { return d.SetAttribute(bid, "data-sw-focused", "1") }); err != nil {
		return err
	}
	t.fire(bid, "focus", nil)
	return nil
}

// Type simulates per-character keystrokes into an input target.
func (t *Tab) Type(sel, text string, delay time.Duration) error {
	bid, err := t.Locator(sel).WaitBackend("visible", 5*time.Second)
	if err != nil {
		return err
	}
	t.fire(bid, "focus", nil)
	for _, r := range text {
		if err := t.appendValue(bid, string(r)); err != nil {
			return err
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	t.fire(bid, "change", nil)
	t.afterAction()
	return nil
}

// appendValue types one character: key events through the prelude (which
// updates the value and fires input unless prevented), with a Go fallback
// when JS is disabled.
func (t *Tab) appendValue(bid int64, s string) error {
	if _, ok := t.jsCall("__swDispatchKey", bid, s); ok {
		return nil
	}
	cur := ""
	_ = t.withDoc(func(d *fnet.Doc) error {
		if v, ok, _ := d.GetAttribute(bid, "value"); ok {
			cur = v
		}
		return nil
	})
	return t.withDoc(func(d *fnet.Doc) error { return d.SetValue(bid, cur+s) })
}

// Press sends one or more key presses ("Enter", "Control+a", "Tab"). Enter on
// a form field submits the enclosing form like real browsers do.
func (t *Tab) Press(sel, keys string) error {
	bid, err := t.Locator(sel).BackendID()
	if err != nil {
		return err
	}
	key := strings.TrimSpace(keys)
	proceed := true
	if r, ok := t.jsCall("__swDispatchKey", bid, key); ok {
		proceed = truthyAny(r)
	}
	if proceed && strings.EqualFold(key, "Enter") {
		info := t.infoForBackend(bid)
		if info != nil && strings.EqualFold(info.Name, "input") {
			return t.SubmitFormFor(sel)
		}
	}
	t.afterAction()
	return nil
}

// Keyboard sends a raw key sequence to the focused element/body.
func (t *Tab) Keyboard(keys string) error {
	t.jsCall("__swDispatchKey", 0, keys)
	t.afterAction()
	return nil
}

// Fill sets a value instantly (Playwright fill fast-path). Works on inputs,
// textareas and selects (select matches value OR visible label).
func (t *Tab) Fill2(sel, value string) error {
	bid, err := t.Locator(sel).WaitBackend("visible", 5*time.Second)
	if err != nil {
		return err
	}
	var isSelect bool
	_ = t.withDoc(func(d *fnet.Doc) error {
		_, _, isSel, err := d.InputTarget(bid)
		isSelect = isSel
		return err
	})
	if isSelect {
		if err := t.withDoc(func(d *fnet.Doc) error { return d.SelectOption(bid, value) }); err != nil {
			return err
		}
	} else if err := t.withDoc(func(d *fnet.Doc) error { return d.SetValue(bid, value) }); err != nil {
		return err
	}
	t.fire(bid, "focus", nil)
	if _, ok := t.jsCall("__swSetValueFromGo", bid, value); !ok {
		t.fire(bid, "input", nil)
		t.fire(bid, "change", nil)
	}
	t.afterAction()
	return nil
}

// Check / Uncheck toggle checkbox-or-radio semantics.
func (t *Tab) Check(sel string) error   { return t.setChecked(sel, true) }
func (t *Tab) Uncheck(sel string) error { return t.setChecked(sel, false) }

func (t *Tab) setChecked(sel string, want bool) error {
	bid, err := t.Locator(sel).WaitBackend("visible", 5*time.Second)
	if err != nil {
		return err
	}
	if err := t.withDoc(func(d *fnet.Doc) error { return d.SetChecked(bid, want) }); err != nil {
		return err
	}
	t.fire(bid, "input", nil)
	t.fire(bid, "change", nil)
	t.afterAction()
	return nil
}

// SelectOption picks option(s) by value or visible label; supports multi.
func (t *Tab) SelectOption(sel, value string) error {
	bid, err := t.Locator(sel).WaitBackend("visible", 5*time.Second)
	if err != nil {
		return err
	}
	if err := t.withDoc(func(d *fnet.Doc) error { return d.SelectOption(bid, value) }); err != nil {
		return err
	}
	t.fire(bid, "input", nil)
	t.fire(bid, "change", nil)
	t.afterAction()
	return nil
}

// SelectedOptions lists currently-selected values of a select.
func (t *Tab) SelectedOptions(sel string) ([]string, error) {
	bid, err := t.Locator(sel).BackendID()
	if err != nil {
		return nil, err
	}
	var out []string
	err = t.withDoc(func(d *fnet.Doc) error {
		o, e := d.SelectOptions(bid)
		out = o
		return e
	})
	return out, err
}

// SetValueRaw writes an input's value without events (debugging aid).
func (t *Tab) SetValueRaw(sel, value string) error {
	bid, err := t.Locator(sel).BackendID()
	if err != nil {
		return err
	}
	return t.withDoc(func(d *fnet.Doc) error { return d.SetValue(bid, value) })
}

// ---- mouse / touch / drag / scroll ------------------------------------------

func (t *Tab) MouseMove(x, y int) { t.jsCall("__swMouse", "mousemove", x, y) }
func (t *Tab) MouseDown(x, y int) { t.jsCall("__swMouse", "mousedown", x, y) }
func (t *Tab) MouseUp(x, y int)   { t.jsCall("__swMouse", "mouseup", x, y) }

// MouseClick hit-tests coordinates against deterministic bounding boxes and
// clicks whatever element covers the point.
func (t *Tab) MouseClick(x, y int) error {
	bid, err := t.hitTest(x, y)
	if err != nil {
		t.jsCall("__swMouse", "click", x, y)
		return nil // no element at point — harmless, like clicking whitespace
	}
	return t.clickBackend(bid, "")
}

// Tap dispatches touchstart/touchend and follows link semantics.
func (t *Tab) Tap(sel string) error {
	bid, err := t.Locator(sel).WaitBackend("visible", 5*time.Second)
	if err != nil {
		return err
	}
	t.jsCall("__swTouch", bid, "touchstart")
	t.jsCall("__swTouch", bid, "touchend")
	return t.clickBackend(bid, sel)
}

// Drag fires dragstart/drop/dragend between two elements' centres.
func (t *Tab) Drag(source, target string) error {
	sb, err := t.Locator(source).WaitBackend("visible", 5*time.Second)
	if err != nil {
		return err
	}
	tb, err := t.Locator(target).WaitBackend("visible", 5*time.Second)
	if err != nil {
		return err
	}
	t.jsCall("__swDrag", sb, tb)
	t.afterAction()
	return nil
}

// ScrollIntoView records scroll state and dispatches scroll events.
func (t *Tab) ScrollIntoView(sel string) error {
	bid, err := t.Locator(sel).WaitBackend("attached", 5*time.Second)
	if err != nil {
		return err
	}
	box := [4]int{0, 0, 0, 0}
	_ = t.withDoc(func(d *fnet.Doc) error {
		b, e := d.BoundingBox(bid)
		if e == nil {
			box = b
		}
		return nil
	})
	t.stateRing.Push(Event{Kind: EvDomMutated, Data: map[string]any{"scrollTo": box}})
	t.fire(bid, "scroll", nil)
	_, vh, _, _, _ := t.Viewport()
	t.SetScrollY(box[1] - vh/3)
	return nil
}

// MouseWheel accumulates wheel deltas (agents use it to trigger lazy-loaders).
func (t *Tab) MouseWheel(dx, dy int) error {
	t.jsCall("__swWheel", dx, dy)
	t.afterAction()
	if dy != 0 {
		y := int(t.ds.scrollY.Load()) + dy
		if y < 0 {
			y = 0
		}
		t.SetScrollY(y) // lazy images follow the wheel
	}
	return nil
}

func (t *Tab) hitTest(x, y int) (int64, error) {
	var found int64
	_ = t.withDoc(func(d *fnet.Doc) error {
		found = d.ElementFromPoint(x, y)
		return nil
	})
	if found == 0 {
		return 0, ErrNotFound
	}
	return found, nil
}

// ---- waits ------------------------------------------------------------------

// WaitForSelector polls until sel satisfies state (attached|detached|visible|
// hidden|enabled) or timeout ms elapses. Returns match count when satisfied.
func (t *Tab) WaitForSelector(sel, state string, timeoutMs float64) (int, error) {
	if state == "" {
		state = "visible"
	}
	to := time.Duration(timeoutMs) * time.Millisecond
	if to <= 0 {
		to = 30 * time.Second
	}
	l := t.Locator(sel)
	if state == "detached" || state == "hidden" {
		deadline := time.Now().Add(to)
		for {
			n, _ := l.Count()
			ok := n == 0
			if state == "hidden" && n > 0 {
				v, _ := l.IsVisible()
				ok = !v
			}
			if ok {
				return n, nil
			}
			if time.Now().After(deadline) {
				return n, fmt.Errorf("%w: selector %q never became %s", ErrTimeout, sel, state)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if err := l.WaitForCondition(state, to); err != nil {
		return 0, err
	}
	return l.Count()
}

// WaitForFunction polls a JS expression until truthy or timeout.
func (t *Tab) WaitForFunction(expr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		res, err := t.Evaluate("(" + expr + ")")
		if err == nil {
			var v any
			if json.Unmarshal(res, &v) == nil && truthy(v) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: waitForFunction timed out", ErrTimeout)
		}
		t.settle(50 * time.Millisecond)
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != "" && x != "null"
	default:
		return true
	}
}

// WaitForLoadState waits for domcontentloaded/load (both synchronous here —
// Navigate completes after scripts run — so this validates readiness).
func (t *Tab) WaitForLoadState(state string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if state == "networkidle" {
		return t.waitNetworkIdle(timeout)
	}
	deadline := time.Now().Add(timeout)
	for {
		t.mu.RLock()
		ready := t.dom != nil
		t.mu.RUnlock()
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: load state %q not reached", ErrTimeout, state)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// WaitForURL blocks until the tab URL matches pattern (glob).
func (t *Tab) WaitForURL(pattern string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		u := t.URL()
		if ok, _ := path.Match(pattern, u); ok || strings.Contains(u, strings.Trim(pattern, "*")) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: url never matched %q", ErrTimeout, pattern)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---- storage bridge -----------------------------------------------------------

// StorageSnapshot returns localStorage+sessionStorage merged view for agents.
func (t *Tab) StorageSnapshot() map[string]string {
	out := map[string]string{}
	c := t.Context()
	if c == nil {
		return out
	}
	origin := t.URL()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.state[origin].(map[string]any); ok {
		for k, v := range m {
			if s, ok2 := v.(string); ok2 {
				out[k] = s
			}
		}
	}
	return out
}

// SetStorageValue writes into the context store for the current origin.
func (t *Tab) SetStorageValue(key, val string) {
	if c := t.Context(); c != nil {
		c.SetStorageValue(t.URL(), key, val)
	}
}

// GetStorageValue reads from the context store for the current origin.
func (t *Tab) GetStorageValue(key string) (string, bool) {
	if c := t.Context(); c != nil {
		return c.GetStorageValue(t.URL(), key)
	}
	return "", false
}
