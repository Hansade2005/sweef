package sitestore

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Throughput and memory numbers for docs/storage.md. They are not part of the normal run:
//
//	SWEEF_STORAGE_BENCH=1 go test -run TestStorageNumbers -v -timeout 30m ./internal/sitestore
//
// runs the 10k and 1M record workloads in this process (the Go side of the engine: no JavaScript), then a
// database of about 1 GB in separate processes, one per phase, so the resident set sizes are not polluted by
// the phases before. SWEEF_STORAGE_BENCH_GB=0.25 shrinks the big database.

func rssLine() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "rss=n/a"
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		for _, k := range []string{"VmRSS", "VmHWM", "RssAnon", "RssFile"} {
			if strings.HasPrefix(l, k+":") {
				f := strings.Fields(l)
				kb, _ := strconv.Atoi(f[1])
				out = append(out, fmt.Sprintf("%s=%dMiB", k, kb>>10))
			}
		}
	}
	return strings.Join(out, " ")
}

type benchDB struct {
	m    *Manager
	conn int64
	sink *testSink
}

func openBenchDB(t testing.TB, dir string, indexed bool) *benchDB {
	// no quota in the way: this measures the store, and a 1 GB database is over the default cap of a small disk
	m := newTestManagerOptions(t, dir, Options{OriginQuota: 64 << 30, Budget: 64 << 30})
	o := m.Origin("http://bench")
	sink := &testSink{}
	d, err := o.DB("db")
	if err != nil {
		t.Fatal(err)
	}
	d.Open(1, sink, 1)
	var msg Msg
	for i := 0; i < 1000 && msg == nil; i++ {
		sink.mu.Lock()
		for j, x := range sink.msgs {
			if x["t"] == "open-ready" {
				msg = x
				sink.msgs = append(sink.msgs[:j], sink.msgs[j+1:]...)
				break
			}
		}
		sink.mu.Unlock()
	}
	if msg == nil {
		t.Fatal("open")
	}
	if msg["upgrade"].(bool) {
		tx := msg["tx"].(int64)
		_ = m.CreateStore(tx, "s", "", false, false)
		if indexed {
			_ = m.CreateIndex(tx, "s", "by", `"g"`, false, false, nil)
		}
		if err := m.Commit(tx); err != nil {
			t.Fatal(err)
		}
	}
	return &benchDB{m: m, conn: msg["conn"].(int64), sink: sink}
}

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// randVal is an incompressible value of n characters, different every time (a repeated or low-entropy value
// would let the store's snappy compression flatter the numbers).
func randVal(rnd *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = b64[rnd.Intn(64)]
	}
	return string(b)
}

// fillTimes says where the time of the last fill went.
var fillTimes struct{ gen, put, commit time.Duration }

func (b *benchDB) fill(t testing.TB, n, perTx int, valueLen int, indexed bool) {
	rnd := rand.New(rand.NewSource(int64(n)))
	fillTimes.gen, fillTimes.put, fillTimes.commit = 0, 0, 0
	for base := 0; base < n; base += perTx {
		tx, _, _ := b.m.Begin(b.conn, []string{"s"}, ModeReadWrite)
		for i := base; i < base+perTx && i < n; i++ {
			var ik map[string][]string
			if indexed {
				ik = map[string][]string{"by": {NumberKey(float64(i % 1000)).Wire()}}
			}
			t0 := time.Now()
			v := randVal(rnd, valueLen)
			t1 := time.Now()
			if _, err := b.m.Put(tx, "s", NumberKey(float64(i)).Wire(), v, false, ik); err != nil {
				t.Fatal(err)
			}
			fillTimes.gen += t1.Sub(t0)
			fillTimes.put += time.Since(t1)
		}
		t0 := time.Now()
		if err := b.m.Commit(tx); err != nil {
			t.Fatal(err)
		}
		fillTimes.commit += time.Since(t0)
	}
}

func (b *benchDB) randomGets(t testing.TB, n, gets int) (perSec float64, p50, p99 time.Duration) {
	tx, _, _ := b.m.Begin(b.conn, []string{"s"}, ModeReadOnly)
	defer b.m.Abort(tx)
	rnd := rand.New(rand.NewSource(5))
	lat := make([]time.Duration, 0, gets)
	t0 := time.Now()
	for i := 0; i < gets; i++ {
		k := NumberKey(float64(rnd.Intn(n))).Wire()
		s := time.Now()
		r, err := b.m.Query(tx, "s", "", QValue, Range{Lo: k, Hi: k}, 0)
		lat = append(lat, time.Since(s))
		if err != nil || len(r) != 1 {
			t.Fatalf("get %s: %v %v", k, r, err)
		}
	}
	d := time.Since(t0)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	return float64(gets) / d.Seconds(), lat[len(lat)/2], lat[len(lat)*99/100]
}

func (b *benchDB) walk(t testing.TB, n int) (perSec float64) {
	tx, _, _ := b.m.Begin(b.conn, []string{"s"}, ModeReadOnly)
	defer b.m.Abort(tx)
	q := CursorReq{}
	steps := 0
	t0 := time.Now()
	for {
		r, err := b.m.Cursor(tx, "s", q)
		if err != nil {
			t.Fatal(err)
		}
		if r == nil {
			break
		}
		steps++
		q.HasCur, q.CurKey = true, r[0].(string)
	}
	if steps != n {
		t.Fatalf("walked %d of %d", steps, n)
	}
	return float64(steps) / time.Since(t0).Seconds()
}

