package main

import (
	"fmt"
	"os"

	bolt "go.etcd.io/bbolt"
)

func main() {
	db, err := bolt.Open(os.Args[1], 0o600, nil)
	if err != nil {
		panic(err)
	}
	db.Update(func(tx *bolt.Tx) error {
		b, _ := tx.CreateBucketIfNotExists([]byte("a"))
		return b.Put([]byte("k"), []byte("v"))
	})
	db.View(func(tx *bolt.Tx) error { fmt.Println(string(tx.Bucket([]byte("a")).Get([]byte("k")))); return nil })
	db.Close()
}
