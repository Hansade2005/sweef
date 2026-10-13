package engine

// sw.go: service worker registrations and their lifecycle.
//
// One swManager per BrowserContext (incognito and agent contexts get their own and
// keep everything in memory). It owns the registrations, the worker state machine
// (parsed -> installing -> installed -> activating -> activated -> redundant), the list of
// documents that are controlled by a registration (clients), and the persistence of
// activated workers under <storage>/<origin>/sw. The worker VMs themselves are in
// sw_runtime.go, the page-facing host functions in sw_host.go, request interception
// in sw_intercept.go.
//
// Locking: m.mu guards every field below. Nothing blocks while holding it: events are
// dispatched to a worker VM, and state changes are announced to documents, after it is
// released.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Timeouts of the lifecycle events; tests shorten them.
var (
	swInstallTimeout  = 30 * time.Second
	swActivateTimeout = 30 * time.Second
	swFetchTimeout    = 30 * time.Second
)

type swHolder struct {
	mu sync.Mutex
	p  atomic.Pointer[swManager]
}

type swManager struct {
	c  *BrowserContext
	mu sync.Mutex

	regs     map[string]*swReg    // origin|scope -> registration
	loaded   map[string]bool      // origins whose persisted registrations were read
	clients  map[string]*swClient // client id -> controlled or uncontrolled document
	subs     map[*jsVM]string     // documents following registration state -> origin
	byWorker map[int64]*swWorker  // every worker that is not redundant
	nextW    int64
	nextR    int64
	closed   bool
	quit     chan struct{}
	wg       sync.WaitGroup
	sys      map[string]*Tab // hidden tabs that fetch worker scripts, per origin
	ports    swPorts
}

// swReg is a registration.
type swReg struct {
	m                           *swManager
	id                          string
	origin, scope, scriptURL    string
	typ, uvc                    string
	installing, waiting, active *swWorker
	unregistered                bool
	retired                     []*swWorker // workers that became redundant since the last announcement
	rev                         int         // counts announced changes: documents ignore snapshots older than what they have
	jobMu                       sync.Mutex  // one update job at a time
	lastCheck                   time.Time
}

// swWorker is one version of a registration's script.
type swWorker struct {
	id          int64
	reg         *swReg
	scriptURL   string
	typ         string
	state       string
	source      string
	imports     map[string]string
	skipWaiting bool
	rtMu        sync.Mutex
	rt          *swRuntime
	rtErr       error
	installed   bool // the install event ran: importScripts may no longer fetch new scripts
}

// swClient is a document the worker can see through clients.
type swClient struct {
	id  string
	tab *Tab
	url string
	key string // the storage key of the document: its origin, plus the top-level site when it is embedded (partition.go)
	reg *swReg // the registration controlling it, nil when uncontrolled
}

func (c *BrowserContext) sw() *swManager {
	if m := c.site.swm.p.Load(); m != nil {
		return m
	}
	c.site.swm.mu.Lock()
	defer c.site.swm.mu.Unlock()
	if m := c.site.swm.p.Load(); m != nil {
		return m
	}
	m := &swManager{c: c, regs: map[string]*swReg{}, loaded: map[string]bool{}, clients: map[string]*swClient{},
		subs: map[*jsVM]string{}, byWorker: map[int64]*swWorker{}, quit: make(chan struct{}), ports: newSWPorts()}
	c.site.swm.p.Store(m)
	return m
}

// swIfStarted returns the context's manager without creating one.
func (c *BrowserContext) swIfStarted() *swManager { return c.site.swm.p.Load() }

// ---- information for the shell and tests -------------------------------------------------------------

// ServiceWorkerInfo describes one registration.
type ServiceWorkerInfo struct {
	Scope      string
	ScriptURL  string
	Installing string // state of the installing worker, "" when none
	Waiting    string
	Active     string
}

// ServiceWorkers lists the context's registrations.
func (c *BrowserContext) ServiceWorkers() []ServiceWorkerInfo {
	m := c.sw()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ServiceWorkerInfo
	for _, r := range m.regs {
		i := ServiceWorkerInfo{Scope: r.scope, ScriptURL: r.scriptURL}
		if r.installing != nil {
			i.Installing = r.installing.state
		}
		if r.waiting != nil {
			i.Waiting = r.waiting.state
		}
		if r.active != nil {
			i.Active = r.active.state
		}
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Scope < out[b].Scope })
	return out
}

