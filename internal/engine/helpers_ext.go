// helpers_ext.go — small engine-side additions that actions.go and the RPC
// layer depend on: WaitBackend, StorageStateDoc, resolveURL.

package engine

import (
	"net/url"
	"strings"
	"time"
)

// WaitBackend resolves sel via Playwright engines and waits for state before
// returning the backend node id (locator auto-wait used by all actions).
func (l *Locator) WaitBackend(state string, timeout time.Duration) (int64, error) {
	if err := l.WaitForCondition(state, timeout); err != nil {
		return 0, err
	}
	return l.BackendID()
}

// resolveURL turns an href (possibly relative) into an absolute URL string.
func resolveURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// StorageStateDoc returns the Playwright storage-state JSON document
// (cookies + origins) for this context, suitable for `state.save()`.
func (c *BrowserContext) StorageStateDoc() map[string]any {
	out := map[string]any{"cookies": []map[string]any{}, "origins": c.originsSnapshot()}
	for _, ck := range c.jar.All("") {
		ss := "Lax"
		switch ck.SameSite {
		case 1:
			ss = "Strict"
		case 3:
			ss = "None"
		}
		out["cookies"] = append(out["cookies"].([]map[string]any), map[string]any{
			"name": ck.Name, "value": ck.Value, "domain": ck.Domain, "path": ck.Path,
			"expires": float64(ck.Expires), "httpOnly": ck.HttpOnly,
			"secure": ck.Secure, "sameSite": ss,
		})
	}
	return out
}

// AriaSnapshotYAML returns the Playwright-style YAML accessibility tree of the
// page. Every node carries [ref=eN] — the stable backend node id — which agents
// can pass back to any action as selector "eN" (see ResolveEngine ref support).
func (t *Tab) AriaSnapshotYAML() (string, error) {
	t.mu.RLock()
	dom := t.dom
	t.mu.RUnlock()
	if dom == nil {
		return "", ErrNotFound
	}
	return dom.AriaSnapshotYAML()
}

// ByRef resolves a snapshot ref like "e42" or "backend://e42" to its Info.
func (t *Tab) ByRef(ref string) (*NodeInfo, error) {
	t.mu.RLock()
	dom := t.dom
	t.mu.RUnlock()
	if dom == nil {
		return nil, ErrNotFound
	}
	infos, err := dom.ResolveEngine(ref)
	if err != nil || len(infos) == 0 {
		return nil, ErrNotFound
	}
	return infoToNode(infos[0]), nil
}
