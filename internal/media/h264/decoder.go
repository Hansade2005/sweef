package h264

import (
	"errors"
	"fmt"
	"image"
	"runtime/debug"

	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// MB type classes stored per macroblock.
const (
	mtI4x4 = iota // Intra NxN (4x4 or 8x8)
	mtI16
	mtPCM
	mtInter
	mtPSkip
	mtBSkip
	mtBDirect // B_Direct_16x16
)

func isIntraMB(t uint8) bool { return t <= mtPCM }

type sliceParams struct {
	disable        int
	alpha, beta    int
	cbOff, crOff   int
	chromaOffCount int
}

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

// Decoder decodes an H.264 stream access unit by access unit.
type Decoder struct {
	// OutPOC and OutSeq describe the picture returned by the last
	// DecodeFrame call: pictures of one sequence (OutSeq) are displayed in
	// ascending OutPOC order; a new sequence starts at each IDR picture and
	// after each memory_management_control_operation 5.
	OutPOC, OutSeq int
	seqNo          int

	f          fmtInfo
	sps        [32]*SPS
	pps        [256]*PPS
	lenSize    int
	dpb        []*Picture
	poc        pocState
	maxLongIdx int
	nextID     int32

	// current picture
	cur       *Picture
	first     *sliceHeader
	mbW, mbH  int
	sliceNum  int32
	mbSlice   []int32
	mbType    []uint8
	mbT8      []bool
	mbCBP     []uint8
	mbQP      []int8
	mbChroma  []uint8
	mbDirect  []uint8
	mbDC      []uint8
	nz        []uint8
	ipm       []int8
	mvd       [2][][2]uint8
	nzMask    []uint16
	slices    []sliceParams
	decodedMB int

	// current slice
	hdr      *sliceHeader
	refList  [2][]*Picture
	br       *bitReader
	cab      cabacDec
	cabacOn  bool
	qp       int
	lastDQ   bool // previous MB had a non-zero mb_qp_delta (CABAC context)
	skipRun  int
	prevSkip bool
	mbx, mby int
	mbIdx    int
	curSlice int32

	// per-MB neighbour availability
	availA, availB, availC, availD bool

	// implicit bi-prediction weights [ref0][ref1] for the slice
	implicitW [32][32]int16

	// reconstruction scratch
	coef  [768]int32
	lv    [64]int32
	dcY   [3][16]int32
	pred  [2][3 * 256]uint16
	tmpMC [(16 + 5) * (16 + 5)]uint16
	tmpI  [(16 + 5) * 16]int32

	// colocated data for direct prediction
	col *Picture

	curDirect uint8 // direct-predicted 8x8 blocks of the current MB
	blkNZ     [48]bool
	i16DC     bool
	bypassMB  bool // transform bypass (lossless) macroblock
	fieldMB   bool // field macroblock (field scans, field CABAC contexts)
	scan4     *[16]uint8
	scan8     *[64]uint8
}

// NewDecoder returns an empty decoder.
func NewDecoder() *Decoder {
	vlcOnce.Do(initVLC)
	return &Decoder{lenSize: 0}
}

// Reset drops all reference pictures (used when seeking).
func (d *Decoder) Reset() {
	d.dpb = nil
	d.cur = nil
	d.poc = pocState{}
	d.maxLongIdx = -1
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
func (d *Decoder) DecodeFrame(data []byte) (img *yuv.Frame, err error) {
	defer func() {
		if r := recover(); r != nil {
			if DebugPanic != nil {
				DebugPanic(r, debug.Stack())
			}
			d.cur = nil
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
	pic := d.finishPicture()
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
		d.sps[s.ID] = s
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

func (d *Decoder) handleSlice(n []byte, typ, refIdc int) error {
	r := newBitReader(unescape(n[1:]))
	h, err := d.parseSliceHeader(r, typ, refIdc)
	if err != nil {
		return err
	}
	if DebugSlice != nil {
		DebugSlice(fmt.Sprintf("nal=%d ref=%d first=%d type=%d fn=%d field=%v/%v poc=%d numRef=%v mods=%v mmco=%v adaptive=%v idr=%v long=%v", typ, refIdc, h.firstMB, h.sliceType, h.frameNum, h.fieldPic, h.bottom, h.pocLsb, h.numRef, h.refMods, h.mmcos, h.adaptiveMarking, h.idr, h.longTermRef))
	}
	if h.redundantPicCnt > 0 {
		return nil
	}
	if d.cur == nil {
		if err := d.startPicture(h); err != nil {
			return err
		}
	}
	return d.decodeSlice(h, r)
}

func (d *Decoder) startPicture(h *sliceHeader) error {
	sps := h.sps
	if h.idr {
		d.poc.prevPOCMsb, d.poc.prevPOCLsb = 0, 0
		d.seqNo++
	}
	d.mbW, d.mbH = sps.MbW, sps.MbH
	d.nextID++
	d.f = newFmtInfo(sps)
	pic := newPicture(d.nextID, d.mbW, d.mbH, sps)
	pic.cf, pic.bdY, pic.bdC = sps.ChromaFormat, sps.BitDepthLuma, sps.BitDepthChroma
	pic.frameNum = h.frameNum
	pic.poc = d.computePOC(h)
	pic.crop = [4]int{sps.CropL, sps.CropR, sps.CropT, sps.CropB}
	pic.colorMtx = sps.ColorMatrix
	pic.fullRange = sps.FullRange
	d.cur = pic
	d.first = h
	n := d.mbW * d.mbH
	d.mbSlice = resizeI32(d.mbSlice, n, -1)
	d.mbType = resizeU8(d.mbType, n)
	if cap(d.mbT8) >= n {
		d.mbT8 = d.mbT8[:n]
		for i := range d.mbT8 {
			d.mbT8[i] = false
		}
	} else {
		d.mbT8 = make([]bool, n)
	}
	d.mbCBP = resizeU8(d.mbCBP, n)
	if cap(d.mbQP) >= n {
		d.mbQP = d.mbQP[:n]
		for i := range d.mbQP {
			d.mbQP[i] = 0
		}
	} else {
		d.mbQP = make([]int8, n)
	}
	d.mbChroma = resizeU8(d.mbChroma, n)
	d.mbDirect = resizeU8(d.mbDirect, n)
	d.mbDC = resizeU8(d.mbDC, n)
	d.nz = resizeU8(d.nz, n*48)
	if cap(d.ipm) >= n*16 {
		d.ipm = d.ipm[:n*16]
		for i := range d.ipm {
			d.ipm[i] = 2
		}
	} else {
		d.ipm = make([]int8, n*16)
		for i := range d.ipm {
			d.ipm[i] = 2
		}
	}
	for l := 0; l < 2; l++ {
		w4 := d.mbW * 4
		if cap(d.mvd[l]) >= w4*d.mbH*4 {
			d.mvd[l] = d.mvd[l][:w4*d.mbH*4]
			for i := range d.mvd[l] {
				d.mvd[l][i] = [2]uint8{}
			}
		} else {
			d.mvd[l] = make([][2]uint8, w4*d.mbH*4)
		}
	}
	if cap(d.nzMask) >= n {
		d.nzMask = d.nzMask[:n]
		for i := range d.nzMask {
			d.nzMask[i] = 0
		}
	} else {
		d.nzMask = make([]uint16, n)
	}
	d.slices = d.slices[:0]
	d.sliceNum = 0
	d.decodedMB = 0
	return nil
}

func resizeI32(s []int32, n int, v int32) []int32 {
	if cap(s) >= n {
		s = s[:n]
	} else {
		s = make([]int32, n)
	}
	for i := range s {
		s[i] = v
	}
	return s
}

func resizeU8(s []uint8, n int) []uint8 {
	if cap(s) >= n {
		s = s[:n]
		for i := range s {
			s[i] = 0
		}
		return s
	}
	return make([]uint8, n)
}

// finishPicture deblocks, conceals missing macroblocks, performs reference
// marking and returns the completed picture.
func (d *Decoder) finishPicture() *Picture {
	pic := d.cur
	if pic == nil {
		return nil
	}
	h := d.first
	defer func() { d.cur = nil }()
	if d.decodedMB < d.mbW*d.mbH {
		d.conceal(pic)
	}
	d.deblockPicture(pic)
	// POC / frame_num state for the next picture
	mm5 := d.markCurrent(h, pic)
	if mm5 {
		d.seqNo++
	}
	pic.seq = d.seqNo
	st := &d.poc
	if mm5 {
		pic.frameNum = 0
		pic.poc = 0
		st.prevFrameNum, st.prevFrameNumOffset = 0, 0
		st.prevPOCMsb, st.prevPOCLsb = 0, 0
	} else {
		st.prevFrameNum = h.frameNum
		st.prevFrameNumOffset = st.frameNumOffset
		if h.nalRefIdc != 0 {
			st.prevPOCMsb = st.pocMsb
			st.prevPOCLsb = h.pocLsb
		}
	}
	pic.frameWrap = pic.frameNum
	return pic
}

// conceal copies undecoded macroblocks from the nearest reference picture.
func (d *Decoder) conceal(pic *Picture) {
	var src *Picture
	for i := len(d.dpb) - 1; i >= 0; i-- {
		if d.dpb[i] != pic {
			src = d.dpb[i]
			break
		}
	}
	neutralY := uint16(1) << uint(d.f.bdY-1)
	neutralC := uint16(1) << uint(d.f.bdC-1)
	for mby := 0; mby < d.mbH; mby++ {
		for mbx := 0; mbx < d.mbW; mbx++ {
			if d.mbSlice[mby*d.mbW+mbx] >= 0 {
				continue
			}
			for y := 0; y < 16; y++ {
				o := (mby*16+y)*pic.ys + mbx*16
				if src != nil {
					copy(pic.y[o:o+16], src.y[o:o+16])
				} else {
					for i := 0; i < 16; i++ {
						pic.y[o+i] = neutralY
					}
				}
			}
			for y := 0; y < d.f.mbHC && pic.cw > 0; y++ {
				o := (mby*d.f.mbHC+y)*pic.cs + mbx*d.f.mbWC
				for i := 0; i < d.f.mbWC; i++ {
					if src != nil {
						pic.cb[o+i], pic.cr[o+i] = src.cb[o+i], src.cr[o+i]
					} else {
						pic.cb[o+i], pic.cr[o+i] = neutralC, neutralC
					}
				}
			}
			d.mbType[mby*d.mbW+mbx] = mtPSkip
		}
	}
}
