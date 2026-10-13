package sitestore

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Crash safety. Two angles on the same promise (a transaction is there whole
// or not at all, and a committed one is not lost):
//
//   - TestKillMidWriteLeavesWholeTransactions runs a writer in a child process
//     and kills it with SIGKILL at a random moment, over and over, against
//     the same directory: process death in the middle of a transaction, of a
//     commit, of a compaction.
//   - TestPowerLossKeepsSyncedTransactionsAndAtomicity replaces the file
//     layer with one that forgets unsynced bytes (and keeps a random part of
//     them, a torn write) and checks what the store makes of that.

const txnRecords = 10

// writeTxn commits transaction n: txnRecords records (key n*100+j), each with one index entry (by transaction),
// every fifth transaction with a value large enough to be a blob file.
func writeTxn(m *Manager, conn int64, n int) error { return writeTxnStrict(m, conn, n, false) }

func openCrashDB(m *Manager, origin string) (int64, error) {
	o := m.Origin(origin)
	d, err := o.DB("db")
	if err != nil {
		return 0, err
	}
	sink := &chanSink{ch: make(chan Msg, 16)}
	d.Open(1, sink, 1)
	msg := <-sink.ch
	if msg["t"] != "open-ready" {
		return 0, fmt.Errorf("open: %v", msg)
	}
	conn := msg["conn"].(int64)
	if msg["upgrade"].(bool) {
		tx := msg["tx"].(int64)
		if err := m.CreateStore(tx, "s", "", false, false); err != nil {
			return 0, err
		}
		if err := m.CreateIndex(tx, "s", "byTxn", `"txn"`, false, false, nil); err != nil {
			return 0, err
		}
		if err := m.Commit(tx); err != nil {
			return 0, err
		}
	}
	return conn, nil
}

type chanSink struct{ ch chan Msg }

func (s *chanSink) Notify(m Msg) { s.ch <- m }

// presentTxns reads the whole store back and returns the transaction numbers that have records, failing the
// test if any of them is partial or disagrees with its index, and the number of blob values.
func presentTxns(t *testing.T, m *Manager, conn int64) (txns []int, blobs int) {
	t.Helper()
	tx, started, err := m.Begin(conn, []string{"s"}, ModeReadOnly)
	if err != nil || !started {
		t.Fatalf("begin: %v %v", err, started)
	}
	defer m.Abort(tx)
	recs, err := m.Query(tx, "s", "", QRecords, Range{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := map[int]int{}
	for i := 0; i < len(recs); i += 3 {
		f, _ := DecodeNumberKey(recs[i].(string))
		n := int(f) / 100
		count[n]++
		v := recs[i+2].(string)
		if len(v) > 100<<10 {
			blobs++
		}
	}
	for n := 0; len(count) > 0 && n < 1<<20; n++ {
		c, ok := count[n]
		if ok {
			if c != txnRecords {
				t.Fatalf("transaction %d is partial: %d of %d records", n, c, txnRecords)
			}
			txns = append(txns, n)
			delete(count, n)
			ic, err := m.Query(tx, "s", "byTxn", QCount, Range{Lo: StringKey(fmt.Sprintf("t%08d", n)).Wire(), Hi: StringKey(fmt.Sprintf("t%08d", n)).Wire()}, 0)
			if err != nil || ic[0] != strconv.Itoa(txnRecords) {
				t.Fatalf("transaction %d has %v index entries (%v), want %d", n, ic, err, txnRecords)
			}
		}
	}
	return txns, blobs
}

func TestCrashChild(t *testing.T) {
	dir := os.Getenv("SWEEF_STORE_CRASH_DIR")
	if dir == "" {
		t.Skip("helper process of TestKillMidWriteLeavesWholeTransactions")
	}
	start, _ := strconv.Atoi(os.Getenv("SWEEF_STORE_CRASH_START"))
	m := NewManager(dir, 0)
	conn, err := openCrashDB(m, "http://crash")
	if err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
	for n := start; ; n++ {
		if err := writeTxn(m, conn, n); err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		fmt.Println("ack", n)
	}
}

func TestKillMidWriteLeavesWholeTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	dir := t.TempDir()
	next := 0
	rnd := rand.New(rand.NewSource(99))
	for round := 0; round < 8; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), "SWEEF_STORE_CRASH_DIR="+dir, "SWEEF_STORE_CRASH_START="+strconv.Itoa(next))
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		lastAck := -1
		done := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				var n int
				if _, err := fmt.Sscanf(sc.Text(), "ack %d", &n); err == nil {
					lastAck = n
				}
			}
			close(done)
		}()
		time.Sleep(time.Duration(300+rnd.Intn(500)) * time.Millisecond)
		_ = cmd.Process.Kill() // SIGKILL: no deferred function, no flush, nothing
		<-done
		_ = cmd.Wait()

		m := NewManager(dir, 0)
		conn, err := openCrashDB(m, "http://crash")
		if err != nil {
			t.Fatalf("round %d: the store does not open after a kill: %v", round, err)
		}
		txns, blobs := presentTxns(t, m, conn)
		for i := 1; i < len(txns); i++ {
			if txns[i] != txns[i-1]+1 {
				t.Fatalf("round %d: surviving transactions have a gap: %v", round, txns)
			}
		}
		if len(txns) > 0 && lastAck >= 0 && txns[len(txns)-1] < lastAck {
			t.Fatalf("round %d: transaction %d was acknowledged but the store has only up to %d", round, lastAck, txns[len(txns)-1])
		}
		// every blob file belongs to a surviving record, and the other way round
		m.Origin("http://crash").WaitGC()
		files, _ := filepath.Glob(filepath.Join(dir, "*", "blobs", "*"))
		if len(files) != blobs {
			t.Fatalf("round %d: %d blob files for %d blob values: %v", round, len(files), blobs, files)
		}
		if len(txns) > 0 {
			next = txns[len(txns)-1] + 1
		}
		m.CloseConn(conn)
		m.Shutdown()
	}
	if next < 20 {
		t.Fatalf("the writer made too little progress to prove anything: %d transactions in 8 rounds", next)
	}
	t.Logf("8 kills, %d transactions survived whole", next)
}

