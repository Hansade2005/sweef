package h264

// Frame stores, reference marking and reference list construction (8.2).
// The structure follows FFmpeg's: a frame store holds both fields of a frame,
// the short-term list is ordered most recent first, and field pictures use
// the same frame-sized macroblock grid as frame pictures (the macroblock
// rows of the two fields interleave).

type mv16 struct{ x, y int16 }

// Picture structure codes (also used as field masks).
const (
	picTop    = 1
	picBottom = 2
	picFrame  = 3
)

// Picture is a frame store.
type Picture struct {
	id int32

	mbW, mbH int // frame macroblock grid
	mbStride int // mbW + 1
	w, h     int // luma frame size in samples
	cw, ch   int // chroma plane size (0 for monochrome)
	cf       int // chroma_format_idc
	bdY, bdC int
	y, cb, cr []uint16
	ys, cs   int
	crop     [4]int
	colorMtx int
	fullRange bool

	// per-macroblock data, indexed by the padded macroblock index
	mbType []uint32
	mv     [2][]mv16 // 16 entries (raster 4x4) per macroblock
	refIdx [2][]int8 // 4 entries (raster 8x8) per macroblock
	refID  [2][]int32

	fieldPicture bool // coded as field pictures
	mbaff        bool
	frameNum     int
	fieldPOC     [2]int
	poc          int
	reference    int // field mask of fields marked as reference
	longRef      bool
	mmcoReset    bool
	invalidGap   bool
	picID        int // pic_id used while reordering
	seq          int
	idr          bool

	// reference pictures of each list per field slot (for temporal direct)
	refCount [2][2]int
	refPOC   [2][2][32]int

	nonExist bool
}

const pocInvalid = 1 << 30

func (d *Decoder) mbIndexBase() int { return 2*(d.mbW+1) + 1 }

func newPicture(id int32, sps *SPS) *Picture {
	mbW, mbH := sps.MbW, sps.MbH
	p := &Picture{id: id, mbW: mbW, mbH: mbH, mbStride: mbW + 1, w: mbW * 16, h: mbH * 16}
	p.ys = p.w
	p.y = make([]uint16, p.w*p.h)
	switch sps.ChromaFormat {
	case 1:
		p.cw, p.ch = p.w/2, p.h/2
	case 2:
		p.cw, p.ch = p.w/2, p.h
	case 3:
		p.cw, p.ch = p.w, p.h
	}
	p.cs = p.cw
	if p.cw > 0 {
		p.cb = make([]uint16, p.cw*p.ch)
		p.cr = make([]uint16, p.cw*p.ch)
	}
	p.cf, p.bdY, p.bdC = sps.ChromaFormat, sps.BitDepthLuma, sps.BitDepthChroma
	p.crop = [4]int{sps.CropL, sps.CropR, sps.CropT, sps.CropB}
	p.colorMtx = sps.ColorMatrix
	p.fullRange = sps.FullRange
	n := (mbH+3)*p.mbStride + 1
	p.mbType = make([]uint32, n)
	for l := 0; l < 2; l++ {
		p.mv[l] = make([]mv16, n*16)
		p.refIdx[l] = make([]int8, n*4)
		p.refID[l] = make([]int32, n*4)
		for i := range p.refIdx[l] {
			p.refIdx[l][i] = -1
			p.refID[l][i] = -1
		}
	}
	p.fieldPOC = [2]int{pocInvalid, pocInvalid}
	return p
}

// mbIdx returns the padded array index of macroblock (mbx, mby).
func (p *Picture) mbIdx(mbx, mby int) int { return (mby+2)*p.mbStride + mbx + 1 }

// ---- picture order count (8.2.1) -------------------------------------------------

type pocState struct {
	frameNum           int
	prevFrameNum       int
	frameNumOffset     int
	prevFrameNumOffset int
	pocLsb             int
	prevPOCLsb         int
	pocMsb             int
	prevPOCMsb         int
	deltaPOCBottom     int
	deltaPOC           [2]int
}

func (d *Decoder) resetPOC() {
	st := &d.poc
	st.prevFrameNum, st.prevFrameNumOffset = 0, 0
	st.prevPOCMsb, st.prevPOCLsb = 1<<16, -1
}

