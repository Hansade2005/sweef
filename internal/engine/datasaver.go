package engine

// datasaver.go: the Data Saver policy, the stats the UI shows and the
// per-context and per-tab state behind them. See docs/data-saver.md (the
// contract) and docs/data-saver-engine.md (what each switch really does).
//
// Honesty rules enforced here: bytes "used" come from socket counters
// (wire.go); "saved" is only reported from measurements (responses aborted
// because Content-Length exceeded a cap, cache hits and revalidations, and
// blocked requests whose size was known from the cache or a Content-Length);
// everything else is a count, plus one estimate that is always kept apart
// (SkippedEstimate).

import (
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

// SaverLevel is the coarse Data Saver setting.
type SaverLevel int

const (
	SaverOff SaverLevel = iota
	SaverStandard
	SaverStrict
)

func (l SaverLevel) String() string {
	switch l {
	case SaverStandard:
		return "standard"
	case SaverStrict:
		return "strict"
	}
	return "off"
}

// ParseSaverLevel maps "off" | "standard" | "strict" to a level (off for anything else).
func ParseSaverLevel(s string) SaverLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "standard":
		return SaverStandard
	case "strict":
		return SaverStrict
	}
	return SaverOff
}

// DataPolicy says what the engine may fetch and when. Level Off ignores every
// other field. PolicyFor gives the documented presets.
type DataPolicy struct {
	Level               SaverLevel
	ImageMaxBytes       int  // tap-to-load above this Content-Length (default 200<<10); 0 = no cap
	ScriptMaxBytes      int  // strict: total first-party script budget per page (default 300<<10); 0 = no budget
	LazyImages          bool // fetch only near the viewport
	BlockFonts          bool // strict: web fonts are not fetched, except icon fonts (webfonts.go)
	BlockMediaLoad      bool // no preload/autoplay; fetch on play only
	ClickToLoad3PFrames bool
	DropHints           bool // prefetch/preload/prerender/dns-prefetch
	SuspendBackground   bool // background tabs: no timers, no requests
	ExtendCache         bool
	SaveDataHeader      bool

	// Additive to the contract (strict mode needs them as separate switches so
	// that the agent preset can be "strict minus script limits").
	NoImages               bool // images only when tapped (placeholders keep their declared size)
	BlockThirdPartyScripts bool

	// Web font limits (webfonts.go). Zero means no limit / no restriction.
	FontMaxBytes   int  // one font file, transfer size (standard: 300 KB)
	FontTotalBytes int  // all fonts of a page, transfer size (standard: 600 KB); icon fonts are exempt
	FontWOFFOnly   bool // only WOFF2/WOFF sources are used (plain TrueType/OpenType files are skipped)
}

// Default caps.
const (
	DefaultImageMaxBytes  = 200 << 10
	DefaultScriptMaxBytes = 300 << 10
	DefaultFontMaxBytes   = 300 << 10
	DefaultFontTotalBytes = 600 << 10
)

// PolicyFor returns the preset of a level (the table in docs/data-saver.md).
func PolicyFor(level SaverLevel) DataPolicy {
	switch level {
	case SaverStandard, SaverStrict:
		p := DataPolicy{
			Level:               level,
			ImageMaxBytes:       DefaultImageMaxBytes,
			LazyImages:          true,
			BlockMediaLoad:      true,
			ClickToLoad3PFrames: true,
			DropHints:           true,
			SuspendBackground:   true,
			ExtendCache:         true,
			SaveDataHeader:      true,
			FontMaxBytes:        DefaultFontMaxBytes,
			FontTotalBytes:      DefaultFontTotalBytes,
			FontWOFFOnly:        true,
		}
		if level == SaverStrict {
			p.BlockFonts = true // icon fonts excepted
			p.NoImages = true
			p.BlockThirdPartyScripts = true
			p.ScriptMaxBytes = DefaultScriptMaxBytes
		}
		return p
	}
	return DataPolicy{Level: SaverOff}
}

