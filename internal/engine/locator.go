package engine

import (
	"fmt"
	"strings"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// Locator is a lazy, re-resolving element handle — exactly Playwright's
// Locator semantics. It stores only the selector (plus optional nth), so it
// costs ~48 bytes and can never leak a detached node. Every action
// re-resolves against the live DOM, which makes agent scripts resilient to
// SPA re-renders without any polling code on their side.
type Locator struct {
	tab      *Tab
	sel      string
	nth      int // -1 = first match
	hasNth   bool
	filterTx string // locator.filter({ hasText })
}

func (t *Tab) Locator(sel string) *Locator {
	return &Locator{tab: t, sel: sel, nth: -1}
}

// Nth mirrors locator.nth(i). Negative counts from the end like Python.
func (l *Locator) Nth(i int) *Locator {
	c := *l
	c.nth = i
	c.hasNth = true
	return &c
}

// First / Last mirror locator.first() / locator.last().
func (l *Locator) First() *Locator { return l.Nth(0) }
func (l *Locator) Last() *Locator {
	c := *l
	c.nth = -1e9 // resolved specially below
	c.hasNth = true
	return &c
}

// Filter mirrors locator.filter({ hasText }) with substring matching.
func (l *Locator) Filter(hasText string) *Locator {
	c := *l
	c.filterTx = hasText
	return &c
}

func (l *Locator) Selector() string { return l.sel }

// resolve returns all matching infos (re-queried every call — lazy by design).
func (l *Locator) resolve() ([]*fnet.Info, error) {
	l.tab.mu.RLock()
	defer l.tab.mu.RUnlock()
	if l.tab.dom == nil {
		return nil, ErrNotFound
	}
	infos, err := l.tab.dom.ResolveEngine(l.sel)
	if err != nil {
		return nil, err
	}
	if l.filterTx != "" {
		var kept []*fnet.Info
		want := strings.ToLower(l.filterTx)
		for _, i := range infos {
			if strings.Contains(strings.ToLower(i.Text), want) {
				kept = append(kept, i)
			}
		}
		infos = kept
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, l.sel)
	}
	return infos, nil
}

// resolveOne picks the single target honouring nth.
func (l *Locator) resolveOne() (*fnet.Info, error) {
	infos, err := l.resolve()
	if err != nil {
		return nil, err
	}
	if !l.hasNth {
		return infos[0], nil
	}
	i := l.nth
	if i <= -1000000 { // Last()
		return infos[len(infos)-1], nil
	}
	if i < 0 {
		i += len(infos)
	}
	if i < 0 || i >= len(infos) {
		return nil, fmt.Errorf("locator %q: no element at index %d (count=%d)", l.sel, l.nth, len(infos))
	}
	return infos[i], nil
}

func (l *Locator) BackendID() (int64, error) {
	i, err := l.resolveOne()
	if err != nil {
		return 0, err
	}
	return i.BackendID, nil
}

func (l *Locator) Count() (int, error) {
	infos, err := l.resolve()
	if err != nil {
		return 0, nil // count of nothing is 0, not an error (Playwright rule)
	}
	return len(infos), nil
}

func (l *Locator) All() ([]*NodeInfo, error) {
	infos, err := l.resolve()
	if err != nil {
		return nil, err
	}
	out := make([]*NodeInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, infoToNode(i))
	}
	return out, nil
}

// ---- assertions ------------------------------------------------------------

func (l *Locator) IsVisible() (bool, error) {
	id, err := l.BackendID()
	if err != nil {
		return false, nil // missing => not visible, but not an error
	}
	l.tab.mu.RLock()
	defer l.tab.mu.RUnlock()
	return l.tab.dom.IsVisible(id)
}

func (l *Locator) IsHidden() (bool, error) {
	v, err := l.IsVisible()
	return !v, err
}

func (l *Locator) IsEnabled() (bool, error) {
	i, err := l.resolveOne()
	if err != nil {
		return false, err
	}
	return !i.Disabled, nil
}

func (l *Locator) IsChecked() (bool, error) {
	i, err := l.resolveOne()
	if err != nil {
		return false, err
	}
	return i.Checked, nil
}

func (l *Locator) TextContent() (string, error) {
	id, err := l.BackendID()
	if err != nil {
		return "", err
	}
	l.tab.mu.RLock()
	defer l.tab.mu.RUnlock()
	return l.tab.dom.TextByBackend(id)
}

func (l *Locator) InnerText() (string, error) { return l.TextContent() }

func (l *Locator) InnerHTML() (string, error) {
	id, err := l.BackendID()
	if err != nil {
		return "", err
	}
	l.tab.mu.RLock()
	defer l.tab.mu.RUnlock()
	return l.tab.dom.InnerHTML(id)
}

func (l *Locator) GetAttribute(k string) (string, bool, error) {
	id, err := l.BackendID()
	if err != nil {
		return "", false, err
	}
	l.tab.mu.RLock()
	defer l.tab.mu.RUnlock()
	return l.tab.dom.GetAttribute(id, k)
}

func (l *Locator) BoundingBox() ([4]int, error) {
	id, err := l.BackendID()
	if err != nil {
		return [4]int{}, err
	}
	l.tab.mu.RLock()
	defer l.tab.mu.RUnlock()
	return l.tab.dom.BoundingBox(id)
}

// WaitForCondition polls the DOM predicate until true or timeout. Polling is
// cheap because predicates are pure tree walks; interval scales with budget so
// a 1-core box isn't spammed. Returns last error text on timeout.
func (l *Locator) WaitForCondition(kind string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	interval := 25 * time.Millisecond
	for {
		ok, err := l.cond(kind)
		if err != nil && kind != "detached" && kind != "hidden" {
			// resolution errors just mean "not yet"
			ok = false
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: locator %q never became %s", ErrTimeout, l.sel, kind)
		}
		l.tab.settle(interval)
		if interval < 200*time.Millisecond {
			interval *= 2
		}
	}
}

func (l *Locator) cond(kind string) (bool, error) {
	l.tab.mu.RLock()
	defer l.tab.mu.RUnlock()
	if l.tab.dom == nil {
		return false, ErrNotFound
	}
	switch kind {
	case "attached":
		_, err := l.resolveOne()
		return err == nil, nil
	case "detached":
		_, err := l.resolveOne()
		return err != nil, nil
	case "visible":
		id, err := l.BackendID()
		if err != nil {
			return false, err
		}
		return l.tab.dom.IsVisible(id)
	case "hidden":
		v, err := l.cond("visible")
		if err != nil {
			return true, nil // gone => hidden
		}
		return !v, nil
	case "enabled":
		i, err := l.resolveOne()
		if err != nil {
			return false, err
		}
		return !i.Disabled, nil
	}
	return false, fmt.Errorf("unknown condition %q", kind)
}
