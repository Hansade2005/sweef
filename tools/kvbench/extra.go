package main

import (
	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/bloom"
)

type pb struct{ db *pebble.DB }

func newExtra(n string) eng { return &pb{} }

func (e *pb) Open(dir string) error {
	opts := &pebble.Options{Cache: pebble.NewCache(8 << 20), MemTableSize: 4 << 20}
	opts.Levels[0].FilterPolicy = bloom.FilterPolicy(10)
	opts.EnsureDefaults()
	db, err := pebble.Open(dir, opts)
	e.db = db
	return err
}
func (e *pb) Close() { e.db.Close() }
func (e *pb) Put(kvs [][2][]byte, sync bool) error {
	b := e.db.NewBatch()
	for _, kv := range kvs {
		if err := b.Set(kv[0], kv[1], nil); err != nil {
			return err
		}
	}
	o := pebble.NoSync
	if sync {
		o = pebble.Sync
	}
	return b.Commit(o)
}
func (e *pb) Get(k []byte) ([]byte, error) {
	v, c, err := e.db.Get(k)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), v...)
	c.Close()
	return out, nil
}
func (e *pb) Scan(lo []byte, fn func(k, v []byte) bool) {
	it, _ := e.db.NewIter(nil)
	defer it.Close()
	for ok := it.SeekGE(lo); ok; ok = it.Next() {
		if !fn(it.Key(), it.Value()) {
			break
		}
	}
}
