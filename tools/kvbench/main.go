package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	bolt "go.etcd.io/bbolt"
)

type eng interface {
	Open(dir string) error
	Close()
	Put(kvs [][2][]byte, sync bool) error
	Get(k []byte) ([]byte, error)
	Scan(lo []byte, fn func(k, v []byte) bool)
}

// ---- bbolt
type bb struct{ db *bolt.DB }

var bucket = []byte("d")

func (e *bb) Open(dir string) error {
	os.MkdirAll(dir, 0o755)
	bo := &bolt.Options{}
	if os.Getenv("BBOPT") != "default" {
		bo = &bolt.Options{NoFreelistSync: true, FreelistType: bolt.FreelistMapType}
	}
	db, err := bolt.Open(dir+"/kv.db", 0o600, bo)
	if err != nil {
		return err
	}
	e.db = db
	return db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucketIfNotExists(bucket); return err })
}
func (e *bb) Close() { e.db.Close() }
func (e *bb) Put(kvs [][2][]byte, sync bool) error {
	e.db.NoSync = !sync
	return e.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		for _, kv := range kvs {
			if err := b.Put(kv[0], kv[1]); err != nil {
				return err
			}
		}
		return nil
	})
}
func (e *bb) Get(k []byte) (v []byte, err error) {
	err = e.db.View(func(tx *bolt.Tx) error { v = append([]byte(nil), tx.Bucket(bucket).Get(k)...); return nil })
	return
}
func (e *bb) Scan(lo []byte, fn func(k, v []byte) bool) {
	e.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucket).Cursor()
		for k, v := c.Seek(lo); k != nil; k, v = c.Next() {
			if !fn(k, v) {
				break
			}
		}
		return nil
	})
}

// ---- goleveldb
type ld struct{ db *leveldb.DB }

func (e *ld) Open(dir string) error {
	db, err := leveldb.OpenFile(dir, &opt.Options{BlockCacheCapacity: 8 << 20, WriteBuffer: 4 << 20, OpenFilesCacheCapacity: 64, Compression: opt.SnappyCompression})
	e.db = db
	return err
}
func (e *ld) Close() { e.db.Close() }
func (e *ld) Put(kvs [][2][]byte, sync bool) error {
	var b leveldb.Batch
	for _, kv := range kvs {
		b.Put(kv[0], kv[1])
	}
	return e.db.Write(&b, &opt.WriteOptions{Sync: sync})
}
func (e *ld) Get(k []byte) ([]byte, error) { return e.db.Get(k, nil) }
func (e *ld) Scan(lo []byte, fn func(k, v []byte) bool) {
	it := e.db.NewIterator(nil, nil)
	defer it.Release()
	for ok := it.Seek(lo); ok; ok = it.Next() {
		if !fn(it.Key(), it.Value()) {
			break
		}
	}
}

func newEng(n string) eng {
	switch n {
	case "bbolt":
		return &bb{}
	case "leveldb":
		return &ld{}
	}
	return newExtra(n)
}

func key(i uint64) []byte {
	k := make([]byte, 16)
	binary.BigEndian.PutUint64(k, i)
	return k
}

// scramble is a bijection on uint64 used to make insert order random
func scramble(i uint64) uint64 {
	i ^= i >> 33
	i *= 0xff51afd7ed558ccd
	i ^= i >> 33
	i *= 0xc4ceb9fe1a85ec53
	i ^= i >> 33
	return i
}

func status() string {
	f, _ := os.Open("/proc/self/status")
	defer f.Close()
	sc := bufio.NewScanner(f)
	var out []string
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "VmRSS") || strings.HasPrefix(l, "RssAnon") || strings.HasPrefix(l, "RssFile") || strings.HasPrefix(l, "VmHWM") {
			out = append(out, strings.Join(strings.Fields(l), " "))
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out = append(out, fmt.Sprintf("goHeapInuse=%dMiB", ms.HeapInuse>>20))
	return strings.Join(out, "  ")
}

func dirSize(dir string) int64 {
	var n int64
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && !e.IsDir() {
			n += fi.Size()
		}
	}
	return n
}

