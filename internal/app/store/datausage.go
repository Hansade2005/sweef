package store

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

const (
	dataUsageFile     = "datausage.json"
	dataKeepDays      = 400 // days of history kept
	dataOriginsPerDay = 30  // origins kept per day; the rest go to Other
	dataFiredKeep     = 8   // periods whose fired thresholds are remembered
)

// DayUsage is one local calendar day of data use.
//
//	Total   - measured bytes for the day (socket totals, normal windows)
//	Incog   - measured bytes used in private windows: counted for the budget,
//	          never attributed to an origin
//	SavedM  - bytes provably not downloaded (cache hits, aborted by a size cap)
//	SavedE  - ESTIMATED bytes avoided (median size per skipped resource kind);
//	          kept apart from SavedM so the UI never mixes the two
//	Origins - bytes by origin for the heaviest origins (approximate attribution)
type DayUsage struct {
	Total   int64            `json:"total"`
	Incog   int64            `json:"incog,omitempty"`
	SavedM  int64            `json:"savedM,omitempty"`
	SavedE  int64            `json:"savedE,omitempty"`
	Other   int64            `json:"other,omitempty"`
	Origins map[string]int64 `json:"origins,omitempty"`
}

// All is Total plus private-window bytes: what counts toward the budget.
func (d DayUsage) All() int64 { return d.Total + d.Incog }

type dataUsageDoc struct {
	V     int                  `json:"v"`
	Days  map[string]*DayUsage `json:"days"`
	Fired map[string]*firedSet `json:"fired,omitempty"`
}

// firedSet remembers which thresholds were announced in one period. Limit is
// the allowance they were computed against: raising the limit re-arms the
// thresholds that are no longer reached.
type firedSet struct {
	Limit  int64 `json:"limit"`
	Warn   []int `json:"warn,omitempty"`
	Exhaus bool  `json:"exhausted,omitempty"`
	At     int64 `json:"at"` // unix seconds of the last change (for pruning)
}

// DataUsage keeps the per-day usage totals. It is safe for concurrent use.
// Writes are batched (Debounce) and atomic, so a crash loses at most the last
// Debounce of counting and never corrupts the file.
type DataUsage struct {
	b     Backend
	clock func() time.Time

	mu    sync.Mutex
	doc   dataUsageDoc
	dirty bool
	timer *time.Timer

	// Debounce is the delay between a change and the write. Zero writes
	// immediately (tests). Set before use.
	Debounce time.Duration
}

// OpenDataUsage loads the usage file (a corrupt file starts empty). clock may
// be nil for time.Now.
func OpenDataUsage(b Backend, clock func() time.Time) *DataUsage {
	if clock == nil {
		clock = time.Now
	}
	u := &DataUsage{b: b, clock: clock, Debounce: 2 * time.Second}
	u.doc = dataUsageDoc{V: 1, Days: map[string]*DayUsage{}, Fired: map[string]*firedSet{}}
	if data, err := b.ReadFile(dataUsageFile); err == nil {
		var d dataUsageDoc
		if json.Unmarshal(data, &d) == nil {
			if d.Days != nil {
				u.doc.Days = d.Days
			}
			if d.Fired != nil {
				u.doc.Fired = d.Fired
			}
		}
	}
	return u
}

// SetClock replaces the clock (tests).
func (u *DataUsage) SetClock(c func() time.Time) {
	u.mu.Lock()
	u.clock = c
	u.mu.Unlock()
}

func (u *DataUsage) day(now time.Time) *DayUsage {
	k := DayKey(now)
	d := u.doc.Days[k]
	if d == nil {
		d = &DayUsage{}
		u.doc.Days[k] = d
		u.pruneLocked(now)
	}
	return d
}

func (u *DataUsage) pruneLocked(now time.Time) {
	if len(u.doc.Days) <= dataKeepDays {
		return
	}
	cut := DayKey(now.AddDate(0, 0, -dataKeepDays))
	for k := range u.doc.Days {
		if k < cut {
			delete(u.doc.Days, k)
		}
	}
}

// Add records measured bytes from a normal window. origins attributes part of
// them (approximate); savedM / savedE are bytes saved, measured / estimated.
func (u *DataUsage) Add(total int64, origins map[string]int64, savedM, savedE int64) {
	if total <= 0 && savedM <= 0 && savedE <= 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	d := u.day(u.clock())
	d.Total += max(total, 0)
	d.SavedM += max(savedM, 0)
	d.SavedE += max(savedE, 0)
	if len(origins) > 0 && d.Origins == nil {
		d.Origins = map[string]int64{}
	}
	for o, n := range origins {
		if o != "" && n > 0 {
			d.Origins[o] += n
		}
	}
	if len(d.Origins) > dataOriginsPerDay*2 {
		trimOrigins(d, dataOriginsPerDay)
	}
	u.touchLocked()
}

// AddIncognito records bytes used in a private window. They count toward the
// budget; no origin, URL or any other detail is stored.
func (u *DataUsage) AddIncognito(total int64) {
	if total <= 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.day(u.clock()).Incog += total
	u.touchLocked()
}

