package hevc

// Video parameter set with the multilayer extension (F.7.3.2.1). Only what
// layered decoding needs is kept: layer ids, view ids, direct dependencies and
// the inter-layer prediction switches; the rest of the extension is parsed to
// reach those elements.

const maxLayers = 64

type vpsRepFormat struct {
	width, height int
	chroma        int
	bitDepthY     int
	bitDepthC     int
}

// VPS is a parsed video parameter set.
type VPS struct {
	id           int
	baseInternal bool
	maxLayers    int // vps_max_layers_minus1 + 1
	maxSubLayers int
	maxLayerID   int

	hasExt bool

	layerSets [][]int // LayerSetLayerIdList

	// indexed by LayerIdxInVps
	layerIDInNuh   [maxLayers]int
	viewOrderIdx   [maxLayers]int
	viewID         [maxLayers]int
	dimID          [maxLayers][16]int
	directDep      [maxLayers][maxLayers]bool
	depFlag        [maxLayers][maxLayers]bool // direct or indirect
	numDirectRef   [maxLayers]int
	directRefIdx   [maxLayers][maxLayers]int // layer indices of the direct reference layers
	pocLsbNotPres  [maxLayers]bool
	subLayersMax   [maxLayers]int // sub_layers_vps_max_minus1
	maxTidIlRef    [maxLayers][maxLayers]int
	repFormatIdx   [maxLayers]int
	directDepType  [maxLayers][maxLayers]int
	layerIdx       [maxLayers]int // by nuh_layer_id; -1 when absent
	scalMask       [16]bool
	allRefActive   bool // default_ref_layers_active_flag
	maxOneActive   bool
	repFormats     []vpsRepFormat
	numViews       int
	numIndependent int
}

func (v *VPS) numLayerSets() int { return len(v.layerSets) }

func ceilLog2u(n int) int {
	l := 0
	for 1<<uint(l) < n {
		l++
	}
	return l
}

func parseVPS(b []byte) (*VPS, error) {
	r := &bitReader{b: b}
	v := &VPS{}
	for i := range v.layerIdx {
		v.layerIdx[i] = -1
	}
	v.id = int(r.u(4))
	v.baseInternal = r.flag()
	r.u1() // vps_base_layer_available_flag
	v.maxLayers = int(r.u(6)) + 1
	maxSub := int(r.u(3))
	v.maxSubLayers = maxSub + 1
	r.u1()
	r.skip(16)
	parseProfileTierLevel(r, true, maxSub)
	present := r.flag()
	start := maxSub
	if present {
		start = 0
	}
	for i := start; i <= maxSub; i++ {
		r.ue()
		r.ue()
		r.ue()
	}
	v.maxLayerID = int(r.u(6))
	numSets := int(r.ue()) + 1
	if numSets > 1024 || v.maxLayerID > 62 {
		return nil, errBitstream
	}
	v.layerSets = make([][]int, 0, numSets)
	v.layerSets = append(v.layerSets, []int{0})
	for i := 1; i < numSets; i++ {
		var ls []int
		for j := 0; j <= v.maxLayerID; j++ {
			if r.flag() {
				ls = append(ls, j)
			}
		}
		v.layerSets = append(v.layerSets, ls)
	}
	if r.flag() { // vps_timing_info_present_flag
		r.skip(64)
		if r.flag() {
			r.ue()
		}
		n := int(r.ue())
		if n > 1024 {
			return nil, errBitstream
		}
		for i := 0; i < n; i++ {
			r.ue()
			cprms := true
			if i > 0 {
				cprms = r.flag()
			}
			parseHRD(r, cprms, maxSub)
			if r.err {
				return nil, errBitstream
			}
		}
	}
	if r.err {
		return nil, errBitstream
	}
	v.layerIDInNuh[0] = 0
	v.layerIdx[0] = 0
	v.numViews = 1
	if v.maxLayers > 1 && r.flag() { // vps_extension_flag
		r.alignByte()
		if err := v.parseExt(r); err != nil {
			return nil, err
		}
		v.hasExt = true
	}
	return v, nil
}