func main() {
	engine := flag.String("e", "bbolt", "")
	dir := flag.String("d", os.TempDir()+"/kvb", "")
	phase := flag.String("p", "fill", "fill|fillrand|read|scan|open|durable|crashw|crashv")
	n := flag.Int("n", 1000000, "")
	vsz := flag.Int("v", 1000, "")
	batch := flag.Int("b", 1000, "")
	flag.Parse()
	e := newEng(*engine)
	t0 := time.Now()
	if err := e.Open(*dir); err != nil {
		panic(err)
	}
	if *phase == "open" {
		fmt.Printf("%s open: %v\n", *engine, time.Since(t0))
		t1 := time.Now()
		v, _ := e.Get(key(uint64(*n / 2)))
		fmt.Printf("first get after open: %v (len %d)\n", time.Since(t1), len(v))
		fmt.Println(status())
		e.Close()
		return
	}
	rng := rand.New(rand.NewSource(1))
	val := make([]byte, *vsz)
	switch *phase {
	case "fill", "fillrand":
		t := time.Now()
		var kvs [][2][]byte
		for i := 0; i < *n; i++ {
			rng.Read(val)
			id := uint64(i)
			if *phase == "fillrand" {
				id = scramble(id) >> 1
			}
			kvs = append(kvs, [2][]byte{key(id), append([]byte(nil), val...)})
			if len(kvs) == *batch {
				if err := e.Put(kvs, false); err != nil {
					panic(err)
				}
				kvs = kvs[:0]
			}
		}
		if len(kvs) > 0 {
			e.Put(kvs, false)
		}
		d := time.Since(t)
		fmt.Printf("%s %s n=%d v=%dB batch=%d nosync: %v  %.0f puts/s  %.1f MiB/s\n", *engine, *phase, *n, *vsz, *batch, d, float64(*n)/d.Seconds(), float64(*n**vsz)/d.Seconds()/(1<<20))
		fmt.Println(status())
		t = time.Now()
		e.Close()
		fmt.Printf("close: %v  on-disk=%.1f MiB\n", time.Since(t), float64(dirSize(*dir))/(1<<20))
	case "durable":
		// small transactions, fsync on every commit
		t := time.Now()
		lat := make([]time.Duration, 0, *n)
		for i := 0; i < *n; i++ {
			kvs := [][2][]byte{}
			for j := 0; j < *batch; j++ {
				rng.Read(val)
				kvs = append(kvs, [2][]byte{key(scramble(uint64(i*1000+j+7777777)) >> 1), append([]byte(nil), val...)})
			}
			t1 := time.Now()
			if err := e.Put(kvs, true); err != nil {
				panic(err)
			}
			lat = append(lat, time.Since(t1))
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		fmt.Printf("%s durable commits=%d x %d puts: total %v  p50=%v p99=%v\n", *engine, *n, *batch, time.Since(t), lat[len(lat)/2], lat[len(lat)*99/100])
		e.Close()
	case "read":
		total := uint64(*n)
		lat := make([]time.Duration, 0, 200000)
		t := time.Now()
		miss := 0
		for i := 0; i < 200000; i++ {
			k := key(uint64(rng.Int63n(int64(total))))
			t1 := time.Now()
			v, _ := e.Get(k)
			lat = append(lat, time.Since(t1))
			if len(v) == 0 {
				miss++
			}
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		fmt.Printf("%s random get 200k: %v p50=%v p99=%v max=%v miss=%d\n", *engine, time.Since(t), lat[len(lat)/2], lat[len(lat)*99/100], lat[len(lat)-1], miss)
		fmt.Println(status())
		e.Close()
	case "scan":
		t := time.Now()
		cnt, bytesN := 0, 0
		e.Scan(nil, func(k, v []byte) bool { cnt++; bytesN += len(k) + len(v); return true })
		d := time.Since(t)
		fmt.Printf("%s full scan: %d recs %v %.0f recs/s %.1f MiB/s\n", *engine, cnt, d, float64(cnt)/d.Seconds(), float64(bytesN)/d.Seconds()/(1<<20))
		fmt.Println(status())
		e.Close()
	case "crashw":
		// writer: each commit writes batch keys c<seq>-<j> with value = seq; reports acked seq on stdout
		for seq := *n; ; seq++ {
			kvs := [][2][]byte{}
			for j := 0; j < *batch; j++ {
				v := make([]byte, *vsz)
				binary.BigEndian.PutUint64(v, uint64(seq))
				kvs = append(kvs, [2][]byte{[]byte(fmt.Sprintf("c%09d-%04d", seq, j)), v})
			}
			if err := e.Put(kvs, false); err != nil {
				panic(err)
			}
			fmt.Println(seq)
		}
	case "crashv":
		// verifier: every seq group must be complete or absent; return highest
		groups := map[uint64]int{}
		e.Scan([]byte("c"), func(k, v []byte) bool {
			if !bytes.HasPrefix(k, []byte("c")) {
				return false
			}
			groups[binary.BigEndian.Uint64(v)]++
			return true
		})
		maxSeq, torn := uint64(0), 0
		for s, c := range groups {
			if c != *batch {
				torn++
			}
			if s > maxSeq {
				maxSeq = s
			}
		}
		fmt.Printf("%s verify: groups=%d max=%d torn=%d\n", *engine, len(groups), maxSeq, torn)
		e.Close()
	}
}