// AgentPolicy is the loading mode for agent-driven loads: strict (agents read
// the accessibility snapshot and never need images, fonts or media) minus the
// script limits (scripts often render the content an agent is after).
func AgentPolicy() DataPolicy {
	p := PolicyFor(SaverStrict)
	p.BlockThirdPartyScripts = false
	p.ScriptMaxBytes = 0
	// agents drive tabs that are not on screen, so background suspension would stall them
	p.SuspendBackground = false
	return p
}

// effective drops everything when the level is off.
func (p DataPolicy) effective() DataPolicy {
	if p.Level == SaverOff {
		return DataPolicy{}
	}
	return p
}

// DataKind classifies a request for the stats.
type DataKind string

const (
	KindDocument DataKind = "document"
	KindScript   DataKind = "script"
	KindStyle    DataKind = "style"
	KindImage    DataKind = "image"
	KindFont     DataKind = "font"
	KindMedia    DataKind = "media"
	KindXHR      DataKind = "xhr"
	KindFrame    DataKind = "frame"
	KindOther    DataKind = "other"
)

// DataStats is what a context or a tab has used and saved. Keys of ByKind and
// ByOrigin hold approximate transfer bytes (see wire.go); WireIn/WireOut of a
// BrowserContext are exact socket totals, those of a Tab are the sums of its
// attributed exchanges (approximate).
type DataStats struct {
	WireIn, WireOut int64
	ByKind          map[DataKind]int64
	ByOrigin        map[string]int64 // "scheme://host[:port]"
	CacheHitBytes   int64            // transfer bytes not downloaded because of a cache hit or a 304 (measured)
	AbortedBytes    int64            // bytes not read because Content-Length exceeded a cap (measured)
	Skipped         map[DataKind]int // requests skipped by policy
	SkippedEstimate int64            // ESTIMATED bytes avoided: always flag as an estimate

	// Additive to the contract.
	SkippedKnownBytes int64 // measured: size of blocked requests learned from the cache
	Requests          int64 // exchanges made (redirect hops and revalidations count)
}

// MeasuredSaved is everything that was actually measured as not downloaded.
func (s DataStats) MeasuredSaved() int64 {
	return s.CacheHitBytes + s.AbortedBytes + s.SkippedKnownBytes
}

// Used is the exact socket total for a context (WireIn+WireOut).
func (s DataStats) Used() int64 { return s.WireIn + s.WireOut }

// ---- accumulators ---------------------------------------------------------------

const maxOrigins = 512

type statsAcc struct {
	mu                                          sync.Mutex
	in, out                                     int64
	requests                                    int64
	byKind                                      map[DataKind]int64
	byOrigin                                    map[string]int64
	cacheHit, aborted, skippedEst, skippedKnown int64
	skipped                                     map[DataKind]int
}

func (a *statsAcc) addTransfer(kind DataKind, origin string, out, in int64) {
	a.mu.Lock()
	a.in += in
	a.out += out
	a.requests++
	if a.byKind == nil {
		a.byKind = map[DataKind]int64{}
		a.byOrigin = map[string]int64{}
	}
	a.byKind[kind] += in + out
	if _, ok := a.byOrigin[origin]; !ok && len(a.byOrigin) >= maxOrigins {
		origin = "(other)"
	}
	a.byOrigin[origin] += in + out
	a.mu.Unlock()
}

func (a *statsAcc) addCacheHit(n int64) {
	a.mu.Lock()
	a.cacheHit += n
	a.mu.Unlock()
}

func (a *statsAcc) addAborted(n int64) {
	a.mu.Lock()
	a.aborted += n
	a.mu.Unlock()
}

func (a *statsAcc) addSkipped(kind DataKind, known, est int64) {
	a.mu.Lock()
	if a.skipped == nil {
		a.skipped = map[DataKind]int{}
	}
	a.skipped[kind]++
	a.skippedKnown += known
	a.skippedEst += est
	a.mu.Unlock()
}

