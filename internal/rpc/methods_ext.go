// Extended RPC surface: locators, Playwright-style actions, waits, dialogs,
// emulation, storage state, contexts and bindings. Method names mirror CDP /
// Playwright verbs so Node & Python SDKs map 1:1.

package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
	fnet "github.com/sweefbrowser/sweef/internal/net"
)

func boolv(p Params, k string) bool { v, _ := p[k].(bool); return v }

func maps(p Params, k string) map[string]string {
	out := map[string]string{}
	if hm, ok := p[k].(map[string]any); ok {
		for key, v := range hm {
			if s, ok2 := v.(string); ok2 {
				out[key] = s
			}
		}
	}
	return out
}

func strs(p Params, k string) []string {
	var out []string
	if arr, ok := p[k].([]any); ok {
		for _, v := range arr {
			if s, ok2 := v.(string); ok2 {
				out = append(out, s)
			}
		}
	}
	return out
}

func (s *Server) locator(p Params) (*engine.Locator, error) {
	t, err := s.tab(p)
	if err != nil {
		return nil, err
	}
	l := t.Locator(str(p, "selector"))
	switch n := p["nth"].(type) {
	case float64:
		l = l.Nth(int(n))
	}
	if s2, ok := p["hasText"].(string); ok && s2 != "" {
		l = l.Filter(s2)
	}
	return l, nil
}

func nodeJSON(i *fnet.Info) map[string]any {
	return map[string]any{
		"nodeId": i.NodeID, "backendNodeId": i.BackendID, "name": i.Name,
		"id": i.ID, "type": i.Type, "role": i.Role, "text": i.Text,
		"href": i.Href, "visible": i.Visible, "disabled": i.Disabled,
		"checked": i.Checked, "focusable": i.Focusable,
	}
}

// dispatchExt handles every method not covered by the core switch. Wired in
// via init() into the ext table consulted by dispatch's default branch.
var extHandlers = map[string]func(*Server, Params) (any, error){}

func registerExt(method string, h func(*Server, Params) (any, error)) { extHandlers[method] = h }

