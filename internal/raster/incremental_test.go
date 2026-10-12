package raster

import (
	"math/rand"
	"testing"
)

func lcsLen(a, b []uint64) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	return dp[len(a)][len(b)]
}

func TestLCSMatch(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for it := 0; it < 3000; it++ {
		alpha := 1 + rng.Intn(6)
		a := make([]uint64, rng.Intn(40))
		for i := range a {
			a[i] = uint64(rng.Intn(alpha))
		}
		b := append([]uint64(nil), a...)
		for e := rng.Intn(8); e > 0; e-- {
			switch rng.Intn(3) {
			case 0:
				if len(b) > 0 {
					i := rng.Intn(len(b))
					b = append(b[:i], b[i+1:]...)
				}
			case 1:
				i := rng.Intn(len(b) + 1)
				b = append(b[:i], append([]uint64{uint64(rng.Intn(alpha))}, b[i:]...)...)
			default:
				if len(b) > 0 {
					b[rng.Intn(len(b))] = uint64(rng.Intn(alpha))
				}
			}
		}
		ma, mb, ok := lcsMatch(a, b, 1000)
		if !ok {
			t.Fatal("not ok")
		}
		var xa, xb []uint64
		for i, m := range ma {
			if m {
				xa = append(xa, a[i])
			}
		}
		for i, m := range mb {
			if m {
				xb = append(xb, b[i])
			}
		}
		if len(xa) != len(xb) {
			t.Fatalf("it %d: matched counts differ %d vs %d\n%v\n%v", it, len(xa), len(xb), a, b)
		}
		for i := range xa {
			if xa[i] != xb[i] {
				t.Fatalf("it %d: matched sequences differ", it)
			}
		}
		if want := lcsLen(a, b); len(xa) != want {
			t.Fatalf("it %d: matched %d, LCS is %d\n%v\n%v", it, len(xa), want, a, b)
		}
	}
}
