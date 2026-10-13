package engine

// pwa_clients.go: what a service worker may do to windows: clients.openWindow(), WindowClient.focus() and
// WindowClient.navigate(). The browser decides how a window is opened, focused or navigated (PWAOptions); the engine
// applies the specification's rules around it:
//
//   - openWindow() and focus() need the user's attention: they are allowed while a notification click is being
//     handled (and for ten seconds after it starts), or when the client being focused had a recent user gesture.
//     Otherwise they reject with InvalidAccessError, as in Chromium. This keeps a worker from opening tabs on its own.
//   - openWindow() resolves with the new WindowClient once its document has loaded, or null for a cross-origin URL.
//   - navigate() only accepts a same-origin URL and a client that this worker controls, and resolves with the
//     WindowClient of the document that loaded, or null if that document is cross-origin.

import (
	"net/url"
	"time"
)

const clientWait = 10 * time.Second

func (st *pwaState) tabState(t *Tab) (visible, focused bool) {
	if f := st.o.TabState; f != nil {
		return f(t)
	}
	return true, false
}

// waitClient waits until tab t has committed a document other than the one with client id oldID ("" = any) and
// returns its client entry.
func (st *pwaState) waitClient(t *Tab, oldID string) *swClient {
	deadline := time.Now().Add(clientWait)
	for time.Now().Before(deadline) {
		if c := t.sw.clientEntry(); c != nil && c.id != oldID {
			return c
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-st.quit:
			return nil
		}
	}
	return nil
}

func (h *pwaHost) clientFns(f map[string]hostFn) {
	f["pwaOpenWindow"] = func(a []any) (any, error) {
		token, raw := argI(a, 0), argS(a, 1)
		rt := h.worker()
		if rt == nil {
			return nil, &swError{"TypeError", "clients.openWindow is only available in a service worker"}
		}
		st := h.st()
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, &swError{"TypeError", "Failed to execute 'openWindow' on 'Clients': Invalid URL: " + raw}
		}
		key := regKey(rt.w.reg.origin, rt.w.reg.scope)
		if !st.openAllowed(key) {
			return nil, &swError{"InvalidAccessError", "Not allowed to open a window."}
		}
		if st.o.OpenWindow == nil {
			return nil, &swError{"NotAllowedError", "This browser cannot open windows for service workers."}
		}
		origin := rt.w.reg.origin
		st.goJob(func() {
			tab, err := st.o.OpenWindow(raw)
			if err != nil || tab == nil {
				h.reply(token, nil, &swError{"TypeError", "Failed to open a window"})
				return
			}
			if siteOriginOf(raw) != origin {
				h.reply(token, nil, nil) // a cross-origin window is opened but cannot be handed to the worker
				return
			}
			c := st.waitClient(tab, "")
			if c == nil {
				h.reply(token, nil, &swError{"TypeError", "The opened window did not load"})
				return
			}
			h.reply(token, st.focusedDesc(c, true), nil)
		})
		return nil, nil
	}
	f["pwaClientFocus"] = func(a []any) (any, error) {
		token, id := argI(a, 0), argS(a, 1)
		rt := h.worker()
		if rt == nil {
			return nil, &swError{"TypeError", "focus is only available in a service worker"}
		}
		st := h.st()
		m := st.c.sw()
		m.mu.Lock()
		c := m.clients[id]
		m.mu.Unlock()
		if c == nil || siteOriginOf(c.url) != rt.w.reg.origin {
			return nil, &swError{"InvalidStateError", "The client is gone."}
		}
		if !st.openAllowed(regKey(rt.w.reg.origin, rt.w.reg.scope)) && !c.tab.UserActive() {
			return nil, &swError{"InvalidAccessError", "Not allowed to focus a window."}
		}
		st.goJob(func() {
			if st.o.FocusTab != nil {
				st.o.FocusTab(c.tab)
			}
			h.reply(token, st.focusedDesc(c, true), nil)
		})
		return nil, nil
	}
	f["pwaClientNavigate"] = func(a []any) (any, error) {
		token, id, raw := argI(a, 0), argS(a, 1), argS(a, 2)
		rt := h.worker()
		if rt == nil {
			return nil, &swError{"TypeError", "navigate is only available in a service worker"}
		}
		st := h.st()
		m := st.c.sw()
		m.mu.Lock()
		c := m.clients[id]
		m.mu.Unlock()
		reg := rt.w.reg
		if c == nil || siteOriginOf(c.url) != reg.origin {
			return nil, &swError{"InvalidStateError", "The client is gone."}
		}
		if c.reg != reg {
			return nil, &swError{"TypeError", "Cannot navigate a client that this service worker does not control."}
		}
		if siteOriginOf(raw) != reg.origin {
			return nil, &swError{"TypeError", "Cannot navigate to a URL of another origin."}
		}
		tab, oldID := c.tab, c.id
		st.goJob(func() {
			var err error
			if st.o.NavigateTab != nil {
				err = st.o.NavigateTab(tab, raw)
			} else {
				err = tab.Navigate(raw, nil)
			}
			if err != nil {
				h.reply(token, nil, &swError{"TypeError", "Navigation failed: " + err.Error()})
				return
			}
			nc := st.waitClient(tab, oldID)
			if nc == nil || siteOriginOf(nc.url) != reg.origin {
				h.reply(token, nil, nil)
				return
			}
			h.reply(token, st.focusedDesc(nc, false), nil)
		})
		return nil, nil
	}
}

// focusedDesc is clientDesc, with the focus the caller just gave (the app confirms through TabState when it can).
func (st *pwaState) focusedDesc(c *swClient, focus bool) map[string]any {
	d := clientDesc(c)
	if focus {
		d["focused"], d["visibilityState"] = true, "visible"
	}
	return d
}
