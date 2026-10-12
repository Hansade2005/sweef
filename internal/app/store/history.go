package store

import (
	"encoding/json"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	historyFile   = "history.jsonl"
	halfLife      = 30 * 24 * time.Hour
	typedBonus    = 2.0
	maxHistory    = 200000
	compactFactor = 2 // compact when log lines > factor * entries
)

// HistoryEntry is one visited URL.
type HistoryEntry struct {
	URL    string    `json:"url"`
	Title  string    `json:"title"`
	Visits int       `json:"visits"`
	Typed  int       `json:"typed"`
	Last   time.Time `json:"last"`
	Frec   float64   `json:"frecency"` // computed at query time
}

type hEntry struct {
	url, title string
	hay        string // lower-case "host/path title", what queries match
	host       string
	mask       [2]uint64 // bigram Bloom bits of hay (pre-filter: a match needs all query bigram bits)
	visits     int32
	typed      int32
	last       int64   // unix seconds
	score      float64 // decayed visit weight as of `last`
	key        float64 // log2(score) + last/halfLife: order-preserving under decay
}

// History is the visit log with a frecency-ordered in-memory index.
type History struct {
	b       Backend
	mu      sync.Mutex
	ents    []hEntry
	byURL   map[string]int32
	order   []int32 // indexes by key descending (lazily rebuilt)
	dirty   bool
	rebuilt time.Time
	lines   int
	now     func() time.Time
}

type hRec struct {
	U string `json:"u,omitempty"`
	T string `json:"t,omitempty"`
	V int64  `json:"v,omitempty"` // unix seconds of the visit
	Y bool   `json:"y,omitempty"` // typed
	D string `json:"d,omitempty"` // delete url
	X bool   `json:"x,omitempty"` // clear all
	// C marks a compacted state record: N visits, Z typed visits, S score.
	C bool    `json:"c,omitempty"`
	N int32   `json:"n,omitempty"`
	Z int32   `json:"z,omitempty"`
	S float64 `json:"s,omitempty"`
	// TT marks a title-only update.
	TT bool `json:"tt,omitempty"`
}

// OpenHistory loads the log.
func OpenHistory(b Backend) *History {
	h := &History{b: b, byURL: map[string]int32{}, now: time.Now}
	data, err := b.ReadFile(historyFile)
	if err == nil {
		forEachLine(data, func(line []byte) {
			var r hRec
			if json.Unmarshal(line, &r) != nil {
				return
			}
			h.lines++
			h.apply(r)
		})
	}
	h.dirty = true
	if h.lines > 1000 && h.lines > compactFactor*len(h.ents) {
		h.compact()
	}
	return h
}

func (h *History) apply(r hRec) {
	switch {
	case r.X:
		h.ents, h.byURL = h.ents[:0], map[string]int32{}
	case r.D != "":
		h.remove(r.D)
	case r.C:
		hay, host := hayOf(r.U, r.T)
		e := hEntry{url: r.U, title: r.T, hay: hay, host: host, mask: charMask(hay),
			visits: r.N, typed: r.Z, last: r.V, score: r.S}
		if e.score <= 0 {
			e.score = 1
		}
		e.key = keyOf(e.score, e.last)
		if i, ok := h.byURL[r.U]; ok {
			h.ents[i] = e
		} else {
			h.byURL[r.U] = int32(len(h.ents))
			h.ents = append(h.ents, e)
		}
	case r.TT:
		if i, ok := h.byURL[r.U]; ok {
			e := &h.ents[i]
			e.title = r.T
			e.hay, e.host = hayOf(e.url, r.T)
			e.mask = charMask(e.hay)
		}
	case r.U != "":
		h.visit(r.U, r.T, time.Unix(r.V, 0), r.Y)
	}
}

func (h *History) remove(u string) {
	i, ok := h.byURL[u]
	if !ok {
		return
	}
	last := int32(len(h.ents) - 1)
	if i != last {
		h.ents[i] = h.ents[last]
		h.byURL[h.ents[i].url] = i
	}
	h.ents = h.ents[:last]
	delete(h.byURL, u)
}

// charMask returns a 128-bit Bloom signature of the byte bigrams of s. A
// haystack can only contain a query if its signature covers the query's, so
// one AND per entry rejects nearly everything that cannot match, without
// touching the string. Single-byte queries have no bigrams and always pass.
func charMask(s string) [2]uint64 {
	var m [2]uint64
	for i := 0; i+1 < len(s); i++ {
		a, b := s[i], s[i+1]
		if a == ' ' || b == ' ' {
			continue
		}
		h := (uint32(a)*0x9E3779B1 ^ uint32(b)*0x85EBCA6B) >> 25 // 7 bits
		m[h>>6] |= 1 << (h & 63)
	}
	return m
}

func hayOf(u, title string) (hay, host string) {
	pu, err := url.Parse(u)
	if err == nil && pu.Host != "" {
		host = strings.TrimPrefix(strings.ToLower(pu.Hostname()), "www.")
		hay = host + strings.ToLower(pu.EscapedPath())
		if pu.RawQuery != "" {
			hay += "?" + strings.ToLower(pu.RawQuery)
		}
	} else {
		hay = strings.ToLower(u)
	}
	if title != "" {
		hay += " " + strings.ToLower(title)
	}
	return hay, host
}

