package store

import (
	"testing"
	"time"
)

func TestDbgSlow(t *testing.T) {
	h, queries := synthHistory(100_000)
	h.Search("warm", 8)
	for _, q := range queries {
		t0 := time.Now()
		r := h.Search(q, 8)
		d := time.Since(t0)
		if d > 400*time.Microsecond {
			t.Logf("%q %v results=%d", q, d, len(r))
		}
	}
}
