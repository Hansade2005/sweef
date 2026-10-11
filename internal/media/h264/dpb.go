package h264

import "sort"

type mv16 struct{ x, y int16 }

// Picture is a decoded frame plus the motion data later pictures need for
// direct prediction.
type Picture struct {
	id        int32
	mbW, mbH  int
	w, h      int
	cw, ch    int // chroma plane size (0 for monochrome)
	seq       int
	cf        int // chroma_format_idc
	bdY, bdC  int
	y, cb, cr []uint16
	ys, cs    int
	poc       int
	frameNum  int
	frameWrap int
	shortRef  bool
	longRef   bool
	longIdx   int
	mv        [2][]mv16 // per 4x4 block
	refIdx    [2][]int8 // per 8x8 block
	refID     [2][]int32
	nonExist  bool
	crop      [4]int
	colorMtx  int
	fullRange bool
}

func newPicture(id int32, mbW, mbH int, sps *SPS) *Picture {
	p := &Picture{id: id, mbW: mbW, mbH: mbH, w: mbW * 16, h: mbH * 16}
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
	for l := 0; l < 2; l++ {
		p.mv[l] = make([]mv16, mbW*4*mbH*4)
		p.refIdx[l] = make([]int8, mbW*2*mbH*2)
		p.refID[l] = make([]int32, mbW*2*mbH*2)
		for i := range p.refIdx[l] {
			p.refIdx[l][i] = -1
			p.refID[l][i] = -1
		}
	}
	return p
}

func (p *Picture) isRef() bool { return p.shortRef || p.longRef }

// ---- picture order count (8.2.1) ------------------------------------------------

type pocState struct {
	prevFrameNum       int
	prevFrameNumOffset int
	prevPOCMsb         int
	prevPOCLsb         int
	frameNumOffset     int
	pocMsb             int
}

func (d *Decoder) computePOC(h *sliceHeader) int {
	sps := h.sps
	st := &d.poc
	maxFrameNum := 1 << uint(sps.Log2MaxFrameNum)
	switch sps.POCType {
	case 0:
		prevMsb, prevLsb := st.prevPOCMsb, st.prevPOCLsb
		if h.idr {
			prevMsb, prevLsb = 0, 0
		}
		maxLsb := 1 << uint(sps.Log2MaxPOCLsb)
		msb := prevMsb
		if h.pocLsb < prevLsb && prevLsb-h.pocLsb >= maxLsb/2 {
			msb = prevMsb + maxLsb
		} else if h.pocLsb > prevLsb && h.pocLsb-prevLsb > maxLsb/2 {
			msb = prevMsb - maxLsb
		}
		st.pocMsb = msb
		top := msb + h.pocLsb
		bot := top + h.deltaPOCBottom
		if bot < top {
			return bot
		}
		return top
	case 1, 2:
		off := st.prevFrameNumOffset
		if h.idr {
			off = 0
		} else if st.prevFrameNum > h.frameNum {
			off = st.prevFrameNumOffset + maxFrameNum
		}
		st.frameNumOffset = off
		if sps.POCType == 2 {
			if h.idr {
				return 0
			}
			t := 2 * (off + h.frameNum)
			if h.nalRefIdc == 0 {
				t--
			}
			return t
		}
		n := len(sps.OffsetForRefFrame)
		abs := 0
		if n != 0 {
			abs = off + h.frameNum
		}
		if h.nalRefIdc == 0 && abs > 0 {
			abs--
		}
		expected := 0
		if abs > 0 {
			var perCycle int32
			for _, o := range sps.OffsetForRefFrame {
				perCycle += o
			}
			cyc := (abs - 1) / n
			in := (abs - 1) % n
			expected = cyc * int(perCycle)
			for i := 0; i <= in; i++ {
				expected += int(sps.OffsetForRefFrame[i])
			}
		}
		if h.nalRefIdc == 0 {
			expected += int(sps.OffsetNonRef)
		}
		top := expected + h.deltaPOC[0]
		bot := top + int(sps.OffsetTopToBottom) + h.deltaPOC[1]
		if bot < top {
			return bot
		}
		return top
	}
	return 0
}