func (a *statsAcc) snapshot() DataStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := DataStats{
		WireIn: a.in, WireOut: a.out, CacheHitBytes: a.cacheHit, AbortedBytes: a.aborted,
		SkippedEstimate: a.skippedEst, SkippedKnownBytes: a.skippedKnown, Requests: a.requests,
		ByKind: make(map[DataKind]int64, len(a.byKind)), ByOrigin: make(map[string]int64, len(a.byOrigin)),
		Skipped: make(map[DataKind]int, len(a.skipped)),
	}
	for k, v := range a.byKind {
		s.ByKind[k] = v
	}
	for k, v := range a.byOrigin {
		s.ByOrigin[k] = v
	}
	for k, v := range a.skipped {
		s.Skipped[k] = v
	}
	return s
}

func (a *statsAcc) reset() {
	a.mu.Lock()
	a.in, a.out, a.requests = 0, 0, 0
	a.byKind, a.byOrigin, a.skipped = nil, nil, nil
	a.cacheHit, a.aborted, a.skippedEst, a.skippedKnown = 0, 0, 0, 0
	a.mu.Unlock()
}

// ---- size estimates (labelled ESTIMATE everywhere) ----------------------------------

// defaultSizeEstimate is the transfer size assumed for a skipped request of
// a kind before this session has seen enough real ones. The figures are round
// numbers in the range of public HTTP Archive medians; they are guesses and
// the UI must say "estimated". Once five responses of a kind were measured in
// this context, their median replaces the default (see ctxData.estimate).
var defaultSizeEstimate = map[DataKind]int64{
	KindImage: 40 << 10, KindScript: 30 << 10, KindStyle: 15 << 10, KindFont: 25 << 10,
	KindMedia: 1 << 20, KindFrame: 30 << 10, KindXHR: 5 << 10, KindDocument: 30 << 10, KindOther: 5 << 10,
}

const sizeSamples = 64

// ---- per-context state ------------------------------------------------------------

type ctxData struct {
	pol   atomic.Pointer[DataPolicy]
	agent atomic.Bool
	wire  wireCounters
	acc   statsAcc

	smu   sync.Mutex
	sizes map[DataKind][]int64

	lastEmit atomic.Int64
	trail    atomic.Bool

	fonts atomic.Pointer[fontStore] // decoded web fonts (webfonts.go)
}

func (c *BrowserContext) data() *ctxData {
	if d := c.ds.Load(); d != nil {
		return d
	}
	d := &ctxData{}
	if c.eng != nil {
		d.wire.parent = &c.eng.wire
	}
	if c.ds.CompareAndSwap(nil, d) {
		return d
	}
	return c.ds.Load()
}

// SetDataPolicy sets the context's Data Saver policy (all tabs inherit it
// unless they have their own override). The zero policy / SaverOff blocks nothing.
func (c *BrowserContext) SetDataPolicy(p DataPolicy) {
	p2 := p
	c.data().pol.Store(&p2)
}

// DataPolicy returns the context's policy.
func (c *BrowserContext) DataPolicy() DataPolicy {
	if p := c.data().pol.Load(); p != nil {
		return *p
	}
	return DataPolicy{}
}

// SetAgentLoading puts every tab of the context in agent loading mode
// (AgentPolicy) until turned off. A tab's own override still wins.
func (c *BrowserContext) SetAgentLoading(on bool) { c.data().agent.Store(on) }

// DataStats returns what the context has used since it was created. WireIn
// and WireOut are exact socket totals; the rest is attribution (see DataStats).
func (c *BrowserContext) DataStats() DataStats {
	d := c.data()
	s := d.acc.snapshot()
	s.WireIn, s.WireOut = d.wire.in.Load(), d.wire.out.Load()
	return s
}

// ResetDataStats zeroes the context's attribution and cache/skip counters
// (the socket totals keep counting: they are the exact record).
func (c *BrowserContext) ResetDataStats() { c.data().acc.reset() }

// policy is the context-level policy for requests that belong to no tab.
func (c *BrowserContext) policy() DataPolicy {
	d := c.data()
	if p := d.pol.Load(); p != nil && p.Level != SaverOff {
		return *p
	}
	if d.agent.Load() {
		return AgentPolicy()
	}
	return DataPolicy{}
}

