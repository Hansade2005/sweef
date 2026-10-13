package engine

import (
	"net/http"
	"sync"
	"time"
)

// client lazily builds the shared HTTP client: cookie jar + bounded transport.
// One client for the whole Engine keeps connection pooling cheap on 1-core VMs.
//
// Every transport dials through a counting wrapper (wire.go) and sits behind a
// meterRT, so socket totals and per-request attribution exist for every request.
func (e *Engine) client() *http.Client {
	dc := e.DefaultContext() // before e.mu: DefaultContext takes it too
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.httpClient == nil {
		e.httpClient = &http.Client{
			Timeout:   30 * time.Second,
			Transport: &meterRT{base: newTransport(&dc.data().wire, e.proxyFor(dc))},
			Jar:       newCookieJar(),
		}
	}
	return e.httpClient
}

var jarOnce sync.Once
var sharedJar http.CookieJar

func newCookieJar() http.CookieJar {
	jarOnce.Do(func() { sharedJar = makeSimpleJar() })
	return sharedJar
}

// clientFor returns the HTTP client for a browsing context. The default
// context shares the engine-wide client; every other context (private
// browsing, Playwright-style isolated contexts) gets a client with its own
// cookie jar and its own transport: its socket counters and its pooled
// connections belong to it alone, and neither the automatic cookie handling of
// net/http (redirect chains) nor a kept-alive connection can carry state
// across contexts.
func (e *Engine) clientFor(c *BrowserContext) *http.Client {
	base := e.client()
	if c == nil || c.name == "default" {
		return base
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: base.Timeout, Transport: &meterRT{base: newTransport(&c.data().wire, e.proxyFor(c))}, Jar: makeSimpleJar()}
	}
	return c.httpClient
}

// closeIdle drops the idle connections of the context's own client.
func (c *BrowserContext) closeIdle() {
	c.mu.Lock()
	hc := c.httpClient
	c.mu.Unlock()
	if hc != nil {
		if rt, ok := hc.Transport.(*meterRT); ok {
			if ci, ok := rt.base.(interface{ CloseIdleConnections() }); ok {
				ci.CloseIdleConnections()
			}
		}
	}
}

// proxyFor is the explicit proxy of a context: its own ContextOptions.ProxyURL, else the
// engine-wide Options.ProxyURL, else "" (HTTPS_PROXY/HTTP_PROXY/NO_PROXY apply).
func (e *Engine) proxyFor(c *BrowserContext) string {
	if c != nil && c.options.ProxyURL != "" {
		return c.options.ProxyURL
	}
	return e.opts.ProxyURL
}