// ---- reference marking (8.2.5) ------------------------------------------------------

func (d *Decoder) refFrames() []*Picture {
	var out []*Picture
	for _, p := range d.dpb {
		if p.isRef() {
			out = append(out, p)
		}
	}
	return out
}

func (d *Decoder) removeUnused() {
	n := d.dpb[:0]
	for _, p := range d.dpb {
		if p.isRef() {
			n = append(n, p)
		}
	}
	d.dpb = n
}

// markCurrent applies the marking process for the just decoded picture and
// returns whether an MMCO 5 occurred.
func (d *Decoder) markCurrent(h *sliceHeader, cur *Picture) (mmco5 bool) {
	sps := h.sps
	maxFrameNum := 1 << uint(sps.Log2MaxFrameNum)
	if h.nalRefIdc == 0 {
		return false
	}
	if h.idr {
		for _, p := range d.dpb {
			p.shortRef, p.longRef = false, false
		}
		d.dpb = d.dpb[:0]
		if h.longTermRef {
			cur.longRef = true
			cur.longIdx = 0
			d.maxLongIdx = 0
		} else {
			cur.shortRef = true
			d.maxLongIdx = -1
		}
		d.dpb = append(d.dpb, cur)
		return false
	}
	// FrameNumWrap of the short-term frames
	for _, p := range d.dpb {
		if p.shortRef {
			if p.frameNum > h.frameNum {
				p.frameWrap = p.frameNum - maxFrameNum
			} else {
				p.frameWrap = p.frameNum
			}
		}
	}
	cur.longRef = false
	markedLong := false
	if h.adaptiveMarking {
		for _, m := range h.mmcos {
			switch m.op {
			case 1:
				pn := h.frameNum - (m.a + 1)
				for _, p := range d.dpb {
					if p.shortRef && p.frameWrap == pn {
						p.shortRef = false
					}
				}
			case 2:
				for _, p := range d.dpb {
					if p.longRef && p.longIdx == m.a {
						p.longRef = false
					}
				}
			case 3:
				pn := h.frameNum - (m.a + 1)
				for _, p := range d.dpb {
					if p.longRef && p.longIdx == m.b {
						p.longRef = false
					}
				}
				for _, p := range d.dpb {
					if p.shortRef && p.frameWrap == pn {
						p.shortRef = false
						p.longRef = true
						p.longIdx = m.b
					}
				}
			case 4:
				d.maxLongIdx = m.a - 1
				for _, p := range d.dpb {
					if p.longRef && p.longIdx > d.maxLongIdx {
						p.longRef = false
					}
				}
			case 5:
				for _, p := range d.dpb {
					p.shortRef, p.longRef = false, false
				}
				d.maxLongIdx = -1
				mmco5 = true
			case 6:
				for _, p := range d.dpb {
					if p.longRef && p.longIdx == m.a {
						p.longRef = false
					}
				}
				cur.longRef = true
				cur.longIdx = m.a
				markedLong = true
			}
		}
	} else {
		// sliding window
		numShort, numLong := 0, 0
		for _, p := range d.dpb {
			if p.shortRef {
				numShort++
			}
			if p.longRef {
				numLong++
			}
		}
		maxRef := sps.MaxNumRefFrames
		if maxRef < 1 {
			maxRef = 1
		}
		if numShort+numLong >= maxRef && numShort > 0 {
			var oldest *Picture
			for _, p := range d.dpb {
				if p.shortRef && (oldest == nil || p.frameWrap < oldest.frameWrap) {
					oldest = p
				}
			}
			if oldest != nil {
				oldest.shortRef = false
			}
		}
	}
	if !markedLong {
		cur.shortRef = true
	}
	d.removeUnused()
	d.dpb = append(d.dpb, cur)
	// bound the DPB defensively
	for len(d.dpb) > 17 {
		d.dpb = d.dpb[1:]
	}
	return mmco5
}

// ---- reference list construction (8.2.4) -----------------------------------------------

