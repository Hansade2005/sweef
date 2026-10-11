package media

import (
	"math"
	"math/cmplx"
	"sync"
)

// imdctPlan computes y[n] = sum_k X[k] cos(pi/M (n+1/2+M/2)(k+1/2)), n < 2M,
// through a DCT-IV evaluated with an M/2-point complex FFT.
type imdctPlan struct {
	m    int
	tw   []complex128 // exp(-i*pi*(4n+1)/(4M))
	post []complex128 // exp(-i*pi*k/M)
	fftW []complex128 // exp(-2*pi*i*k/(M/2))
	rev  []int
	t    []complex128
	u    []float64
}

var (
	imdctMu    sync.Mutex
	imdctPlans = map[int]*imdctPlan{}
)

func newIMDCT(m int) *imdctPlan {
	imdctMu.Lock()
	defer imdctMu.Unlock()
	if p, ok := imdctPlans[m]; ok {
		return p
	}
	n := m / 2
	p := &imdctPlan{m: m, tw: make([]complex128, n), post: make([]complex128, n), fftW: make([]complex128, n/2+1), rev: make([]int, n)}
	for i := 0; i < n; i++ {
		p.tw[i] = cmplx.Exp(complex(0, -math.Pi*float64(4*i+1)/float64(4*m)))
		p.post[i] = cmplx.Exp(complex(0, -math.Pi*float64(i)/float64(m)))
	}
	for k := range p.fftW {
		p.fftW[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n)))
	}
	bitsN := 0
	for 1<<uint(bitsN) < n {
		bitsN++
	}
	for i := 0; i < n; i++ {
		r := 0
		for b := 0; b < bitsN; b++ {
			if i>>uint(b)&1 != 0 {
				r |= 1 << uint(bitsN-1-b)
			}
		}
		p.rev[i] = r
	}
	imdctPlans[m] = p
	return p
}

// a plan holds scratch buffers, so each decoder gets its own copy.
func (p *imdctPlan) clone() *imdctPlan {
	q := *p
	q.t = make([]complex128, p.m/2)
	q.u = make([]float64, p.m)
	return &q
}

func (p *imdctPlan) fft(a []complex128) {
	n := len(a)
	for i := 0; i < n; i++ {
		if j := p.rev[i]; j > i {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size / 2
		step := n / size
		for s := 0; s < n; s += size {
			for k := 0; k < half; k++ {
				w := p.fftW[k*step]
				x, y := a[s+k], a[s+k+half]*w
				a[s+k], a[s+k+half] = x+y, x-y
			}
		}
	}
}

// run writes the 2M output samples of X (length M) into y.
func (p *imdctPlan) run(x []float64, y []float64) {
	m := p.m
	n := m / 2
	t := p.t
	for i := 0; i < n; i++ {
		t[i] = complex(x[2*i], x[m-1-2*i]) * p.tw[i]
	}
	p.fft(t)
	u := p.u
	for k := 0; k < n; k++ {
		c := t[k] * p.post[k]
		u[2*k] = real(c)
		u[m-1-2*k] = -imag(c)
	}
	h := m / 2
	for i := 0; i < 2*m; i++ {
		mm := i + h
		switch {
		case mm < m:
			y[i] = u[mm]
		case mm < 2*m:
			y[i] = -u[2*m-1-mm]
		default:
			y[i] = -u[mm-2*m]
		}
	}
}

func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 60; k++ {
		term *= (x / 2) / float64(k)
		sum += term * term
	}
	return sum
}

// aacWindows holds the rising half of every AAC window: [shape][short?][n].
var (
	aacWinOnce sync.Once
	aacWin     [2][2][]float64 // shape 0 sine, 1 KBD; index 0 long(1024) 1 short(128)
)

func aacInitWindows() {
	aacWinOnce.Do(func() {
		for si, half := range []int{1024, 128} {
			aacWin[0][si] = make([]float64, half)
			for n := 0; n < half; n++ {
				aacWin[0][si][n] = math.Sin(math.Pi * (float64(n) + 0.5) / float64(2*half))
			}
			alpha := 4.0
			if si == 1 {
				alpha = 6.0
			}
			k := make([]float64, half+1)
			var tot float64
			for j := 0; j <= half; j++ {
				r := 2*float64(j)/float64(half) - 1
				k[j] = besselI0(math.Pi * alpha * math.Sqrt(1-r*r))
				tot += k[j]
			}
			aacWin[1][si] = make([]float64, half)
			var acc float64
			for n := 0; n < half; n++ {
				acc += k[n]
				aacWin[1][si][n] = math.Sqrt(acc / tot)
			}
		}
	})
}