// ServiceWorkerConsole returns the console log of a registration's active worker (nil when it has no running VM).
func (c *BrowserContext) ServiceWorkerConsole(scope string) []Event {
	m := c.sw()
	m.mu.Lock()
	var rt *swRuntime
	for _, r := range m.regs {
		if r.scope == scope && r.active != nil {
			r.active.rtMu.Lock()
			rt = r.active.rt
			r.active.rtMu.Unlock()
		}
	}
	m.mu.Unlock()
	if rt == nil {
		return nil
	}
	return rt.tab.ConsoleLog(0)
}

// ---- snapshots and announcements ------------------------------------------------------------------------

func (w *swWorker) info() map[string]any {
	return map[string]any{"id": w.id, "scriptURL": w.scriptURL, "state": w.state}
}

// snapshotAnnounce is snapshot plus the workers that turned redundant since the last announcement (so their
// objects in the documents see the state change); it clears that list.
func (r *swReg) snapshotAnnounce() map[string]any {
	r.rev++
	s := r.snapshot()
	if len(r.retired) > 0 {
		var red []any
		for _, w := range r.retired {
			red = append(red, w.info())
		}
		s["redundant"] = red
		r.retired = nil
	}
	return s
}

func (r *swReg) snapshot() map[string]any {
	s := map[string]any{"id": r.id, "scope": r.scope, "scriptURL": r.scriptURL, "updateViaCache": r.uvc, "rev": r.rev,
		"installing": nil, "waiting": nil, "active": nil}
	if r.installing != nil {
		s["installing"] = r.installing.info()
	}
	if r.waiting != nil {
		s["waiting"] = r.waiting.info()
	}
	if r.active != nil {
		s["active"] = r.active.info()
	}
	return s
}

// push delivers a JSON message to a document's event loop.
func pushSW(j *jsVM, msg map[string]any) {
	b, err := json.Marshal(msg)
	if err != nil || j == nil {
		return
	}
	j.rt().push(asyncEvent{kind: "sw", typ: "event", data: string(b)})
	if t := j.tab; t != nil {
		t.wake()
	}
}

// announce sends a registration snapshot to every document following that origin.
func (m *swManager) announce(origin string, snap map[string]any) {
	m.mu.Lock()
	var to []*jsVM
	for j, o := range m.subs {
		if o == origin {
			to = append(to, j)
		}
	}
	m.mu.Unlock()
	for _, j := range to {
		pushSW(j, map[string]any{"t": "reg", "reg": snap})
	}
}

// change applies fn to a registration under the lock and announces the result.
func (m *swManager) change(r *swReg, fn func()) {
	m.mu.Lock()
	fn()
	snap := r.snapshotAnnounce()
	m.mu.Unlock()
	m.announce(r.origin, snap)
}

// ---- lookup ---------------------------------------------------------------------------------------------------

func regKey(origin, scope string) string { return origin + "|" + scope }

// lookupLocked finds the registration with the longest scope that is a prefix of rawurl, among those of the
// storage key (a registration made by an embedded document is only visible to documents embedded the same way).
func (m *swManager) lookupLocked(key, rawurl string) *swReg {
	if siteOriginOf(rawurl) == "" || key == "" {
		return nil
	}
	origin := key
	u := rawurl
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	var best *swReg
	for _, r := range m.regs {
		if r.origin == origin && !r.unregistered && strings.HasPrefix(u, r.scope) && (best == nil || len(r.scope) > len(best.scope)) {
			best = r
		}
	}
	return best
}

