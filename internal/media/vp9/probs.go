package vp9

import "unsafe"

// probs holds every adaptive probability table (spec 6.3, 10.5).
type probs struct {
	tx           [4][2][3]uint8
	skip         [3]uint8
	coef         [4][2][2][6][6][3]uint8
	partition    [16][3]uint8
	yMode        [4][9]uint8
	uvMode       [10][9]uint8
	isInter      [4]uint8
	compMode     [5]uint8
	singleRef    [5][2]uint8
	compRef      [5]uint8
	interMode    [7][3]uint8
	interpFilter [4][2]uint8
	mvJoint      [3]uint8
	mvSign       [2]uint8
	mvClass      [2][10]uint8
	mvClass0Bit  [2]uint8
	mvBits       [2][10]uint8
	mvClass0Fr   [2][2][3]uint8
	mvFr         [2][3]uint8
	mvClass0Hp   [2]uint8
	mvHp         [2]uint8
}

func defaultProbs() probs {
	return probs{
		tx:           defaultTxProbs,
		skip:         defaultSkipProb,
		coef:         defaultCoefProbs,
		partition:    defaultPartitionProbs,
		yMode:        defaultYModeProbs,
		uvMode:       defaultUVModeProbs,
		isInter:      defaultIsInterProb,
		compMode:     defaultCompModeProb,
		singleRef:    defaultSingleRefProb,
		compRef:      defaultCompRefProb,
		interMode:    defaultInterModeProbs,
		interpFilter: defaultInterpFilterProbs,
		mvJoint:      defaultMvJointProbs,
		mvSign:       defaultMvSignProb,
		mvClass:      defaultMvClassProbs,
		mvClass0Bit:  defaultMvClass0BitProb,
		mvBits:       defaultMvBitsProb,
		mvClass0Fr:   defaultMvClass0FrProbs,
		mvFr:         defaultMvFrProbs,
		mvClass0Hp:   defaultMvClass0HpProb,
		mvHp:         defaultMvHpProb,
	}
}

// counts are the symbol counters used for backward adaptation (spec 8.3).
type counts struct {
	intraMode    [4][10]uint32
	uvMode       [10][10]uint32
	partition    [16][4]uint32
	interpFilter [4][3]uint32
	interMode    [7][4]uint32
	tx           [4][2][4]uint32
	isInter      [4][2]uint32
	compMode     [5][2]uint32
	singleRef    [5][2][2]uint32
	compRef      [5][2]uint32
	skip         [3][2]uint32
	mvJoint      [4]uint32
	mvSign       [2][2]uint32
	mvClass      [2][11]uint32
	mvClass0Bit  [2][2]uint32
	mvClass0Fr   [2][2][4]uint32
	mvClass0Hp   [2][2]uint32
	mvBits       [2][10][2]uint32
	mvFr         [2][4]uint32
	mvHp         [2][2]uint32
	token        [4][2][2][6][6][3]uint32
	moreCoefs    [4][2][2][6][6][2]uint32
}

// add adds the counts of o (the counts of another tile column) to c.
func (c *counts) add(o *counts) {
	n := int(unsafe.Sizeof(*c)) / 4
	a := unsafe.Slice((*uint32)(unsafe.Pointer(c)), n)
	b := unsafe.Slice((*uint32)(unsafe.Pointer(o)), n)
	for i := range a {
		a[i] += b[i]
	}
}

func round2(x int, n uint) int { return (x + (1 << n >> 1)) >> n }

// mergeProb implements spec 8.4.1.
func mergeProb(pre uint8, ct0, ct1 uint32, countSat, maxUpdate int) uint8 {
	den := int(ct0 + ct1)
	prob := 128
	if den != 0 {
		prob = (int(ct0)*256 + (den >> 1)) / den
		if prob < 1 {
			prob = 1
		} else if prob > 255 {
			prob = 255
		}
	}
	count := den
	if count > countSat {
		count = countSat
	}
	factor := maxUpdate * count / countSat
	return uint8((int(pre)*(256-factor) + prob*factor + 128) >> 8)
}