func TestPowerLossKeepsSyncedTransactionsAndAtomicity(t *testing.T) {
	lost, kept := 0, 0
	for seed := int64(1); seed <= 25; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		root := t.TempDir()
		lossy := newLossyStorage()
		opts := Options{openStore: func(dir string) (*kvStore, error) { _ = os.MkdirAll(dir, 0o700); return openKVOn(lossy, "") }}
		m := NewManagerOptions(root, opts)
		conn, err := openCrashDB(m, "http://power")
		if err != nil {
			t.Fatal(err)
		}
		total := 20 + rnd.Intn(150)
		strictAt := -1
		for n := 0; n < total; n++ {
			strict := rnd.Intn(12) == 0 // committed with a synchronous write
			if strict {
				strictAt = n
			}
			if err := writeTxnStrict(m, conn, n, strict); err != nil {
				t.Fatal(err)
			}
		}
		crashed := lossy.crash(rnd)
		m.Shutdown()

		m2 := NewManagerOptions(root, Options{openStore: func(dir string) (*kvStore, error) { _ = os.MkdirAll(dir, 0o700); return openKVOn(crashed, "") }})
		conn2, err := openCrashDB(m2, "http://power")
		if err != nil {
			t.Fatalf("seed %d: the store does not open after a power loss: %v", seed, err)
		}
		txns, _ := presentTxns(t, m2, conn2)
		for i := 1; i < len(txns); i++ {
			if txns[i] != txns[i-1]+1 {
				t.Fatalf("seed %d: surviving transactions are not a prefix: %v", seed, txns)
			}
		}
		if strictAt >= 0 && (len(txns) == 0 || txns[len(txns)-1] < strictAt) {
			t.Fatalf("seed %d: transaction %d was committed synchronously but did not survive (have %d transactions)", seed, strictAt, len(txns))
		}
		lost += total - len(txns)
		kept += len(txns)
		m2.CloseConn(conn2)
		m2.Shutdown()
	}
	if lost == 0 || kept == 0 {
		t.Fatalf("the fault layer proved nothing: %d transactions lost, %d kept", lost, kept)
	}
	t.Logf("25 power losses: %d transactions kept, %d unsynced ones lost, none torn", kept, lost)
}

func writeTxnStrict(m *Manager, conn int64, n int, strict bool) error {
	tx, started, err := m.Begin(conn, []string{"s"}, ModeReadWrite)
	if err != nil || !started {
		return fmt.Errorf("begin: %v started=%v", err, started)
	}
	m.SetDurability(tx, strict)
	for j := 0; j < txnRecords; j++ {
		val := fmt.Sprintf("txn-%d-%d", n, j)
		if n%5 == 0 && j == 0 {
			val = strings.Repeat(fmt.Sprintf("%d", n%10), 200<<10)
		}
		ik := map[string][]string{"byTxn": {StringKey(fmt.Sprintf("t%08d", n)).Wire()}}
		if _, err := m.Put(tx, "s", NumberKey(float64(n*100+j)).Wire(), val, false, ik); err != nil {
			m.Abort(tx)
			return err
		}
	}
	return m.Commit(tx)
}