// initPOC derives the picture order counts of the picture being started.
func (d *Decoder) initPOC(pic *Picture, sps *SPS, structure, nalRefIdc int) {
	st := &d.poc
	maxFrameNum := 1 << uint(sps.Log2MaxFrameNum)
	st.frameNumOffset = st.prevFrameNumOffset
	if st.frameNum < st.prevFrameNum {
		st.frameNumOffset += maxFrameNum
	}
	var fp [2]int
	switch sps.POCType {
	case 0:
		maxLsb := 1 << uint(sps.Log2MaxPOCLsb)
		if st.prevPOCLsb < 0 {
			st.prevPOCLsb = st.pocLsb
		}
		switch {
		case st.pocLsb < st.prevPOCLsb && st.prevPOCLsb-st.pocLsb >= maxLsb/2:
			st.pocMsb = st.prevPOCMsb + maxLsb
		case st.pocLsb > st.prevPOCLsb && st.prevPOCLsb-st.pocLsb < -maxLsb/2:
			st.pocMsb = st.prevPOCMsb - maxLsb
		default:
			st.pocMsb = st.prevPOCMsb
		}
		fp[0] = st.pocMsb + st.pocLsb
		fp[1] = fp[0]
		if structure == picFrame {
			fp[1] += st.deltaPOCBottom
		}
	case 1:
		n := len(sps.OffsetForRefFrame)
		abs := 0
		if n != 0 {
			abs = st.frameNumOffset + st.frameNum
		}
		if nalRefIdc == 0 && abs > 0 {
			abs--
		}
		var perCycle int
		for _, o := range sps.OffsetForRefFrame {
			perCycle += int(o)
		}
		expected := 0
		if abs > 0 {
			cyc := (abs - 1) / n
			in := (abs - 1) % n
			expected = cyc * perCycle
			for i := 0; i <= in; i++ {
				expected += int(sps.OffsetForRefFrame[i])
			}
		}
		if nalRefIdc == 0 {
			expected += int(sps.OffsetNonRef)
		}
		fp[0] = expected + st.deltaPOC[0]
		fp[1] = fp[0] + int(sps.OffsetTopToBottom)
		if structure == picFrame {
			fp[1] += st.deltaPOC[1]
		}
	default:
		poc := 2 * (st.frameNumOffset + st.frameNum)
		if nalRefIdc == 0 {
			poc--
		}
		fp[0], fp[1] = poc, poc
	}
	if structure != picBottom {
		pic.fieldPOC[0] = fp[0]
	}
	if structure != picTop {
		pic.fieldPOC[1] = fp[1]
	}
	pic.poc = min(pic.fieldPOC[0], pic.fieldPOC[1])
}

// ---- reference marking (8.2.5) -----------------------------------------------------

func (d *Decoder) unreferencePic(p *Picture, mask int) bool {
	p.reference &= mask
	return p.reference == 0
}

func (d *Decoder) findShort(frameNum int) (*Picture, int) {
	for i, p := range d.shortRef {
		if p.frameNum == frameNum {
			return p, i
		}
	}
	return nil, -1
}

func (d *Decoder) removeShortAt(i int) {
	d.shortRef = append(d.shortRef[:i], d.shortRef[i+1:]...)
}

func (d *Decoder) removeShort(frameNum, mask int) *Picture {
	p, i := d.findShort(frameNum)
	if p != nil && d.unreferencePic(p, mask) {
		d.removeShortAt(i)
	}
	return p
}

func (d *Decoder) removeLong(i, mask int) *Picture {
	p := d.longRef[i]
	if p != nil && d.unreferencePic(p, mask) {
		p.longRef = false
		d.longRef[i] = nil
		d.longCount--
	}
	return p
}

func (d *Decoder) removeAllRefs() {
	for i := 0; i < 16; i++ {
		d.removeLong(i, 0)
	}
	for _, p := range d.shortRef {
		d.unreferencePic(p, 0)
	}
	d.shortRef = d.shortRef[:0]
	d.defaultRef = [2]refEntry{}
}

// picNumExtract returns the frame number (or long-term index) and structure
// of the picture identified by picNum (8.2.4.1).
func picNumExtract(structure, picNum int) (int, int) {
	st := structure
	if structure != picFrame {
		if picNum&1 == 0 {
			st ^= picFrame // opposite field
		}
		picNum >>= 1
	}
	return picNum, st
}