func (d *Decoder) buildRefLists(h *sliceHeader, cur *Picture) {
	d.refList = [2][]*Picture{}
	if h.isI() {
		return
	}
	maxFrameNum := 1 << uint(h.sps.Log2MaxFrameNum)
	var shorts, longs []*Picture
	for _, p := range d.dpb {
		if p == cur {
			continue
		}
		if p.shortRef {
			if p.frameNum > h.frameNum {
				p.frameWrap = p.frameNum - maxFrameNum
			} else {
				p.frameWrap = p.frameNum
			}
			shorts = append(shorts, p)
		} else if p.longRef {
			longs = append(longs, p)
		}
	}
	sort.SliceStable(longs, func(i, j int) bool { return longs[i].longIdx < longs[j].longIdx })
	var l0, l1 []*Picture
	if !h.isB() {
		sort.SliceStable(shorts, func(i, j int) bool { return shorts[i].frameWrap > shorts[j].frameWrap })
		l0 = append(append(l0, shorts...), longs...)
	} else {
		var before, after []*Picture
		for _, p := range shorts {
			if p.poc < cur.poc {
				before = append(before, p)
			} else {
				after = append(after, p)
			}
		}
		sort.SliceStable(before, func(i, j int) bool { return before[i].poc > before[j].poc })
		sort.SliceStable(after, func(i, j int) bool { return after[i].poc < after[j].poc })
		l0 = append(append(append(l0, before...), after...), longs...)
		l1 = append(append(append(l1, after...), before...), longs...)
		if len(l1) > 1 && len(l0) == len(l1) {
			same := true
			for i := range l0 {
				if l0[i] != l1[i] {
					same = false
					break
				}
			}
			if same {
				l1[0], l1[1] = l1[1], l1[0]
			}
		}
	}
	lists := [2][]*Picture{l0, l1}
	for l := 0; l < 2; l++ {
		n := h.numRef[l]
		if n == 0 {
			continue
		}
		lst := make([]*Picture, n)
		copy(lst, lists[l])
		d.refList[l] = lst
		// modification
		pred := h.frameNum
		idx := 0
		for _, m := range h.refMods[l] {
			if idx >= n {
				break
			}
			var target *Picture
			switch m.idc {
			case 0, 1:
				if m.idc == 0 {
					pred -= m.val + 1
					if pred < 0 {
						pred += maxFrameNum
					}
				} else {
					pred += m.val + 1
					if pred >= maxFrameNum {
						pred -= maxFrameNum
					}
				}
				pn := pred
				if pn > h.frameNum {
					pn -= maxFrameNum
				}
				for _, p := range shorts {
					if p.frameWrap == pn {
						target = p
					}
				}
			case 2:
				for _, p := range longs {
					if p.longIdx == m.val {
						target = p
					}
				}
			default:
				continue
			}
			if target == nil {
				continue
			}
			// insert at idx and drop the later duplicate (8.2.4.3.1)
			tmp := make([]*Picture, n+1)
			copy(tmp, lst)
			copy(tmp[idx+1:], tmp[idx:n])
			tmp[idx] = target
			idx++
			k := idx
			for j := idx; j <= n; j++ {
				if tmp[j] != target {
					tmp[k] = tmp[j]
					k++
				}
			}
			copy(lst, tmp[:n])
			for q := k; q < n; q++ {
				lst[q] = nil
			}
		}
		// fill missing entries
		var fallback *Picture
		for _, p := range lst {
			if p != nil {
				fallback = p
				break
			}
		}
		if fallback == nil {
			for _, p := range d.dpb {
				if p != cur {
					fallback = p
				}
			}
		}
		for i := range lst {
			if lst[i] == nil {
				if fallback == nil {
					fallback = d.grayPicture(cur)
				}
				lst[i] = fallback
			}
		}
	}
}

func (d *Decoder) grayPicture(like *Picture) *Picture {
	p := newPicture(-2, like.mbW, like.mbH, d.first.sps)
	for i := range p.y {
		p.y[i] = uint16(1) << uint(d.f.bdY-1)
	}
	for i := range p.cb {
		p.cb[i], p.cr[i] = uint16(1)<<uint(d.f.bdC-1), uint16(1)<<uint(d.f.bdC-1)
	}
	p.nonExist = true
	return p
}