// estimate returns the estimated transfer size of a skipped request of kind.
func (d *ctxData) estimate(kind DataKind) int64 {
	d.smu.Lock()
	s := append([]int64(nil), d.sizes[kind]...)
	d.smu.Unlock()
	if len(s) >= 5 {
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[len(s)/2]
	}
	if v, ok := defaultSizeEstimate[kind]; ok {
		return v
	}
	return 5 << 10
}

// observe records the transfer size of a completed response (for estimate).
func (d *ctxData) observe(kind DataKind, n int64) {
	if n <= 0 {
		return
	}
	d.smu.Lock()
	if d.sizes == nil {
		d.sizes = map[DataKind][]int64{}
	}
	s := append(d.sizes[kind], n)
	if len(s) > sizeSamples {
		s = s[len(s)-sizeSamples:]
	}
	d.sizes[kind] = s
	d.smu.Unlock()
}

// ---- engine-level totals --------------------------------------------------------------

// WireTotals returns the socket totals of every context since the engine
// started (closed contexts included).
func (e *Engine) WireTotals() (in, out int64) { return e.wire.in.Load(), e.wire.out.Load() }

// ---- events ---------------------------------------------------------------------------

// EvData is published on the Bus (Event.Kind "data") when a tab's or a
// context's numbers change; throttled to 4 per second per tab. Event.Data is a DataEvent.
const EvData EventKind = "data"

// DataEvent is the payload of EvData events.
type DataEvent struct {
	Context string    // BrowserContext.ID
	Tab     DataStats // the tab's stats (zero value for context-level traffic)
	WireIn  int64     // exact socket totals of the context
	WireOut int64
}

const dataEventEvery = 250 * time.Millisecond

func (t *Tab) noteDataChanged() {
	if t.closed.Load() {
		return
	}
	now := time.Now().UnixNano()
	last := t.ds.lastEmit.Load()
	if now-last >= int64(dataEventEvery) && t.ds.lastEmit.CompareAndSwap(last, now) {
		t.publishData()
		return
	}
	if t.ds.trail.CompareAndSwap(false, true) {
		wait := dataEventEvery - time.Duration(now-last)
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		time.AfterFunc(wait, func() {
			t.ds.trail.Store(false)
			t.ds.lastEmit.Store(time.Now().UnixNano())
			t.publishData()
		})
	}
}

func (t *Tab) publishData() {
	if t.closed.Load() || t.eng.closed.Load() || !t.eng.bus.hasSubscribers() {
		return
	}
	c := t.Context()
	d := c.data()
	t.emit(EvData, DataEvent{Context: c.ID, Tab: t.ds.acc.snapshot(), WireIn: d.wire.in.Load(), WireOut: d.wire.out.Load()})
}

func (c *BrowserContext) noteDataChanged() {
	if !c.eng.bus.hasSubscribers() {
		return
	}
	d := c.data()
	now := time.Now().UnixNano()
	last := d.lastEmit.Load()
	if now-last >= int64(dataEventEvery) && d.lastEmit.CompareAndSwap(last, now) {
		c.eng.bus.Publish(Event{Kind: EvData, At: time.Now(), Data: DataEvent{Context: c.ID, WireIn: d.wire.in.Load(), WireOut: d.wire.out.Load()}})
		return
	}
	if d.trail.CompareAndSwap(false, true) {
		time.AfterFunc(dataEventEvery, func() {
			d.trail.Store(false)
			d.lastEmit.Store(time.Now().UnixNano())
			if !c.eng.closed.Load() {
				c.eng.bus.Publish(Event{Kind: EvData, At: time.Now(), Data: DataEvent{Context: c.ID, WireIn: d.wire.in.Load(), WireOut: d.wire.out.Load()}})
			}
		})
	}
}

// ---- per-tab state ---------------------------------------------------------------------

