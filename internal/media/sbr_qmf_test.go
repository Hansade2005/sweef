package media

import (
	"math"
	"math/rand"
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

// The fast (FFT based) filterbanks must equal the defining sums.
func TestSBRQMFFastMatchesDirect(t *testing.T) {
	sbrQMFInit()
	rng := rand.New(rand.NewSource(1))
	// DCT-IV
	var x, got [64]float64
	for i := range x {
		x[i] = rng.NormFloat64()
	}
	sbrDCT4(&x, &got)
	for m := 0; m < 64; m++ {
		want := 0.0
		for k := 0; k < 64; k++ {
			want += x[k] * math.Cos(math.Pi/64*(float64(m)+0.5)*(float64(k)+0.5))
		}
		if math.Abs(want-got[m]) > 1e-9 {
			t.Fatalf("dct4[%d] = %v want %v", m, got[m], want)
		}
	}
	// analysis
	var an sbrAnalysis
	var ref sbrAnalysis
	for f := 0; f < 3; f++ {
		in := make([]float32, 1024)
		for i := range in {
			in[i] = float32(rng.NormFloat64() * 0.1)
		}
		var w, wr [32][32][2]float64
		an.run(in, &w)
		copy(ref.x[:288], ref.x[1024:1024+288])
		for i := 0; i < 1024; i++ {
			ref.x[288+i] = float64(in[i])
		}
		for l := 0; l < 32; l++ {
			xx := ref.x[32*l : 32*l+320]
			var u [64]float64
			for n := 0; n < 64; n++ {
				for j := 0; j < 5; j++ {
					u[n] += sbrWinDS[n+64*j] * xx[319-n-64*j]
				}
			}
			for k := 0; k < 32; k++ {
				var re, im float64
				for n := 0; n < 64; n++ {
					a := math.Pi / 64 * (float64(k) + 0.5) * (float64(2*n) + sbrAnaOff)
					re += u[n] * math.Cos(a)
					im += u[n] * math.Sin(a)
				}
				wr[l][k] = [2]float64{re * sbrAnaScale, im * sbrAnaScale}
			}
		}
		for l := 0; l < 32; l++ {
			for k := 0; k < 32; k++ {
				for c := 0; c < 2; c++ {
					if d := math.Abs(w[l][k][c] - wr[l][k][c]); d > 1e-6*(1+math.Abs(wr[l][k][c])) {
						t.Fatalf("analysis frame %d slot %d band %d: %v want %v", f, l, k, w[l][k], wr[l][k])
					}
				}
			}
		}
	}
	// synthesis
	var sy sbrSynthesis
	var v [1280]float64
	for f := 0; f < 3; f++ {
		var xs [38][64][2]float64
		for l := 0; l < 32; l++ {
			for k := 0; k < 64; k++ {
				xs[l][k] = [2]float64{rng.NormFloat64() * 1000, rng.NormFloat64() * 1000}
			}
		}
		out := make([]float32, 2048)
		sy.run(&xs, out, false)
		for l := 0; l < 32; l++ {
			copy(v[128:], v[:1152])
			for n := 0; n < 128; n++ {
				sum := 0.0
				for k := 0; k < 64; k++ {
					a := math.Pi / 128 * (float64(k) + 0.5) * (float64(2*n) + sbrSynOff)
					sum += xs[l][k][0]*math.Cos(a) - xs[l][k][1]*math.Sin(a)
				}
				v[n] = sum * sbrSynScale
			}
			for k := 0; k < 64; k++ {
				sum := 0.0
				for j := 0; j < 5; j++ {
					sum += v[256*j+k]*sbrWinUS[128*j+k] + v[256*j+192+k]*sbrWinUS[128*j+64+k]
				}
				if d := math.Abs(float64(out[l*64+k]) - sum); d > 1e-6*(1+math.Abs(sum)) {
					t.Fatalf("synthesis frame %d slot %d sample %d: %v want %v", f, l, k, out[l*64+k], sum)
				}
			}
		}
	}
}
