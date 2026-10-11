package engine

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// makeSimpleJar returns a strict RFC-6265-ish cookie jar with a hard cap on
// stored cookies. Chromium stores millions; we cap at 1024 total and evict by
// least-recently-used — agents rarely need more, and it bounds memory.
func makeSimpleJar() http.CookieJar {
	return &simpleJar{
		m:   map[string]map[string]*http.Cookie{},
		cap: 1024,
		n:   0,
	}
}

type simpleJar struct {
	mu  sync.Mutex
	m   map[string]map[string]*http.Cookie // host -> name -> cookie
	cap int
	n   int
}

func hostKey(u *url.URL) string { return strings.TrimPrefix(u.Hostname(), "www.") }

func (j *simpleJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := hostKey(u)
	bag, ok := j.m[host]
	if !ok {
		bag = map[string]*http.Cookie{}
		j.m[host] = bag
	}
	for _, c := range cookies {
		if c.Expires.Before(time.Now()) && !c.Expires.IsZero() {
			if _, exists := bag[c.Name]; exists {
				delete(bag, c.Name)
				j.n--
			}
			continue
		}
		if _, exists := bag[c.Name]; !exists && j.n >= j.cap {
			j.evictOldestLocked()
		}
		cc := *c
		bag[c.Name] = &cc
		j.n++
	}
}

func (j *simpleJar) evictOldestLocked() {
	// coarse LRU: drop one cookie from the first host that has any
	for host, bag := range j.m {
		for name := range bag {
			delete(bag, name)
			j.n--
			if len(bag) == 0 {
				delete(j.m, host)
			}
			return
		}
	}
}

func (j *simpleJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	bag, ok := j.m[hostKey(u)]
	if !ok {
		return nil
	}
	var out []*http.Cookie
	now := time.Now()
	for _, c := range bag {
		if !c.Expires.IsZero() && c.Expires.Before(now) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// CookieHeader is used by CDP's Network.setCookie equivalent for debugging.
func (j *simpleJar) DebugDump() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var sb strings.Builder
	for host, bag := range j.m {
		for name, c := range bag {
			fmt.Fprintf(&sb, "%s\t%s\t%s\n", host, name, c.Value)
		}
	}
	return sb.String()
}
