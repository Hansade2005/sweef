package engine

// partition.go: state partitioning by top-level site (see docs/storage-partitioning.md).
//
// Every piece of per-origin state this engine keeps (cookies, localStorage, sessionStorage, IndexedDB, Cache
// Storage, the Origin Private File System, service worker registrations, BroadcastChannel / SharedWorker / Web Locks
// namespaces, the HTTP cache) is keyed by a storage key. In a first-party context the key is the plain origin, so
// nothing changes for the pages a person visits directly. In a third-party context (the document, or the worker
// that document started, is embedded under a different site) the key is the pair (origin, top-level site):
// the same embedded origin under site A and under site B sees two unrelated sets of state, which is what stops
// an embed from recognising a person across the sites that embed it.
//
// "Site" is the scheme plus the registrable domain (eTLD+1, public suffix list), as in Chromium and Firefox.

import (
	"net/url"
	"strings"
	"sync"
)

// browsingCtx describes a tab that is not a top-level document of its own: a nested browsing context (an
// <iframe>) or the hidden tab of a worker. It is immutable once the tab is in use. A top-level tab has none.
type browsingCtx struct {
	parent  *Tab // the embedding document (nil for a worker's hidden tab: see owner)
	top     *Tab // the top-level tab (nil for a worker: only topSite is kept, the page may navigate away)
	isFrame bool // a nested browsing context (as opposed to a worker)

	topSite  string // schemeful site of the top-level document
	crossAnc bool   // some ancestor is cross-site with this context's own site

	inherit string // origin an about:blank / about:srcdoc document inherits from its creator ("" = use the URL)
	opaque  bool   // sandboxed without allow-same-origin: the origin is opaque

	elem    int64  // backend node id of the <iframe> element in the parent's document
	name    string // browsing context name
	sandbox uint32 // sandbox flags (frames.go), 0 = not sandboxed
	allow   string // the allow attribute
}

// inheritContext gives t (the hidden tab of a worker) the partition of the tab that created it.
func (t *Tab) inheritContext(owner *Tab) {
	if owner == nil {
		return
	}
	bc := &browsingCtx{topSite: owner.topLevelSite(), crossAnc: owner.crossAncestor(), inherit: owner.docOrigin(), opaque: owner.docOrigin() == ""}
	t.bc = bc
}

func (t *Tab) browsing() *browsingCtx { return t.bc }

// docOrigin is the origin of the tab's document: scheme://host[:port] for http(s), "" for an opaque origin
// (data:, file:, about:blank without an inheriting creator, a sandboxed frame).
func (t *Tab) docOrigin() string {
	bc := t.bc
	if bc == nil {
		return siteOriginOf(t.URL())
	}
	if bc.opaque {
		return ""
	}
	u := t.URL()
	if o := siteOriginOf(u); o != "" {
		return o
	}
	if bc.inherit != "" && (u == "about:blank" || strings.HasPrefix(u, "about:srcdoc")) {
		return bc.inherit
	}
	return ""
}

// schemefulSite is scheme://registrable-domain of an http(s) URL or origin, "" for anything else.
func schemefulSite(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ""
	}
	return u.Scheme + "://" + siteOf(u.Hostname())
}

// topLevelSite is the schemeful site of the top-level document this tab is part of.
func (t *Tab) topLevelSite() string {
	if bc := t.bc; bc != nil && bc.topSite != "" {
		if bc.top != nil {
			if s := schemefulSite(bc.top.URL()); s != "" {
				return s
			}
		}
		return bc.topSite
	}
	return schemefulSite(t.URL())
}

func (t *Tab) crossAncestor() bool { return t.bc != nil && t.bc.crossAnc }

// thirdPartyContext reports whether the tab's own document is embedded under a different site than its own, or
// under a chain of ancestors that includes one.
func (t *Tab) thirdPartyContext() bool {
	if t.bc == nil {
		return false
	}
	own := schemefulSite(t.docOrigin())
	if own == "" {
		return true // an opaque origin never shares state with the top-level site
	}
	return own != t.topLevelSite() || t.bc.crossAnc
}

// storageKeyFor derives the storage key for an origin of this tab's context: the plain origin in a first-party
// context, "origin^top=<top-level site>" (plus "^xa" when an ancestor is cross-site) in a third-party one.
func (t *Tab) storageKeyFor(origin string) string {
	if origin == "" {
		return ""
	}
	return partitionedKey(origin, t.topLevelSite(), t.crossAncestor())
}

