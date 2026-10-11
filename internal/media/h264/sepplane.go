package h264

// Separately coded colour planes (separate_colour_plane_flag = 1). Each
// colour plane is decoded like a monochrome picture with its own macroblock
// state. The planes are handled by switching the "active" plane: the luma
// sample buffer and every per-macroblock table are exchanged for those of the
// selected colour plane, in the decoder and in all pictures of the DPB.

type planeTables struct {
	sliceTable      []uint16
	qpTable         []int8
	nz              []uint8
	cbpTable        []uint16
	chromaPredTable []uint8
	ipm             []int8
	mvd             [2][][2]uint8
	directTable     []uint8
	mbNZMask        []uint16
	mbBypass        []bool
}

type picMB struct {
	mbType []uint32
	mv     [2][]mv16
	refIdx [2][]int8
	refID  [2][]int32
}

func (d *Decoder) numPlanes() int {
	if d.f.sep {
		return 3
	}
	return 1
}

func (d *Decoder) saveTables(t *planeTables) {
	t.sliceTable, t.qpTable, t.nz, t.cbpTable = d.sliceTable, d.qpTable, d.nz, d.cbpTable
	t.chromaPredTable, t.ipm, t.mvd, t.directTable = d.chromaPredTable, d.ipm, d.mvd, d.directTable
	t.mbNZMask, t.mbBypass = d.mbNZMask, d.mbBypass
}

func (d *Decoder) loadTables(t *planeTables) {
	d.sliceTable, d.qpTable, d.nz, d.cbpTable = t.sliceTable, t.qpTable, t.nz, t.cbpTable
	d.chromaPredTable, d.ipm, d.mvd, d.directTable = t.chromaPredTable, t.ipm, t.mvd, t.directTable
	d.mbNZMask, d.mbBypass = t.mbNZMask, t.mbBypass
}

func (p *Picture) savePlane(k int) {
	m := &p.sep[k]
	m.mbType, m.mv, m.refIdx, m.refID = p.mbType, p.mv, p.refIdx, p.refID
}

func (p *Picture) loadPlane(k int) {
	m := &p.sep[k]
	p.mbType, p.mv, p.refIdx, p.refID = m.mbType, m.mv, m.refIdx, m.refID
}

// swapPix exchanges the luma buffer with colour plane k's buffer.
func (p *Picture) swapPix(k int) {
	switch k {
	case 1:
		p.y, p.cb = p.cb, p.y
	case 2:
		p.y, p.cr = p.cr, p.y
	}
}

func (d *Decoder) dpbPictures(f func(*Picture)) {
	seen := map[*Picture]bool{}
	visit := func(p *Picture) {
		if p != nil && !seen[p] {
			seen[p] = true
			f(p)
		}
	}
	for _, p := range d.shortRef {
		visit(p)
	}
	for _, p := range d.longRef {
		visit(p)
	}
	visit(d.curPic)
}

// selectPlane makes colour plane k the active one.
func (d *Decoder) selectPlane(k int) {
	if k == d.cplane {
		return
	}
	old := d.cplane
	d.saveTables(&d.planeTab[old])
	d.loadTables(&d.planeTab[k])
	d.dpbPictures(func(p *Picture) {
		if p.sep[1].mbType == nil {
			return // picture without separate planes
		}
		p.savePlane(old)
		p.loadPlane(k)
		p.swapPix(old)
		p.swapPix(k)
	})
	d.cplane = k
}