// lookup is lookupLocked for callers that do not hold the lock; it loads the origin's persisted registrations first.
func (m *swManager) lookup(key, rawurl string) *swReg {
	if siteOriginOf(rawurl) == "" || key == "" {
		return nil
	}
	m.ensure(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lookupLocked(key, rawurl)
}

// ---- registration -----------------------------------------------------------------------------------------------

type swError struct{ name, msg string }

func (e *swError) Error() string { return e.name + ": " + e.msg }

func swErr(name, f string, a ...any) error { return &swError{name, fmt.Sprintf(f, a...)} }

var jsMIMEs = map[string]bool{"text/javascript": true, "application/javascript": true, "application/x-javascript": true,
	"text/ecmascript": true, "application/ecmascript": true, "text/jscript": true, "text/x-javascript": true,
	"application/x-ecmascript": true, "text/livescript": true, "text/javascript1.0": true, "text/javascript1.1": true,
	"text/javascript1.2": true, "text/javascript1.3": true, "text/javascript1.4": true, "text/javascript1.5": true,
	"text/jscript1.0": true}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1]
	}
	return "/"
}

// swPrecheck is the part of register() that needs no network: schemes, origins and the scope rules that do not depend
// on the script's Service-Worker-Allowed header. It returns the scope to use.
func swPrecheck(clientURL, scriptURL, scope string) (string, error) {
	if !potentiallyTrustworthy(clientURL) {
		return "", swErr("SecurityError", "Failed to register a ServiceWorker: The URL protocol of the current origin ('%s') is not supported.", siteOriginOf(clientURL))
	}
	su, err := url.Parse(scriptURL)
	if err != nil || (su.Scheme != "http" && su.Scheme != "https") {
		return "", swErr("TypeError", "Failed to register a ServiceWorker: The URL protocol of the script ('%s') is not supported.", scriptURL)
	}
	origin := siteOriginOf(scriptURL)
	if origin != siteOriginOf(clientURL) {
		return "", swErr("SecurityError", "Failed to register a ServiceWorker: The origin of the provided scriptURL ('%s') does not match the current origin ('%s').", origin, siteOriginOf(clientURL))
	}
	if strings.Contains(strings.ToLower(su.Path), "%2f") || strings.Contains(strings.ToLower(su.Path), "%5c") {
		return "", swErr("TypeError", "Failed to register a ServiceWorker: The script URL contains an escaped slash or backslash.")
	}
	if scope == "" {
		scope = su.Scheme + "://" + su.Host + dirOf(su.EscapedPath())
	}
	cu, err := url.Parse(scope)
	if err != nil || siteOriginOf(scope) != origin {
		return "", swErr("SecurityError", "Failed to register a ServiceWorker: The origin of the provided scope ('%s') does not match the current origin ('%s').", scope, origin)
	}
	cu.Fragment = ""
	return cu.String(), nil
}

// maxScope is the widest scope a script may register: its directory, or what Service-Worker-Allowed says.
func maxScopePath(scriptURL string, allowed string) string {
	su, _ := url.Parse(scriptURL)
	max := dirOf(su.EscapedPath())
	if allowed != "" {
		if a, err := su.Parse(allowed); err == nil && a.Host == su.Host && a.Scheme == su.Scheme {
			max = a.EscapedPath()
		}
	}
	return max
}

// fetchScript downloads a worker script (bypassing the HTTP cache) and checks its status and type.
func (m *swManager) fetchScript(key, scriptURL string) (string, map[string][]string, error) {
	t := m.sysTab(key)
	r, err := t.httpDo(httpOpts{method: "GET", url: scriptURL, kind: KindScript, headers: map[string]string{"Cache-Control": "max-age=0", "Service-Worker": "script"}})
	if err != nil {
		return "", nil, swErr("TypeError", "Failed to register a ServiceWorker: A bad HTTP response code or network error occurred while fetching the script ('%s'): %v", scriptURL, err)
	}
	if r.status < 200 || r.status >= 300 {
		return "", nil, swErr("TypeError", "Failed to register a ServiceWorker: A bad HTTP response code (%d) was received when fetching the script.", r.status)
	}
	ct := r.header.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	if !jsMIMEs[strings.ToLower(mt)] {
		return "", nil, swErr("SecurityError", "Failed to register a ServiceWorker: The script has an unsupported MIME type ('%s').", ct)
	}
	return string(r.body), r.header, nil
}