func (s *Server) dispatchExt(method string, p Params) (any, error) {
	if h, ok := extHandlers[method]; ok {
		return h(s, p)
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

func init() {
	// ---- Locator resolution (Playwright engines: css/text=/role=/label=...) ----
	registerExt("DOM.resolveLocator", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		all, err := l.All()
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(all), "nodes": all}, nil
	})

	// ---- Actions ----
	act := func(name string, fn func(*engine.Tab, string, string) error) {
		registerExt(name, func(sv *Server, p Params) (any, error) {
			t, err := sv.tab(p)
			if err != nil {
				return nil, err
			}
			return map[string]bool{"ok": true}, fn(t, str(p, "selector"), str(p, "value"))
		})
	}
	act("Input.check", func(t *engine.Tab, sel, _ string) error { return t.Check(sel) })
	act("Input.uncheck", func(t *engine.Tab, sel, _ string) error { return t.Uncheck(sel) })
	act("Input.selectOption", (*engine.Tab).SelectOption)
	act("Input.hover", func(t *engine.Tab, sel, _ string) error { return t.Hover(sel) })
	act("Input.focus", func(t *engine.Tab, sel, _ string) error { return t.Focus(sel) })
	act("Input.tap", func(t *engine.Tab, sel, _ string) error { return t.Tap(sel) })

	registerExt("Input.type", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		d := time.Duration(num(p, "delayMs") * float64(time.Millisecond))
		return map[string]bool{"ok": true}, t.Type(str(p, "selector"), str(p, "text"), d)
	})

	registerExt("Input.mouse", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		x, y := int(num(p, "x")), int(num(p, "y"))
		switch strOr(p, "action", "click") {
		case "move":
			t.MouseMove(x, y)
		case "down":
			t.MouseDown(x, y)
		case "up":
			t.MouseUp(x, y)
		default:
			if err := t.MouseClick(x, y); err != nil {
				return nil, err
			}
		}
		return map[string]bool{"ok": true}, nil
	})

	registerExt("Input.drag", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.Drag(str(p, "source"), str(p, "target"))
	})

	registerExt("Input.scroll", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		if sel := str(p, "selector"); sel != "" {
			return map[string]bool{"ok": true}, t.ScrollIntoView(sel)
		}
		return map[string]bool{"ok": true}, t.MouseWheel(int(num(p, "deltaX")), int(num(p, "deltaY")))
	})

	registerExt("Input.keyboard", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.Keyboard(str(p, "keys"))
	})

	// ---- DOM getters ----
	registerExt("DOM.getOuterHTML", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		bid, err := l.BackendID()
		if err != nil {
			return nil, err
		}
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"outerHTML": t.OuterHTMLByBackend(bid)}, nil
	})

	registerExt("DOM.getAttributes", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		bid, err := l.BackendID()
		if err != nil {
			return nil, err
		}
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return t.AttributesByBackend(bid), nil
	})

	registerExt("Locator.textContent", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		tx, err := l.TextContent()
		return map[string]any{"text": tx}, err
	})
	registerExt("Locator.innerHTML", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		h, err := l.InnerHTML()
		return map[string]any{"innerHTML": h}, err
	})
	registerExt("Locator.getAttribute", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		v, ok, err := l.GetAttribute(str(p, "name"))
		return map[string]any{"value": v, "present": ok}, err
	})
	registerExt("Locator.isVisible", predicate(func(l *engine.Locator) (bool, error) { return l.IsVisible() }))
	registerExt("Locator.isEditable", predicate(func(l *engine.Locator) (bool, error) { return l.IsEnabled() }))
	registerExt("Locator.isEnabled", predicate(func(l *engine.Locator) (bool, error) { return l.IsEnabled() }))
	registerExt("Locator.isChecked", predicate(func(l *engine.Locator) (bool, error) { return l.IsChecked() }))
	registerExt("Locator.count", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		c, err := l.Count()
		return map[string]any{"count": c}, err
	})
	registerExt("Locator.boundingBox", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		b, err := l.BoundingBox()
		return map[string]any{"x": b[0], "y": b[1], "width": b[2], "height": b[3]}, err
	})

	// ---- Waits ----
	registerExt("Page.waitForSelector", func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			return nil, err
		}
		err = l.WaitForCondition(strOr(p, "state", "visible"), dur(p, "timeout", 30*time.Second))
		if err != nil {
			return nil, err
		}
		n, _ := l.All()
		return map[string]any{"count": len(n)}, nil
	})
	registerExt("Page.waitForFunction", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.WaitForFunction(str(p, "expression"), dur(p, "timeout", 30*time.Second))
	})
	registerExt("Page.waitForLoadState", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.WaitForLoadState(strOr(p, "state", "load"), dur(p, "timeout", 30*time.Second))
	})
	registerExt("Page.waitForURL", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.WaitForURL(str(p, "pattern"), dur(p, "timeout", 30*time.Second))
	})
	registerExt("Page.waitForTimeout", func(sv *Server, p Params) (any, error) {
		d := time.Second
		if ms, ok := p["ms"].(float64); ok && ms >= 0 {
			d = time.Duration(ms) * time.Millisecond
		} else if ms, ok := p["timeout"].(float64); ok && ms >= 0 {
			d = time.Duration(ms) * time.Millisecond
		}
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		// the page keeps running meanwhile (timers, animations, responses), as it would in a browser
		if t, err := sv.tab(p); err == nil {
			t.RunFor(d)
		} else {
			time.Sleep(d)
		}
		return map[string]bool{"ok": true}, nil
	})

	// ---- Dialogs ----
	registerExt("Page.armDialog", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.ArmDialogResponse(boolv(p, "accept"), str(p, "promptText"))
		return map[string]bool{"ok": true}, nil
	})
	registerExt("Page.getDialogs", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return t.DialogLog(int64(num(p, "since"))), nil
	})

	// ---- Emulation ----
	registerExt("Emulation.setDeviceMetrics", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.SetViewport(int(num(p, "width")), int(num(p, "height")), num(p, "deviceScaleFactor"),
			boolv(p, "mobile"), boolv(p, "landscape"))
		return map[string]bool{"ok": true}, nil
	})
	registerExt("Emulation.setUserAgentOverride", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.SetUserAgent(str(p, "userAgent"))
		return map[string]bool{"ok": true}, nil
	})
	registerExt("Emulation.setGeolocation", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.SetGeolocation(num(p, "latitude"), num(p, "longitude"))
		return map[string]bool{"ok": true}, nil
	})
	registerExt("Emulation.grantPermissions", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.GrantPermissions(strs(p, "permissions"))
		return map[string]any{"granted": t.PermissionsList()}, nil
	})
	registerExt("Emulation.setOffline", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.SetOffline(boolv(p, "offline"))
		return map[string]any{"offline": t.IsOfflineMode()}, nil
	})
	registerExt("Emulation.setColorScheme", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.SetColorScheme(str(p, "colorScheme"))
		return map[string]any{"colorScheme": t.ColorSchemeName()}, nil
	})
	registerExt("Network.setExtraHTTPHeaders", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.SetExtraHeaders(maps(p, "headers"))
		return map[string]bool{"ok": true}, nil
	})

	// ---- Init scripts & bindings ----
	registerExt("Page.addInitScript", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": t.AddInitScript(str(p, "source"))}, nil
	})
	registerExt("Page.removeInitScript", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.RemoveInitScript(str(p, "id"))
		return map[string]bool{"ok": true}, nil
	})
	registerExt("Runtime.addBinding", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		if r, ok := p["reply"]; ok {
			reply, err := replyJSON(r)
			if err != nil {
				return nil, err
			}
			t.SetBindingReply(str(p, "name"), reply)
		}
		if err := t.ExposeBinding(str(p, "name")); err != nil {
			return nil, err
		}
		return map[string]any{"bindings": t.Bindings()}, nil
	})

	// ---- Storage state (Playwright-compatible JSON) ----
	registerExt("Storage.getScripts", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"scripts": t.StorageSnapshot()}, nil
	})
	registerExt("BrowserContext.storageState", func(sv *Server, p Params) (any, error) {
		c := sv.eng.DefaultContext()
		if n := str(p, "context"); n != "" {
			if got, ok := sv.eng.LookupContext(n); ok {
				c = got
			}
		}
		return map[string]any{"state": c.StorageStateDoc()}, nil
	})
	registerExt("BrowserContext.saveStorageState", func(sv *Server, p Params) (any, error) {
		c := sv.eng.DefaultContext()
		if n := str(p, "context"); n != "" {
			if got, ok := sv.eng.LookupContext(n); ok {
				c = got
			}
		}
		return map[string]bool{"ok": true}, c.SaveState(str(p, "path"))
	})
	registerExt("BrowserContext.loadStorageState", func(sv *Server, p Params) (any, error) {
		c := sv.eng.DefaultContext()
		if n := str(p, "context"); n != "" {
			if got, ok := sv.eng.LookupContext(n); ok {
				c = got
			}
		}
		return map[string]bool{"ok": true}, c.LoadState(str(p, "path"))
	})
	registerExt("BrowserContext.addCookies", func(sv *Server, p Params) (any, error) {
		c := sv.eng.DefaultContext()
		var items []map[string]any
		if arr, ok := p["cookies"].([]any); ok {
			for _, v := range arr {
				if m, ok2 := v.(map[string]any); ok2 {
					items = append(items, m)
				}
			}
		}
		return map[string]bool{"ok": true}, c.AddCookies(items)
	})
	registerExt("BrowserContext.clearCookies", func(sv *Server, p Params) (any, error) {
		sv.eng.DefaultContext().ClearCookies(str(p, "name"))
		return map[string]bool{"ok": true}, nil
	})

	// ---- Contexts ----
	registerExt("Browser.newContext", func(sv *Server, p Params) (any, error) {
		opts := engine.ContextOptions{
			UserAgent:         str(p, "userAgent"),
			StoragePath:       str(p, "storagePath"),
			ProxyURL:          str(p, "proxy"),
			ViewportWidth:     int(num(p, "viewportWidth")),
			ViewportHeight:    int(num(p, "viewportHeight")),
			DeviceScaleFactor: num(p, "deviceScaleFactor"),
			Mobile:            boolv(p, "mobile"),
			Landscape:         boolv(p, "landscape"),
			ColorScheme:       str(p, "colorScheme"),
			Offline:           boolv(p, "offline"),
			ExtraHeaders:      maps(p, "extraHTTPHeaders"),
			Permissions:       strs(p, "permissions"),
		}
		if arr, ok := p["httpCredentials"].([]any); ok && len(arr) == 2 {
			opts.HTTPCredentials = &[2]string{fmt.Sprint(arr[0]), fmt.Sprint(arr[1])}
		}
		c := sv.eng.NewContext(strOr(p, "name", ""), opts)
		return map[string]any{"context": c.Name(), "id": c.ID}, nil
	})
	registerExt("Browser.listContexts", func(sv *Server, _ Params) (any, error) {
		return sv.eng.ListContexts(), nil
	})
	registerExt("Browser.closeContext", func(sv *Server, p Params) (any, error) {
		c, ok := sv.eng.LookupContext(str(p, "context"))
		if !ok {
			return nil, engine.ErrNotFound
		}
		return map[string]bool{"ok": true}, c.Close()
	})
	registerExt("Browser.newTabInContext", func(sv *Server, p Params) (any, error) {
		c, ok := sv.eng.LookupContext(str(p, "context"))
		if !ok {
			return nil, engine.ErrNotFound
		}
		t, err := c.NewTab(str(p, "url"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"tab": t.ID, "url": t.URL(), "title": t.Title()}, nil
	})

	// ---- APIRequestContext (context-level fetch, no tab needed) ----
	registerExt("Fetch.request", func(sv *Server, p Params) (any, error) {
		c := sv.eng.DefaultContext()
		if n := str(p, "context"); n != "" {
			if got, ok := sv.eng.LookupContext(n); ok {
				c = got
			}
		}
		method := strings.ToUpper(strOr(p, "method", http.MethodGet))
		code, hdrs, body, err := c.Fetch(method, str(p, "url"), maps(p, "headers"), str(p, "body"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": code, "headers": hdrs, "body": body}, nil
	})

	// ---- Network route with explicit unroute ----
	registerExt("Network.unroute", func(sv *Server, p Params) (any, error) {
		t, err := sv.tab(p)
		if err != nil {
			return nil, err
		}
		t.Unroute(str(p, "pattern"))
		t.RemoveRoutes(str(p, "pattern"))
		return map[string]bool{"ok": true}, nil
	})

	// ---- Events stream (bus subscribe over one-shot poll; long-poll style) ----
	registerExt("Events.poll", func(sv *Server, p Params) (any, error) {
		id, ch := sv.eng.Bus().Subscribe(int(num(p, "depth")))
		defer sv.eng.Bus().Unsubscribe(id)
		timeout := dur(p, "timeout", 5*time.Second)
		deadline := time.After(timeout)
		var evs []engine.Event
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return evs, nil
				}
				evs = append(evs, e)
				if len(evs) >= int(num(p, "max")) {
					return evs, nil
				}
			case <-deadline:
				return evs, nil
			}
		}
	})

	// keep json import used even if future handlers drop it
	_ = json.Marshal
}

func predicate(fn func(*engine.Locator) (bool, error)) func(*Server, Params) (any, error) {
	return func(sv *Server, p Params) (any, error) {
		l, err := sv.locator(p)
		if err != nil {
			// predicates tolerate "not found" as false per Playwright semantics
			return map[string]any{"value": false}, nil
		}
		v, err := fn(l)
		if err != nil {
			return map[string]any{"value": false}, nil
		}
		return map[string]any{"value": v}, nil
	}
}

func dur(p Params, k string, def time.Duration) time.Duration {
	if ms, ok := p[k].(float64); ok && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	// the CLI, the batch runner and the SDKs send "timeoutMs": reading only "timeout" made every wait run
	// for the 30 s default, past the caller's own deadline (the CLI then failed with a socket i/o timeout)
	if ms, ok := p[k+"Ms"].(float64); ok && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return def
}
