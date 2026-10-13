package vp8enc

import (
	"fmt"
	"testing"
)

func benchEncode(b *testing.B, w, h, kbps, speed int, kind seqKind) {
	gen := newSeq(w, h, kind)
	const n = 30
	frames := make([]*Image, n)
	for i := range frames {
		frames[i] = gen.frame(i)
	}
	enc, err := New(Config{Width: w, Height: h, Bitrate: kbps * 1000, FrameRate: 30, Speed: speed})
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 5; i++ { // warm up: key frame and rate control
		enc.Encode(frames[i])
	}
	b.ResetTimer()
	c0 := cpuTime()
	for i := 0; i < b.N; i++ {
		enc.Encode(frames[(i+5)%n])
	}
	b.ReportMetric(float64(cpuTime()-c0)/float64(b.N)/1e6, "cpu-ms/frame")
}

func BenchmarkEncode(b *testing.B) {
	for _, c := range []struct{ w, h, kbps int }{{640, 360, 800}, {1280, 720, 1500}} {
		for _, sp := range []int{0, 1, 2} {
			b.Run(fmt.Sprintf("%dx%d/speed%d", c.w, c.h, sp), func(b *testing.B) { benchEncode(b, c.w, c.h, c.kbps, sp, seqNatural) })
		}
	}
}
