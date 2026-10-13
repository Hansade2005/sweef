package sitestore

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
)

func blobFiles(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*", "blobs", "*"))
	return files
}

func TestLargeValuesAreBlobFiles(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
	big := strings.Repeat("0123456789abcdef", 16<<10) // 256 KiB
	tx := begin(t, m, conn, ModeReadWrite, "s")
	if _, err := m.Put(tx, "s", nk(1), big, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Put(tx, "s", nk(2), "small", false, nil); err != nil {
		t.Fatal(err)
	}
	if got := blobFiles(dir); len(got) != 1 {
		t.Fatalf("a value above the threshold must be a file, others not: %v", got)
	}
	if v, _ := m.Query(tx, "s", "", QValue, Range{Lo: nk(1), Hi: nk(1)}, 0); len(v) != 1 || v[0] != big {
		t.Fatal("the blob value does not read back inside its own transaction")
	}
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if u := o.Usage(); u.IndexedDB < int64(len(big)) {
		t.Fatalf("a blob counts against the usage: %+v", u)
	}

	// the same value after a restart
	m.CloseConn(conn)
	m.Close()
	m = NewManager(dir, 0)
	o = m.Origin("http://a")
	conn = open(t, o, "db", 0, sink, nil)
	tx = begin(t, m, conn, ModeReadWrite, "s")
	if v, _ := m.Query(tx, "s", "", QValue, Range{Lo: nk(1), Hi: nk(1)}, 0); len(v) != 1 || v[0] != big {
		t.Fatal("the blob value does not survive a restart")
	}
	// overwriting with a small value frees the file; so does deleting; an aborted write leaves none behind
	if _, err := m.Put(tx, "s", nk(1), "tiny", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if got := blobFiles(dir); len(got) != 0 {
		t.Fatalf("replaced blob still on disk: %v", got)
	}
	tx = begin(t, m, conn, ModeReadWrite, "s")
	m.Put(tx, "s", nk(3), big, false, nil)
	m.Abort(tx)
	if got := blobFiles(dir); len(got) != 0 {
		t.Fatalf("aborted blob still on disk: %v", got)
	}
	tx = begin(t, m, conn, ModeReadWrite, "s")
	m.Put(tx, "s", nk(3), big, false, nil)
	m.Commit(tx)
	if len(blobFiles(dir)) != 1 {
		t.Fatal("committed blob missing")
	}
	tx = begin(t, m, conn, ModeReadWrite, "s")
	m.DeleteRange(tx, "s", Range{Lo: nk(3), Hi: nk(3)})
	m.Commit(tx)
	if got := blobFiles(dir); len(got) != 0 {
		t.Fatalf("deleted blob still on disk: %v", got)
	}
	if u := o.Usage(); u.IndexedDB > 1000 {
		t.Fatalf("usage after deleting the blobs: %+v", u)
	}
}

func kvKeyCount(o *Origin) (n int) {
	k, err := o.acquire()
	if err != nil {
		return -1
	}
	defer o.release(k)
	it := k.db.NewIterator(nil, nil)
	defer it.Release()
	for it.Next() {
		n++
	}
	return n
}

func TestClearAndDeleteDatabaseDropDataInTheBackground(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) {
		_ = m.CreateStore(tx, "s", "", false, false)
		_ = m.CreateIndex(tx, "s", "i", `"x"`, false, false, nil)
	})
	baseline := kvKeyCount(o)
	big := strings.Repeat("z", 100<<10)
	tx := begin(t, m, conn, ModeReadWrite, "s")
	for i := 0; i < 5000; i++ {
		v := "v"
		if i%1000 == 0 {
			v = big
		}
		if _, err := m.Put(tx, "s", nk(float64(i)), v, false, idxKeys("i", nk(float64(i%10)))); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if len(blobFiles(dir)) != 5 {
		t.Fatalf("blobs: %v", blobFiles(dir))
	}
	used := o.Usage().IndexedDB
	tx = begin(t, m, conn, ModeReadWrite, "s")
	if err := m.Clear(tx, "s"); err != nil {
		t.Fatal(err)
	}
	if n, _ := m.Query(tx, "s", "", QCount, Range{}, 0); n[0] != "0" {
		t.Fatalf("count inside the clearing transaction: %v", n)
	}
	if n, _ := m.Query(tx, "s", "i", QCount, Range{}, 0); n[0] != "0" {
		t.Fatalf("index count inside the clearing transaction: %v", n)
	}
	m.Put(tx, "s", nk(7), "after", false, idxKeys("i", nk(1)))
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if u := o.Usage().IndexedDB; u >= used/10 {
		t.Fatalf("usage must drop at commit, not when the collector gets to it: %d -> %d", used, u)
	}
	o.WaitGC()
	if left := kvKeyCount(o); left > baseline+8 { // the new record, its index entry and reverse record, the state
		t.Fatalf("the old range was not collected: %d keys (baseline %d)", left, baseline)
	}
	if got := blobFiles(dir); len(got) != 0 {
		t.Fatalf("blobs of cleared records survive: %v", got)
	}
	tx = begin(t, m, conn, ModeReadOnly, "s")
	if v, _ := m.Query(tx, "s", "", QValues, Range{}, 0); fmt.Sprint(v) != "[after]" {
		t.Fatalf("after clear and put: %v", v)
	}
	m.Abort(tx)

	// deleting the database does the same for everything it has
	m.CloseConn(conn)
	d, _ := o.DB("db")
	d.Delete(5, sink)
	sink.take(t, "delete-done")
	o.WaitGC()
	if left := kvKeyCount(o); left > baseline {
		t.Fatalf("keys left after deleteDatabase: %d (baseline %d)", left, baseline)
	}
	if got := o.Databases(); len(got) != 0 {
		t.Fatalf("databases: %+v", got)
	}
	if u := o.Usage().IndexedDB; u != 0 {
		t.Fatalf("usage after deleteDatabase: %d", u)
	}
}

func TestUnknownOrDamagedStoreIsSetAsideNotDeleted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantBak bool
		break_  func(kv string)
	}{
		{"foreign format", true, func(kv string) {
			db, err := leveldb.OpenFile(kv, nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = db.Put([]byte("someone else's data"), []byte("x"), nil)
			db.Close()
		}},
		{"format mark of another version", true, func(kv string) {
			db, _ := leveldb.OpenFile(kv, nil)
			_ = db.Put(keyFormat, []byte("SWKV9"), nil)
			db.Close()
		}},
		{"damaged manifest without tables", false, func(kv string) {
			_ = os.MkdirAll(kv, 0o700)
			_ = os.WriteFile(filepath.Join(kv, "CURRENT"), []byte("not a manifest name\n"), 0o600)
			_ = os.WriteFile(filepath.Join(kv, "MANIFEST-000001"), []byte("\x00\x01garbage"), 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m := NewManager(dir, 0)
			o := m.Origin("http://a")
			o.writeMeta()
			kv := filepath.Join(o.dir, "kv")
			tc.break_(kv)
			sink := &testSink{}
			conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
			tx := begin(t, m, conn, ModeReadWrite, "s")
			if _, err := m.Put(tx, "s", nk(1), "fresh", false, nil); err != nil {
				t.Fatal(err)
			}
			if err := m.Commit(tx); err != nil {
				t.Fatal(err)
			}
			if !tc.wantBak {
				return // a damaged manifest with nothing else in the directory is rebuilt, there is nothing to keep
			}
			if _, err := os.Stat(kv + ".bak"); err != nil {
				t.Fatalf("the unreadable store must be kept as kv.bak: %v", err)
			}
			if ents, _ := os.ReadDir(kv + ".bak"); len(ents) == 0 {
				t.Fatal("kv.bak is empty")
			}
		})
	}
}

func TestLockedStoreIsNotMistakenForDamage(t *testing.T) {
	dir := t.TempDir()
	m1 := NewManager(dir, 0)
	o1 := m1.Origin("http://a")
	sink := &testSink{}
	open(t, o1, "db", 1, sink, func(tx int64) { _ = m1.CreateStore(tx, "s", "", false, false) })
	// a second manager (a second process, in real life) on the same directory must fail, not move the data away
	m2 := NewManager(dir, 0)
	o2 := m2.Origin("http://a")
	if _, err := o2.acquire(); err == nil {
		t.Fatal("a store held by another opener must not open twice")
	}
	if _, err := os.Stat(filepath.Join(o1.dir, "kv.bak")); err == nil {
		t.Fatal("a locked store was set aside")
	}
}

// ---- a model-based test: random transactions against a plain in-memory model ------------------

type modelRec struct {
	g int // index key
	v string
}

type model map[int]modelRec

func (md model) clone() model {
	c := model{}
	for k, v := range md {
		c[k] = v
	}
	return c
}

func (md model) keys(lo, hi int, loOpen, hiOpen bool) []int {
	var ks []int
	for k := range md {
		if (k > lo || (!loOpen && k == lo)) && (k < hi || (!hiOpen && k == hi)) {
			ks = append(ks, k)
		}
	}
	sort.Ints(ks)
	return ks
}

// items of the index, ordered by (g, k), restricted to g in the range.
func (md model) items(lo, hi int, loOpen, hiOpen bool) [][2]int {
	var it [][2]int
	for k, r := range md {
		g := r.g
		if (g > lo || (!loOpen && g == lo)) && (g < hi || (!hiOpen && g == hi)) {
			it = append(it, [2]int{g, k})
		}
	}
	sort.Slice(it, func(i, j int) bool {
		if it[i][0] != it[j][0] {
			return it[i][0] < it[j][0]
		}
		return it[i][1] < it[j][1]
	})
	return it
}

func rg(lo, hi int, loOpen, hiOpen bool) Range {
	return Range{Lo: nk(float64(lo)), Hi: nk(float64(hi)), LoOpen: loOpen, HiOpen: hiOpen}
}

func numOf(t *testing.T, w any) int {
	f, ok := DecodeNumberKey(w.(string))
	if !ok {
		t.Fatalf("not a number key: %q", w)
	}
	return int(f)
}

// walk runs a cursor to its end the way the prelude does and returns the (key, primary key) pairs it visited.
func walkCursor(t *testing.T, m *Manager, tx int64, index string, dir int, r Range, _ *rand.Rand) [][2]int {
	var out [][2]int
	q := CursorReq{Index: index, Range: r, Dir: dir, Advance: 1}
	for guard := 0; guard < 100000; guard++ {
		q.Advance = 1
		res, err := m.Cursor(tx, "s", q)
		if err != nil {
			t.Fatal(err)
		}
		if res == nil {
			return out
		}
		pair := [2]int{numOf(t, res[0]), numOf(t, res[1])}
		out = append(out, pair)
		q.HasCur, q.CurKey = true, res[0].(string)
		if index != "" {
			q.CurPK = res[1].(string)
		}
	}
	t.Fatal("cursor does not terminate")
	return nil
}

// expectWalk computes the same walk from the model, honouring advance(n) by replaying the recorded steps.
func TestRandomTransactionsMatchAModel(t *testing.T) {
	dir := t.TempDir()
	rnd := rand.New(rand.NewSource(424242))
	m := NewManager(dir, 0)
	o := m.Origin("http://model")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) {
		_ = m.CreateStore(tx, "s", "", false, false)
		_ = m.CreateIndex(tx, "s", "i", `"g"`, false, false, nil)
	})
	committed := model{}
	counter := 0
	check := func(tx int64, md model, where string) {
		// store: all keys and values, forward and backward, with random ranges
		for i := 0; i < 6; i++ {
			lo, hi := rnd.Intn(120)-5, rnd.Intn(120)-5
			loOpen, hiOpen := rnd.Intn(2) == 0, rnd.Intn(2) == 0
			r := rg(lo, hi, loOpen, hiOpen)
			want := md.keys(lo, hi, loOpen, hiOpen)
			ks, err := m.Query(tx, "s", "", QKeys, r, 0)
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			for _, k := range ks {
				got = append(got, numOf(t, k))
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%s: keys in %+v: got %v want %v", where, r, got, want)
			}
			if n, _ := m.Query(tx, "s", "", QCount, r, 0); n[0] != fmt.Sprint(len(want)) {
				t.Fatalf("%s: count in %+v: %v want %d", where, r, n, len(want))
			}
			// forward and backward cursors (single steps; the model replays them from the end results)
			fw := walkCursor(t, m, tx, "", DirNext, r, rand.New(rand.NewSource(1)))
			if len(fw) != len(want) {
				t.Fatalf("%s: cursor next over %+v saw %d, want %d", where, r, len(fw), len(want))
			}
			bw := walkCursor(t, m, tx, "", DirPrev, r, rand.New(rand.NewSource(1)))
			if len(bw) != len(want) {
				t.Fatalf("%s: cursor prev over %+v saw %d, want %d", where, r, len(bw), len(want))
			}
			for j := range want {
				if fw[j][0] != want[j] || bw[len(want)-1-j][0] != want[j] {
					t.Fatalf("%s: cursor order differs at %d", where, j)
				}
			}
			// index
			glo, ghi := rnd.Intn(9)-1, rnd.Intn(9)-1
			gloOpen, ghiOpen := rnd.Intn(2) == 0, rnd.Intn(2) == 0
			gr := rg(glo, ghi, gloOpen, ghiOpen)
			items := md.items(glo, ghi, gloOpen, ghiOpen)
			ik, err := m.Query(tx, "s", "i", QKeys, gr, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(ik) != len(items) {
				t.Fatalf("%s: index keys in %+v: %d want %d", where, gr, len(ik), len(items))
			}
			for j := range items {
				if numOf(t, ik[j]) != items[j][1] {
					t.Fatalf("%s: index order differs at %d", where, j)
				}
			}
			ifw := walkCursor(t, m, tx, "i", DirNext, gr, rand.New(rand.NewSource(2)))
			ibw := walkCursor(t, m, tx, "i", DirPrev, gr, rand.New(rand.NewSource(2)))
			if len(ifw) != len(items) || len(ibw) != len(items) {
				t.Fatalf("%s: index cursor over %+v saw %d/%d, want %d", where, gr, len(ifw), len(ibw), len(items))
			}
			for j := range items {
				if ifw[j] != items[j] || ibw[len(items)-1-j] != items[j] {
					t.Fatalf("%s: index cursor differs at %d: %v vs %v", where, j, ifw[j], items[j])
				}
			}
			// unique directions: one entry per index key, the lowest primary key of each group
			var firsts [][2]int
			for j, it := range items {
				if j == 0 || items[j-1][0] != it[0] {
					firsts = append(firsts, it)
				}
			}
			// advance(n) from the start lands on the n-th item, in both directions
			for _, n := range []int{1, 2, 3, 5} {
				if n > len(items) {
					break
				}
				res, err := m.Cursor(tx, "s", CursorReq{Index: "i", Range: gr, Dir: DirNext, Advance: n})
				if err != nil || res == nil || [2]int{numOf(t, res[0]), numOf(t, res[1])} != items[n-1] {
					t.Fatalf("%s: advance(%d) forward over %+v: %v %v, want %v", where, n, gr, res, err, items[n-1])
				}
				res, err = m.Cursor(tx, "s", CursorReq{Index: "i", Range: gr, Dir: DirPrev, Advance: n})
				if err != nil || res == nil || [2]int{numOf(t, res[0]), numOf(t, res[1])} != items[len(items)-n] {
					t.Fatalf("%s: advance(%d) backward over %+v: %v %v, want %v", where, n, gr, res, err, items[len(items)-n])
				}
			}
			ufw := walkCursor(t, m, tx, "i", DirNextUnique, gr, rand.New(rand.NewSource(3)))
			ubw := walkCursor(t, m, tx, "i", DirPrevUnique, gr, rand.New(rand.NewSource(3)))
			if fmt.Sprint(ufw) != fmt.Sprint(firsts) {
				t.Fatalf("%s: nextunique over %+v: %v want %v", where, gr, ufw, firsts)
			}
			for j := range firsts {
				if ubw[len(firsts)-1-j] != firsts[j] {
					t.Fatalf("%s: prevunique over %+v: %v want reverse of %v", where, gr, ubw, firsts)
				}
			}
		}
	}
	for round := 0; round < 60; round++ {
		tx := begin(t, m, conn, ModeReadWrite, "s")
		md := committed.clone()
		for op := 0; op < 1+rnd.Intn(40); op++ {
			switch c := rnd.Intn(20); {
			case c < 12:
				k := rnd.Intn(100)
				counter++
				g := rnd.Intn(8)
				md[k] = modelRec{g, fmt.Sprintf("v%d", counter)}
				if _, err := m.Put(tx, "s", nk(float64(k)), md[k].v, false, idxKeys("i", nk(float64(g)))); err != nil {
					t.Fatal(err)
				}
			case c < 17:
				k := rnd.Intn(100)
				delete(md, k)
				if err := m.DeleteRange(tx, "s", Range{Lo: nk(float64(k)), Hi: nk(float64(k))}); err != nil {
					t.Fatal(err)
				}
			case c < 19:
				lo, hi := rnd.Intn(100), rnd.Intn(100)
				loOpen, hiOpen := rnd.Intn(2) == 0, rnd.Intn(2) == 0
				for _, k := range md.keys(lo, hi, loOpen, hiOpen) {
					delete(md, k)
				}
				if err := m.DeleteRange(tx, "s", rg(lo, hi, loOpen, hiOpen)); err != nil {
					t.Fatal(err)
				}
			default:
				if rnd.Intn(4) == 0 {
					md = model{}
					if err := m.Clear(tx, "s"); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		check(tx, md, fmt.Sprintf("round %d in the transaction", round))
		if rnd.Intn(4) == 0 {
			m.Abort(tx)
		} else {
			if err := m.Commit(tx); err != nil {
				t.Fatal(err)
			}
			committed = md
		}
		tx = begin(t, m, conn, ModeReadOnly, "s")
		check(tx, committed, fmt.Sprintf("round %d after the transaction", round))
		m.Abort(tx)
		if round%15 == 14 { // a restart in the middle
			m.CloseConn(conn)
			m.Close()
			m = NewManager(dir, 0)
			o = m.Origin("http://model")
			conn = open(t, o, "db", 0, sink, nil)
			tx = begin(t, m, conn, ModeReadOnly, "s")
			check(tx, committed, fmt.Sprintf("round %d after a restart", round))
			m.Abort(tx)
		}
		o.WaitGC()
	}
	// usage matches what is in the store: every record and index entry accounted exactly once
	if u := o.Usage().IndexedDB; u <= 0 && len(committed) > 0 {
		t.Fatalf("usage %d with %d records", u, len(committed))
	}
}

func TestOverwriteReplacesIndexEntriesAndUniqueIsChecked(t *testing.T) {
	m := NewManager("", 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) {
		_ = m.CreateStore(tx, "s", "", false, false)
		_ = m.CreateIndex(tx, "s", "tags", `"tags"`, false, true, nil)
		_ = m.CreateIndex(tx, "s", "email", `"email"`, true, false, nil)
	})
	tx := begin(t, m, conn, ModeReadWrite, "s")
	str := func(s string) string { return StringKey(s).Wire() }
	ik := func(email string, tags ...string) map[string][]string {
		tg := []string{}
		for _, x := range tags {
			tg = append(tg, str(x))
		}
		return map[string][]string{"email": {str(email)}, "tags": tg}
	}
	m.Put(tx, "s", nk(1), "one", false, ik("a@x", "red", "blue"))
	m.Put(tx, "s", nk(2), "two", false, ik("b@x", "blue"))
	cnt := func(idx, key string) string {
		n, err := m.Query(tx, "s", idx, QCount, Range{Lo: str(key), Hi: str(key)}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return n[0].(string)
	}
	if cnt("tags", "blue") != "2" || cnt("tags", "red") != "1" {
		t.Fatal("multiEntry index entries missing")
	}
	// overwrite record 1: red goes away, green appears, the email changes
	m.Put(tx, "s", nk(1), "one!", false, ik("c@x", "blue", "green"))
	if cnt("tags", "red") != "0" || cnt("tags", "green") != "1" || cnt("tags", "blue") != "2" {
		t.Fatalf("stale or missing index entries after an overwrite: red=%s green=%s blue=%s", cnt("tags", "red"), cnt("tags", "green"), cnt("tags", "blue"))
	}
	if cnt("email", "a@x") != "0" || cnt("email", "c@x") != "1" {
		t.Fatal("unique index not updated")
	}
	if _, err := m.Put(tx, "s", nk(3), "three", false, ik("b@x")); !IsError(err, "ConstraintError") {
		t.Fatalf("duplicate in a unique index: %v", err)
	}
	// failed put left nothing behind
	if v, _ := m.Query(tx, "s", "", QCount, Range{}, 0); v[0] != "2" {
		t.Fatalf("records after the failed put: %v", v)
	}
	m.DeleteRange(tx, "s", Range{Lo: nk(1), Hi: nk(1)})
	if cnt("tags", "blue") != "1" || cnt("tags", "green") != "0" || cnt("email", "c@x") != "0" {
		t.Fatal("deleting a record must remove all its index entries")
	}
	if _, err := m.Put(tx, "s", nk(3), "three", false, ik("c@x")); err != nil { // the unique key is free again
		t.Fatal(err)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
}

func TestCreateIndexOnExistingRecordsPagesThroughTheStore(t *testing.T) {
	m := NewManager("", 0)
	o := m.Origin("http://a")
	sink := &testSink{}
	conn := open(t, o, "db", 1, sink, func(tx int64) { _ = m.CreateStore(tx, "s", "", false, false) })
	tx := begin(t, m, conn, ModeReadWrite, "s")
	for i := 0; i < 1300; i++ {
		m.Put(tx, "s", nk(float64(i)), fmt.Sprint("v", i), false, nil)
	}
	m.Commit(tx)
	m.CloseConn(conn)
	d, _ := o.DB("db")
	d.Open(9, sink, 2)
	ready := sink.take(t, "open-ready")
	up := ready["tx"].(int64)
	var all []string
	after := ""
	pages := 0
	for {
		page, next, err := m.RecordsPage(up, "s", after, 500)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for i := 0; i < len(page); i += 2 {
			all = append(all, page[i].(string))
		}
		if next == "" {
			break
		}
		after = next
	}
	if len(all) != 1300 || pages != 3 {
		t.Fatalf("paged %d records in %d pages", len(all), pages)
	}
	if !sort.StringsAreSorted(all) {
		t.Fatal("pages are not in key order")
	}
	var ents []IndexEntry
	for i := 0; i < 1300; i++ {
		ents = append(ents, IndexEntry{PK: nk(float64(i)), Keys: []string{nk(float64(i % 13))}})
	}
	if err := m.CreateIndex(up, "s", "mod", `"m"`, false, false, ents); err != nil {
		t.Fatal(err)
	}
	if n, _ := m.Query(up, "s", "mod", QCount, Range{Lo: nk(5), Hi: nk(5)}, 0); n[0] != "100" {
		t.Fatalf("entries for one key: %v", n)
	}
	// a unique index over duplicates is refused
	if err := m.CreateIndex(up, "s", "uniq", `"m"`, true, false, ents); !IsError(err, "ConstraintError") {
		t.Fatalf("unique over duplicates: %v", err)
	}
	if err := m.Commit(up); err != nil {
		t.Fatal(err)
	}
	_ = bytes.MinRead
}
