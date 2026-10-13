package main

import (
	"fmt"
	"os"

	"github.com/cockroachdb/pebble/v2"
)

func main() {
	db, err := pebble.Open(os.Args[1], &pebble.Options{})
	if err != nil {
		panic(err)
	}
	db.Set([]byte("k"), []byte("v"), pebble.Sync)
	v, c, _ := db.Get([]byte("k"))
	fmt.Println(string(v))
	c.Close()
	db.Close()
}
