package media

import (
	"math"
	"math/rand"
	"testing"
)

func TestUSACMDCT(t *testing.T) {
	for _, m := range []int{96, 128, 192, 256, 384, 512, 768, 1024, 640} {
		p := newUsacMDCT(m)
		x := make([]float64, m)
		for i := range x {
			x[i] = rand.Float64()*2 - 1
		}
		y := make([]float64, 2*m)
		p.run(x, y)
		var maxe float64
		for n := 0; n < 2*m; n += 1 + m/37 {
			var s float64
			for k := 0; k < m; k++ {
				s += x[k] * math.Cos(math.Pi/float64(m)*(float64(n)+0.5+float64(m)/2)*(float64(k)+0.5))
			}
			if e := math.Abs(s - y[n]); e > maxe {
				maxe = e
			}
		}
		if maxe > 1e-9 {
			t.Errorf("m=%d max error %g", m, maxe)
		}
	}
}