func trimOrigins(d *DayUsage, keep int) {
	type kv struct {
		k string
		v int64
	}
	all := make([]kv, 0, len(d.Origins))
	for k, v := range d.Origins {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	for _, e := range all[min(keep, len(all)):] {
		d.Other += e.v
		delete(d.Origins, e.k)
	}
}

// PeriodTotals is the sum of the days in [start, end).
type PeriodTotals struct {
	Used   int64 // counts toward the budget (normal + private)
	Normal int64
	Incog  int64
	SavedM int64
	SavedE int64
}

// Totals sums the days whose date is in [start, end).
func (u *DataUsage) Totals(start, end time.Time) PeriodTotals {
	u.mu.Lock()
	defer u.mu.Unlock()
	lo, hi := DayKey(start), DayKey(end)
	var t PeriodTotals
	for k, d := range u.doc.Days {
		if k >= lo && k < hi {
			t.Normal += d.Total
			t.Incog += d.Incog
			t.SavedM += d.SavedM
			t.SavedE += d.SavedE
		}
	}
	t.Used = t.Normal + t.Incog
	return t
}

// Today is the usage of the current local day.
func (u *DataUsage) Today() DayUsage {
	u.mu.Lock()
	defer u.mu.Unlock()
	if d := u.doc.Days[DayKey(u.clock())]; d != nil {
		c := *d
		c.Origins = nil
		return c
	}
	return DayUsage{}
}

// OriginBytes is one row of the top-sites list.
type OriginBytes struct {
	Origin string `json:"origin"`
	Bytes  int64  `json:"bytes"`
}

// Top returns the n heaviest origins in [start, end), heaviest first. Private
// window bytes never appear here.
func (u *DataUsage) Top(n int, start, end time.Time) []OriginBytes {
	u.mu.Lock()
	defer u.mu.Unlock()
	lo, hi := DayKey(start), DayKey(end)
	sum := map[string]int64{}
	for k, d := range u.doc.Days {
		if k >= lo && k < hi {
			for o, v := range d.Origins {
				sum[o] += v
			}
		}
	}
	out := make([]OriginBytes, 0, len(sum))
	for o, v := range sum {
		out = append(out, OriginBytes{o, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Origin < out[j].Origin
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Reset forgets all usage and fired thresholds ("Reset counter").
func (u *DataUsage) Reset() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.doc.Days = map[string]*DayUsage{}
	u.doc.Fired = map[string]*firedSet{}
	u.touchLocked()
}

// ThresholdEvent is something worth telling the person once.
type ThresholdEvent struct {
	Kind string // "warn" or "exhausted"
	Pct  int    // the warn threshold (100 for exhausted)
}

// Check compares st with what was already announced in this period and
// returns the thresholds newly reached, marking them so each fires once per
// period. Raising or lowering the limit mid-period re-baselines: thresholds
// that the new limit has not reached any more are armed again, those already
// reached are marked quietly.
func (u *DataUsage) Check(b Budget, st BudgetStatus) []ThresholdEvent {
	if !b.Enabled || st.LimitBytes <= 0 {
		return nil
	}
	b = b.Normalized()
	u.mu.Lock()
	defer u.mu.Unlock()
	f := u.doc.Fired[st.Key]
	if f == nil {
		f = &firedSet{Limit: st.LimitBytes}
		u.doc.Fired[st.Key] = f
	}
	if f.Limit != st.LimitBytes { // the limit changed: rebuild from the new pct, silently
		u.rebaselineLocked(f, b, st)
		return nil
	}
	var ev []ThresholdEvent
	for _, w := range b.WarnAt {
		if st.Pct >= float64(w) && !hasInt(f.Warn, w) {
			f.Warn = append(f.Warn, w)
			ev = append(ev, ThresholdEvent{"warn", w})
		}
	}
	if st.Exhausted && !f.Exhaus {
		f.Exhaus = true
		ev = append(ev, ThresholdEvent{"exhausted", 100})
	}
	if len(ev) > 0 {
		f.At = u.clock().Unix()
		u.pruneFiredLocked()
		u.touchLocked()
	}
	return ev
}

func (u *DataUsage) rebaselineLocked(f *firedSet, b Budget, st BudgetStatus) {
	f.Limit = st.LimitBytes
	f.Warn = nil
	for _, w := range b.WarnAt {
		if st.Pct >= float64(w) {
			f.Warn = append(f.Warn, w)
		}
	}
	f.Exhaus = st.Exhausted
	f.At = u.clock().Unix()
	u.touchLocked()
}

// Rebaseline marks every threshold the status has already reached as
// announced, without firing. The app calls it when the person changes the
// budget, so saving a new limit never answers with a toast.
func (u *DataUsage) Rebaseline(b Budget, st BudgetStatus) {
	if !b.Enabled || st.LimitBytes <= 0 {
		return
	}
	b = b.Normalized()
	u.mu.Lock()
	defer u.mu.Unlock()
	f := u.doc.Fired[st.Key]
	if f == nil {
		f = &firedSet{}
		u.doc.Fired[st.Key] = f
	}
	u.rebaselineLocked(f, b, st)
}

func hasInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (u *DataUsage) pruneFiredLocked() {
	if len(u.doc.Fired) <= dataFiredKeep {
		return
	}
	type kv struct {
		k string
		t int64
	}
	var all []kv
	for k, f := range u.doc.Fired {
		all = append(all, kv{k, f.At})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t > all[j].t })
	for _, e := range all[dataFiredKeep:] {
		delete(u.doc.Fired, e.k)
	}
}

// ---- persistence ------------------------------------------------------------------------

func (u *DataUsage) touchLocked() {
	u.dirty = true
	if !u.b.Persistent() {
		return // private profile: nothing to write
	}
	if u.Debounce <= 0 {
		u.saveLocked()
		return
	}
	if u.timer == nil {
		u.timer = time.AfterFunc(u.Debounce, func() {
			u.mu.Lock()
			u.timer = nil
			u.saveLocked()
			u.mu.Unlock()
		})
	}
}

func (u *DataUsage) saveLocked() {
	if !u.dirty {
		return
	}
	u.dirty = false
	b, err := json.Marshal(u.doc)
	if err != nil {
		return
	}
	_ = u.b.WriteAtomic(dataUsageFile, b)
}

// Flush writes pending changes now (called on exit).
func (u *DataUsage) Flush() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	if u.b.Persistent() {
		u.saveLocked()
	}
}
