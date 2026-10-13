package main

import (
	"sort"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Mode names. The three sweef-* modes use the engine's DataPolicy once it exists; the chromium-* modes are headless
// Chromium driven over CDP.
const (
	ModeSweefOff      = "sweef-off"
	ModeSweefStandard = "sweef-standard"
	ModeSweefStrict   = "sweef-strict"
	ModeChromium      = "chromium"
	ModeChromiumSD    = "chromium-savedata"
	ModeChromiumNoImg = "chromium-noimg" // optional context run, not part of the default set
)

var defaultModes = []string{ModeSweefOff, ModeSweefStandard, ModeSweefStrict, ModeChromium, ModeChromiumSD}

func isSweef(mode string) bool { return strings.HasPrefix(mode, "sweef-") }

// HostAgg is the per-host split of one visit, from the recording proxy (exact bytes).
type HostAgg struct {
	Host  string `json:"host"`
	Conns int    `json:"conns"`
	In    int64  `json:"in"`
	Out   int64  `json:"out"`
}

// Resource is one request as the browser reported it (CDP or engine network log).
type Resource struct {
	URL      string `json:"url"`
	Type     string `json:"type"`
	Status   int    `json:"status"`
	Proto    string `json:"proto,omitempty"`
	Encoded  int64  `json:"encoded"` // CDP transfer size (headers+body) for chromium; -1 when the engine does not report it
	FromCach bool   `json:"from_cache,omitempty"`
	Failed   string `json:"failed,omitempty"`
}

// Visit is one measured page visit (cold or warm).
type Visit struct {
	OK     bool   `json:"ok"`
	Fail   string `json:"fail,omitempty"`
	Status int    `json:"status"` // main document status after redirects

	// Independent measurement, from the recording proxy.
	WireIn  int64 `json:"wire_in"`
	WireOut int64 `json:"wire_out"`
	Conns   int   `json:"conns"`
	// The CONNECT exchange between browser and recorder, not included in wire_in/out. It is what a browser's own
	// socket counter sees on top of the tunnel when it talks to a proxy, so cross-checks add it back.
	HandshakeIn  int64 `json:"handshake_in,omitempty"`
	HandshakeOut int64 `json:"handshake_out,omitempty"`

	// Browser-internal traffic (Chromium only) that is kept out of the three numbers above and reported separately.
	InternalIn    int64    `json:"internal_in,omitempty"`
	InternalOut   int64    `json:"internal_out,omitempty"`
	InternalConns int      `json:"internal_conns,omitempty"`
	InternalHosts []string `json:"internal_hosts,omitempty"`

	// The browser's own view.
	Requests    int              `json:"requests"`     // requests started (including cache hits)
	CacheServed int              `json:"cache_served"` // of those, answered without network
	ReadyMs     float64          `json:"ready_ms"`
	BrowserIn   int64            `json:"browser_in"`  // sum of CDP encodedDataLength; -1 if not available
	BrowserOut  int64            `json:"browser_out"` // estimate from request headers (+post data); -1 if not available
	ByType      map[string]int64 `json:"by_type,omitempty"`
	ByTypeCount map[string]int   `json:"by_type_count,omitempty"`
	Protos      map[string]int   `json:"protos,omitempty"`
	DocBytes    int64            `json:"doc_bytes"`
	CDPHostIn   map[string]int64 `json:"cdp_host_in,omitempty"`   // CDP transfer bytes by host, to explain gaps against the proxy
	SaveDataReq int              `json:"save_data_req,omitempty"` // requests that carried Save-Data: on (chromium-savedata)

	Hosts     []HostAgg  `json:"hosts,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
	Notes     []string   `json:"notes,omitempty"`

	Engine *EngineCounters `json:"engine,omitempty"` // Sweef's own counters, when the engine API exists
}

// EngineCounters is what the engine reports about itself (DataStats). Deltas for this visit.
type EngineCounters struct {
	WireIn          int64          `json:"wire_in"`
	WireOut         int64          `json:"wire_out"`
	CacheHitBytes   int64          `json:"cache_hit_bytes"`
	AbortedBytes    int64          `json:"aborted_bytes"`
	Skipped         map[string]int `json:"skipped,omitempty"`
	SkippedEstimate int64          `json:"skipped_estimate"`    // an ESTIMATE, never a measurement
	SkippedKnown    int64          `json:"skipped_known_bytes"` // measured: size of blocked requests learned from the cache
	Requests        int64          `json:"requests"`            // exchanges the engine made
}

// Cell is one (site, mode, run): a cold visit and then a warm visit.
type Cell struct {
	Site     string `json:"site"`
	Mode     string `json:"mode"`
	Run      int    `json:"run"` // 1-based
	Attempts int    `json:"attempts"`
	Cold     Visit  `json:"cold"`
	Warm     *Visit `json:"warm,omitempty"`
}

func (c Cell) OK() bool { return c.Cold.OK && (c.Warm == nil || c.Warm.OK) }

// Baseline is an about:blank load with the same flags and idle window: shows what a browser sends with no page.
type Baseline struct {
	Mode        string   `json:"mode"`
	Run         int      `json:"run"`
	WireIn      int64    `json:"wire_in"`
	WireOut     int64    `json:"wire_out"`
	Conns       int      `json:"conns"`
	Hosts       []string `json:"hosts,omitempty"`
	InternalIn  int64    `json:"internal_in,omitempty"`
	InternalOut int64    `json:"internal_out,omitempty"`
}

// Env describes the machine and tools. Filled once per run.
type Env struct {
	Date          string            `json:"date"`
	GoVersion     string            `json:"go"`
	OS            string            `json:"os"`
	Kernel        string            `json:"kernel"`
	CPU           string            `json:"cpu"`
	Cores         int               `json:"cores"`
	MemMB         int               `json:"mem_mb"`
	Chromium      string            `json:"chromium"`
	ChromiumUA    string            `json:"chromium_ua"`
	SweefCommit   string            `json:"sweef_commit"`
	SweefDirty    bool              `json:"sweef_dirty"`
	PolicyAPI     bool              `json:"policy_api"`
	Network       string            `json:"network"`
	Upstream      string            `json:"upstream"`
	ChromiumFlags []string          `json:"chromium_flags"`
	SweefOpts     map[string]any    `json:"sweef_opts"`
	Extra         map[string]string `json:"extra,omitempty"`
}

// Results is the whole run, the JSON file that gets committed.
type Results struct {
	Schema      int               `json:"schema"`
	Label       string            `json:"label"`
	Started     string            `json:"started"`
	Finished    string            `json:"finished,omitempty"`
	Complete    bool              `json:"complete"`
	Env         Env               `json:"env"`
	SitesHash   string            `json:"sites_sha256"`
	Sites       []Site            `json:"sites"`
	Modes       []string          `json:"modes"`                   // modes that were actually executed
	ModesWanted []string          `json:"modes_wanted"`            // modes requested
	Skipped     map[string]string `json:"modes_skipped,omitempty"` // mode -> why it was not run
	Runs        int               `json:"runs"`
	IdleSec     float64           `json:"idle_sec"`
	Cells       []Cell            `json:"cells"`
	Baselines   []Baseline        `json:"baselines,omitempty"`
}

// Site is one entry of sites.txt.
type Site struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

func (s Site) Host() string {
	u := s.URL
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return u
}

// ShortName is host plus the first path element, enough to tell sites apart in a table.
func (s Site) ShortName() string {
	u := s.URL
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	u = strings.TrimPrefix(u, "www.")
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimSuffix(u, "/")
	if len(u) > 46 {
		u = u[:45] + "…"
	}
	return u
}

// regDomain returns the registrable domain of a host[:port] (falls back to the host).
func regDomain(hostport string) string {
	h := hostport
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(h); err == nil {
		return d
	}
	return h
}

// hostAggs folds connection records into per-host rows, biggest first.
func hostAggs(recs []ConnRecord) []HostAgg {
	m := map[string]*HostAgg{}
	for _, r := range recs {
		h := r.Host
		if i := strings.LastIndex(h, ":"); i >= 0 {
			h = h[:i]
		}
		a := m[h]
		if a == nil {
			a = &HostAgg{Host: h}
			m[h] = a
		}
		a.Conns++
		a.In += r.In
		a.Out += r.Out
	}
	out := make([]HostAgg, 0, len(m))
	for _, a := range m {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].In+out[i].Out > out[j].In+out[j].Out })
	return out
}
