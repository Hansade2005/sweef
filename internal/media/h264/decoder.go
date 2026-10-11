package h264

import (
	"errors"
	"fmt"
	"image"
	"runtime/debug"

	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// DebugPanic, when set, receives the value and stack of a recovered decoder panic.
var DebugPanic func(v any, stack []byte)

// DebugSlice, when set, receives a summary of every parsed slice header.
var DebugSlice func(string)

// fmtInfo caches the sample format of the active sequence.
type fmtInfo struct {
	cat          int // ChromaArrayType
	cf           int // chroma_format_idc
	sep          bool
	bdY, bdC     int
	qpBdY, qpBdC int
	maxY, maxC   int32
	mbWC, mbHC   int // chroma macroblock size (0 when not coded)
	nBlkC        int // 4x4 chroma blocks per plane (cat 1: 4, cat 2: 8)
	bypass       bool
}

func newFmtInfo(s *SPS) fmtInfo {
	f := fmtInfo{cat: s.ChromaArrayType(), cf: s.ChromaFormat, sep: s.SeparatePlanes, bdY: s.BitDepthLuma, bdC: s.BitDepthChroma, bypass: s.Bypass}
	f.qpBdY, f.qpBdC = 6*(f.bdY-8), 6*(f.bdC-8)
	f.maxY, f.maxC = int32(1)<<uint(f.bdY)-1, int32(1)<<uint(f.bdC)-1
	switch f.cat {
	case 1:
		f.mbWC, f.mbHC, f.nBlkC = 8, 8, 4
	case 2:
		f.mbWC, f.mbHC, f.nBlkC = 8, 16, 8
	case 3:
		f.mbWC, f.mbHC, f.nBlkC = 16, 16, 16
	}
	return f
}

type sliceParams struct {
	disable        int
	alpha, beta    int
	cbOff, crOff   int
	listCount      int
}

// Decoder decodes an H.264 stream access unit by access unit.
type Decoder struct {
	// OutPOC and OutSeq describe the picture returned by the last
	// DecodeFrame call: pictures of one sequence (OutSeq) are displayed in
	// ascending OutPOC order; a new sequence starts at each IDR picture and
	// after each memory_management_control_operation 5.
	OutPOC, OutSeq int
	seqNo          int

	f       fmtInfo
	sps     [32]*SPS
	pps     [256]*PPS
	lenSize int

	// reference pictures
	shortRef   []*Picture
	longRef    [16]*Picture
	longCount  int
	defaultRef [2]refEntry
	grey       *Picture
	poc        pocState

	// the picture being decoded
	cur          *Picture // alias of curPic used by sample access helpers
	curPic       *Picture
	first        *sliceHeader // header of the first slice of the picture
	nextID       int32
	firstField   bool
	picStructure int
	fieldPic     bool
	mbaffFrame   bool
	droppable    bool
	picActive    bool
	lastStruct   int
	lastDrop     bool
	pending      *Picture // completed frame to return

	mbW, mbH, mbStride int
	sliceTable         []uint16
	qpTable            []int8
	nz                 []uint8
	cbpTable           []uint16
	chromaPredTable    []uint8
	ipm                []int8
	mvd                [2][][2]uint8
	directTable        []uint8
	mbNZMask           []uint16
	mbBypass           []bool
	slices             []sliceParams
	sliceNum           int
	decodedMB          int
	curSlice           uint16

	// current slice
	hdr           *sliceHeader
	br            *bitReader
	cab           cabacDec
	cabacOn       bool
	qp            int
	lastDQ        bool
	skipRun       int
	prevMBSkipped bool
	nextMBSkipped bool
	listCount     int
	refList       [2][]refEntry
	refListField  [2][]refEntry
	fmoMap        []uint8
	fmoNext       []int
	fmoPos        int
	rowAdvanced   bool

	implicitW          [3][64][64]int16
	distScale          [64]int
	distScaleField     [2][64]int
	mapColToList0      [2][16 + 32]int
	mapColToList0Field [2][2][16 + 32]int
	colParity          int
	colFieldOff        int

	// current macroblock
	mbx, mby, mbIdx int
	mbField         bool
	nb              nbState
	availA, availB  bool
	availC, availD  bool
	nzCache         [3][40]uint8
	mvCache         [2][40]mv16
	refCache        [2][40]int8
	mvdCache        [2][40][2]uint8
	directCache     [40]uint8
	ipmCache        [40]int8
	subMbType       [4]uint32
	topCBP, leftCBP int
	neighborT8      int
	cbpBits         uint16
	nzMask          uint16
	curDirect       uint8
	blkNZ           [48]bool
	bypassMB        bool
	scan4           *[16]uint8
	scan8           *[64]uint8
	mbOff, mbStr    [3]int

	// reconstruction scratch
	coef  [768]int32
	lv    [64]int32
	dcY   [3][16]int32
	pred  [2][3 * 256]uint16
	tmpMC [(16 + 5) * (16 + 5)]uint16
	tmpI  [(16 + 5) * 16]int32
}

// NewDecoder returns an empty decoder.
func NewDecoder() *Decoder {
	vlcOnce.Do(initVLC)
	d := &Decoder{lenSize: 0}
	d.resetPOC()
	return d
}

// Reset drops all reference pictures (used when seeking).
func (d *Decoder) Reset() {
	d.removeAllRefs()
	d.curPic, d.cur = nil, nil
	d.picActive = false
	d.firstField = false
	d.pending = nil
	d.resetPOC()
}

// SetAVCC configures the decoder from an AVCDecoderConfigurationRecord and
// switches it to length-prefixed NAL input.
func (d *Decoder) SetAVCC(avcc []byte) error {
	if len(avcc) < 7 || avcc[0] != 1 {
		return errors.New("h264: bad avcC")
	}
	d.lenSize = int(avcc[4]&3) + 1
	p := 5
	n := int(avcc[p] & 31)
	p++
	for i := 0; i < n; i++ {
		if p+2 > len(avcc) {
			return errors.New("h264: bad avcC")
		}
		l := int(avcc[p])<<8 | int(avcc[p+1])
		p += 2
		if p+l > len(avcc) {
			return errors.New("h264: bad avcC")
		}
		d.handleNAL(avcc[p : p+l])
		p += l
	}
	if p >= len(avcc) {
		return nil
	}
	m := int(avcc[p])
	p++
	for i := 0; i < m; i++ {
		if p+2 > len(avcc) {
			return errors.New("h264: bad avcC")
		}
		l := int(avcc[p])<<8 | int(avcc[p+1])
		p += 2
		if p+l > len(avcc) {
			return errors.New("h264: bad avcC")
		}
		d.handleNAL(avcc[p : p+l])
		p += l
	}
	return nil
}

// Decode decodes one access unit and returns its picture as an 8-bit
// image.YCbCr (in decoding order, not display order), cropped to the SPS
// frame cropping rectangle. Streams with other sample depths must use
// DecodeFrame.
func (d *Decoder) Decode(data []byte) (*image.YCbCr, error) {
	fr, err := d.DecodeFrame(data)
	if fr == nil {
		return nil, err
	}
	img, ok := fr.ToYCbCr()
	if !ok {
		return nil, ErrUnsupported
	}
	return img, err
}

// DecodeFrame decodes one access unit and returns its picture in any sample
// format, in decoding order, cropped to the SPS frame cropping rectangle.
// A frame whose second field is still to come is returned by the call that
// decodes that field.
func (d *Decoder) DecodeFrame(data []byte) (img *yuv.Frame, err error) {
	defer func() {
		if r := recover(); r != nil {
			if DebugPanic != nil {
				DebugPanic(r, debug.Stack())
			}
			d.picActive = false
			d.curPic, d.cur = nil, nil
			d.firstField = false
			img, err = nil, errBitstream
		}
	}()
	var nals [][]byte
	if d.lenSize == 0 {
		nals = splitAnnexB(data)
	} else {
		nals = splitLengthPrefixed(data, d.lenSize)
	}
	var firstErr error
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		if err := d.handleNAL(n); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	d.finishPicture()
	pic := d.pending
	d.pending = nil
	if pic == nil {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, nil
	}
	d.OutPOC, d.OutSeq = pic.poc, pic.seq
	return d.pictureFrame(pic), firstErr
}

// pictureFrame wraps a decoded picture as a cropped output frame.
func (d *Decoder) pictureFrame(p *Picture) *yuv.Frame {
	f := &yuv.Frame{Depth: p.bdY, Matrix: p.colorMtx, FullRange: p.fullRange}
	cl, cr, ct, cb := p.crop[0], p.crop[1], p.crop[2], p.crop[3]
	f.Width, f.Height = p.w-cl-cr, p.h-ct-cb
	f.Planes[0] = p.y[ct*p.ys+cl:]
	f.Stride[0] = p.ys
	switch p.cf {
	case 0:
		f.Format = yuv.F400
		return f
	case 1:
		f.Format = yuv.F420
	case 2:
		f.Format = yuv.F422
	default:
		f.Format = yuv.F444
	}
	sx, sy := f.Format.Subsampling()
	f.CWidth, f.CHeight = f.Width>>sx, f.Height>>sy
	co := (ct>>sy)*p.cs + cl>>sx
	f.Planes[1], f.Planes[2] = p.cb[co:], p.cr[co:]
	f.Stride[1], f.Stride[2] = p.cs, p.cs
	return f
}

func (d *Decoder) handleNAL(n []byte) error {
	typ := int(n[0] & 31)
	refIdc := int(n[0]>>5) & 3
	switch typ {
	case 7:
		s, err := parseSPS(unescape(n[1:]))
		if err != nil {
			return err
		}
		d.setSPS(s)
	case 8:
		p, err := parsePPS(unescape(n[1:]), &d.sps)
		if err != nil {
			return err
		}
		d.pps[p.ID] = p
	case 1, 5:
		return d.handleSlice(n, typ, refIdc)
	case 2, 3, 4:
		return ErrUnsupported
	}
	return nil
}

// setSPS stores a sequence parameter set. A set identical to the one in use
// is ignored; a changed one re-resolves the picture parameter sets that
// refer to it.
func (d *Decoder) setSPS(s *SPS) {
	old := d.sps[s.ID]
	if old != nil && string(old.raw) == string(s.raw) {
		return
	}
	d.sps[s.ID] = s
	for i, p := range d.pps {
		if p != nil && p.SPSID == s.ID {
			if np, err := parsePPS(p.raw, &d.sps); err == nil {
				d.pps[i] = np
			} else {
				d.pps[i] = nil
			}
		}
	}
}

func (d *Decoder) handleSlice(n []byte, typ, refIdc int) error {
	r := newBitReader(unescape(n[1:]))
	h, err := d.parseSliceHeader(r, typ, refIdc)
	if err != nil {
		return err
	}
	if DebugSlice != nil {
		DebugSlice(fmt.Sprintf("nal=%d ref=%d first=%d type=%d fn=%d struct=%d poc=%d numRef=%v mods=%v mmco=%v adaptive=%v idr=%v long=%v", typ, refIdc, h.firstMB, h.sliceType, h.frameNum, h.structure, h.pocLsb, h.numRef, h.refMods, h.mmcos, h.adaptiveMarking, h.idr, h.longTermRef))
	}
	if h.redundantPicCnt > 0 {
		return nil
	}
	if d.picActive && d.isNewPicture(h) {
		d.finishPicture()
	}
	if !d.picActive {
		if err := d.startPicture(h); err != nil {
			return err
		}
	}
	return d.decodeSlice(h, r)
}

// isNewPicture implements the first-slice-of-a-picture detection of 7.4.1.2.4.
func (d *Decoder) isNewPicture(h *sliceHeader) bool {
	f := d.first
	switch {
	case f.frameNum != h.frameNum, f.pps.ID != h.pps.ID, f.fieldPic != h.fieldPic, f.bottom != h.bottom:
		return true
	case (f.nalRefIdc == 0) != (h.nalRefIdc == 0):
		return true
	case f.idr != h.idr:
		return true
	case f.idr && f.idrPicID != h.idrPicID:
		return true
	}
	switch h.sps.POCType {
	case 0:
		if f.pocLsb != h.pocLsb || f.deltaPOCBottom != h.deltaPOCBottom {
			return true
		}
	case 1:
		if f.deltaPOC != h.deltaPOC {
			return true
		}
	}
	return false
}

func (d *Decoder) allocTables(sps *SPS) {
	d.mbW, d.mbH = sps.MbW, sps.MbH
	d.mbStride = d.mbW + 1
	n := (d.mbH+3)*d.mbStride + 1
	d.sliceTable = make([]uint16, n)
	d.qpTable = make([]int8, n)
	d.nz = make([]uint8, n*48)
	d.cbpTable = make([]uint16, n)
	d.chromaPredTable = make([]uint8, n)
	d.ipm = make([]int8, n*16)
	for l := 0; l < 2; l++ {
		d.mvd[l] = make([][2]uint8, n*16)
	}
	d.directTable = make([]uint8, n*4)
	d.mbNZMask = make([]uint16, n)
	d.mbBypass = make([]bool, n)
}

// startPicture begins a new frame, field or second field.
func (d *Decoder) startPicture(h *sliceHeader) error {
	sps := h.sps
	if d.first != nil && d.first.sps != sps && (d.mbW != sps.MbW || d.mbH != sps.MbH || d.f.cf != sps.ChromaFormat || d.f.bdY != sps.BitDepthLuma || d.f.bdC != sps.BitDepthChroma) {
		d.flushForNewSequence()
	}
	if d.sliceTable == nil || d.mbW != sps.MbW || d.mbH != sps.MbH {
		d.allocTables(sps)
		d.flushForNewSequence()
	}
	d.f = newFmtInfo(sps)
	st := &d.poc
	if h.idr {
		d.removeAllRefs()
		d.shortRef = d.shortRef[:0]
		d.resetPOC()
		d.seqNo++
	}
	lastStruct, lastDrop := d.lastStruct, d.lastDrop
	d.droppable = h.nalRefIdc == 0
	d.picStructure = h.structure
	d.fieldPic = h.structure != picFrame
	d.mbaffFrame = sps.MBAFF && !d.fieldPic
	st.frameNum = h.frameNum
	st.pocLsb = h.pocLsb
	st.deltaPOCBottom = h.deltaPOCBottom
	st.deltaPOC = h.deltaPOC
	maxFrameNum := 1 << uint(sps.Log2MaxFrameNum)

	// shorten frame_num gaps so no more frames than can be referenced are made
	if st.frameNum != st.prevFrameNum {
		unwrap := st.prevFrameNum
		if unwrap > st.frameNum {
			unwrap -= maxFrameNum
		}
		if st.frameNum-unwrap > sps.MaxNumRefFrames {
			unwrap = st.frameNum - sps.MaxNumRefFrames - 1
			if unwrap < 0 {
				unwrap += maxFrameNum
			}
			st.prevFrameNum = unwrap
		}
	}
	if d.firstField && d.curPic != nil {
		// a pending first field: decide whether this slice pairs with it
		switch {
		case !d.fieldPic || d.picStructure == lastStruct:
			// the previous field stays unpaired
		case d.curPic.frameNum != st.frameNum:
		default:
			if (lastStruct == picTop && d.picStructure != picBottom) || (lastStruct == picBottom && d.picStructure != picTop) || lastDrop != d.droppable {
				// invalid combination: treat as a new frame
			}
		}
	}
	for st.frameNum != st.prevFrameNum && !d.firstField && st.frameNum != (st.prevFrameNum+1)%maxFrameNum {
		if !d.makeGapFrame(h, maxFrameNum) {
			break
		}
	}
	if d.firstField && d.curPic != nil {
		switch {
		case !d.fieldPic || d.picStructure == lastStruct:
			d.curPic = nil
			d.firstField = d.fieldPic
		case d.curPic.frameNum != st.frameNum:
			d.firstField = true
			d.curPic = nil
		default:
			d.firstField = false
		}
	} else {
		d.firstField = d.fieldPic
	}
	if !d.fieldPic || d.firstField || d.curPic == nil {
		d.nextID++
		pic := newPicture(d.nextID, sps)
		pic.frameNum = st.frameNum
		pic.fieldPicture = d.fieldPic
		pic.mbaff = d.mbaffFrame
		pic.seq = d.seqNo
		pic.idr = h.idr
		d.curPic = pic
		if d.fieldPic {
			d.firstField = true
		}
	}
	d.cur = d.curPic
	if d.curPic.mbaff != d.mbaffFrame && !d.fieldPic {
		d.curPic.mbaff = d.mbaffFrame
	}
	d.lastStruct, d.lastDrop = d.picStructure, d.droppable
	// reset the slice table of this picture / field
	if d.fieldPic {
		for r := btoi(d.picStructure == picBottom); r < d.mbH; r += 2 {
			base := (r + 2) * d.mbStride
			for i := 0; i < d.mbStride; i++ {
				d.sliceTable[base+i] = 0xffff
			}
		}
	} else {
		for i := range d.sliceTable {
			d.sliceTable[i] = 0xffff
		}
	}
	d.initPOC(d.curPic, sps, d.picStructure, h.nalRefIdc)
	d.curPic.reference = d.curPic.reference // keep marking of an already decoded first field
	if h.nalRefIdc != 0 && d.picStructure != picFrame && d.curPic.reference == 0 {
		// marked at the end of the field
	}
	d.first = h
	d.slices = d.slices[:0]
	d.sliceNum = 0
	d.decodedMB = 0
	d.picActive = true
	return nil
}

// makeGapFrame inserts a substitute frame for a missing frame_num.
func (d *Decoder) makeGapFrame(h *sliceHeader, maxFrameNum int) bool {
	st := &d.poc
	sps := h.sps
	prev := (*Picture)(nil)
	if len(d.shortRef) > 0 {
		prev = d.shortRef[0]
	}
	d.nextID++
	pic := newPicture(d.nextID, sps)
	st.prevFrameNum = (st.prevFrameNum + 1) % maxFrameNum
	pic.frameNum = st.prevFrameNum
	pic.invalidGap = !sps.GapsAllowed
	pic.fieldPOC = [2]int{0, 0}
	pic.seq = d.seqNo
	if prev != nil && prev.w == pic.w && prev.h == pic.h && prev.cf == pic.cf {
		copy(pic.y, prev.y)
		copy(pic.cb, prev.cb)
		copy(pic.cr, prev.cr)
		pic.poc = prev.poc + 2
		pic.fieldPOC = [2]int{prev.fieldPOC[0] + 2, prev.fieldPOC[1] + 2}
	} else {
		for i := range pic.y {
			pic.y[i] = uint16(1) << uint(sps.BitDepthLuma-1)
		}
		for i := range pic.cb {
			pic.cb[i], pic.cr[i] = uint16(1)<<uint(sps.BitDepthChroma-1), uint16(1)<<uint(sps.BitDepthChroma-1)
		}
	}
	d.curPic = pic
	gapHdr := &sliceHeader{sps: sps}
	saved := d.picStructure
	d.picStructure = picFrame
	d.executeRefMarking(gapHdr, picFrame, false)
	d.picStructure = saved
	d.curPic = nil
	st.prevFrameNumOffset = st.frameNumOffset
	return true
}

func (d *Decoder) flushForNewSequence() {
	d.removeAllRefs()
	d.shortRef = d.shortRef[:0]
	d.curPic, d.cur = nil, nil
	d.firstField = false
	d.grey = nil
	d.resetPOC()
}

// finishPicture completes the current frame or field: concealment,
// deblocking, reference marking and POC state.
func (d *Decoder) finishPicture() {
	if !d.picActive {
		return
	}
	d.picActive = false
	pic := d.curPic
	h := d.first
	if d.decodedMB < d.mbCountPicture() {
		d.conceal(pic)
	}
	d.deblockPicture(pic)
	st := &d.poc
	if !d.droppable {
		if d.executeRefMarking(h, d.picStructure, d.firstField) {
			// MMCO 5: frame_num restarts at 0 (POC handling follows FFmpeg)
		}
		st.prevPOCMsb = st.pocMsb
		st.prevPOCLsb = st.pocLsb
	}
	st.prevFrameNumOffset = st.frameNumOffset
	st.prevFrameNum = st.frameNum
	if pic.mmcoReset {
		d.seqNo++
		pic.seq = d.seqNo
	}
	if !d.fieldPic || !d.firstField {
		d.pending = pic
	}
}

func (d *Decoder) mbCountPicture() int {
	if d.fieldPic {
		return d.mbW * d.mbH / 2
	}
	return d.mbW * d.mbH
}

// conceal copies undecoded macroblocks from the nearest reference picture.
func (d *Decoder) conceal(pic *Picture) {
	var src *Picture
	for _, p := range d.shortRef {
		if p != pic {
			src = p
			break
		}
	}
	neutralY := uint16(1) << uint(d.f.bdY-1)
	neutralC := uint16(1) << uint(d.f.bdC-1)
	step := 1
	row0 := 0
	if d.fieldPic {
		step = 2
		row0 = btoi(d.picStructure == picBottom)
	}
	for mby := row0; mby < d.mbH; mby += step {
		for mbx := 0; mbx < d.mbW; mbx++ {
			idx := pic.mbIdx(mbx, mby)
			if d.sliceTable[idx] != 0xffff {
				continue
			}
			// rows of this macroblock (field macroblocks interleave)
			rows := make([]int, 16)
			for y := 0; y < 16; y++ {
				if d.fieldPic {
					rows[y] = ((mby>>1)*16+y)*2 + (mby & 1)
				} else {
					rows[y] = mby*16 + y
				}
			}
			for y := 0; y < 16; y++ {
				o := rows[y]*pic.ys + mbx*16
				if src != nil {
					copy(pic.y[o:o+16], src.y[o:o+16])
				} else {
					for i := 0; i < 16; i++ {
						pic.y[o+i] = neutralY
					}
				}
			}
			if pic.cw > 0 {
				w, h := 8, 8
				if pic.cf == 2 {
					h = 16
				} else if pic.cf == 3 {
					w, h = 16, 16
				}
				for y := 0; y < h; y++ {
					var row int
					if d.fieldPic {
						row = ((mby>>1)*h+y)*2 + (mby & 1)
					} else {
						row = mby*h + y
					}
					o := row*pic.cs + mbx*w
					for i := 0; i < w; i++ {
						if src != nil {
							pic.cb[o+i], pic.cr[o+i] = src.cb[o+i], src.cr[o+i]
						} else {
							pic.cb[o+i], pic.cr[o+i] = neutralC, neutralC
						}
					}
				}
			}
			pic.mbType[idx] = mbSkip | mb16x16 | mbP0L0
			d.qpTable[idx] = 0
			d.mbNZMask[idx] = 0
			d.sliceTable[idx] = 0xfffe
		}
	}
}