func keyOf(score float64, last int64) float64 {
	return math.Log2(score) + float64(last)/halfLife.Seconds()
}

func (h *History) visit(u, title string, at time.Time, typed bool) {
	ts := at.Unix()
	w := 1.0
	if typed {
		w += typedBonus
	}
	if i, ok := h.byURL[u]; ok {
		e := &h.ents[i]
		if ts >= e.last {
			e.score = e.score*math.Exp2(-float64(ts-e.last)/halfLife.Seconds()) + w
			e.last = ts
		} else {
			e.score += w * math.Exp2(-float64(e.last-ts)/halfLife.Seconds())
		}
		e.visits++
		if typed {
			e.typed++
		}
		if title != "" && title != e.title {
			e.title = title
			e.hay, e.host = hayOf(u, title)
			e.mask = charMask(e.hay)
		}
		e.key = keyOf(e.score, e.last)
		return
	}
	hay, host := hayOf(u, title)
	e := hEntry{url: u, title: title, hay: hay, host: host, mask: charMask(hay), visits: 1, last: ts, score: w}
	if typed {
		e.typed = 1
	}
	e.key = keyOf(e.score, e.last)
	h.byURL[u] = int32(len(h.ents))
	h.ents = append(h.ents, e)
}

// Visit records a page visit. Only http(s)/file/sweef-internal-free URLs
// should be passed; callers filter internal pages.
func (h *History) Visit(u, title string, typed bool) {
	h.VisitAt(u, title, typed, h.now())
}

// VisitAt is Visit with an explicit time (tests, import).
func (h *History) VisitAt(u, title string, typed bool, at time.Time) {
	r := hRec{U: u, T: title, V: at.Unix(), Y: typed}
	b, _ := json.Marshal(r)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.visit(u, title, at, typed)
	h.dirty = true
	h.lines++
	_ = h.b.Append(historyFile, b)
	if len(h.ents) > maxHistory {
		h.trimLocked()
	}
}

// SetTitle updates the title of an existing entry without counting a visit.
func (h *History) SetTitle(u, title string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i, ok := h.byURL[u]
	if !ok || title == "" || h.ents[i].title == title {
		return
	}
	e := &h.ents[i]
	e.title = title
	e.hay, e.host = hayOf(u, title)
	e.mask = charMask(e.hay)
	b, _ := json.Marshal(hRec{U: u, T: title, TT: true})
	h.lines++
	_ = h.b.Append(historyFile, b)
	h.dirty = true
}

// Delete removes one URL.
func (h *History) Delete(u string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(u)
	b, _ := json.Marshal(hRec{D: u})
	h.lines++
	_ = h.b.Append(historyFile, b)
	h.dirty = true
}

// Clear wipes all history.
func (h *History) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ents, h.byURL = nil, map[string]int32{}
	h.order = nil
	h.lines = 0
	_ = h.b.Remove(historyFile)
	h.dirty = true
}

// ClearSince removes visits whose last visit is at or after t.
func (h *History) ClearSince(t time.Time) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var del []string
	for i := range h.ents {
		if h.ents[i].last >= t.Unix() {
			del = append(del, h.ents[i].url)
		}
	}
	for _, u := range del {
		h.remove(u)
	}
	h.compactLocked()
	h.dirty = true
	return len(del)
}

// Len is the number of distinct URLs.
func (h *History) Len() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.ents) }

func (h *History) trimLocked() {
	h.rebuildLocked()
	drop := h.order[maxHistory*9/10:]
	for _, i := range drop {
		h.ents[i].visits = -1
	}
	out := h.ents[:0]
	for _, e := range h.ents {
		if e.visits >= 0 {
			out = append(out, e)
		}
	}
	h.ents = out
	h.byURL = make(map[string]int32, len(out))
	for i := range h.ents {
		h.byURL[h.ents[i].url] = int32(i)
	}
	h.compactLocked()
	h.dirty = true
}

func (h *History) compact() { h.mu.Lock(); defer h.mu.Unlock(); h.compactLocked() }

// compactLocked rewrites the log as one record per entry. The rewrite is a
// snapshot-style atomic replace, so a crash keeps the old log.
func (h *History) compactLocked() {
	var buf []byte
	for i := range h.ents {
		e := &h.ents[i]
		b, _ := json.Marshal(hRec{U: e.url, T: e.title, V: e.last, C: true, N: e.visits, Z: e.typed, S: e.score})
		buf = append(append(buf, b...), '\n')
	}
	if h.b.WriteAtomic(historyFile, buf) == nil {
		h.lines = len(h.ents)
	}
}