// tabDS is the Data Saver state of one tab. The zero value is ready to use.
type tabDS struct {
	acc statsAcc

	override atomic.Pointer[DataPolicy]
	agent    atomic.Bool
	bg       atomic.Bool
	fullDoc  atomic.Bool // "load fully" is in effect for the current document
	fullNext atomic.Bool // the next document load runs without policy (LoadFully)

	scrollY atomic.Int64
	env     atomic.Pointer[tabEnv]

	scriptSpent atomic.Int64 // first-party script transfer bytes this page
	epoch       atomic.Int64 // navigation counter (cache window entries do not outlive their navigation)

	lastEmit atomic.Int64
	trail    atomic.Bool

	mu       sync.Mutex
	skipped  []SkippedItem
	skipSeen map[string]bool
	stashed  map[string]string

	memo      map[string]*resMemo // sub-resources read in this navigation (fetchres.go)
	memoBytes int64
	flights   map[string]*flight // requests in flight, shared by concurrent consumers
}

// SkippedItem describes one resource the policy withheld on this page.
type SkippedItem struct {
	NodeID int64 // backend node id of the placeholder element (0 for resources without one)
	Kind   DataKind
	URL    string
	Bytes  int64  // size when known (Content-Length or cache), else -1
	Reason string // "strict", "too-large", "third-party", "script-budget", "frame", "background"
}

// SetDataPolicy sets a per-tab override of the context policy (nil = inherit).
// "Load fully, once" uses LoadFully; this is for the app's per-site rules.
func (t *Tab) SetDataPolicy(p *DataPolicy) {
	if p == nil {
		t.ds.override.Store(nil)
		return
	}
	p2 := *p
	t.ds.override.Store(&p2)
}

// SetAgentLoading puts this tab in agent loading mode (AgentPolicy): strict
// minus script limits. The agent bridge calls it per tab for agent-driven loads.
func (t *Tab) SetAgentLoading(on bool) { t.ds.agent.Store(on) }

// AgentLoading reports whether the tab is in agent loading mode.
func (t *Tab) AgentLoading() bool { return t.ds.agent.Load() }

// SetBackground tells the engine the tab is not visible. With a policy that
// suspends background tabs, page timers stop and page code (fetch, XHR,
// polling, media) makes no requests until the tab is shown again.
func (t *Tab) SetBackground(bg bool) {
	was := t.ds.bg.Swap(bg)
	if was != bg {
		t.jsCall("__swSetHidden", bg, false) // document.hidden / visibilitychange follow the tab
	}
	if was && !bg {
		t.lazyReeval() // visible again: images near the viewport may be due
		if env := t.ds.env.Load(); env != nil {
			env.fonts.resume() // and fonts refused while hidden
		}
	}
}

// IsBackground reports the last SetBackground value.
func (t *Tab) IsBackground() bool { return t.ds.bg.Load() }

// policy is the policy in force for this tab right now.
func (t *Tab) policy() DataPolicy {
	if t.ds.fullDoc.Load() {
		return DataPolicy{}
	}
	if p := t.ds.override.Load(); p != nil {
		return p.effective()
	}
	c := t.Context()
	d := c.data()
	if t.ds.agent.Load() {
		return AgentPolicy()
	}
	if p := d.pol.Load(); p != nil && p.Level != SaverOff {
		return *p
	}
	if d.agent.Load() {
		return AgentPolicy()
	}
	return DataPolicy{}
}

// suspended: a background tab under a policy that suspends background tabs.
func (t *Tab) suspended() bool {
	return t.ds.bg.Load() && t.policy().SuspendBackground
}

// DataStats returns this tab's numbers since its last navigation. WireIn and
// WireOut are the sums of the attributed exchanges (approximate); the exact
// socket totals live on the BrowserContext.
func (t *Tab) DataStats() DataStats { return t.ds.acc.snapshot() }

// Skipped lists what the policy withheld on the current page.
func (t *Tab) Skipped() []SkippedItem {
	t.ds.mu.Lock()
	defer t.ds.mu.Unlock()
	return append([]SkippedItem(nil), t.ds.skipped...)
}