// register runs the Register and Update algorithms for navigator.serviceWorker.register().
func (m *swManager) register(storageKey, scriptURL, scope, typ, uvc string) (map[string]any, error) {
	origin := storageKey
	m.ensure(origin)
	key := regKey(origin, scope)
	m.mu.Lock()
	reg := m.regs[key]
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, swErr("InvalidStateError", "The browser is shutting down.")
	}
	if reg != nil {
		reg.jobMu.Lock()
		defer reg.jobMu.Unlock()
		m.mu.Lock()
		newest := reg.installing
		if newest == nil {
			newest = reg.waiting
		}
		if newest == nil {
			newest = reg.active
		}
		same := newest != nil && newest.scriptURL == scriptURL && newest.typ == typ && !reg.unregistered
		snap := reg.snapshot()
		m.mu.Unlock()
		if same { // registering what is already registered changes nothing
			return snap, nil
		}
	}
	src, hdr, err := m.fetchScript(origin, scriptURL)
	if err != nil {
		return nil, err
	}
	if max := maxScopePath(scriptURL, firstHeader(hdr, "Service-Worker-Allowed")); !strings.HasPrefix(scopePath(scope), max) {
		return nil, swErr("SecurityError", "Failed to register a ServiceWorker: The path of the provided scope ('%s') is not under the max scope allowed ('%s'). Adjust the scope, move the Service Worker script, or use the Service-Worker-Allowed HTTP header to allow the scope.",
			scopePath(scope), max)
	}
	m.mu.Lock()
	if reg == nil || reg.unregistered {
		m.nextR++
		reg = &swReg{m: m, id: fmt.Sprintf("r%d", m.nextR), origin: origin, scope: scope}
		m.regs[key] = reg
		reg.jobMu.Lock()
		defer reg.jobMu.Unlock()
	}
	reg.scriptURL, reg.typ, reg.uvc = scriptURL, typ, uvc
	w := m.newWorkerLocked(reg, scriptURL, typ, src)
	reg.lastCheck = time.Now()
	m.mu.Unlock()
	return m.startInstall(reg, w), nil
}

func scopePath(scope string) string {
	u, err := url.Parse(scope)
	if err != nil {
		return "/"
	}
	return u.EscapedPath()
}

func firstHeader(h map[string][]string, k string) string {
	for n, v := range h {
		if strings.EqualFold(n, k) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func (m *swManager) newWorkerLocked(reg *swReg, scriptURL, typ, src string) *swWorker {
	m.nextW++
	w := &swWorker{id: m.nextW, reg: reg, scriptURL: scriptURL, typ: typ, state: "parsed", source: src, imports: map[string]string{}}
	m.byWorker[w.id] = w
	return w
}

// update re-fetches the script of a registration and installs it when it changed.
func (m *swManager) update(reg *swReg) error {
	reg.jobMu.Lock()
	defer reg.jobMu.Unlock()
	m.mu.Lock()
	if reg.unregistered {
		m.mu.Unlock()
		return swErr("InvalidStateError", "Failed to update a ServiceWorkerRegistration: The registration is unregistered.")
	}
	newest := reg.installing
	if newest == nil {
		newest = reg.waiting
	}
	if newest == nil {
		newest = reg.active
	}
	scriptURL, typ := reg.scriptURL, reg.typ
	m.mu.Unlock()
	src, _, err := m.fetchScript(reg.origin, scriptURL)
	if err != nil {
		return err
	}
	m.mu.Lock()
	reg.lastCheck = time.Now()
	if newest != nil && newest.source == src {
		m.mu.Unlock()
		return nil
	}
	w := m.newWorkerLocked(reg, scriptURL, typ, src)
	m.mu.Unlock()
	m.startInstall(reg, w)
	return nil
}

// startInstall makes w the registration's installing worker and runs the rest of the Install algorithm in the
// background. It returns the registration's snapshot with the installing worker already in place, which is what
// register() resolves with.
func (m *swManager) startInstall(reg *swReg, w *swWorker) map[string]any {
	m.mu.Lock()
	if reg.installing != nil { // an installing worker that is replaced before it finishes becomes redundant
		m.retireLocked(reg.installing)
	}
	reg.installing = w
	w.state = "installing"
	announce := reg.snapshotAnnounce()
	snap := reg.snapshot()
	m.mu.Unlock()
	m.announce(reg.origin, announce)
	m.goJob(func() { m.install(reg, w) })
	return snap
}

func (m *swManager) goJob(f func()) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		f()
	}()
}

