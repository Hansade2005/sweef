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
