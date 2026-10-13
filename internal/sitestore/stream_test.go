package sitestore

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func heapAfterGC() uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// A cursor over a million records costs the memory of a cursor over three: the heap stays within a few
// megabytes of where it started while the whole store is walked, although the records add up to far more
// than that (the old log engine held the lot in memory).
func TestCursorOverAMillionRecordsDoesNotGrowTheHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("fills a million records")
	}
	dir := t.TempDir()
	m := NewManager(dir, 0)
	o := m.Origin("http://million")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
	const total = 1_000_000
	value := strings.Repeat("v", 80)
	t0 := time.Now()
	for base := 0; base < total; base += 25_000 {
		tx := begin(t, m, conn, ModeReadWrite, "s")
		for i := base; i < base+25_000; i++ {
			if _, err := m.Put(tx, "s", nk(float64(i)), value, false, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Commit(tx); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("filled %d records in %v", total, time.Since(t0))
	m.CloseConn(conn)
	m.Shutdown() // flush: the walk below reads tables, not the memtable

	m = NewManager(dir, 0)
	defer m.Shutdown()
	o = m.Origin("http://million")
	conn = open(t, o, "db", 0, sink, nil)
	tx := begin(t, m, conn, ModeReadOnly, "s")
	base := heapAfterGC()
	peak := base
	t0 = time.Now()
	q := CursorReq{Dir: DirNext}
	steps := 0
	for {
		r, err := m.Cursor(tx, "s", q)
		if err != nil {
			t.Fatal(err)
		}
		if r == nil {
			break
		}
		if want := nk(float64(steps)); r[0] != want {
			t.Fatalf("step %d returned key %v", steps, r[0])
		}
		steps++
		q.HasCur, q.CurKey = true, r[0].(string)
		if steps%100_000 == 0 {
			if h := heapAfterGC(); h > peak {
				peak = h
			}
		}
	}
	d := time.Since(t0)
	if steps != total {
		t.Fatalf("the cursor visited %d of %d records", steps, total)
	}
	t.Logf("walked %d records in %v (%.0f steps/s); heap %d KiB before, peak %d KiB", steps, d, float64(steps)/d.Seconds(), base>>10, peak>>10)
	if grew := int64(peak) - int64(base); grew > 40<<20 {
		t.Fatalf("heap grew by %d MiB while a cursor walked %d records", grew>>20, total)
	}
	// counting a range and reading a bounded number of keys stream too
	if n, _ := m.Query(tx, "s", "", QCount, Range{Lo: nk(1000), Hi: nk(900000)}, 0); n[0] != fmt.Sprint(900000-1000+1) {
		t.Fatalf("range count: %v", n)
	}
	keys, _ := m.Query(tx, "s", "", QKeys, Range{Lo: nk(500000)}, 10)
	if len(keys) != 10 || keys[0] != nk(500000) {
		t.Fatalf("bounded read: %v", keys)
	}
	if h := heapAfterGC(); int64(h)-int64(base) > 40<<20 {
		t.Fatalf("heap after the reads: +%d MiB", (int64(h)-int64(base))>>20)
	}
	m.Abort(tx)
}

func openFDs() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(ents)
}

// Opening and closing databases and stores over and over must not leave goroutines, file descriptors or
// memory behind (the ordered store runs several goroutines per open store).
func TestOpenCloseHundredTimesLeaksNothing(t *testing.T) {
	dir := t.TempDir()
	cycle := func(i int) {
		m := NewManagerOptions(dir, Options{IdleClose: time.Millisecond})
		o := m.Origin("http://leak")
		sink := &testSink{}
		conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
		tx := begin(t, m, conn, ModeReadWrite, "s")
		if _, err := m.Put(tx, "s", nk(float64(i)), fmt.Sprint("value", i), false, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.Commit(tx); err != nil {
			t.Fatal(err)
		}
		c, _ := o.Caches()
		_, _ = c.Open("c")
		_ = o.OPFS().Get([]string{"f"}, false, true)
		m.CloseConn(conn)
		m.Shutdown()
	}
	for i := 0; i < 3; i++ { // warm up: pools, lazily started runtime goroutines
		cycle(i)
	}
	time.Sleep(50 * time.Millisecond)
	g0, f0, h0 := runtime.NumGoroutine(), openFDs(), heapAfterGC()
	for i := 3; i < 103; i++ {
		cycle(i)
	}
	// goleveldb's pool janitor lingers up to a second after Close (it waits for a pooled memtable): settle first
	for i := 0; i < 300 && runtime.NumGoroutine() > g0+2; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	g1, f1, h1 := runtime.NumGoroutine(), openFDs(), heapAfterGC()
	if g1 > g0+2 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines: %d -> %d after 100 open/close cycles\n%s", g0, g1, buf[:runtime.Stack(buf, true)])
	}
	if f0 >= 0 && f1 > f0+3 {
		t.Fatalf("file descriptors: %d -> %d", f0, f1)
	}
	if int64(h1)-int64(h0) > 8<<20 {
		t.Fatalf("heap: %d KiB -> %d KiB", h0>>10, h1>>10)
	}
	// all 103 values are there
	m := NewManager(dir, 0)
	defer m.Shutdown()
	sink := &testSink{}
	conn := open(t, m.Origin("http://leak"), "db", 0, sink, nil)
	tx := begin(t, m, conn, ModeReadOnly, "s")
	if n, _ := m.Query(tx, "s", "", QCount, Range{}, 0); n[0] != "103" {
		t.Fatalf("records: %v", n)
	}
	m.Abort(tx)
}

// The idle timer closes a store nobody uses and the next access reopens it, with its data.
func TestIdleStoreClosesAndReopens(t *testing.T) {
	dir := t.TempDir()
	m := NewManagerOptions(dir, Options{IdleClose: 20 * time.Millisecond})
	defer m.Shutdown()
	o := m.Origin("http://idle")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
	tx := begin(t, m, conn, ModeReadWrite, "s")
	m.Put(tx, "s", nk(1), "kept", false, nil)
	m.Commit(tx)
	if !o.StoreOpen() {
		t.Fatal("a store with an open connection must be open")
	}
	m.CloseConn(conn)
	waitFor(t, func() bool { return !o.StoreOpen() })
	conn = open(t, o, "db", 0, sink, nil)
	tx = begin(t, m, conn, ModeReadOnly, "s")
	if v, _ := m.Query(tx, "s", "", QValues, Range{}, 0); fmt.Sprint(v) != "[kept]" {
		t.Fatalf("after the idle close: %v", v)
	}
	m.Abort(tx)
}

// Only a few origins keep their store open at once; the least recently released are closed first.
func TestOpenStoresAreCapped(t *testing.T) {
	m := NewManagerOptions(t.TempDir(), Options{MaxOpenStores: 3, IdleClose: time.Hour})
	defer m.Shutdown()
	var origins []*Origin
	for i := 0; i < 8; i++ {
		o := m.Origin(fmt.Sprintf("http://site%d", i))
		sink := &testSink{}
		conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
		m.CloseConn(conn)
		origins = append(origins, o)
	}
	n := 0
	for _, o := range origins {
		if o.StoreOpen() {
			n++
		}
	}
	if n > 3 {
		t.Fatalf("%d stores open, want at most 3", n)
	}
}
