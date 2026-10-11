package media

import (
	"math"
	"math/rand"
	"testing"
)

func TestIMDCTMatchesDirect(t *testing.T) {
	for _, m := range []int{16, 64, 128, 256, 512, 1024} {
		p := newIMDCT(m).clone()
		x := make([]float64, m)
		for i := range x {
			x[i] = rand.Float64() - 0.5
		}
		y := make([]float64, 2*m)
		p.run(x, y)
		var maxErr float64
		for n := 0; n < 2*m; n++ {
			var s float64
			for k := 0; k < m; k++ {
				s += x[k] * math.Cos(math.Pi/float64(m)*(float64(n)+0.5+float64(m)/2)*(float64(k)+0.5))
			}
			maxErr = math.Max(maxErr, math.Abs(s-y[n]))
		}
		if maxErr > 1e-9 {
			t.Errorf("m=%d max error %g", m, maxErr)
		}
	}
}
