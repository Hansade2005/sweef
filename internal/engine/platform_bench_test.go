package engine

import "testing"

// BenchmarkBuildVM is the cost of one pooled VM: host registration plus
// loading the prelude bytecode. Run it before and after adding prelude code:
//
//	go test ./internal/engine -run xxx -bench BuildVM -benchtime 40x
func BenchmarkBuildVM(b *testing.B) {
	o := DefaultOptions()
	compiledPrelude()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j, err := buildVM(o)
		if err != nil {
			b.Fatal(err)
		}
		j.close()
	}
}
