package media

import (
	"fmt"
	"os"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/h264"
)

func TestDbgTrace(t *testing.T) {
	f := os.Getenv("DBG_FILE")
	if f == "" {
		t.Skip()
	}
	es, _ := os.ReadFile(f)
	aus := esAccessUnits(es)
	d := h264.NewDecoder()
	h264.DebugSlice = func(s string) { fmt.Println(s) }
	h264.DebugPanic = func(v any, st []byte) { fmt.Printf("PANIC %v\n%s\n", v, st) }
	n := 25
	for i, au := range aus {
		if i >= n {
			break
		}
		fr, err := d.DecodeFrame(au)
		fmt.Printf("AU %d -> frame=%v poc=%d err=%v\n", i, fr != nil, d.OutPOC, err)
	}
}