func (d *Decoder) generateSlidingWindow(h *sliceHeader, structure int, firstField bool) []mmco {
	var out []mmco
	if len(d.shortRef) > 0 && d.longCount+len(d.shortRef) >= max(h.sps.MaxNumRefFrames, 1) &&
		!(structure != picFrame && !firstField && d.curPic.reference != 0) {
		m := mmco{op: 1, a: d.shortRef[len(d.shortRef)-1].frameNum}
		if structure != picFrame {
			m.a *= 2
			out = append(out, m, mmco{op: 1, a: m.a + 1})
		} else {
			out = append(out, m)
		}
	}
	return out
}

// executeRefMarking applies the marking process of the picture just
// decoded (8.2.5). mmcoList holds the operations (already converted to
// picture numbers) when explicit.
func (d *Decoder) executeRefMarking(h *sliceHeader, structure int, firstField bool) bool {
	sps := h.sps
	cur := d.curPic
	var ops []mmco
	if !h.adaptiveMarking && !h.idr {
		ops = d.generateSlidingWindow(h, structure, firstField)
	} else {
		ops = h.mmcos
	}
	currentAssigned := false
	reset := false
	for _, m := range ops {
		var frameNum, st int
		var pic *Picture
		var idx int
		switch m.op {
		case 1, 3:
			frameNum, st = picNumExtract(structure, m.a)
			pic, idx = d.findShort(frameNum)
			if pic == nil {
				continue
			}
		}
		switch m.op {
		case 1:
			d.removeShort(frameNum, st^picFrame)
		case 3:
			if d.longRef[m.b] != pic {
				d.removeLong(m.b, 0)
			}
			d.removeShortAt(idx)
			d.longRef[m.b] = pic
			pic.longRef = true
			d.longCount++
		case 2:
			j, st2 := picNumExtract(structure, m.a)
			if j < 16 && d.longRef[j] != nil {
				d.removeLong(j, st2^picFrame)
			}
		case 6:
			if len(d.shortRef) > 0 && d.shortRef[0] == cur {
				d.removeShortAt(0)
			}
			if cur.longRef {
				for j := range d.longRef {
					if d.longRef[j] == cur {
						d.removeLong(j, 0)
					}
				}
			}
			if d.longRef[m.a] != cur {
				d.removeLong(m.a, 0)
				d.longRef[m.a] = cur
				cur.longRef = true
				d.longCount++
			}
			cur.reference |= structure
			currentAssigned = true
		case 4:
			for j := m.a; j < 16; j++ {
				d.removeLong(j, 0)
			}
		case 5:
			for len(d.shortRef) > 0 {
				d.removeShort(d.shortRef[0].frameNum, 0)
			}
			for j := 0; j < 16; j++ {
				d.removeLong(j, 0)
			}
			d.poc.frameNum = 0
			cur.frameNum = 0
			cur.mmcoReset = true
			reset = true
		}
	}
	if !currentAssigned {
		switch {
		case len(d.shortRef) > 0 && d.shortRef[0] == cur:
			cur.reference |= structure
		case cur.longRef:
			// second field of a long-term frame without MMCO 6: invalid
		default:
			d.removeShort(cur.frameNum, 0)
			d.shortRef = append([]*Picture{cur}, d.shortRef...)
			cur.reference |= structure
		}
	}
	if d.longCount+len(d.shortRef) > max(sps.MaxNumRefFrames, 1) {
		if d.longCount > 0 && len(d.shortRef) == 0 {
			for i := 0; i < 16; i++ {
				if d.longRef[i] != nil {
					d.removeLong(i, 0)
					break
				}
			}
		} else if len(d.shortRef) > 0 {
			p := d.shortRef[len(d.shortRef)-1]
			d.removeShort(p.frameNum, 0)
		}
	}
	for _, p := range append([]*Picture(nil), d.shortRef...) {
		if p.invalidGap {
			dd := (cur.frameNum - p.frameNum) & (1<<uint(sps.Log2MaxFrameNum) - 1)
			if dd > sps.MaxNumRefFrames {
				d.removeShort(p.frameNum, 0)
			}
		}
	}
	return reset
}

// ---- reference lists (8.2.4) ----------------------------------------------------------

// refEntry is one reference picture list entry: a frame or one field of a
// frame store.
type refEntry struct {
	pic   *Picture
	ref   int // picTop, picBottom or picFrame
	poc   int
	picID int
}

