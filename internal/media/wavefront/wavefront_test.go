package wavefront

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A wavefront of rows, each unit waiting for the unit two ahead in the row above, finishes with every unit
// processed after what it depends on (also with more rows than CPUs and under the race detector).
func TestWavefrontOrder(t *testing.T) {
	const rows, cols = 24, 40
	for round := 0; round < 20; round++ {
		prog := New(rows)
		var stamp [rows][cols]atomic.Int64
		var clock atomic.Int64
		var next atomic.Int32
		var wg sync.WaitGroup
		for w := 0; w < 6; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					r := int(next.Add(1)) - 1
					if r >= rows {
						return
					}
					for c := 0; c < cols; c++ {
						if r > 0 {
							prog.Wait(r-1, min(c+2, cols))
							need := min(c+1, cols-1)
							if stamp[r-1][need].Load() == 0 {
								t.Errorf("row %d unit %d ran before row %d unit %d", r, c, r-1, need)
							}
						}
						if c%7 == 0 {
							time.Sleep(time.Microsecond) // let the next row catch up and sleep
						}
						stamp[r][c].Store(clock.Add(1))
						prog.Done(r, c+1)
					}
				}
			}()
		}
		wg.Wait()
	}
}

func TestWaitReturnsImmediatelyWhenDone(t *testing.T) {
	p := New(2)
	p.Done(0, 5)
	done := make(chan struct{})
	go func() {
		p.Wait(0, 3)
		p.Wait(0, 5)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait blocked on units that are done")
	}
}
