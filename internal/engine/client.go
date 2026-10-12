package engine

import (
	"net/http"
	"sync"
	"time"
)

// client lazily builds the shared HTTP client: cookie jar + bounded transport.
// One client for the whole Engine keeps connection pooling cheap on 1-core VMs.
func (e *Engine) client() *http.Client {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.httpClient == nil {
		e.httpClient = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 4, // tiny boxes don't need Chromium's 256/host
				IdleConnTimeout:     60 * time.Second,
			},
			Jar: newCookieJar(),
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
// cookie jar on the same transport, so the automatic cookie handling of
// net/http (redirect chains) cannot carry cookies across contexts.
func (e *Engine) clientFor(c *BrowserContext) *http.Client {
	base := e.client()
	if c == nil || c.name == "default" {
		return base
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: base.Timeout, Transport: base.Transport, Jar: makeSimpleJar()}
	}
	return c.httpClient
}