func (m *swManager) install(reg *swReg, w *swWorker) {
	rt, err := m.runtimeFor(w)
	if err == nil {
		_, err = rt.dispatch("install", nil, swInstallTimeout)
	}
	if err != nil {
		m.installFailed(reg, w, err)
		return
	}
	m.mu.Lock()
	superseded := reg.installing != w || reg.unregistered
	m.mu.Unlock()
	if superseded {
		return
	}
	m.change(reg, func() {
		w.installed = true
		reg.installing = nil
		if reg.waiting != nil {
			m.retireLocked(reg.waiting)
		}
		reg.waiting = w
		w.state = "installed"
	})
	m.tryActivate(reg)
}

func (m *swManager) installFailed(reg *swReg, w *swWorker, err error) {
	if err != nil && !errors.Is(err, errSWStopped) {
		if rt := w.runtime(); rt != nil {
			rt.tab.LogConsole("error", "service worker installation failed: "+err.Error())
		}
	}
	m.change(reg, func() {
		if reg.installing == w {
			reg.installing = nil
		}
		m.retireLocked(w)
	})
	m.dropIfEmpty(reg)
}

// retireLocked marks a worker redundant; its VM is stopped by the caller after the lock is released
// (stopping waits for the VM's goroutine).
func (m *swManager) retireLocked(w *swWorker) {
	if w.state == "redundant" {
		return
	}
	w.state = "redundant"
	delete(m.byWorker, w.id)
	w.reg.retired = append(w.reg.retired, w)
	if !m.closed {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			w.stopRuntime()
		}()
	}
}

// dropIfEmpty removes a registration that has no worker left (its first install failed).
func (m *swManager) dropIfEmpty(reg *swReg) {
	m.mu.Lock()
	if reg.installing != nil || reg.waiting != nil || reg.active != nil || reg.unregistered {
		m.mu.Unlock()
		return
	}
	reg.unregistered = true
	delete(m.regs, regKey(reg.origin, reg.scope))
	id := reg.id
	m.mu.Unlock()
	m.announceRemoved(reg, id)
}

func (m *swManager) announceRemoved(reg *swReg, id string) {
	m.mu.Lock()
	var to []*jsVM
	for j, o := range m.subs {
		if o == reg.origin {
			to = append(to, j)
		}
	}
	m.mu.Unlock()
	for _, j := range to {
		pushSW(j, map[string]any{"t": "removed", "id": id})
	}
}

// clientsUsingLocked counts the documents controlled by reg.
func (m *swManager) clientsUsingLocked(reg *swReg) int {
	n := 0
	for _, c := range m.clients {
		if c.reg == reg {
			n++
		}
	}
	return n
}

// tryActivate is the Try Activate algorithm: the waiting worker takes over when nothing needs the old one.
func (m *swManager) tryActivate(reg *swReg) {
	m.mu.Lock()
	w := reg.waiting
	if w == nil || reg.unregistered || m.closed {
		m.mu.Unlock()
		return
	}
	if reg.active != nil && !w.skipWaiting && m.clientsUsingLocked(reg) > 0 {
		m.mu.Unlock()
		return
	}
	old := reg.active
	reg.waiting = nil
	reg.active = w
	w.state = "activating"
	if old != nil {
		m.retireLocked(old)
	}
	snap := reg.snapshotAnnounce()
	var docs []*jsVM
	if old != nil { // the documents this registration controls get a new controller
		for _, c := range m.clients {
			if c.reg == reg {
				c.tab.mu.RLock()
				docs = append(docs, c.tab.js)
				c.tab.mu.RUnlock()
			}
		}
	}
	info := w.info()
	m.mu.Unlock()
	m.announce(reg.origin, snap)
	for _, d := range docs {
		pushSW(d, map[string]any{"t": "controller", "worker": info, "reg": snap})
	}
	m.goJob(func() { m.activate(reg, w) })
}

func (m *swManager) activate(reg *swReg, w *swWorker) {
	rt, err := m.runtimeFor(w)
	if err == nil {
		_, err = rt.dispatch("activate", nil, swActivateTimeout)
	}
	if err != nil && !errors.Is(err, errSWStopped) {
		if rt != nil {
			rt.tab.LogConsole("error", "service worker activation failed: "+err.Error())
		}
	}
	m.mu.Lock()
	if reg.active != w {
		m.mu.Unlock()
		return
	}
	w.state = "activated"
	m.mu.Unlock()
	m.persist(reg.origin) // durable before anyone is told it is activated
	m.mu.Lock()
	snap := reg.snapshotAnnounce()
	m.mu.Unlock()
	m.announce(reg.origin, snap)
}

