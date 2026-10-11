package h264

import "testing"

func checkPrefixFree(t *testing.T, name string, lens, codes []uint8) {
	type cw struct {
		code uint32
		l    uint
	}
	var all []cw
	var kraft float64
	for i := range lens {
		if lens[i] == 0 {
			continue
		}
		all = append(all, cw{uint32(codes[i]), uint(lens[i])})
		kraft += 1 / float64(uint64(1)<<lens[i])
	}
	for i := range all {
		if all[i].code >= 1<<all[i].l {
			t.Errorf("%s: code %d too long for len %d", name, all[i].code, all[i].l)
		}
		for j := range all {
			if i == j {
				continue
			}
			a, b := all[i], all[j]
			if a.l <= b.l && b.code>>(b.l-a.l) == a.code {
				t.Errorf("%s: code %d/%d is a prefix of %d/%d", name, a.code, a.l, b.code, b.l)
			}
		}
	}
	if kraft > 1.0000001 {
		t.Errorf("%s: kraft sum %f > 1", name, kraft)
	}
	t.Logf("%s: kraft %f", name, kraft)
}

func TestVLCTablesPrefixFree(t *testing.T) {
	for k := 0; k < 3; k++ {
		checkPrefixFree(t, "coeff_token", coeffTokenLen[k][:], coeffTokenBits[k][:])
	}
	checkPrefixFree(t, "chroma_dc", chromaDCCoeffTokenLen[:], chromaDCCoeffTokenBits[:])
	for i := 0; i < 15; i++ {
		checkPrefixFree(t, "total_zeros", totalZerosLen[i][:], totalZerosBits[i][:])
	}
	for i := 0; i < 3; i++ {
		checkPrefixFree(t, "chroma_tz", chromaDCTotalZerosLen[i][:], chromaDCTotalZerosBits[i][:])
	}
	for i := 0; i < 7; i++ {
		checkPrefixFree(t, "run", runLen[i][:], runBits[i][:])
	}
}
