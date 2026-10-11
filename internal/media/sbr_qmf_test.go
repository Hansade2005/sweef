package media

import (
	"math"
	"testing"
)

func TestSBRQMFIdentity(t *testing.T) {
	for _, down := range []bool{false, true} {
		var an sbrAnalysis
		var sy sbrSynthesis
		var all []float32
		for f := 0; f < 8; f++ {
			in := make([]float32, 1024)
			for i := range in {
				n := float64(f*1024 + i)
				in[i] = float32(0.5 * math.Sin(2*math.Pi*1000*n/24000))
			}
			var w [32][32][2]float64
			an.run(in, &w)
			var x [38][64][2]float64
			for l := 0; l < 32; l++ {
				for k := 0; k < 32; k++ {
					x[l][k] = w[l][k]
				}
			}
			n := 2048
			if down {
				n = 1024
			}
			out := make([]float32, n)
			sy.run(&x, out, down)
			all = append(all, out...)
		}
		// rms of last frames
		var e float64
		cnt := 0
		for _, v := range all[len(all)*3/4:] {
			e += float64(v) * float64(v)
			cnt++
		}
		t.Logf("down=%v rms=%.5f expected %.5f", down, math.Sqrt(e/float64(cnt)), 0.5/math.Sqrt2)
	}
}