func refFromPic(p *Picture) refEntry {
	return refEntry{pic: p, ref: p.reference, poc: p.poc, picID: p.picID}
}

func (r *refEntry) asField(parity int) {
	r.ref = parity
	r.poc = r.pic.fieldPOC[parity-1]
}

// identity returns a value that identifies the referenced frame or field.
func (r *refEntry) identity() int32 {
	if r.pic == nil {
		return -1
	}
	return r.pic.id*4 + int32(r.ref&3)
}

func splitFieldCopy(src *Picture, parity, idAdd int) (refEntry, bool) {
	if src.reference&parity == 0 {
		return refEntry{}, false
	}
	e := refFromPic(src)
	if parity != picFrame {
		e.asField(parity)
		e.picID = e.picID*2 + idAdd
	}
	return e, true
}

func buildDefList(in []*Picture, isLong bool, sel int) []refEntry {
	var out []refEntry
	i := [2]int{}
	n := len(in)
	for i[0] < n || i[1] < n {
		for i[0] < n && !(in[i[0]] != nil && in[i[0]].reference&sel != 0) {
			i[0]++
		}
		for i[1] < n && !(in[i[1]] != nil && in[i[1]].reference&(sel^3) != 0) {
			i[1]++
		}
		if i[0] < n {
			if isLong {
				in[i[0]].picID = i[0]
			} else {
				in[i[0]].picID = in[i[0]].frameNum
			}
			if e, ok := splitFieldCopy(in[i[0]], sel, 1); ok {
				out = append(out, e)
			}
			i[0]++
		}
		if i[1] < n {
			if isLong {
				in[i[1]].picID = i[1]
			} else {
				in[i[1]].picID = in[i[1]].frameNum
			}
			if e, ok := splitFieldCopy(in[i[1]], sel^3, 0); ok {
				out = append(out, e)
			}
			i[1]++
		}
	}
	return out
}

func addSorted(src []*Picture, limit, dir int) []*Picture {
	var sorted []*Picture
	for {
		best := pocInvalid * 2
		if dir != 0 {
			best = -pocInvalid * 2
		}
		var pick *Picture
		for _, p := range src {
			poc := p.poc
			if ((poc > limit) != (dir != 0)) && ((poc < best) != (dir != 0)) {
				best = poc
				pick = p
			}
		}
		if pick == nil {
			break
		}
		sorted = append(sorted, pick)
		limit = pick.poc - dir
	}
	return sorted
}

// initRefLists builds the default reference lists of the current slice.
func (d *Decoder) initRefLists(h *sliceHeader, structure int) {
	lists := &d.refList
	var curPOC int
	if structure != picFrame {
		curPOC = d.curPic.fieldPOC[structure-1]
	} else {
		curPOC = d.curPic.poc
	}
	longs := make([]*Picture, 16)
	copy(longs, d.longRef[:])
	if h.isB() {
		var lens [2]int
		for l := 0; l < 2; l++ {
			sorted := addSorted(d.shortRef, curPOC, 1^l)
			sorted = append(sorted, addSorted(d.shortRef, curPOC, 0^l)...)
			lst := buildDefList(sorted, false, structure)
			lst = append(lst, buildDefList(longs, true, structure)...)
			lists[l] = lst
			lens[l] = len(lst)
		}
		if lens[0] == lens[1] && lens[1] > 1 {
			same := true
			for i := 0; i < lens[0]; i++ {
				if lists[0][i].pic != lists[1][i].pic {
					same = false
					break
				}
			}
			if same {
				lists[1][0], lists[1][1] = lists[1][1], lists[1][0]
			}
		}
	} else {
		lst := buildDefList(d.shortRef, false, structure)
		lst = append(lst, buildDefList(longs, true, structure)...)
		lists[0] = lst
		lists[1] = nil
	}
	for l := 0; l < 2; l++ {
		for len(lists[l]) < h.numRef[l] {
			lists[l] = append(lists[l], refEntry{})
		}
	}
	for l := 0; l < 2; l++ {
		if len(lists[l]) > 0 {
			d.defaultRef[l] = lists[l][0]
		}
	}
}