func (h *History) rebuildLocked() {
	if cap(h.order) < len(h.ents) {
		h.order = make([]int32, len(h.ents), len(h.ents)+len(h.ents)/4+16)
	}
	h.order = h.order[:len(h.ents)]
	for i := range h.order {
		h.order[i] = int32(i)
	}
	ents := h.ents
	sort.Slice(h.order, func(a, b int) bool { return ents[h.order[a]].key > ents[h.order[b]].key })
	h.dirty = false
	h.rebuilt = time.Now()
}

func (h *History) frecency(e *hEntry, now int64) float64 {
	return e.score * math.Exp2(-float64(now-e.last)/halfLife.Seconds())
}

// Recent returns the most recently visited entries.
func (h *History) Recent(limit int) []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	idx := make([]int, len(h.ents))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return h.ents[idx[a]].last > h.ents[idx[b]].last })
	if limit > len(idx) {
		limit = len(idx)
	}
	out := make([]HistoryEntry, 0, limit)
	now := h.now().Unix()
	for _, i := range idx[:limit] {
		out = append(out, h.export(&h.ents[i], now))
	}
	return out
}

func (h *History) export(e *hEntry, now int64) HistoryEntry {
	return HistoryEntry{URL: e.url, Title: e.title, Visits: int(e.visits), Typed: int(e.typed),
		Last: time.Unix(e.last, 0), Frec: h.frecency(e, now)}
}

// Match quality tiers, best first.
const (
	mHostPrefix = iota
	mURLPrefix
	mWordPrefix
	mSubstring
)

// Search returns up to limit entries matching every whitespace-separated
// term of q, ranked by match quality then frecency. Terms match as a prefix
// of the host or URL, a prefix of a word in URL/title, or any substring.
func (h *History) Search(q string, limit int) []HistoryEntry {
	q = strings.ToLower(strings.TrimSpace(q))
	if limit <= 0 {
		limit = 8
	}
	if q == "" {
		return h.Top(limit)
	}
	terms := strings.Fields(q)
	var qmask [2]uint64
	for _, t := range terms {
		m := charMask(t)
		qmask[0] |= m[0]
		qmask[1] |= m[1]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dirty && (len(h.ents) < 5000 || h.order == nil || time.Since(h.rebuilt) > time.Second) {
		h.rebuildLocked()
	}
	order := h.order
	if len(order) > len(h.ents) { // entries removed since the last rebuild
		h.rebuildLocked()
		order = h.order
	}
	type cand struct {
		i    int32
		tier int8
	}
	want := limit * 6
	cands := make([]cand, 0, want)
	first := terms[0]
	for _, i := range order {
		if int(i) >= len(h.ents) {
			continue
		}
		e := &h.ents[i]
		if e.mask[0]&qmask[0] != qmask[0] || e.mask[1]&qmask[1] != qmask[1] {
			continue
		}
		tier, ok := matchTier(e, terms, first)
		if !ok {
			continue
		}
		cands = append(cands, cand{i, tier})
		if len(cands) >= want*4 {
			break
		}
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].tier != cands[b].tier {
			return cands[a].tier < cands[b].tier
		}
		return h.ents[cands[a].i].key > h.ents[cands[b].i].key
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	now := h.now().Unix()
	out := make([]HistoryEntry, len(cands))
	for k, c := range cands {
		out[k] = h.export(&h.ents[c.i], now)
	}
	return out
}

func matchTier(e *hEntry, terms []string, first string) (int8, bool) {
	hay := e.hay
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return 0, false
		}
	}
	switch {
	case strings.HasPrefix(e.host, first):
		return mHostPrefix, true
	case strings.HasPrefix(hay, first):
		return mURLPrefix, true
	}
	// word prefix: the term follows a separator
	if i := strings.Index(hay, first); i > 0 {
		for i >= 0 {
			if p := hay[i-1]; p == ' ' || p == '/' || p == '.' || p == '-' || p == '_' || p == '?' || p == '=' {
				return mWordPrefix, true
			}
			j := strings.Index(hay[i+1:], first)
			if j < 0 {
				break
			}
			i += 1 + j
		}
	}
	return mSubstring, true
}

// Top returns the highest-frecency entries.
func (h *History) Top(limit int) []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dirty {
		h.rebuildLocked()
	}
	now := h.now().Unix()
	var out []HistoryEntry
	for _, i := range h.order {
		out = append(out, h.export(&h.ents[i], now))
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Get returns one entry.
func (h *History) Get(u string) (HistoryEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i, ok := h.byURL[u]
	if !ok {
		return HistoryEntry{}, false
	}
	return h.export(&h.ents[i], h.now().Unix()), true
}

// TopSites returns up to n sites, one per host, best frecency first.
func (h *History) TopSites(n int) []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dirty {
		h.rebuildLocked()
	}
	now := h.now().Unix()
	seen := map[string]bool{}
	var out []HistoryEntry
	for _, i := range h.order {
		e := &h.ents[i]
		if e.host == "" || seen[e.host] {
			continue
		}
		pu, err := url.Parse(e.url)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") {
			continue
		}
		seen[e.host] = true
		out = append(out, h.export(e, now))
		if len(out) >= n {
			break
		}
	}
	return out
}
