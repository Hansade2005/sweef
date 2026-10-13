package main

import (
	"fmt"
	"os"

	"github.com/syndtr/goleveldb/leveldb"
)

func main() {
	db, err := leveldb.OpenFile(os.Args[1], nil)
	if err != nil {
		panic(err)
	}
	db.Put([]byte("k"), []byte("v"), nil)
	v, _ := db.Get([]byte("k"), nil)
	fmt.Println(string(v))
	db.Close()
}
