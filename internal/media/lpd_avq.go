package media

// Algebraic vector quantisation (RE8 lattice, Gosset) decoding of ISO/IEC
// 23003-3 7.15, used for the LPC parameters and the FAC data of the LPD core.
// The structure follows libxaac's ixheaacd_avq_dec.c / ixheaacd_lpc_dec.c
// (Apache-2.0, see NOTICE).

var lpdFactorial7 = [8]int{5040, 720, 120, 24, 6, 2, 1, 1}

func lpdNearestNeighbor(x []int, y []int, count uint, rem []int) {
	var remT [8]int
	copy(remT[:], rem[:8])
	sum := 0
	for i := 0; i < 8; i++ {
		if x[i] < 0 {
			y[i] = -(((1 - x[i]) >> 1) << 1)
		} else {
			y[i] = ((1 + x[i]) >> 1) << 1
		}
		sum += y[i]
		if x[i]%2 != 0 {
			if x[i] < 0 {
				remT[i] = -(remT[i] - (1 << count))
			} else {
				remT[i] -= 1 << count
			}
		}
	}
	if sum%4 != 0 {
		em, j := 0, 0
		for i := 0; i < 8; i++ {
			s := remT[i]
			if s < 0 {
				s = -s
			}
			if em < s {
				em, j = s, i
			}
		}
		if remT[j] < 0 {
			y[j] -= 2
			remT[j] += 2 << count
		} else {
			y[j] += 2
			remT[j] -= 2 << count
		}
	}
	copy(rem[:8], remT[:])
}

func lpdVoronoiSearch(x []int, y []int, count uint, rem1, rem2 []int) {
	var y0, y1, x1 [8]int
	lpdNearestNeighbor(x, y0[:], count, rem1)
	for i := 0; i < 8; i++ {
		if x[i] == 0 {
			if rem2[i] == 0 {
				x1[i] = x[i] - 1
			} else {
				x1[i] = 0
				rem2[i] -= 1 << count
			}
		} else {
			x1[i] = x[i] - 1
		}
	}
	lpdNearestNeighbor(x1[:], y1[:], count, rem2)
	for i := 0; i < 8; i++ {
		y1[i]++
	}
	e0, e1 := 0, 0
	for i := 0; i < 8; i++ {
		e0 += rem1[i] * rem1[i]
		e1 += rem2[i] * rem2[i]
	}
	if e0 < e1 {
		copy(y[:8], y0[:])
	} else {
		copy(y[:8], y1[:])
	}
}

func lpdVoronoiIdxDec(kv []int, m int, y []int, count uint) {
	var v, z, rem1, rem2 [8]int
	for i := 0; i < 8; i++ {
		y[i] = kv[7]
	}
	z[7] = y[7] >> count
	rem1[7] = y[7] & (m - 1)
	sum := 0
	for i := 6; i >= 1; i-- {
		tmp := kv[i] << 1
		sum += tmp
		y[i] += tmp
		z[i] = y[i] >> count
		rem1[i] = y[i] & (m - 1)
	}
	y[0] += 4*kv[0] + sum
	z[0] = (y[0] - 2) >> count
	if m != 0 {
		rem1[0] = (y[0] - 2) % m
	} else {
		rem1[0] = y[0] - 2
	}
	rem2 = rem1
	lpdVoronoiSearch(z[:], v[:], count, rem1[:], rem2[:])
	for i := 0; i < 8; i++ {
		y[i] -= m * v[i]
	}
}

func lpdRankOfPermutation(rank int, xs []int) {
	var a, w [8]int
	j := 0
	w[j] = 1
	a[j] = xs[0]
	base := 1
	for i := 1; i < 8; i++ {
		if xs[i] != xs[i-1] {
			j++
			w[j] = 1
			a[j] = xs[i]
		} else {
			w[j]++
			base *= w[j]
		}
	}
	if w[0] == 8 {
		for i := 0; i < 8; i++ {
			xs[i] = a[0]
		}
		return
	}
	target := rank * base
	facB := 1
	for i := 0; i < 8; i++ {
		fac := facB * lpdFactorial7[i]
		j = -1
		for {
			j++
			target -= w[j] * fac
			if target < 0 {
				break
			}
		}
		xs[i] = a[j]
		target += w[j] * fac
		facB *= w[j]
		w[j]--
	}
}

func lpdAbsLeaderIdx(table []uint32, ind uint32, size int) int {
	i := 4
	for ; i < size; i += 4 {
		if ind < table[i] {
			break
		}
	}
	if i > size {
		i = size
	}
	if ind < table[i-2] {
		i -= 2
	}
	if ind < table[i-1] {
		i--
	}
	return i - 1
}

func lpdGossetDecodeBase(n int, ind uint32, ya []int) {
	if n < 2 {
		for i := 0; i < 8; i++ {
			ya[i] = 0
		}
		return
	}
	var idx int
	switch n {
	case 2, 3:
		i := lpdAbsLeaderIdx(usacCardOffI3[:], ind, 9)
		idx = int(usacPosAbsLeadersA3[i])
	case 4:
		i := lpdAbsLeaderIdx(usacCardOffI4[:], ind, 28)
		idx = int(usacPosAbsLeadersA4[i])
	}
	for i := 0; i < 8; i++ {
		ya[i] = int(usacAbsLeaderDa[idx*8+i])
	}
	t := int(usacIsoCodeIndex[idx])
	im := int(usacIsoCodeNum[idx])
	ks := lpdAbsLeaderIdx(usacSignedLeaderIs[t:], ind, im)
	signCode := 2 * int(usacIsoCodeData[t+ks])
	for i := 7; i >= 0; i-- {
		ya[i] *= 1 - (signCode & 2)
		signCode >>= 1
	}
	rank := int(ind) - int(usacSignedLeaderIs[t+ks])
	lpdRankOfPermutation(rank, ya)
}

// lpdRotatedGosset decodes one 8-dimensional AVQ vector.
func lpdRotatedGosset(qn int, idx uint32, kv []int, b []int) {
	if qn <= 4 {
		lpdGossetDecodeBase(qn, idx, b)
		return
	}
	count := uint(0)
	for qn > 4 {
		count++
		qn -= 2
	}
	m := 1 << count
	var c [8]int
	lpdGossetDecodeBase(qn, idx, b)
	lpdVoronoiIdxDec(kv, m, c[:], count)
	for i := 0; i < 8; i++ {
		b[i] = m*b[i] + c[i]
	}
}