// partitionedKey is the double key for (origin, top-level site). It equals the plain origin when the origin's
// site is the top-level site and no ancestor is cross-site.
func partitionedKey(origin, topSite string, crossAnc bool) string {
	if topSite == "" || (!crossAnc && schemefulSite(origin) == topSite) {
		return origin
	}
	k := origin + "^top=" + topSite
	if crossAnc {
		k += "^xa"
	}
	return k
}

// splitStorageKey is the inverse of partitionedKey.
func splitStorageKey(key string) (origin, topSite string, partitioned bool) {
	i := strings.Index(key, "^top=")
	if i < 0 {
		return key, "", false
	}
	origin, rest := key[:i], key[i+len("^top="):]
	rest = strings.TrimSuffix(rest, "^xa")
	return origin, rest, true
}

// keyMatchesSite reports whether a storage key holds state of the given origin or site: the plain origin, or a
// partition in which origin is the embedded party or the top-level site (so clearing a site clears the state it
// holds as an embed and the partitions other sites hold of it).
func keyMatches(key, origin string) bool {
	if origin == "" || key == origin {
		return true
	}
	o, top, part := splitStorageKey(key)
	if !part {
		return false
	}
	return o == origin || top == origin || (schemefulSite(origin) != "" && (top == schemefulSite(origin)))
}

// ---- cookie policy -------------------------------------------------------------------------------

// CookieMode is what a third-party context may do with cookies.
type CookieMode int

const (
	// CookiesPartitioned (the default): a third-party context sees and stores cookies in a jar of its own for the
	// top-level site. Nothing a third party set while embedded under site A is sent while it is embedded under B,
	// and the cookies it holds as a first party are never sent to it as an embed.
	CookiesPartitioned CookieMode = iota
	// CookiesBlocked: a third-party context sends and stores only cookies that carry the Partitioned attribute.
	CookiesBlocked
	// CookiesAllowed: no partitioning (third-party cookies behave as they did before this feature). Opt-out for
	// compatibility; every other rule of the policy table stays in force.
	CookiesAllowed
)

// PartitionPolicy is the cookie policy of a browsing context.
type PartitionPolicy struct {
	Cookies CookieMode
	// Exception, when set, is asked for every third-party cookie decision with the top-level site and the site of
	// the request or document; returning true gives that pair unpartitioned cookie access (the first-party jar).
	// It runs on the request path and must be fast and safe for concurrent use.
	Exception func(topSite, embeddedSite string) bool
}

type partState struct {
	mu     sync.RWMutex
	pol    PartitionPolicy
	grants map[[2]string]bool // (topSite, embeddedSite) pairs that were granted unpartitioned cookie access
}

func (c *BrowserContext) partitions() *partState {
	if p := c.part.Load(); p != nil {
		return p
	}
	p := &partState{grants: map[[2]string]bool{}}
	if !c.part.CompareAndSwap(nil, p) {
		return c.part.Load()
	}
	return p
}

// SetPartitionPolicy replaces the context's cookie policy.
func (c *BrowserContext) SetPartitionPolicy(p PartitionPolicy) {
	ps := c.partitions()
	ps.mu.Lock()
	ps.pol = p
	ps.mu.Unlock()
}

// PartitionPolicy returns the context's cookie policy.
func (c *BrowserContext) PartitionPolicy() PartitionPolicy {
	ps := c.partitions()
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.pol
}

// GrantThirdPartyCookies lets embeddedSite use unpartitioned cookies while embedded under topSite (what a
// granted document.requestStorageAccess() records; also the hook for a per-site exception list in the UI).
// Sites are schemeful ("https://example.com").
func (c *BrowserContext) GrantThirdPartyCookies(topSite, embeddedSite string) {
	ps := c.partitions()
	ps.mu.Lock()
	ps.grants[[2]string{topSite, embeddedSite}] = true
	ps.mu.Unlock()
}

// RevokeThirdPartyCookies undoes GrantThirdPartyCookies.
func (c *BrowserContext) RevokeThirdPartyCookies(topSite, embeddedSite string) {
	ps := c.partitions()
	ps.mu.Lock()
	delete(ps.grants, [2]string{topSite, embeddedSite})
	ps.mu.Unlock()
}

// unpartitionedFor reports whether the pair has unpartitioned cookie access by grant, exception or mode.
func (c *BrowserContext) unpartitionedFor(topSite, embeddedSite string) bool {
	ps := c.partitions()
	ps.mu.RLock()
	pol, granted := ps.pol, ps.grants[[2]string{topSite, embeddedSite}]
	ps.mu.RUnlock()
	if pol.Cookies == CookiesAllowed || granted {
		return true
	}
	return pol.Exception != nil && pol.Exception(topSite, embeddedSite)
}
