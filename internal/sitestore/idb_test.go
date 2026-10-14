package sitestore

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

type testSink struct {
	mu   sync.Mutex
	msgs []Msg
}

func (s *testSink) Notify(m Msg) { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }

func (s *testSink) take(t *testing.T, typ string) Msg {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.msgs {
		if m["t"] == typ {
			s.msgs = append(s.msgs[:i], s.msgs[i+1:]...)
			return m
		}
	}
	t.Fatalf("no %q message in %v", typ, s.msgs)
	return nil
}

func (s *testSink) has(typ string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.msgs {
		if m["t"] == typ {
			return true
		}
	}
	return false
}

func nk(f float64) string { return EncodeNumberKey(f) }

// open opens a database and runs fn inside the upgrade transaction when one is needed.
func open(t *testing.T, o *Origin, name string, version int64, sink *testSink, upgrade func(tx int64)) (conn int64) {
	t.Helper()
	d, err := o.DB(name)
	if err != nil {
		t.Fatal(err)
	}
	d.Open(1, sink, version)
	m := sink.take(t, "open-ready")
	conn = m["conn"].(int64)
	if m["upgrade"].(bool) {
		tx := m["tx"].(int64)
		if upgrade != nil {
			upgrade(tx)
		}
		if err := o.m.Commit(tx); err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

func begin(t *testing.T, m *Manager, conn int64, mode int, stores ...string) int64 {
	t.Helper()
	id, started, err := m.Begin(conn, stores, mode)
	if err != nil || !started {
		t.Fatalf("begin: started=%v err=%v", started, err)
	}
	return id
}

func TestPutGetOrderAndRange(t *testing.T) {
	m := newTestManager(t, "", 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 0, sink, func(tx int64) {
		if err := m.CreateStore(tx, "s", "", false, false); err != nil {
			t.Fatal(err)
		}
	})
	tx := begin(t, m, conn, ModeReadWrite, "s")
	for _, n := range []float64{5, -1, 3, 10, 0.5} {
		if _, err := m.Put(tx, "s", nk(n), fmt.Sprint("v", n), false, nil); err != nil {
			t.Fatal(err)
		}
	}
	keys, _ := m.Query(tx, "s", "", QKeys, Range{}, 0)
	var got []float64
	for _, k := range keys {
		f, _ := DecodeNumberKey(k.(string))
		got = append(got, f)
	}
	want := []float64{-1, 0.5, 3, 5, 10}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %v want %v", got, want)
	}
	vals, _ := m.Query(tx, "s", "", QValues, Range{Lo: nk(0.5), Hi: nk(5), LoOpen: true}, 0)
	if fmt.Sprint(vals) != "[v3 v5]" {
		t.Fatalf("range: %v", vals)
	}
	if n, _ := m.Query(tx, "s", "", QCount, Range{}, 0); n[0] != "5" {
		t.Fatalf("count %v", n)
	}
	if _, err := m.Put(tx, "s", nk(3), "x", true, nil); !IsError(err, "ConstraintError") {
		t.Fatalf("add over existing: %v", err)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
}

func TestAbortRollsBackEverything(t *testing.T) {
	m := newTestManager(t, "", 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 0, sink, func(tx int64) {
		_ = m.CreateStore(tx, "s", "", false, true)
	})
	tx := begin(t, m, conn, ModeReadWrite, "s")
	k1, _ := m.Put(tx, "s", "", "a", false, nil)
	if f, _ := DecodeNumberKey(k1); f != 1 {
		t.Fatalf("first generated key %v", f)
	}
	_ = m.Commit(tx)

	tx = begin(t, m, conn, ModeReadWrite, "s")
	m.Put(tx, "s", "", "b", false, nil)
	m.Put(tx, "s", nk(1), "overwritten", false, nil)
	_ = m.Clear(tx, "s")
	m.Put(tx, "s", nk(50), "big", false, nil)
	m.Abort(tx)

	tx = begin(t, m, conn, ModeReadOnly, "s")
	vals, _ := m.Query(tx, "s", "", QValues, Range{}, 0)
	if fmt.Sprint(vals) != "[a]" {
		t.Fatalf("after abort: %v", vals)
	}
	m.Abort(tx)
	// the generator went back with the abort
	tx = begin(t, m, conn, ModeReadWrite, "s")
	k, _ := m.Put(tx, "s", "", "c", false, nil)
	if f, _ := DecodeNumberKey(k); f != 2 {
		t.Fatalf("generator after abort: %v want 2", f)
	}
	// an explicit key above the generator moves it
	m.Put(tx, "s", nk(10.7), "d", false, nil)
	k, _ = m.Put(tx, "s", "", "e", false, nil)
	if f, _ := DecodeNumberKey(k); f != 11 {
		t.Fatalf("generator after explicit key: %v want 11", f)
	}
	_ = m.Commit(tx)
}

func idxKeys(name string, keys ...string) map[string][]string { return map[string][]string{name: keys} }

func TestIndexUniqueAndCursor(t *testing.T) {
	m := newTestManager(t, "", 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 0, sink, func(tx int64) {
		_ = m.CreateStore(tx, "s", "", false, false)
		if err := m.CreateIndex(tx, "s", "byName", `"name"`, true, false, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.CreateIndex(tx, "s", "byTag", `"tag"`, false, false, nil); err != nil {
			t.Fatal(err)
		}
	})
	tx := begin(t, m, conn, ModeReadWrite, "s")
	put := func(k float64, name, tag string) error {
		ik := map[string][]string{"byName": {"\x30" + name + "\x01"}, "byTag": {"\x30" + tag + "\x01"}}
		_, err := m.Put(tx, "s", nk(k), name, false, ik)
		return err
	}
	for i, e := range [][2]string{{"a", "x"}, {"b", "y"}, {"c", "x"}, {"d", "y"}, {"e", "x"}} {
		if err := put(float64(i+1), e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := put(9, "a", "z"); !IsError(err, "ConstraintError") {
		t.Fatalf("unique violation: %v", err)
	}
	// the failed put left the first record's index entries alone
	vals, _ := m.Query(tx, "s", "byName", QValues, Range{Lo: "\x30a\x01", Hi: "\x30a\x01"}, 0)
	if fmt.Sprint(vals) != "[a]" {
		t.Fatalf("by name: %v", vals)
	}
	// replacing record 1 with the same unique key is fine
	if err := put(1, "a", "q"); err != nil {
		t.Fatal(err)
	}
	walk := func(dir int, idx string, q CursorReq) string {
		var out []string
		q.Dir, q.Index = dir, idx
		for {
			r, err := m.Cursor(tx, "s", q)
			if err != nil {
				t.Fatal(err)
			}
			if r == nil {
				break
			}
			out = append(out, r[2].(string))
			q.HasCur, q.CurKey = true, r[0].(string)
			if idx != "" {
				q.CurPK = r[1].(string)
			}
			q.Advance = 1
		}
		return strings.Join(out, "")
	}
	if got := walk(DirNext, "", CursorReq{}); got != "abcde" {
		t.Fatalf("next: %s", got)
	}
	if got := walk(DirPrev, "", CursorReq{}); got != "edcba" {
		t.Fatalf("prev: %s", got)
	}
	// byTag: q(a) x(c e) y(b d) in (tag, pk) order
	if got := walk(DirNext, "byTag", CursorReq{}); got != "acebd" {
		t.Fatalf("index next: %s", got)
	}
	if got := walk(DirPrev, "byTag", CursorReq{}); got != "dbeca" {
		t.Fatalf("index prev: %s", got)
	}
	if got := walk(DirNextUnique, "byTag", CursorReq{}); got != "acb" {
		t.Fatalf("nextunique: %s", got)
	}
	if got := walk(DirPrevUnique, "byTag", CursorReq{}); got != "bca" {
		t.Fatalf("prevunique: %s", got)
	}
	// continue(key) and advance(n)
	r, _ := m.Cursor(tx, "s", CursorReq{Target: nk(3)})
	if r[2] != "c" {
		t.Fatalf("continue(3): %v", r)
	}
	r, _ = m.Cursor(tx, "s", CursorReq{HasCur: true, CurKey: nk(1), Advance: 3})
	if r[2] != "d" {
		t.Fatalf("advance(3): %v", r)
	}
	r, _ = m.Cursor(tx, "s", CursorReq{Dir: DirPrev, Target: nk(3)})
	if r[2] != "c" {
		t.Fatalf("prev continue(3): %v", r)
	}
	r, _ = m.Cursor(tx, "s", CursorReq{Dir: DirPrev, Range: Range{Hi: nk(4), HiOpen: true}})
	if r[2] != "c" {
		t.Fatalf("prev with open upper bound: %v", r)
	}
	if r, _ = m.Cursor(tx, "s", CursorReq{HasCur: true, CurKey: nk(5)}); r != nil {
		t.Fatalf("cursor past the end: %v", r)
	}
	// deleting a record removes its index entries
	_ = m.DeleteRange(tx, "s", Range{Lo: nk(2), Hi: nk(2)})
	if got := walk(DirNext, "byTag", CursorReq{}); got != "aced" {
		t.Fatalf("after delete: %s", got)
	}
	_ = m.Commit(tx)
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, dir, 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 3, sink, func(tx int64) {
		_ = m.CreateStore(tx, "s", `"id"`, true, true)
		_ = m.CreateIndex(tx, "s", "i", `"n"`, false, false, nil)
	})
	for round := 0; round < 40; round++ {
		tx := begin(t, m, conn, ModeReadWrite, "s")
		for j := 0; j < 50; j++ {
			k, _ := m.Put(tx, "s", "", strings.Repeat("x", 200), false, idxKeys("i", fmt.Sprintf("\x30%05d\x01", j)))
			if j%2 == 0 {
				_ = m.DeleteRange(tx, "s", Range{Lo: k, Hi: k})
			}
		}
		if err := m.Commit(tx); err != nil {
			t.Fatal(err)
		}
	}
	m.CloseConn(conn)
	m.Close()

	m2 := newTestManager(t, dir, 0)
	o2 := m2.Origin("http://a")
	if got := o2.Databases(); len(got) != 1 || got[0].Name != "db" || got[0].Version != 3 {
		t.Fatalf("databases: %+v", got)
	}
	sink2 := &testSink{}
	conn2 := open(t, o2, "db", 0, sink2, nil)
	tx := begin(t, m2, conn2, ModeReadWrite, "s")
	if n, _ := m2.Query(tx, "s", "", QCount, Range{}, 0); n[0] != "1000" {
		t.Fatalf("records after restart: %v", n)
	}
	if n, _ := m2.Query(tx, "s", "i", QCount, Range{Lo: "\x3000001\x01", Hi: "\x3000001\x01"}, 0); n[0] != "40" {
		t.Fatalf("index entries after restart: %v", n)
	}
	k, _ := m2.Put(tx, "s", "", "next", false, nil)
	if f, _ := DecodeNumberKey(k); f != 2001 {
		t.Fatalf("key generator after restart: %v want 2001", f)
	}
}

func TestTransactionScheduling(t *testing.T) {
	m := newTestManager(t, "", 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 0, sink, func(tx int64) {
		_ = m.CreateStore(tx, "a", "", false, false)
		_ = m.CreateStore(tx, "b", "", false, false)
	})
	rw := begin(t, m, conn, ModeReadWrite, "a")
	// a read-only transaction on the same store waits for the earlier writer
	ro, started, _ := m.Begin(conn, []string{"a"}, ModeReadOnly)
	if started {
		t.Fatal("readonly overlapping a running readwrite must wait")
	}
	// a disjoint one does not
	other, started, _ := m.Begin(conn, []string{"b"}, ModeReadWrite)
	if !started {
		t.Fatal("disjoint scope must start at once")
	}
	if err := m.Commit(rw); err != nil {
		t.Fatal(err)
	}
	if msg := sink.take(t, "txstart"); msg["tx"] != ro {
		t.Fatalf("txstart for %v want %v", msg["tx"], ro)
	}
	// two readers overlap
	ro2, started, _ := m.Begin(conn, []string{"a"}, ModeReadOnly)
	if !started {
		t.Fatal("two readonly transactions may overlap")
	}
	m.Abort(ro)
	m.Abort(ro2)
	m.Abort(other)
}

func TestVersionChangeHandshake(t *testing.T) {
	m := newTestManager(t, "", 0)
	o := m.Origin("http://a")
	s1, s2 := &testSink{}, &testSink{}
	c1 := open(t, o, "db", 1, s1, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
	d, _ := o.DB("db")
	d.Open(7, s2, 2)
	vc := s1.take(t, "versionchange")
	if vc["conn"] != c1 || vc["oldVersion"] != int64(1) || vc["newVersion"] != int64(2) {
		t.Fatalf("versionchange: %v", vc)
	}
	// c1 does not close: the opener is told it is blocked
	m.VersionChangeDone(c1)
	b := s2.take(t, "blocked")
	if b["req"] != int64(7) {
		t.Fatalf("blocked: %v", b)
	}
	if s2.has("open-ready") {
		t.Fatal("upgrade started while a connection was still open")
	}
	m.CloseConn(c1)
	ready := s2.take(t, "open-ready")
	if !ready["upgrade"].(bool) || ready["oldVersion"] != int64(1) {
		t.Fatalf("ready: %v", ready)
	}
	// aborting the upgrade restores the version
	m.Abort(ready["tx"].(int64))
	if d.version != 1 {
		t.Fatalf("version after aborted upgrade: %d", d.version)
	}
	// a lower version is rejected
	d.Open(8, s2, 0)
	if r := s2.take(t, "open-ready"); r["version"] != int64(1) {
		t.Fatalf("plain open: %v", r)
	}
	d.Open(9, s2, 0)
	_ = s2.take(t, "open-ready")
	s3 := &testSink{}
	d.Open(10, s3, 2)
	_ = s2.take(t, "versionchange")
	d2, _ := o.DB("db")
	if d2 != d {
		t.Fatal("database object changed")
	}
}

func TestQuotaExceeded(t *testing.T) {
	m := newTestManager(t, "", 10_000)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
	tx := begin(t, m, conn, ModeReadWrite, "s")
	for i := 0; i < 5; i++ {
		m.Put(tx, "s", nk(float64(i)), strings.Repeat("x", 4000), false, nil)
	}
	if err := m.Commit(tx); !IsError(err, "QuotaExceededError") {
		t.Fatalf("commit over quota: %v", err)
	}
	m.Abort(tx)
	if u := o.Usage(); u.IndexedDB > 1000 {
		t.Fatalf("usage after rolled back write: %+v", u)
	}
}

func TestClearOriginWipesDisk(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, dir, 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
	_ = conn
	m.ClearOrigin("http://a")
	if !sink.has("closed") {
		t.Fatal("open connection was not told")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("files left: %v", ents)
	}
	if got := m.Origin("http://a").Databases(); len(got) != 0 {
		t.Fatalf("databases after clear: %+v", got)
	}
}