// skipWaiting is self.skipWaiting().
func (m *swManager) skipWaiting(w *swWorker) {
	m.mu.Lock()
	w.skipWaiting = true
	reg := w.reg
	waiting := reg.waiting == w
	m.mu.Unlock()
	if waiting {
		m.tryActivate(reg)
	}
}

// unregister removes a registration. Documents it controls keep their worker until they go away.
func (m *swManager) unregister(reg *swReg) bool {
	m.mu.Lock()
	if reg.unregistered {
		m.mu.Unlock()
		return false
	}
	reg.unregistered = true
	delete(m.regs, regKey(reg.origin, reg.scope))
	if reg.installing != nil {
		m.retireLocked(reg.installing)
		reg.installing = nil
	}
	if reg.waiting != nil {
		m.retireLocked(reg.waiting)
		reg.waiting = nil
	}
	inUse := m.clientsUsingLocked(reg) > 0
	id, origin := reg.id, reg.origin
	if !inUse && reg.active != nil {
		m.retireLocked(reg.active)
	}
	snap := reg.snapshotAnnounce()
	m.mu.Unlock()
	m.announce(origin, snap)
	m.announceRemoved(reg, id)
	m.persist(origin)
	return true
}

// afterClientGone completes what waited for a document to go away: a waiting worker may activate and an
// unregistered registration with no document left disappears.
func (m *swManager) afterClientGone(reg *swReg) {
	if reg == nil {
		return
	}
	m.mu.Lock()
	inUse := m.clientsUsingLocked(reg) > 0
	hasWaiting := reg.waiting != nil
	var snap map[string]any
	if reg.unregistered && !inUse && reg.active != nil && reg.active.state != "redundant" {
		m.retireLocked(reg.active)
		snap = reg.snapshotAnnounce()
	}
	m.mu.Unlock()
	if snap != nil {
		m.announce(reg.origin, snap)
	}
	if !inUse && hasWaiting {
		m.tryActivate(reg)
	}
}

// swUnregisterAll removes the registrations of origin ("" = all) when site data is cleared.
func (c *BrowserContext) swUnregisterAll(origin string) {
	m := c.swIfStarted()
	if m == nil {
		// nothing running, but persisted registrations must go with the rest of the origin's data (ClearOrigin removes the files)
		return
	}
	m.mu.Lock()
	var regs []*swReg
	for _, r := range m.regs {
		if origin == "" || keyMatches(r.origin, origin) {
			regs = append(regs, r)
		}
	}
	m.mu.Unlock()
	for _, r := range regs {
		m.unregister(r)
	}
	m.mu.Lock()
	for _, r := range regs {
		for _, w := range []*swWorker{r.active} {
			if w != nil && w.state != "redundant" {
				m.retireLocked(w)
			}
		}
	}
	for o := range m.loaded {
		if origin == "" || o == origin {
			delete(m.loaded, o)
		}
	}
	m.mu.Unlock()
}

// swShutdown stops every worker when the context goes away.
func (c *BrowserContext) swShutdown() {
	m := c.swIfStarted()
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.quit)
	var ws []*swWorker
	for _, w := range m.byWorker {
		ws = append(ws, w)
	}
	for _, r := range m.regs {
		for _, w := range []*swWorker{r.installing, r.waiting, r.active} {
			if w != nil {
				ws = append(ws, w)
			}
		}
	}
	sys := m.sys
	m.sys = nil
	m.mu.Unlock()
	for _, w := range ws {
		w.stopRuntime()
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	for _, t := range sys {
		t.close()
	}
}

// sysTab is the hidden tab through which worker scripts are fetched (cookies, headers and the network log of the
// context apply, no service worker intercepts).
func (m *swManager) sysTab(origin string) *Tab {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.sys[origin]; t != nil {
		return t
	}
	t := newTab(m.c.eng, "sw-sys")
	t.ctx = m.c
	t.sw.noNav = true
	t.bc = partCtxFor(origin) // the script of a partitioned registration is fetched with the partition's cookies
	t.url, _ = url.Parse(keyOrigin(origin) + "/")
	if m.sys == nil {
		m.sys = map[string]*Tab{}
	}
	m.sys[origin] = t
	return t
}