// buildRefList builds, modifies and completes the reference lists of the
// slice. It returns false when a reference is missing and cannot be replaced.
func (d *Decoder) buildRefList(h *sliceHeader, structure int) bool {
	d.refList = [2][]refEntry{}
	d.initRefLists(h, structure)
	curPicNum := h.frameNum
	maxPicNum := 1 << uint(h.sps.Log2MaxFrameNum)
	if structure != picFrame {
		curPicNum = 2*h.frameNum + 1
		maxPicNum <<= 1
	}
	listCount := 1 + btoi(h.isB())
	for l := 0; l < listCount; l++ {
		lst := d.refList[l]
		n := h.numRef[l]
		pred := curPicNum
		for index, m := range h.refMods[l] {
			if index >= n {
				break
			}
			var ref *Picture
			var pstruct int
			found := false
			switch m.idc {
			case 0, 1:
				abs := m.val + 1
				if abs > maxPicNum {
					return false
				}
				if m.idc == 0 {
					pred -= abs
				} else {
					pred += abs
				}
				pred &= maxPicNum - 1
				var fn int
				fn, pstruct = picNumExtract(structure, pred)
				for i := len(d.shortRef) - 1; i >= 0; i-- {
					r := d.shortRef[i]
					if r.frameNum == fn && r.reference&pstruct != 0 {
						ref = r
						found = true
						break
					}
				}
				if found {
					ref.picID = pred
				}
			case 2:
				var li int
				li, pstruct = picNumExtract(structure, m.val)
				if li > 31 {
					return false
				}
				if li < 16 {
					r := d.longRef[li]
					if r != nil && r.reference&pstruct != 0 {
						r.picID = m.val
						ref = r
						found = true
					}
				}
			}
			if !found {
				if index < len(lst) {
					lst[index] = refEntry{}
				}
				continue
			}
			i := index
			for i = index; i+1 < n; i++ {
				if lst[i].pic != nil && ref.longRef == lst[i].pic.longRef && ref.picID == lst[i].picID {
					break
				}
			}
			for ; i > index; i-- {
				lst[i] = lst[i-1]
			}
			e := refFromPic(ref)
			if structure != picFrame {
				e.asField(pstruct)
			}
			lst[index] = e
		}
		d.refList[l] = lst
	}
	for l := 0; l < listCount; l++ {
		for i := 0; i < h.numRef[l]; i++ {
			e := d.refList[l][i]
			if e.pic == nil || (structure == picFrame && e.ref&3 != 3) {
				def := d.defaultRef[l]
				if def.pic != nil && !(structure == picFrame && def.ref&3 != 3) {
					d.refList[l][i] = def
				} else {
					// no usable reference: substitute a neutral picture
					d.refList[l][i] = d.missingRef(structure)
				}
			}
		}
		d.refList[l] = d.refList[l][:h.numRef[l]]
	}
	if d.curPic.mbaff {
		d.fillMbaffRefList(h)
	}
	return true
}

// missingRef returns a mid-grey stand-in for a reference that does not exist.
func (d *Decoder) missingRef(structure int) refEntry {
	if d.grey == nil {
		g := newPicture(-2, d.first.sps)
		for i := range g.y {
			g.y[i] = uint16(1) << uint(d.f.bdY-1)
		}
		for i := range g.cb {
			g.cb[i], g.cr[i] = uint16(1)<<uint(d.f.bdC-1), uint16(1)<<uint(d.f.bdC-1)
		}
		g.nonExist = true
		g.fieldPOC = [2]int{0, 0}
		g.reference = 3
		d.grey = g
	}
	e := refFromPic(d.grey)
	e.ref = picFrame
	if structure != picFrame {
		e.asField(structure)
	}
	return e
}

// fillMbaffRefList derives the field reference lists of MBAFF field
// macroblocks: entry 2i is the field of frame i with the same parity as the
// macroblock, entry 2i+1 the opposite one (8.4.2.1).
func (d *Decoder) fillMbaffRefList(h *sliceHeader) {
	for l := 0; l < 1+btoi(h.isB()); l++ {
		d.refListField[l] = d.refListField[l][:0]
		for i := range d.refList[l] {
			fr := d.refList[l][i]
			top := fr
			top.ref = picTop
			top.poc = fr.pic.fieldPOC[0]
			bot := fr
			bot.ref = picBottom
			bot.poc = fr.pic.fieldPOC[1]
			d.refListField[l] = append(d.refListField[l], top, bot)
		}
	}
}
