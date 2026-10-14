package engine

import (
	"runtime"
	"sync"
	"testing"
)

func benchBuild(b *testing.B) {
	o := DefaultOptions()
	compiledPrelude()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Collect the previous VM outside the timed region, or its garbage is charged to the next build.
		b.StopTimer()
		runtime.GC()
		b.StartTimer()
		j, err := buildVM(o)
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		j.close()
		b.StartTimer()
	}
}

// BenchmarkBuildVM is the cost of one pooled VM: host registration plus
// loading the prelude bytecode. Run it before and after adding prelude code:
//
//	go test ./internal/engine -run xxx -bench BuildVM -benchtime 40x
func BenchmarkBuildVM(b *testing.B) { benchBuild(b) }

// BenchmarkBuildVMWithoutPlatform is the same with the web-platform layer
// (platform.go) taken out of the prelude and the host table, so the two
// benchmarks can be run back to back to see what the layer costs at start-up.
func BenchmarkBuildVMWithoutPlatform(b *testing.B) {
	extMu.Lock()
	src, hostFn := preludeZExtras["platform"], hostExtensions["platform"]
	delete(preludeZExtras, "platform")
	delete(hostExtensions, "platform")
	extMu.Unlock()
	preludeOnce, preludeBytecode = sync.Once{}, nil
	defer func() {
		extMu.Lock()
		preludeZExtras["platform"], hostExtensions["platform"] = src, hostFn
		extMu.Unlock()
		preludeOnce, preludeBytecode = sync.Once{}, nil
	}()
	benchBuild(b)
}