// ---- persistence ------------------------------------------------------------------------------------------------------

type swPersisted struct {
	Scope     string            `json:"scope"`
	ScriptURL string            `json:"scriptURL"`
	Type      string            `json:"type"`
	UVC       string            `json:"updateViaCache"`
	Script    string            `json:"script"`
	Imports   map[string]string `json:"imports,omitempty"`
}

func swFileName(src string) string {
	h := sha256.Sum256([]byte(src))
	return hex.EncodeToString(h[:12]) + ".js"
}

func (m *swManager) swDir(origin string) string {
	return m.c.SiteStorage().Origin(origin).SWDir()
}

// persist writes the activated workers of an origin's registrations.
func (m *swManager) persist(origin string) {
	dir := m.swDir(origin)
	if dir == "" {
		return
	}
	m.mu.Lock()
	var list []swPersisted
	scripts := map[string]string{}
	for _, r := range m.regs {
		if r.origin != origin || r.unregistered || r.active == nil || r.active.state != "activated" {
			continue
		}
		w := r.active
		p := swPersisted{Scope: r.scope, ScriptURL: w.scriptURL, Type: w.typ, UVC: r.uvc, Script: swFileName(w.source), Imports: map[string]string{}}
		scripts[p.Script] = w.source
		for u, src := range w.imports {
			f := swFileName(src)
			p.Imports[u] = f
			scripts[f] = src
		}
		list = append(list, p)
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Scope < list[j].Scope })
	o := m.c.SiteStorage().Origin(origin)
	if len(list) == 0 {
		_ = os.RemoveAll(dir)
		o.SetOtherBytes(0)
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o700); err != nil {
		return
	}
	var total int64
	for f, src := range scripts {
		p := filepath.Join(dir, "scripts", f)
		if _, err := os.Stat(p); err != nil {
			tmp := p + ".tmp"
			if os.WriteFile(tmp, []byte(src), 0o600) == nil {
				_ = os.Rename(tmp, p)
			}
		}
		total += int64(len(src))
	}
	// scripts nobody refers to any more
	if ents, err := os.ReadDir(filepath.Join(dir, "scripts")); err == nil {
		for _, e := range ents {
			if _, ok := scripts[e.Name()]; !ok {
				_ = os.Remove(filepath.Join(dir, "scripts", e.Name()))
			}
		}
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := filepath.Join(dir, "regs.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, "regs.json"))
	}
	o.SetOtherBytes(total + int64(len(b)))
	o.Touch()
}

// ensure loads an origin's persisted registrations the first time it is needed. Their workers are
// activated already: the VM starts when something needs it.
func (m *swManager) ensure(origin string) {
	m.mu.Lock()
	if m.loaded[origin] {
		m.mu.Unlock()
		return
	}
	m.loaded[origin] = true
	m.mu.Unlock()
	dir := m.swDir(origin)
	if dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "regs.json"))
	if err != nil {
		return
	}
	var list []swPersisted
	if json.Unmarshal(b, &list) != nil {
		return
	}
	read := func(f string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(dir, "scripts", filepath.Base(f)))
		return string(b), err == nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range list {
		src, ok := read(p.Script)
		if !ok || siteOriginOf(p.Scope) != keyOrigin(origin) || m.regs[regKey(origin, p.Scope)] != nil {
			continue
		}
		m.nextR++
		reg := &swReg{m: m, id: fmt.Sprintf("r%d", m.nextR), origin: origin, scope: p.Scope, scriptURL: p.ScriptURL, typ: p.Type, uvc: p.UVC, lastCheck: time.Now()}
		w := m.newWorkerLocked(reg, p.ScriptURL, p.Type, src)
		w.state, w.installed = "activated", true
		for u, f := range p.Imports {
			if s, ok := read(f); ok {
				w.imports[u] = s
			}
		}
		reg.active = w
		m.regs[regKey(origin, p.Scope)] = reg
	}
}