// resetDataForNavigation starts a new stats epoch for the tab.
func (t *Tab) resetDataForNavigation() {
	t.ds.acc.reset()
	t.ds.scriptSpent.Store(0)
	t.ds.mu.Lock()
	t.ds.skipped = nil
	t.ds.skipSeen = nil
	t.ds.stashed = nil
	t.ds.memo, t.ds.memoBytes = nil, 0
	t.ds.mu.Unlock()
	if t.ds.fullNext.Swap(false) {
		t.ds.fullDoc.Store(true)
	} else {
		t.ds.fullDoc.Store(false)
	}
	t.ds.scrollY.Store(0)
	t.ds.epoch.Add(1)
}

// noteSkipped books a request the policy did not make. account says how its
// bytes are accounted: >= 0 the size is known (from the cache or a
// Content-Length) and counts as measured SkippedKnownBytes; -1 the size is
// unknown and the per-kind estimate is added to SkippedEstimate; -2 nothing
// (the bytes are already booked as AbortedBytes). itemBytes is the size shown
// for the SkippedItem (-1 unknown).
func (t *Tab) noteSkipped(kind DataKind, rawurl string, account, itemBytes int64, reason string, node int64) {
	t.ds.mu.Lock()
	key := string(kind) + " " + rawurl
	if t.ds.skipSeen[key] && rawurl != "" {
		t.ds.mu.Unlock() // the same resource asked for again (prefetch, then the loader)
		return
	}
	if t.ds.skipSeen == nil {
		t.ds.skipSeen = map[string]bool{}
	}
	t.ds.skipSeen[key] = true
	t.ds.mu.Unlock()
	d := t.Context().data()
	var est, known int64
	switch {
	case account == -1:
		est = d.estimate(kind)
	case account >= 0:
		known = account
	}
	t.ds.acc.addSkipped(kind, known, est)
	d.acc.addSkipped(kind, known, est)
	t.ds.mu.Lock()
	if len(t.ds.skipped) < 2000 {
		t.ds.skipped = append(t.ds.skipped, SkippedItem{NodeID: node, Kind: kind, URL: rawurl, Bytes: itemBytes, Reason: reason})
	}
	t.ds.mu.Unlock()
	t.noteDataChanged()
}

func (t *Tab) noteCacheHit(n int64) {
	if n <= 0 {
		return
	}
	t.ds.acc.addCacheHit(n)
	t.Context().data().acc.addCacheHit(n)
	t.noteDataChanged()
}

func (t *Tab) noteAborted(n int64) {
	if n <= 0 {
		return
	}
	t.ds.acc.addAborted(n)
	t.Context().data().acc.addAborted(n)
	t.noteDataChanged()
}

// ---- helpers ------------------------------------------------------------------------------

// siteOf returns the registrable domain of host (the host itself for IPs and
// names the public suffix list does not cover).
func siteOf(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || net.ParseIP(host) != nil {
		return host
	}
	if s, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return s
	}
	return host
}

// thirdParty reports whether ref's site differs from the page's site.
func thirdParty(pageURL, ref string) bool {
	pu, err1 := url.Parse(pageURL)
	ru, err2 := url.Parse(ref)
	if err1 != nil || err2 != nil || ru.Hostname() == "" {
		return false
	}
	return siteOf(pu.Hostname()) != siteOf(ru.Hostname())
}

// attrVal reads an attribute of a parsed node.
func attrVal(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

// liteMark is the attribute the layout engine draws placeholders for.
const liteMark = "data-sweef-lite"

// CountedTransport returns the RoundTripper the engine uses for the context
// (own cookie-free connection pool, socket counters, Content-Encoding decoding).
// Code outside the engine that makes requests for the person (favicons, downloads,
// update checks) can use it so that its traffic is part of WireIn/WireOut; requests
// made through any other client are not counted.
func (c *BrowserContext) CountedTransport() http.RoundTripper {
	return c.eng.clientFor(c).Transport
}
