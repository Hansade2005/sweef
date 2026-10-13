package sitestore

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The quota tests run on a fake volume: a tiny disk whose free space is its size minus what the manager
// stores, and a fake clock, so budgets, caps and eviction order are exact and need no real disk.

const MiB = 1 << 20

type fakeVolume struct {
	size  int64
	other int64 // used by other programs
	m     atomic.Pointer[Manager]
}

func (v *fakeVolume) free(string) (uint64, error) {
	var used int64
	if m := v.m.Load(); m != nil {
		used = m.totalUsage()
	}
	f := v.size - v.other - used
	if f < 0 {
		f = 0
	}
	return uint64(f), nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) tick() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Minute)
	return c.t
}

func newQuotaManager(t *testing.T, root string, volume int64, extra func(*Options)) (*Manager, *fakeVolume, *fakeClock) {
	t.Helper()
	vol := &fakeVolume{size: volume}
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	opts := Options{FreeSpace: vol.free, Now: clk.now}
	if extra != nil {
		extra(&opts)
	}
	m := NewManagerOptions(root, opts)
	vol.m.Store(m)
	t.Cleanup(m.Shutdown)
	return m, vol, clk
}

// fill stores about n bytes in a database of the origin (values of 64 KiB, which are inline, and bigger ones as blobs).
func fill(t *testing.T, m *Manager, origin string, n int, sink Sink) (conn int64, err error) {
	t.Helper()
	o := m.Origin(origin)
	d, derr := o.DB("db")
	if derr != nil {
		t.Fatal(derr)
	}
	ts, _ := sink.(*testSink)
	if ts == nil {
		ts = &testSink{}
	}
	d.Open(1, ts, 0)
	msg := ts.take(t, "open-ready")
	conn = msg["conn"].(int64)
	if msg["upgrade"].(bool) {
		tx := msg["tx"].(int64)
		if err := m.CreateStore(tx, "s", "", false, true); err != nil {
			t.Fatal(err)
		}
		if err := m.Commit(tx); err != nil {
			t.Fatal(err)
		}
	}
	tx := begin(t, m, conn, ModeReadWrite, "s")
	chunk := strings.Repeat("q", 100<<10)
	for written := 0; written < n; written += len(chunk) {
		if _, perr := m.Put(tx, "s", "", chunk, false, nil); perr != nil {
			m.Abort(tx)
			return conn, perr
		}
	}
	err = m.Commit(tx)
	if err != nil {
		m.Abort(tx) // a refused commit leaves the transaction to the caller
	}
	return conn, err
}

func TestBudgetIsDerivedFromFreeSpaceWithAPerOriginCap(t *testing.T) {
	m, vol, _ := newQuotaManager(t, t.TempDir(), 100*MiB, nil)
	b := m.Budget()
	if !b.Derived || b.Global != 60*MiB || b.OriginCap != 12*MiB || b.Used != 0 {
		t.Fatalf("budget of an empty 100 MiB volume: %+v", b)
	}
	// other programs fill the disk: the budget follows the free space
	vol.other = 50 * MiB
	if b := m.Budget(); b.Global != 30*MiB || b.OriginCap != 6*MiB {
		t.Fatalf("budget with 50 MiB taken by others: %+v", b)
	}
	// storing data does not shrink the budget (free space plus our own use is what counts)
	vol.other = 0
	if _, err := fill(t, m, "http://a", 3*MiB, nil); err != nil {
		t.Fatal(err)
	}
	if b := m.Budget(); b.Global != 60*MiB || b.Used < 3*MiB {
		t.Fatalf("budget after storing 3 MiB: %+v", b)
	}
	// explicit options win
	m2, _, _ := newQuotaManager(t, t.TempDir(), 100*MiB, func(o *Options) { o.Budget = 7 * MiB; o.OriginQuota = 2 * MiB })
	if b := m2.Budget(); b.Global != 7*MiB || b.OriginCap != 2*MiB || b.Derived {
		t.Fatalf("explicit budget: %+v", b)
	}
	// the derived budget has a ceiling
	m3, _, _ := newQuotaManager(t, t.TempDir(), 100*MiB, func(o *Options) { o.BudgetCap = 10 * MiB })
	if b := m3.Budget(); b.Global != 10*MiB || b.OriginCap != 2*MiB {
		t.Fatalf("capped budget: %+v", b)
	}
}

