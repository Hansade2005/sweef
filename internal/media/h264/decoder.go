package h264

import (
	"errors"
	"image"
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

// Decoder decodes an H.264 stream access unit by access unit.
type Decoder struct {
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
	coef  [384]int32
	lv    [64]int32
	dcY   [16]int32
	pred  [2][256 + 128]byte
	tmpMC [(16 + 5) * (16 + 5)]byte
	tmpI  [(16 + 5) * 16]int32

	// colocated data for direct prediction
	col *Picture

	curDirect uint8 // direct-predicted 8x8 blocks of the current MB
	blkNZ     [24]bool
	i16DC     bool
}

// NewDecoder returns an empty decoder.
func NewDecoder() *Decoder { return &Decoder{lenSize: 0} }

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

// Decode decodes one access unit and returns its picture (in decoding order,
// not display order), cropped to the SPS frame cropping rectangle.
func (d *Decoder) Decode(data []byte) (img *image.YCbCr, err error) {
	defer func() {
		if r := recover(); r != nil {
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
	return pictureImage(pic), firstErr
}

func pictureImage(p *Picture) *image.YCbCr {
	img := &image.YCbCr{Y: p.y, Cb: p.cb, Cr: p.cr, YStride: p.ys, CStride: p.cs, SubsampleRatio: image.YCbCrSubsampleRatio420}
	r := image.Rect(p.crop[0], p.crop[2], p.w-p.crop[1], p.h-p.crop[3])
	img.Rect = image.Rect(0, 0, p.w, p.h)
	return img.SubImage(r).(*image.YCbCr)
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
	}
	d.mbW, d.mbH = sps.MbW, sps.MbH
	d.nextID++
	pic := newPicture(d.nextID, d.mbW, d.mbH)
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
	d.nz = resizeU8(d.nz, n*24)
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
	pic.frameWrap = pic.frameNum
	st := &d.poc
	if mm5 {
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
	if h.nalRefIdc == 0 {
		// non-reference pictures are not kept
	}
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
						pic.y[o+i] = 128
					}
				}
			}
			for y := 0; y < 8; y++ {
				o := (mby*8+y)*pic.cs + mbx*8
				for i := 0; i < 8; i++ {
					if src != nil {
						pic.cb[o+i], pic.cr[o+i] = src.cb[o+i], src.cr[o+i]
					} else {
						pic.cb[o+i], pic.cr[o+i] = 128, 128
					}
				}
			}
			d.mbType[mby*d.mbW+mbx] = mtPSkip
		}
	}
}
