package mainthread

import "testing"

func TestDoRunsOnLoop(t *testing.T) {
	// The test binary's TestMain thread is not guaranteed to be the process
	// main thread, but the semantics (serialised, waited) are the same.
	var order []int
	Run(func() {
		for i := 0; i < 3; i++ {
			i := i
			if err := Do(func() { order = append(order, i) }); err != nil {
				t.Error(err)
			}
		}
		defer func() {
			if recover() == nil {
				t.Error("panic not propagated")
			}
		}()
		_ = Do(func() { panic("x") })
	})
	if len(order) != 3 || order[2] != 2 {
		t.Fatalf("order %v", order)
	}
	if Running() {
		t.Fatal("still running")
	}
	if err := Do(func() {}); err != ErrNotRunning {
		t.Fatal("expected ErrNotRunning")
	}
}