func TestOneOriginCannotExceedItsCap(t *testing.T) {
	m, _, _ := newQuotaManager(t, t.TempDir(), 100*MiB, nil) // origin cap 12 MiB
	if _, err := fill(t, m, "http://hog", 8*MiB, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fill(t, m, "http://hog", 6*MiB, nil); !IsError(err, "QuotaExceededError") {
		t.Fatalf("a write past the origin's cap: %v", err)
	}
	// the refused write left the data as it was
	if u := m.Origin("http://hog").Usage().IndexedDB; u < 8*MiB || u > 9*MiB {
		t.Fatalf("usage after the refused write: %d", u)
	}
	// and other origins are unaffected
	if _, err := fill(t, m, "http://other", 8*MiB, nil); err != nil {
		t.Fatal(err)
	}
}

func TestEvictionRemovesLeastRecentlyUsedBestEffortOriginsFirst(t *testing.T) {
	// 10 MiB volume: budget 6 MiB, cap per origin 1.2 MiB
	m, _, clk := newQuotaManager(t, t.TempDir(), 10*MiB, nil)
	var evicted []string
	var emu sync.Mutex
	m.SetEvictHook(func(o string) { emu.Lock(); evicted = append(evicted, o); emu.Unlock() })
	sinks := map[string]*testSink{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		origin := "http://" + name
		sinks[name] = &testSink{}
		clk.tick()
		conn, err := fill(t, m, origin, 1*MiB, sinks[name])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = conn
	}
	// c asks for persistent storage: it is never evicted
	m.SetPersistPolicy(func(o string) bool { return o == "http://c" })
	if !m.Origin("http://c").RequestPersist() {
		t.Fatal("c was refused")
	}
	if m.Origin("http://b").RequestPersist() {
		t.Fatal("b must be refused by the policy")
	}
	// a is used again: it is no longer the least recently used; b is
	clk.tick()
	m.Origin("http://a").access()
	if got := m.EvictionOrder(); fmt.Sprint(got) != "[http://b http://d http://e http://a]" {
		t.Fatalf("eviction order: %v", got)
	}
	// five origins hold about 5.4 MiB of the 6 MiB budget; each further one needs room for 1.07 MiB
	for _, name := range []string{"f", "g", "h"} {
		clk.tick()
		if _, err := fill(t, m, "http://"+name, 1*MiB, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	waitFor(t, func() bool { emu.Lock(); defer emu.Unlock(); return len(evicted) >= 3 })
	emu.Lock()
	got := append([]string(nil), evicted...)
	emu.Unlock()
	if fmt.Sprint(got) != "[http://b http://d http://e]" {
		t.Fatalf("evicted: %v, want the least recently used best-effort origins in order [http://b http://d http://e]", got)
	}
	if u := m.Origin("http://b").Usage().Total; u != 0 {
		t.Fatalf("an evicted origin still has data: %d", u)
	}
	if u := m.Origin("http://c").Usage().Total; u < 1*MiB {
		t.Fatalf("the persistent origin lost data: %d", u)
	}
	if u := m.Origin("http://a").Usage().Total; u < 1*MiB {
		t.Fatalf("a recently used origin lost data: %d", u)
	}
	if m.Evicted() != 3 {
		t.Fatalf("evictions: %d", m.Evicted())
	}
	if b := m.Budget(); b.Used > b.Global {
		t.Fatalf("over budget after eviction: %+v", b)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestEvictionNotifiesOpenConnectionsAndFailsPendingRequests(t *testing.T) {
	m, _, clk := newQuotaManager(t, t.TempDir(), 10*MiB, nil)
	victim := &testSink{}
	clk.tick()
	conn, err := fill(t, m, "http://victim", 1*MiB, victim)
	if err != nil {
		t.Fatal(err)
	}
	// an upgrade request queued behind the open connection (it waits for the connection to close)
	d, _ := m.Origin("http://victim").DB("db")
	waiting := &testSink{}
	d.Open(77, waiting, 5)
	_ = victim.take(t, "versionchange")
	// a transaction in flight
	tx := begin(t, m, conn, ModeReadOnly, "s")
	_ = tx
	// the others fill the budget (6 MiB) until the victim has to go
	for i, name := range []string{"x1", "x2", "x3", "x4", "x5"} {
		clk.tick()
		if _, err := fill(t, m, "http://"+name, 1*MiB, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = i
	}
	closed := victim.take(t, "closed")
	if closed["conn"] != conn || closed["reason"] != "evicted" {
		t.Fatalf("the open connection must be told it was closed by the browser: %v", closed)
	}
	failed := waiting.take(t, "open-error")
	if failed["req"] != int64(77) || failed["name"] != "AbortError" {
		t.Fatalf("a request waiting to open must fail: %v", failed)
	}
	if err := m.Commit(tx); err == nil {
		t.Fatal("the transaction of an evicted connection must not commit")
	}
	if got := m.Origin("http://victim").Databases(); len(got) != 0 {
		t.Fatalf("databases of an evicted origin: %+v", got)
	}
}

func TestEvictedOriginCanStartAgain(t *testing.T) {
	m, _, clk := newQuotaManager(t, t.TempDir(), 10*MiB, nil)
	clk.tick()
	if _, err := fill(t, m, "http://old", 1*MiB, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p", "q", "r", "s", "t"} {
		clk.tick()
		if _, err := fill(t, m, "http://"+name, 1*MiB, nil); err != nil {
			t.Fatal(err)
		}
	}
	if m.Origin("http://old").Usage().Total != 0 {
		t.Fatal("old should have been evicted")
	}
	conn, err := fill(t, m, "http://old", 200<<10, nil)
	if err != nil {
		t.Fatalf("an evicted origin must be able to store again: %v", err)
	}
	_ = conn
	if m.Origin("http://old").Usage().IndexedDB < 200<<10 {
		t.Fatal("new data not accounted")
	}
}

func TestNothingToEvictMeansQuotaExceeded(t *testing.T) {
	m, _, clk := newQuotaManager(t, t.TempDir(), 10*MiB, nil)
	m.SetPersistPolicy(func(string) bool { return true })
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		clk.tick()
		if _, err := fill(t, m, "http://"+name, 1*MiB, nil); err != nil {
			t.Fatal(err)
		}
		if !m.Origin("http://" + name).RequestPersist() {
			t.Fatal("persist refused")
		}
	}
	clk.tick()
	if _, err := fill(t, m, "http://f", 300<<10, nil); err != nil {
		t.Fatalf("0.3 MiB still fits next to 5.4 MiB in 6 MiB: %v", err)
	}
	clk.tick()
	if _, err := fill(t, m, "http://g", 1*MiB, nil); !IsError(err, "QuotaExceededError") {
		t.Fatalf("with every other origin persistent, the browser budget is the limit: %v", err)
	}
}

func TestPersistSemantics(t *testing.T) {
	root := t.TempDir()
	m, _, _ := newQuotaManager(t, root, 100*MiB, nil)
	o := m.Origin("http://site")
	if o.Persisted() || o.RequestPersist() {
		t.Fatal("persistent storage must be refused without a policy")
	}
	var asked []string
	m.SetPersistPolicy(func(origin string) bool { asked = append(asked, origin); return origin == "http://site" })
	if !o.RequestPersist() || !o.Persisted() {
		t.Fatal("the policy granted it")
	}
	if !o.RequestPersist() || len(asked) != 1 {
		t.Fatalf("a persistent origin is not asked twice: %v", asked)
	}
	if m.Origin("http://nope").RequestPersist() {
		t.Fatal("the policy refused it")
	}
	// the grant survives a restart (it is in meta.json)
	m.Shutdown()
	m2 := NewManagerOptions(root, Options{})
	if !m2.Origin("http://site").Persisted() {
		t.Fatal("persisted flag lost across a restart")
	}
	// revoking works (the application's "clear" / "forget")
	m2.Origin("http://site").SetPersisted(false)
	if m2.Origin("http://site").Persisted() {
		t.Fatal("revoked")
	}
	// clearing the site's data also clears the flag: persistent storage is a property of the data
	m2.Origin("http://site").SetPersisted(true)
	m2.ClearOrigin("http://site")
	if m2.Origin("http://site").Persisted() {
		t.Fatal("persistence survived clearing the site")
	}
	// a memory-only manager can never grant it, whatever the policy says
	mem := NewManagerOptions("", Options{PersistPolicy: func(string) bool { return true }})
	if mem.Origin("http://site").RequestPersist() {
		t.Fatal("nothing survives a memory-only manager")
	}
}

func TestEstimateReportsRealUsageAndQuota(t *testing.T) {
	m, vol, _ := newQuotaManager(t, t.TempDir(), 100*MiB, nil) // budget 60, origin cap 12
	o := m.Origin("http://est")
	if o.Usage().Total != 0 || o.Quota() != 12*MiB {
		t.Fatalf("empty: usage %+v quota %d", o.Usage(), o.Quota())
	}
	if _, err := fill(t, m, "http://est", 2*MiB, nil); err != nil {
		t.Fatal(err)
	}
	u := o.Usage()
	if u.IndexedDB < 2*MiB || u.Total != u.IndexedDB {
		t.Fatalf("usage after 2 MiB: %+v", u)
	}
	if o.Quota() != 12*MiB {
		t.Fatalf("quota: %d", o.Quota())
	}
	// the disk is nearly full: the quota an origin can actually use shrinks to what is left
	vol.other = 100*MiB - 3*MiB - u.Total
	if q := o.Quota(); q > u.Total+4*MiB || q < u.Total {
		t.Fatalf("quota on a nearly full disk: %d (usage %d)", q, u.Total)
	}
	// service worker bytes count
	o.SetOtherBytes(1000)
	if o.Usage().Other != 1000 || o.Usage().Total != u.Total+1000 {
		t.Fatalf("other bytes: %+v", o.Usage())
	}
}

func TestSitesListsUsageForTheSettingsPage(t *testing.T) {
	m, _, clk := newQuotaManager(t, t.TempDir(), 100*MiB, nil)
	clk.tick()
	fill(t, m, "http://small", 300<<10, nil)
	clk.tick()
	fill(t, m, "http://big", 2*MiB, nil)
	m.SetPersistPolicy(func(string) bool { return true })
	m.Origin("http://small").RequestPersist()
	sites := m.Sites()
	if len(sites) != 2 || sites[0].Origin != "http://big" || sites[1].Origin != "http://small" || !sites[1].Persisted || sites[0].Persisted {
		t.Fatalf("sites: %+v", sites)
	}
	if sites[0].Usage.Total < 2*MiB || !sites[1].LastAccess.Before(sites[0].LastAccess) {
		t.Fatalf("usage or access times: %+v", sites)
	}
	m.ClearOrigin("http://big")
	if got := m.Sites(); len(got) != 1 || got[0].Origin != "http://small" {
		t.Fatalf("after clearing a site: %+v", got)
	}
}

func TestCacheAndOPFSCountAgainstTheSameQuota(t *testing.T) {
	m, _, _ := newQuotaManager(t, t.TempDir(), 100*MiB, func(o *Options) { o.OriginQuota = 1 * MiB })
	o := m.Origin("http://both")
	c, _ := o.Caches()
	if _, err := c.Open("v1"); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 300<<10)
	if err := c.Put("v1", CacheEntry{URL: "http://both/a", Method: "GET", Status: 200}, body); err != nil {
		t.Fatal(err)
	}
	fs := o.OPFS()
	if err := fs.Get([]string{"f.bin"}, false, true); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write([]string{"f.bin"}, make([]byte, 500<<10)); err != nil {
		t.Fatal(err)
	}
	u := o.Usage()
	if u.Caches < 300<<10 || u.OPFS < 500<<10 || u.Total < 800<<10 {
		t.Fatalf("usage: %+v", u)
	}
	// 1 MiB cap: a second cache body and a bigger file both fail with QuotaExceededError
	if err := c.Put("v1", CacheEntry{URL: "http://both/b", Method: "GET", Status: 200}, body); !IsError(err, "QuotaExceededError") {
		t.Fatalf("cache.put over the cap: %v", err)
	}
	if err := fs.Write([]string{"f.bin"}, make([]byte, 900<<10)); !IsError(err, "QuotaExceededError") {
		t.Fatalf("OPFS write over the cap: %v", err)
	}
	// replacing the file with something smaller frees the difference for the cache
	if err := fs.Write([]string{"f.bin"}, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("v1", CacheEntry{URL: "http://both/b", Method: "GET", Status: 200}, body); err != nil {
		t.Fatalf("room freed by the smaller file: %v", err)
	}
}

// Usage is not stored as a number of its own: it is recomputed from the records the data carries when a store
// opens. After a restart it must come out exactly as it was, whatever mix of creates, overwrites, clears and
// deletes got it there.
func TestUsageIsExactAfterARestart(t *testing.T) {
	root := t.TempDir()
	m, _, _ := newQuotaManager(t, root, 1000*MiB, nil)
	o := m.Origin("http://usage")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) {
		_ = m.CreateStore(tx, "a", "", false, false)
		_ = m.CreateStore(tx, "b", "", false, true)
		_ = m.CreateIndex(tx, "a", "i", `"x"`, false, false, nil)
	})
	tx := begin(t, m, conn, ModeReadWrite, "a", "b")
	for i := 0; i < 300; i++ {
		m.Put(tx, "a", nk(float64(i)), strings.Repeat("a", 100+i), false, idxKeys("i", nk(float64(i%7))))
		m.Put(tx, "b", "", strings.Repeat("b", 80<<10), false, nil) // blobs
	}
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	tx = begin(t, m, conn, ModeReadWrite, "a", "b")
	m.DeleteRange(tx, "a", Range{Lo: nk(10), Hi: nk(60)})
	m.Put(tx, "a", nk(5), "shorter", false, idxKeys("i", nk(1)))
	m.Clear(tx, "b")
	m.Put(tx, "b", "", "after clear", false, nil)
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	c, _ := o.Caches()
	c.Open("one")
	c.Open("two")
	for i := 0; i < 20; i++ {
		c.Put("one", CacheEntry{URL: fmt.Sprint("http://usage/", i), Method: "GET", Status: 200}, make([]byte, 1000*i))
	}
	c.Put("two", CacheEntry{URL: "http://usage/x", Method: "GET", Status: 200}, make([]byte, 100<<10))
	c.DeleteEntries("one", CacheQuery{URL: "http://usage/3", Method: "GET"})
	fs := o.OPFS()
	fs.Get([]string{"f"}, false, true)
	fs.Write([]string{"f"}, make([]byte, 123456))
	o.WaitGC()
	want := o.Usage()
	if want.IndexedDB == 0 || want.Caches == 0 || want.OPFS == 0 {
		t.Fatalf("expected all three kinds of use: %+v", want)
	}
	m.CloseConn(conn)
	m.Shutdown()

	m2, _, _ := newQuotaManager(t, root, 1000*MiB, nil)
	o2 := m2.Origin("http://usage")
	if _, err := o2.acquire(); err != nil { // opening the store recomputes the usage
		t.Fatal(err)
	}
	if got := o2.Usage(); got != want {
		t.Fatalf("usage after a restart %+v, before it %+v", got, want)
	}
	// deleting a whole cache and a whole database brings it back to nothing
	c2, _ := o2.Caches()
	c2.Delete("one")
	c2.Delete("two")
	fs2 := o2.OPFS()
	fs2.Remove([]string{"f"}, false)
	d, _ := o2.DB("db")
	d.Delete(1, sink)
	o2.WaitGC()
	if u := o2.Usage(); u.Total != 0 {
		t.Fatalf("usage after deleting everything: %+v", u)
	}
}