func TestStorageNumbers(t *testing.T) {
	if os.Getenv("SWEEF_STORAGE_BENCH") == "" {
		t.Skip("set SWEEF_STORAGE_BENCH=1 to run the throughput and memory measurements")
	}
	const value = 100
	for _, n := range []int{10_000, 1_000_000} {
		dir := t.TempDir()
		b := openBenchDB(t, dir, false)
		t0 := time.Now()
		b.fill(t, n, 1000, value, false)
		d := time.Since(t0)
		t.Logf("%8d records, 100 B random values, 1000 puts per transaction: %7.0f puts/s (%v)", n, float64(n)/d.Seconds(), d.Round(time.Millisecond))
		gets := 200_000
		if n < gets {
			gets = n * 10
		}
		ps, p50, p99 := b.randomGets(t, n, gets)
		t.Logf("%8d records: %7.0f random gets/s (p50 %v, p99 %v)", n, ps, p50, p99)
		t.Logf("%8d records: %7.0f cursor steps/s", n, b.walk(t, n))
		b.m.CloseConn(b.conn)
		b.m.Shutdown()
		// one record per transaction, as a page that saves state on every click does it
		b2 := openBenchDB(t, dir, false)
		t0 = time.Now()
		cnt := 2000
		for i := 0; i < cnt; i++ {
			b2.fill(t, 1, 1, value, false)
		}
		t.Logf("%8d records: %7.0f one-put transactions/s (journal append, no fsync)", n, float64(cnt)/time.Since(t0).Seconds())
		b2.m.CloseConn(b2.conn)
		b2.m.Shutdown()
	}
	{
		dir := t.TempDir()
		b := openBenchDB(t, dir, true)
		t0 := time.Now()
		b.fill(t, 10_000, 1000, value, true)
		t.Logf("   10000 records with one index: %7.0f puts/s", 10_000/time.Since(t0).Seconds())
		b.m.CloseConn(b.conn)
		b.m.Shutdown()
	}

	// ~1 GB in separate processes
	gb := 1.0
	if s := os.Getenv("SWEEF_STORAGE_BENCH_GB"); s != "" {
		gb, _ = strconv.ParseFloat(s, 64)
	}
	records := int(gb * (1 << 20)) // 1 KiB values
	dir := t.TempDir()
	run := func(phase string) []string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStorageBigChild$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), "SWEEF_STORAGE_BIG_DIR="+dir, "SWEEF_STORAGE_BIG_PHASE="+phase, "SWEEF_STORAGE_BIG_N="+strconv.Itoa(records))
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var lines, all []string
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			l := sc.Text()
			all = append(all, l)
			if strings.HasPrefix(l, "RESULT ") {
				lines = append(lines, strings.TrimPrefix(l, "RESULT "))
			}
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("phase %s: %v\n%s", phase, err, strings.Join(all, "\n"))
		}
		return lines
	}
	for _, phase := range []string{"fill", "open", "reads", "scan"} {
		for _, l := range run(phase) {
			t.Logf("big database (%d records of 1 KiB), %s", records, l)
		}
	}
	var size int64
	_ = filepathWalkSize(dir, &size)
	t.Logf("big database on disk: %d MiB", size>>20)
}

func filepathWalkSize(dir string, total *int64) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		p := dir + "/" + e.Name()
		if e.IsDir() {
			_ = filepathWalkSize(p, total)
		} else if fi, err := e.Info(); err == nil {
			*total += fi.Size()
		}
	}
	return nil
}

func TestStorageBigChild(t *testing.T) {
	dir := os.Getenv("SWEEF_STORAGE_BIG_DIR")
	if dir == "" {
		t.Skip("helper process of TestStorageNumbers")
	}
	phase := os.Getenv("SWEEF_STORAGE_BIG_PHASE")
	n, _ := strconv.Atoi(os.Getenv("SWEEF_STORAGE_BIG_N"))
	report := func(format string, a ...any) { fmt.Printf("RESULT "+format+" | "+rssLine()+"\n", a...) }
	switch phase {
	case "fill":
		b := openBenchDB(t, dir, false)
		t0 := time.Now()
		b.fill(t, n, 2000, 1024, false)
		d := time.Since(t0)
		report("fill: %v, %.0f puts/s (generating values %v, Put %v, Commit %v)", d.Round(time.Millisecond), float64(n)/d.Seconds(),
			fillTimes.gen.Round(time.Millisecond), fillTimes.put.Round(time.Millisecond), fillTimes.commit.Round(time.Millisecond))
		b.m.CloseConn(b.conn)
		t0 = time.Now()
		b.m.Shutdown()
		report("close and flush: %v", time.Since(t0).Round(time.Millisecond))
	case "open":
		t0 := time.Now()
		b := openBenchDB(t, dir, false)
		tx, _, _ := b.m.Begin(b.conn, []string{"s"}, ModeReadOnly)
		r, err := b.m.Query(tx, "s", "", QValue, Range{Lo: NumberKey(float64(n / 2)).Wire(), Hi: NumberKey(float64(n / 2)).Wire()}, 0)
		if err != nil || len(r) != 1 {
			t.Fatal(r, err)
		}
		report("open to first read: %v", time.Since(t0).Round(time.Millisecond))
		b.m.Abort(tx)
		b.m.CloseConn(b.conn)
		b.m.Shutdown()
	case "reads":
		b := openBenchDB(t, dir, false)
		ps, p50, p99 := b.randomGets(t, n, 200_000)
		report("200k random gets: %.0f/s, p50 %v, p99 %v", ps, p50, p99)
		b.m.CloseConn(b.conn)
		b.m.Shutdown()
	case "scan":
		b := openBenchDB(t, dir, false)
		ps := b.walk(t, n)
		report("full cursor walk: %.0f steps/s", ps)
		b.m.CloseConn(b.conn)
		b.m.Shutdown()
	}
}