func mergeProbs(tree []int8, i int, p []uint8, c []uint32, countSat, maxUpdate int) uint32 {
	s := int(tree[i])
	var left uint32
	if s <= 0 {
		left = c[-s]
	} else {
		left = mergeProbs(tree, s, p, c, countSat, maxUpdate)
	}
	r := int(tree[i+1])
	var right uint32
	if r <= 0 {
		right = c[-r]
	} else {
		right = mergeProbs(tree, r, p, c, countSat, maxUpdate)
	}
	p[i>>1] = mergeProb(p[i>>1], left, right, countSat, maxUpdate)
	return left + right
}

func adaptProbs(tree []int8, p []uint8, c []uint32) {
	mergeProbs(tree, 0, p, c, 20, 128)
}

func adaptProb(p *uint8, c [2]uint32) {
	*p = mergeProb(*p, c[0], c[1], 20, 128)
}

// adaptCoefProbs implements spec 8.4.3.
func (p *probs) adaptCoef(c *counts, updateFactor int) {
	for t := 0; t < 4; t++ {
		for i := 0; i < 2; i++ {
			for j := 0; j < 2; j++ {
				for k := 0; k < 6; k++ {
					maxL := 6
					if k == 0 {
						maxL = 3
					}
					for l := 0; l < maxL; l++ {
						pr := p.coef[t][i][j][k][l][:]
						mergeProbs(smallTokenTree, 2, pr, c.token[t][i][j][k][l][:], 24, updateFactor)
						mergeProbs(binaryTree, 0, pr, c.moreCoefs[t][i][j][k][l][:], 24, updateFactor)
					}
				}
			}
		}
	}
}

// adaptNonCoef implements spec 8.4.4.
func (p *probs) adaptNonCoef(c *counts, interpSwitchable, txSelect, allowHp bool) {
	for i := range p.isInter {
		adaptProb(&p.isInter[i], c.isInter[i])
	}
	for i := range p.compMode {
		adaptProb(&p.compMode[i], c.compMode[i])
	}
	for i := range p.compRef {
		adaptProb(&p.compRef[i], c.compRef[i])
	}
	for i := range p.singleRef {
		for j := 0; j < 2; j++ {
			adaptProb(&p.singleRef[i][j], c.singleRef[i][j])
		}
	}
	for i := range p.interMode {
		adaptProbs(interModeTree, p.interMode[i][:], c.interMode[i][:])
	}
	for i := range p.yMode {
		adaptProbs(intraModeTree, p.yMode[i][:], c.intraMode[i][:])
	}
	for i := range p.uvMode {
		adaptProbs(intraModeTree, p.uvMode[i][:], c.uvMode[i][:])
	}
	for i := range p.partition {
		adaptProbs(partitionTree, p.partition[i][:], c.partition[i][:])
	}
	for i := range p.skip {
		adaptProb(&p.skip[i], c.skip[i])
	}
	if interpSwitchable {
		for i := range p.interpFilter {
			adaptProbs(interpFilterTree, p.interpFilter[i][:], c.interpFilter[i][:])
		}
	}
	if txSelect {
		for i := 0; i < 2; i++ {
			adaptProbs(txSize8Tree, p.tx[tx8x8][i][:], c.tx[tx8x8][i][:])
			adaptProbs(txSize16Tree, p.tx[tx16x16][i][:], c.tx[tx16x16][i][:])
			adaptProbs(txSize32Tree, p.tx[tx32x32][i][:], c.tx[tx32x32][i][:])
		}
	}
	adaptProbs(mvJointTree, p.mvJoint[:], c.mvJoint[:])
	for i := 0; i < 2; i++ {
		adaptProb(&p.mvSign[i], c.mvSign[i])
		adaptProbs(mvClassTree, p.mvClass[i][:], c.mvClass[i][:])
		adaptProb(&p.mvClass0Bit[i], c.mvClass0Bit[i])
		for j := 0; j < 10; j++ {
			adaptProb(&p.mvBits[i][j], c.mvBits[i][j])
		}
		for j := 0; j < 2; j++ {
			adaptProbs(mvFrTree, p.mvClass0Fr[i][j][:], c.mvClass0Fr[i][j][:])
		}
		adaptProbs(mvFrTree, p.mvFr[i][:], c.mvFr[i][:])
		if allowHp {
			adaptProb(&p.mvClass0Hp[i], c.mvClass0Hp[i])
			adaptProb(&p.mvHp[i], c.mvHp[i])
		}
	}
}
