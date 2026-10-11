package raster

import "math"

// Perlin turbulence exactly as in the SVG 1.1 reference implementation
// (the same code Skia's SkPerlinNoiseShader uses).

const (
	perlinBSize = 0x100
	perlinBM    = 0xff
	perlinN     = 0x1000
)

type turbGen struct {
	lat  [perlinBSize + perlinBSize + 2]int
	grad [4][perlinBSize + perlinBSize + 2][2]float64
}

func perlinRandom(seed int64) int64 {
	const m, a, q, r = 2147483647, 16807, 127773, 2836
	res := a*(seed%q) - r*(seed/q)
	if res <= 0 {
		res += m
	}
	return res
}

func newTurbGen(seed int64) *turbGen {
	const m = 2147483647
	g := &turbGen{}
	if seed <= 0 {
		seed = -(seed % (m - 1)) + 1
	}
	if seed > m-1 {
		seed = m - 1
	}
	var i, j int
	for k := 0; k < 4; k++ {
		for i = 0; i < perlinBSize; i++ {
			g.lat[i] = i
			for j = 0; j < 2; j++ {
				seed = perlinRandom(seed)
				g.grad[k][i][j] = float64(int(seed%(perlinBSize+perlinBSize))-perlinBSize) / perlinBSize
			}
			s := math.Sqrt(g.grad[k][i][0]*g.grad[k][i][0] + g.grad[k][i][1]*g.grad[k][i][1])
			g.grad[k][i][0] /= s
			g.grad[k][i][1] /= s
		}
	}
	for i = perlinBSize - 1; i > 0; i-- {
		k := g.lat[i]
		seed = perlinRandom(seed)
		j = int(seed % perlinBSize)
		g.lat[i] = g.lat[j]
		g.lat[j] = k
	}
	for i = 0; i < perlinBSize+2; i++ {
		g.lat[perlinBSize+i] = g.lat[i]
		for k := 0; k < 4; k++ {
			for j = 0; j < 2; j++ {
				g.grad[k][perlinBSize+i][j] = g.grad[k][i][j]
			}
		}
	}
	return g
}

type stitchInfo struct{ w, h, wrapX, wrapY int }

func (g *turbGen) noise2(ch int, vx, vy float64, st *stitchInfo) float64 {
	t := vx + perlinN
	bx0 := int(t) & perlinBM
	bx1 := (bx0 + 1) & perlinBM
	rx0 := t - float64(int(t))
	rx1 := rx0 - 1
	t = vy + perlinN
	by0 := int(t) & perlinBM
	by1 := (by0 + 1) & perlinBM
	ry0 := t - float64(int(t))
	ry1 := ry0 - 1
	if st != nil {
		if bx0 >= st.wrapX {
			bx0 -= st.w
		}
		if bx1 >= st.wrapX {
			bx1 -= st.w
		}
		if by0 >= st.wrapY {
			by0 -= st.h
		}
		if by1 >= st.wrapY {
			by1 -= st.h
		}
	}
	bx0 &= perlinBM
	bx1 &= perlinBM
	by0 &= perlinBM
	by1 &= perlinBM
	i, j := g.lat[bx0], g.lat[bx1]
	b00, b10 := g.lat[i+by0], g.lat[j+by0]
	b01, b11 := g.lat[i+by1], g.lat[j+by1]
	sx := rx0 * rx0 * (3 - 2*rx0)
	sy := ry0 * ry0 * (3 - 2*ry0)
	q := g.grad[ch][b00]
	u := rx0*q[0] + ry0*q[1]
	q = g.grad[ch][b10]
	v := rx1*q[0] + ry0*q[1]
	a := u + sx*(v-u)
	q = g.grad[ch][b01]
	u = rx0*q[0] + ry1*q[1]
	q = g.grad[ch][b11]
	v = rx1*q[0] + ry1*q[1]
	b := u + sx*(v-u)
	return a + sy*(b-a)
}

// turbulence evaluates one channel at point (px, py) (filter user space).
func (g *turbGen) turbulence(ch int, px, py, bfx, bfy float64, octaves int, fractal, stitch bool, tx, ty, tw, th float64) float64 {
	var st *stitchInfo
	var si stitchInfo
	if stitch {
		if bfx != 0 {
			lo := math.Floor(tw*bfx) / tw
			hi := math.Ceil(tw*bfx) / tw
			if bfx/lo < hi/bfx {
				bfx = lo
			} else {
				bfx = hi
			}
		}
		if bfy != 0 {
			lo := math.Floor(th*bfy) / th
			hi := math.Ceil(th*bfy) / th
			if bfy/lo < hi/bfy {
				bfy = lo
			} else {
				bfy = hi
			}
		}
		st = &si
		si.w = int(tw*bfx + 0.5)
		si.wrapX = int(tx*bfx + perlinN + float64(si.w))
		si.h = int(th*bfy + 0.5)
		si.wrapY = int(ty*bfy + perlinN + float64(si.h))
	}
	sum := 0.0
	vx, vy := px*bfx, py*bfy
	ratio := 1.0
	for o := 0; o < octaves; o++ {
		n := g.noise2(ch, vx, vy, st)
		if fractal {
			sum += n / ratio
		} else {
			sum += math.Abs(n) / ratio
		}
		vx *= 2
		vy *= 2
		ratio *= 2
		if st != nil {
			st.w *= 2
			st.wrapX = 2*st.wrapX - perlinN
			st.h *= 2
			st.wrapY = 2*st.wrapY - perlinN
		}
	}
	return sum
}
