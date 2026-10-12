package main

import (
	"fmt"
	"os"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

func main() {
	e := engine.New(engine.Options{ViewportWidth: 600, ViewportHeight: 300, CacheDir: "off", SettleMs: 100, ConsoleRingSize: 50, NetworkRingSize: 50, StateRingSize: 50})
	defer e.Close()
	b, _ := os.ReadFile(os.Args[1])
	t, _ := e.NewTab("")
	t.SetViewport(600, 300, 1, false, false)
	t.SetDocument(string(b), "http://x.test/")
	t.Settle(200 * time.Millisecond)
	for _, ev := range t.ConsoleLog(0) {
		fmt.Println(ev.Data)
	}
	fmt.Println("title:", t.Title())
	os.WriteFile(os.Args[2], t.PNGScreenshot(600, 300, 1), 0644)
}
