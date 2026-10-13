package main

import (
	"fmt"
	"os"
)

func main() {
	f, err := os.OpenFile(os.Args[1], os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		panic(err)
	}
	f.WriteString("v")
	f.Close()
	fmt.Println("v")
}