func (v *VPS) parseExt(r *bitReader) error {
	n := v.maxLayers
	maxSub := v.maxSubLayers - 1
	if n > 1 && v.baseInternal {
		parseProfileTierLevel(r, false, maxSub)
	}
	splitting := r.flag()
	numScal := 0
	for i := 0; i < 16; i++ {
		v.scalMask[i] = r.flag()
		if v.scalMask[i] {
			numScal++
		}
	}
	var dimLen [16]int
	cnt := numScal
	if splitting {
		cnt--
	}
	for j := 0; j < cnt; j++ {
		dimLen[j] = int(r.u(3)) + 1
	}
	dimBitOffset := func(j int) int {
		if splitting && j == numScal {
			return 6
		}
		o := 0
		for k := 0; k < j; k++ {
			o += dimLen[k]
		}
		return o
	}
	if splitting {
		dimLen[numScal-1] = 5 - dimBitOffset(numScal-1) + 1
	}
	nuhPresent := r.flag()
	for i := 1; i < n; i++ {
		if nuhPresent {
			v.layerIDInNuh[i] = int(r.u(6))
		} else {
			v.layerIDInNuh[i] = i
		}
		if v.layerIDInNuh[i] >= maxLayers {
			return errBitstream
		}
		v.layerIdx[v.layerIDInNuh[i]] = i
		for j := 0; j < numScal; j++ {
			if !splitting {
				v.dimID[i][j] = int(r.u(dimLen[j]))
			} else {
				v.dimID[i][j] = (v.layerIDInNuh[i] & (1<<uint(dimBitOffset(j+1)) - 1)) >> uint(dimBitOffset(j))
			}
		}
	}
	// ScalabilityId -> view order index
	scalIdx := func(smIdx int) int { // index among the set mask bits
		k := 0
		for t := 0; t < smIdx; t++ {
			if v.scalMask[t] {
				k++
			}
		}
		return k
	}
	for i := 0; i < n; i++ {
		v.viewOrderIdx[i] = 0
		if v.scalMask[1] {
			v.viewOrderIdx[i] = v.dimID[i][scalIdx(1)]
		}
	}
	v.numViews = 1
	for i := 1; i < n; i++ {
		isNew := true
		for j := 0; j < i; j++ {
			if v.viewOrderIdx[i] == v.viewOrderIdx[j] {
				isNew = false
			}
		}
		if isNew {
			v.numViews++
		}
	}
	viewIDLen := int(r.u(4))
	var viewIDVal [maxLayers]int
	if viewIDLen > 0 {
		for i := 0; i < v.numViews; i++ {
			viewIDVal[i] = int(r.u(viewIDLen))
		}
	}
	for i := 0; i < n; i++ {
		v.viewID[i] = viewIDVal[v.viewOrderIdx[i]&63]
	}
	for i := 1; i < n; i++ {
		for j := 0; j < i; j++ {
			v.directDep[i][j] = r.flag()
		}
	}
	if r.err {
		return errBitstream
	}
	// dependency closure, direct reference lists
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			v.depFlag[i][j] = v.directDep[i][j]
			for k := 0; k < i; k++ {
				if v.directDep[i][k] && v.depFlag[k][j] {
					v.depFlag[i][j] = true
				}
			}
		}
		for j := 0; j < n; j++ {
			if v.directDep[i][j] {
				v.directRefIdx[i][v.numDirectRef[i]] = j
				v.numDirectRef[i]++
			}
		}
	}
	// independent layers and tree partitions
	var treeList [maxLayers][maxLayers]int
	var treeLen [maxLayers]int
	inList := [maxLayers]bool{}
	k := 0
	for i := 0; i < n; i++ {
		if v.numDirectRef[i] == 0 {
			treeList[k][0] = v.layerIDInNuh[i]
			h := 1
			for j := 0; j < n; j++ {
				if v.depFlag[j][i] { // layer j is predicted from layer i
					id := v.layerIDInNuh[j]
					if !inList[id] {
						treeList[k][h] = id
						h++
						inList[id] = true
					}
				}
			}
			treeLen[k] = h
			k++
		}
	}
	v.numIndependent = k
	numAdd := 0
	if v.numIndependent > 1 {
		numAdd = int(r.ue())
		if numAdd > 1024 {
			return errBitstream
		}
	}
	for i := 0; i < numAdd; i++ {
		var ls []int
		for j := 1; j < v.numIndependent; j++ {
			hi := int(r.u(ceilLog2u(treeLen[j] + 1)))
			for c := 0; c < hi && c < treeLen[j]; c++ {
				ls = append(ls, treeList[j][c])
			}
		}
		v.layerSets = append(v.layerSets, ls)
	}
	numLayerSets := len(v.layerSets)
	origSets := numLayerSets - numAdd
	if r.flag() { // vps_sub_layers_max_minus1_present_flag
		for i := 0; i < n; i++ {
			v.subLayersMax[i] = int(r.u(3))
		}
	} else {
		for i := 0; i < n; i++ {
			v.subLayersMax[i] = maxSub
		}
	}
	if r.flag() { // max_tid_ref_present_flag
		for i := 0; i < n-1; i++ {
			for j := i + 1; j < n; j++ {
				if v.directDep[j][i] {
					v.maxTidIlRef[i][j] = int(r.u(3))
				}
			}
		}
	} else {
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				v.maxTidIlRef[i][j] = 7
			}
		}
	}
	v.allRefActive = r.flag()
	numPTL := int(r.ue()) + 1
	if numPTL > 64 {
		return errBitstream
	}
	start := 1
	if v.baseInternal {
		start = 2
	}
	for i := start; i < numPTL; i++ {
		pp := r.flag()
		parseProfileTierLevel(r, pp, maxSub)
	}
	numAddOls := 0
	defOutIdc := 0
	if numLayerSets > 1 {
		numAddOls = int(r.ue())
		defOutIdc = int(r.u(2))
		if defOutIdc > 2 {
			defOutIdc = 2
		}
		if numAddOls > 1024 {
			return errBitstream
		}
	}
	numOLS := numLayerSets + numAddOls
	olsToLs := make([]int, numOLS)
	necessary := make([][]bool, numOLS)
	outputFlag := make([][]bool, numOLS)
	layerIdxOf := func(id int) int {
		if id < 0 || id >= maxLayers {
			return -1
		}
		return v.layerIdx[id]
	}
	derive := func(ols int) {
		ls := v.layerSets[olsToLs[ols]]
		necessary[ols] = make([]bool, len(ls))
		for j := range ls {
			if outputFlag[ols][j] {
				necessary[ols][j] = true
				cur := layerIdxOf(ls[j])
				for rj := 0; rj < j; rj++ {
					ref := layerIdxOf(ls[rj])
					if cur >= 0 && ref >= 0 && v.depFlag[cur][ref] {
						necessary[ols][rj] = true
					}
				}
			}
		}
	}
	// OLS 0: the base layer
	olsToLs[0] = 0
	outputFlag[0] = []bool{true}
	derive(0)
	ptlIdxLen := ceilLog2u(numPTL)
	for i := 1; i < numOLS; i++ {
		if numLayerSets > 2 && i >= numLayerSets {
			olsToLs[i] = int(r.u(ceilLog2u(numLayerSets-1))) + 1
		} else if i < numLayerSets {
			olsToLs[i] = i
		} else {
			olsToLs[i] = 1
		}
		if olsToLs[i] >= numLayerSets {
			return errBitstream
		}
		ls := v.layerSets[olsToLs[i]]
		outputFlag[i] = make([]bool, len(ls))
		if i >= origSets || defOutIdc == 2 {
			for j := range ls {
				outputFlag[i][j] = r.flag()
			}
		} else {
			for j := range ls {
				switch defOutIdc {
				case 0:
					outputFlag[i][j] = true
				case 1:
					outputFlag[i][j] = j == len(ls)-1
				}
			}
		}
		derive(i)
		for j := range ls {
			if necessary[i][j] && numPTL > 1 {
				r.skip(ptlIdxLen)
			}
		}
		numOut, highest := 0, -1
		for j := range ls {
			if outputFlag[i][j] {
				numOut++
				highest = ls[j]
			}
		}
		if numOut == 1 && highest >= 0 {
			hi := layerIdxOf(highest)
			if hi >= 0 && v.numDirectRef[hi] > 0 {
				r.u1() // alt_output_layer_flag
			}
		}
		if r.err {
			return errBitstream
		}
	}
	numRep := int(r.ue()) + 1
	if numRep > 256 {
		return errBitstream
	}
	v.repFormats = make([]vpsRepFormat, numRep)
	for i := 0; i < numRep; i++ {
		rf := &v.repFormats[i]
		rf.width = int(r.u(16))
		rf.height = int(r.u(16))
		if r.flag() { // chroma_and_bit_depth_vps_present_flag
			rf.chroma = int(r.u(2))
			if rf.chroma == 3 {
				r.u1()
			}
			rf.bitDepthY = int(r.u(4)) + 8
			rf.bitDepthC = int(r.u(4)) + 8
		} else if i > 0 {
			rf.chroma, rf.bitDepthY, rf.bitDepthC = v.repFormats[i-1].chroma, v.repFormats[i-1].bitDepthY, v.repFormats[i-1].bitDepthC
		}
		if r.flag() { // conformance_window_vps_flag
			r.ue()
			r.ue()
			r.ue()
			r.ue()
		}
	}
	idxPresent := false
	if numRep > 1 {
		idxPresent = r.flag()
	}
	s := 0
	if v.baseInternal {
		s = 1
	}
	for i := s; i < n; i++ {
		if idxPresent {
			v.repFormatIdx[i] = int(r.u(ceilLog2u(numRep)))
		} else {
			v.repFormatIdx[i] = min(i, numRep-1)
		}
	}
	v.maxOneActive = r.flag()
	r.u1() // vps_poc_lsb_aligned_flag
	for i := 1; i < n; i++ {
		if v.numDirectRef[i] == 0 {
			v.pocLsbNotPres[i] = r.flag()
		}
	}
	// dpb_size()
	for i := 1; i < numOLS; i++ {
		curLs := olsToLs[i]
		ls := v.layerSets[curLs]
		maxSL := 0
		for _, id := range ls {
			if li := layerIdxOf(id); li >= 0 && v.subLayersMax[li] > maxSL {
				maxSL = v.subLayersMax[li]
			}
		}
		flagInfo := r.flag()
		present := true
		for j := 0; j <= maxSL; j++ {
			if j > 0 && flagInfo {
				present = r.flag()
			} else if j > 0 {
				present = false
			}
			if present {
				for k := range ls {
					if necessary[i][k] && (v.baseInternal || ls[k] != 0) {
						r.ue()
					}
				}
				r.ue()
				r.ue()
			}
		}
		if r.err {
			return errBitstream
		}
	}
	dirTypeLen := int(r.ue()) + 2
	defDirDep := r.flag()
	defDirType := 0
	if defDirDep {
		defDirType = int(r.u(dirTypeLen))
	}
	ds, js := 2, 1
	if v.baseInternal {
		ds, js = 1, 0
	}
	for i := ds; i < n; i++ {
		for j := js; j < i; j++ {
			if v.directDep[i][j] {
				if defDirDep {
					v.directDepType[i][j] = defDirType
				} else {
					v.directDepType[i][j] = int(r.u(dirTypeLen))
				}
			}
		}
	}
	if r.err {
		return errBitstream
	}
	return nil
}
