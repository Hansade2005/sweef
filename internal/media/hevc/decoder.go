package hevc

import (
	"errors"
	"image"
	"runtime/debug"

	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// DebugPanic, when set, receives panics recovered while decoding.
var DebugPanic func(r any, stack []byte)

type mvField struct {
	mv  [2][2]int16
	poc [2]int32
	ref [2]int8
	pf  uint8 // bit0: list 0 used, bit1: list 1 used
	lt  uint8 // bit0/1: reference of list 0/1 is long-term
}

// Picture is a decoded (or being decoded) picture.
type Picture struct {
	planes [3][]uint16
	stride [3]int
	w, h   int
	cw, ch int
	poc    int
	mvf    []mvField
	w4     int
	sps    *SPS

	refShort, refLong bool
	needOutput        bool
	latency           int
	seq               int
	frame             *yuv.Frame
	decodeIdx         int
}

type cuInfo struct {
	depth uint8
	flags uint8
	qp    int8
}

const (
	cuIntra    = 1
	cuSkip     = 2
	cuNoFilter = 4 // pcm with loop filter disabled, or transquant bypass
	cuPcm      = 8
	cuBypass   = 16
)

type saoParams struct {
	typ    [3]uint8 // 0 none, 1 band, 2 edge
	band   [3]uint8
	eo     [3]uint8
	offset [3][5]int16
}

// Decoder decodes an HEVC bitstream access unit by access unit.
type Decoder struct {
	lenSize int

	sps [16]*SPS
	pps [64]*PPS

	asps *SPS
	apps *PPS

	dpb []*Picture
	cur *Picture

	stBefore, stAfter, ltCurr []*Picture

	// POC state
	prevTid0Poc  int
	firstPic     bool
	afterEOS     bool
	noRaslOutput bool // of the last IRAP
	skipPic      bool // current picture is skipped (RASL after a clean start)
	seq          int

	pending  *Picture
	outQueue []*Picture
	decIdx   int

	// per picture tables
	cuTab        []cuInfo
	ipm          []uint8
	edge         []uint8
	cbfTab       []uint8
	ctbSliceIdx  []int32
	ctbSliceAddr []int32
	lastMerge    bool
	sao          []saoParams
	slices       []*sliceHeader
	tabSPS       *SPS
	lastIndep    *sliceHeader
	stickBeta    int // deblocking offsets of the previous slice header
	stickTc      int
	picInit      bool
	decodedCtbs  int

	// slice decoding state
	sh        *sliceHeader
	sp        *SPS
	pp        *PPS
	cab       cabac
	statCoeff [4]uint8
	wppCtx    [numCtx]uint8
	wppStat   [4]uint8
	dsCtx     [numCtx]uint8
	dsStat    [4]uint8
	data      []byte

	ctbAddrRs, ctbAddrTs int
	ctbX, ctbY           int

	// QP state
	isCuQpDeltaCoded           bool
	cuQpDeltaVal               int
	qgPred                     int
	qpY                        int
	lastQpY                    int
	isCuChromaQpOffsetCoded    bool
	cuQpOffsetCb, cuQpOffsetCr int
	log2MinCuQpDeltaSize       int
	log2MinCuChromaQpSize      int

	cu              cuState
	staleChromaMode int

	// CheckHash makes the decoder verify every picture that carries a decoded
	// picture hash SEI (see HashStats).
	CheckHash bool
	// compatFFmpeg reproduces ffmpeg's deviations from the specification in
	// transquant bypass coding (tests that compare with ffmpeg only).
	compatFFmpeg bool
	hashChecked  int
	hashBad      int
	curHash      *picHash

	scratch    scratch
	saoApplied [3][]bool
	saoH, saoV [3][]uint16
}

type cuState struct {
	x0, y0, log2  int
	intra         bool
	partMode      int
	bypass        bool
	pcm           bool
	maxTrafoDepth int
	intraSplit    bool
	chromaMode    [4]int // final chroma prediction mode (after 4:2:2 mapping)
	chromaModeSyn [4]int // intra_chroma_pred_mode syntax value
	rqtRoot       bool
}

// NewDecoder returns an empty decoder.
func NewDecoder() *Decoder {
	d := &Decoder{firstPic: true}
	return d
}

// Reset drops all pictures and parameter state needed to restart decoding
// at a random access point.
func (d *Decoder) Reset() {
	d.dpb = nil
	d.cur = nil
	d.pending = nil
	d.outQueue = nil
	d.firstPic = true
	d.afterEOS = false
	d.picInit = false
	d.lastIndep = nil
	d.skipPic = false
	d.seq++
}

// SetHVCC configures the decoder from an HEVCDecoderConfigurationRecord and
// switches it to length-prefixed NAL input.
func (d *Decoder) SetHVCC(c []byte) error {
	if len(c) < 23 || c[0] != 1 {
		return errors.New("hevc: bad hvcC")
	}
	d.lenSize = int(c[21]&3) + 1
	n := int(c[22])
	p := 23
	for i := 0; i < n; i++ {
		if p+3 > len(c) {
			return errors.New("hevc: bad hvcC")
		}
		cnt := int(c[p+1])<<8 | int(c[p+2])
		p += 3
		for j := 0; j < cnt; j++ {
			if p+2 > len(c) {
				return errors.New("hevc: bad hvcC")
			}
			l := int(c[p])<<8 | int(c[p+1])
			p += 2
			if p+l > len(c) {
				return errors.New("hevc: bad hvcC")
			}
			if l > 2 {
				d.handleParamNAL(c[p : p+l])
			}
			p += l
		}
	}
	return nil
}

func (d *Decoder) handleParamNAL(n []byte) {
	typ := int(n[0]>>1) & 63
	switch typ {
	case nalSPS:
		if s, err := parseSPS(unescape(n[2:])); err == nil {
			d.sps[s.id] = s
		}
	case nalPPS:
		if p, err := parsePPS(unescape(n[2:])); err == nil {
			d.pps[p.id] = p
		}
	}
}

// DecodeFrame decodes one access unit (length-prefixed NAL units after
// SetHVCC, an Annex B byte stream otherwise) and returns the picture decoded
// from it in decoding order (cropped), or nil when it has none. Pictures that
// are not to be output (pic_output_flag = 0, skipped RASL pictures) are not
// returned.
func (d *Decoder) DecodeFrame(data []byte) (fr *yuv.Frame, err error) {
	defer func() {
		if r := recover(); r != nil {
			if DebugPanic != nil {
				DebugPanic(r, debug.Stack())
			}
			d.cur = nil
			d.picInit = false
			fr, err = nil, errBitstream
		}
	}()
	err = d.decodeData(data)
	d.finishPicture()
	p := d.pending
	d.pending = nil
	d.outQueue = d.outQueue[:0]
	if p == nil {
		return nil, err
	}
	return d.frameOf(p), err
}

// Decode is DecodeFrame for 8-bit streams, returning an image.YCbCr.
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

// Push decodes an access unit (or any chunk of NAL units) and returns the
// pictures that become ready for output, in output order (C.5.2 bumping).
func (d *Decoder) Push(data []byte) (out []*yuv.Frame, err error) {
	defer func() {
		if r := recover(); r != nil {
			if DebugPanic != nil {
				DebugPanic(r, debug.Stack())
			}
			d.cur = nil
			d.picInit = false
			err = errBitstream
		}
	}()
	err = d.decodeData(data)
	d.finishPicture()
	d.pending = nil
	for _, p := range d.outQueue {
		out = append(out, d.frameOf(p))
	}
	d.outQueue = d.outQueue[:0]
	return out, err
}

// Flush finishes the current picture and returns all remaining pictures in
// output order.
func (d *Decoder) Flush() []*yuv.Frame {
	d.finishPicture()
	d.pending = nil
	for d.bump() {
	}
	var out []*yuv.Frame
	for _, p := range d.outQueue {
		out = append(out, d.frameOf(p))
	}
	d.outQueue = d.outQueue[:0]
	return out
}

func (d *Decoder) decodeData(data []byte) error {
	var nals [][]byte
	if d.lenSize == 0 {
		nals = splitAnnexB(data)
	} else {
		nals = splitLengthPrefixed(data, d.lenSize)
	}
	var first error
	for _, n := range nals {
		if len(n) < 3 {
			continue
		}
		if err := d.handleNAL(n); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (d *Decoder) handleNAL(n []byte) error {
	typ := int(n[0]>>1) & 63
	layer := int(n[0]&1)<<5 | int(n[1]>>3)
	tid := int(n[1]&7) - 1
	if layer != 0 || tid < 0 {
		return nil
	}
	switch {
	case typ == nalSPS:
		s, err := parseSPS(unescape(n[2:]))
		if err != nil {
			return err
		}
		d.sps[s.id] = s
		return nil
	case typ == nalPPS:
		p, err := parsePPS(unescape(n[2:]))
		if err != nil {
			return err
		}
		d.pps[p.id] = p
		return nil
	case typ == nalEOS || typ == nalEOB:
		d.finishPicture()
		d.afterEOS = true
		return nil
	case typ == nalAUD:
		d.finishPicture()
		return nil
	case typ == nalSuffixSEI:
		if d.CheckHash && d.cur != nil {
			if h := parseSEIHash(unescape(n[2:])); h != nil {
				d.curHash = h
			}
		}
		return nil
	case typ <= 9 || (typ >= 16 && typ <= 21):
		return d.handleSlice(n, typ, tid)
	}
	return nil
}

func (d *Decoder) handleSlice(n []byte, typ, tid int) error {
	rbsp := unescape(n[2:])
	r := &bitReader{b: rbsp}
	// peek first_slice_segment_in_pic_flag to finish the previous picture early
	if len(rbsp) > 0 && rbsp[0]&0x80 != 0 {
		d.finishPicture()
	}
	sh, err := d.parseSliceHeader(r, typ, tid, d.lastIndep)
	if err != nil {
		return err
	}
	if sh.firstSlice {
		d.finishPicture()
		if err := d.startPicture(sh); err != nil {
			d.cur = nil
			d.picInit = false
			if err == errSkipPicture {
				d.skipPic = true
				return nil
			}
			return err
		}
		d.skipPic = false
	} else if d.skipPic || d.cur == nil {
		return nil
	}
	if !sh.dependent {
		d.lastIndep = sh
		if err := d.setupRefLists(sh); err != nil {
			return err
		}
		sh.idx = len(d.slices)
		d.slices = append(d.slices, sh)
	} else {
		sh.idx = len(d.slices)
		d.slices = append(d.slices, sh)
	}
	return d.decodeSliceData(sh, rbsp)
}

var errSkipPicture = errors.New("hevc: skipped picture")

func isIRAP(t int) bool { return t >= 16 && t <= 23 }

// startPicture performs POC derivation, RPS handling and DPB management for
// the first slice of a new picture.
func (d *Decoder) startPicture(sh *sliceHeader) error {
	pps := d.pps[sh.ppsID]
	sps := d.sps[pps.spsID]
	nt := sh.nalType
	irap := isIRAP(nt)
	if irap {
		d.noRaslOutput = nt >= nalBlaWLP && nt <= nalIdrNLP || d.firstPic || d.afterEOS
	}
	if (nt == nalRaslN || nt == nalRaslR) && d.noRaslOutput {
		return errSkipPicture
	}
	if d.firstPic && !irap {
		// decoding cannot start before a random access point
		return errSkipPicture
	}
	if err := pps.setup(sps); err != nil {
		return err
	}
	if sps.chromaFormat != 0 && sps.bitDepthY != sps.bitDepthC {
		return ErrUnsupported
	}
	if d.asps != sps {
		if d.asps != nil && !irap {
			return errBitstream
		}
		d.asps = sps
	}
	d.apps = pps
	if d.tabSPS != sps {
		d.allocTables(sps)
	}
	// POC
	maxLsb := 1 << uint(sps.log2MaxPocLsb)
	msb := 0
	if !(irap && d.noRaslOutput) {
		prevLsb := d.prevTid0Poc & (maxLsb - 1)
		prevMsb := d.prevTid0Poc - prevLsb
		switch {
		case sh.pocLsb < prevLsb && prevLsb-sh.pocLsb >= maxLsb/2:
			msb = prevMsb + maxLsb
		case sh.pocLsb > prevLsb && sh.pocLsb-prevLsb > maxLsb/2:
			msb = prevMsb - maxLsb
		default:
			msb = prevMsb
		}
	}
	poc := msb + sh.pocLsb
	if sh.temporalID == 0 && !(nt >= nalRadlN && nt <= nalRaslR) && !(nt < 16 && nt%2 == 0) {
		d.prevTid0Poc = poc
	}

	// IRAP handling of the DPB
	if irap && d.noRaslOutput {
		noOutPrior := sh.noOutputPrior
		if nt == nalCra {
			noOutPrior = true
		}
		if d.firstPic {
			noOutPrior = false
		}
		if !noOutPrior {
			for d.bump() {
			}
		}
		for _, p := range d.dpb {
			p.refShort, p.refLong = false, false
			if noOutPrior {
				p.needOutput = false
			}
		}
		d.dpb = nil
		d.seq++
	}
	// RPS
	if err := d.applyRPS(sh, sps, poc); err != nil {
		return err
	}
	d.removeUnused()
	if irap && d.noRaslOutput {
		// already emptied
	} else {
		hi := sps.maxSubLayers - 1
		for {
			need := 0
			lat := false
			for _, p := range d.dpb {
				if p.needOutput {
					need++
					if sps.maxLatencyPlus1[hi] != 0 && p.latency >= sps.maxNumReorder[hi]+sps.maxLatencyPlus1[hi]-1 {
						lat = true
					}
				}
			}
			if (need > sps.maxNumReorder[hi] || lat || len(d.dpb) >= sps.maxDecPicBuf[hi]) && need > 0 {
				d.bump()
				d.removeUnused()
				continue
			}
			break
		}
	}
	pic := d.newPicture(sps)
	pic.poc = poc
	pic.seq = d.seq
	pic.refShort = true
	pic.needOutput = sh.picOutput && !((nt == nalRaslN || nt == nalRaslR) && d.noRaslOutput)
	pic.decodeIdx = d.decIdx
	d.decIdx++
	d.cur = pic
	d.dpb = append(d.dpb, pic)
	d.picInit = true
	d.firstPic = false
	d.afterEOS = false
	d.resetPictureTables()
	d.slices = d.slices[:0]
	d.lastIndep = nil
	d.decodedCtbs = 0
	return nil
}

func (d *Decoder) removeUnused() {
	k := 0
	for _, p := range d.dpb {
		if p.refShort || p.refLong || p.needOutput || p == d.cur {
			d.dpb[k] = p
			k++
		}
	}
	for i := k; i < len(d.dpb); i++ {
		d.dpb[i] = nil
	}
	d.dpb = d.dpb[:k]
}

// bump outputs the picture with the smallest POC among those needed for
// output. It reports whether a picture was output.
func (d *Decoder) bump() bool {
	var best *Picture
	for _, p := range d.dpb {
		if p.needOutput && p != d.cur && (best == nil || p.poc < best.poc) {
			best = p
		}
	}
	if best == nil {
		return false
	}
	best.needOutput = false
	d.outQueue = append(d.outQueue, best)
	return true
}

func (d *Decoder) newPicture(sps *SPS) *Picture {
	p := &Picture{w: sps.width, h: sps.height, sps: sps}
	p.stride[0] = sps.width
	p.planes[0] = make([]uint16, sps.width*sps.height)
	if sps.chromaFormat != 0 {
		p.cw = sps.width >> sps.subW
		p.ch = sps.height >> sps.subH
		for i := 1; i < 3; i++ {
			p.stride[i] = p.cw
			p.planes[i] = make([]uint16, p.cw*p.ch)
		}
	}
	p.w4 = (sps.width + 3) >> 2
	p.mvf = make([]mvField, p.w4*((sps.height+3)>>2))
	return p
}

// applyRPS derives the reference picture set of the current picture and
// updates the DPB marking.
func (d *Decoder) applyRPS(sh *sliceHeader, sps *SPS, poc int) error {
	nt := sh.nalType
	// per-picture lists stored on the (independent) slice header; the lists
	// are rebuilt for each slice from these sets.
	idr := nt == nalIdrWRadl || nt == nalIdrNLP
	if idr {
		for _, p := range d.dpb {
			p.refShort, p.refLong = false, false
		}
		d.stBefore, d.stAfter, d.ltCurr = d.stBefore[:0], d.stAfter[:0], d.ltCurr[:0]
		return nil
	}
	maxLsb := 1 << uint(sps.log2MaxPocLsb)
	type ent struct {
		poc  int
		used bool
		lt   bool
		msb  bool
	}
	var st0, st1, ltAll []ent
	for i := 0; i < sh.rps.numNeg; i++ {
		st0 = append(st0, ent{poc: poc + int(sh.rps.delta[0][i]), used: sh.rps.used[0][i]})
	}
	for i := 0; i < sh.rps.numPos; i++ {
		st1 = append(st1, ent{poc: poc + int(sh.rps.delta[1][i]), used: sh.rps.used[1][i]})
	}
	for i := 0; i < sh.numLT; i++ {
		e := ent{poc: sh.ltPocLsb[i], used: sh.ltUsed[i], lt: true, msb: sh.ltMsbPresent[i]}
		if e.msb {
			e.poc += poc - sh.ltMsbCycle[i]*maxLsb - (poc & (maxLsb - 1))
		}
		ltAll = append(ltAll, e)
	}
	keep := map[*Picture]bool{}
	d.stBefore, d.stAfter, d.ltCurr = d.stBefore[:0], d.stAfter[:0], d.ltCurr[:0]
	findST := func(p int) *Picture {
		for _, q := range d.dpb {
			if q != d.cur && q.refShort && !q.refLong && q.poc == p {
				return q
			}
		}
		return nil
	}
	findLT := func(e ent) *Picture {
		for _, q := range d.dpb {
			if q == d.cur || !(q.refShort || q.refLong) {
				continue
			}
			if e.msb {
				if q.poc == e.poc {
					return q
				}
			} else if q.poc&(maxLsb-1) == e.poc {
				return q
			}
		}
		return nil
	}
	// long-term first (spec derives LT candidates before ST so that a picture
	// is not matched twice)
	var ltPics []*Picture
	for _, e := range ltAll {
		q := findLT(e)
		if q == nil && e.used {
			q = d.missingPicture(sps, e.poc)
		}
		ltPics = append(ltPics, q)
		if q != nil {
			keep[q] = true
		}
	}
	for _, e := range st0 {
		q := findST(e.poc)
		if q == nil && e.used {
			q = d.missingPicture(sps, e.poc)
			q.refShort = true
		}
		if q != nil {
			keep[q] = true
			if e.used {
				d.stBefore = append(d.stBefore, q)
			}
		}
	}
	for _, e := range st1 {
		q := findST(e.poc)
		if q == nil && e.used {
			q = d.missingPicture(sps, e.poc)
			q.refShort = true
		}
		if q != nil {
			keep[q] = true
			if e.used {
				d.stAfter = append(d.stAfter, q)
			}
		}
	}
	for i, q := range ltPics {
		if q != nil && ltAll[i].used {
			d.ltCurr = append(d.ltCurr, q)
		}
	}
	for _, q := range ltPics {
		if q != nil {
			q.refLong, q.refShort = true, false
		}
	}
	for _, q := range d.dpb {
		if !keep[q] {
			q.refShort, q.refLong = false, false
		}
	}
	return nil
}

// missingPicture creates a grey reference picture for a reference that is
// not in the DPB.
func (d *Decoder) missingPicture(sps *SPS, poc int) *Picture {
	p := d.newPicture(sps)
	p.poc = poc
	p.seq = d.seq
	g := uint16(1) << uint(sps.bitDepthY-1)
	for i := range p.planes[0] {
		p.planes[0][i] = g
	}
	g = uint16(1) << uint(sps.bitDepthC-1)
	for c := 1; c < 3; c++ {
		for i := range p.planes[c] {
			p.planes[c][i] = g
		}
	}
	p.refShort = true
	d.dpb = append(d.dpb, p)
	return p
}

// setupRefLists builds RefPicList0/1 of an independent slice.
func (d *Decoder) setupRefLists(sh *sliceHeader) error {
	if sh.sliceType == sliceI {
		return nil
	}
	nb, na, nl := len(d.stBefore), len(d.stAfter), len(d.ltCurr)
	total := nb + na + nl
	if total == 0 {
		return errBitstream
	}
	for l := 0; l < 2; l++ {
		if l == 1 && sh.sliceType != sliceB {
			break
		}
		n := sh.numRefIdx[l]
		nTemp := n
		if total > nTemp {
			nTemp = total
		}
		temp := make([]*Picture, 0, nTemp)
		tlt := make([]bool, 0, nTemp)
		first, second := d.stBefore, d.stAfter
		if l == 1 {
			first, second = d.stAfter, d.stBefore
		}
		for len(temp) < nTemp {
			for _, p := range first {
				if len(temp) < nTemp {
					temp = append(temp, p)
					tlt = append(tlt, false)
				}
			}
			for _, p := range second {
				if len(temp) < nTemp {
					temp = append(temp, p)
					tlt = append(tlt, false)
				}
			}
			for _, p := range d.ltCurr {
				if len(temp) < nTemp {
					temp = append(temp, p)
					tlt = append(tlt, true)
				}
			}
		}
		for i := 0; i < n; i++ {
			idx := i
			if sh.listMod[l] {
				idx = sh.listEntry[l][i]
			}
			if idx >= len(temp) {
				return errBitstream
			}
			sh.refPic[l][i] = temp[idx]
			sh.refPoc[l][i] = int32(temp[idx].poc)
			sh.refLT[l][i] = tlt[idx]
		}
	}
	// NoBackwardPredFlag
	sh.noBackward = true
	for l := 0; l < 2; l++ {
		for i := 0; i < sh.numRefIdx[l]; i++ {
			if int(sh.refPoc[l][i]) > d.cur.poc {
				sh.noBackward = false
			}
		}
	}
	return nil
}

// finishPicture runs the in-loop filters on the current picture and queues
// it for output.
func (d *Decoder) finishPicture() {
	p := d.cur
	if p == nil || !d.picInit {
		d.cur = nil
		return
	}
	d.picInit = false
	d.cur = nil
	d.loopFilterPicture(p)
	if d.curHash != nil {
		d.hashChecked++
		if !d.curHash.verify(p) {
			d.hashBad++
		}
		d.curHash = nil
	}
	if p.needOutput {
		d.pending = p
		// C.5.2.3: latency counts and additional bumping
		for _, q := range d.dpb {
			if q != p && q.needOutput && q.poc > p.poc {
				q.latency++
			}
		}
		p.latency = 0
		sps := p.sps
		hi := sps.maxSubLayers - 1
		for {
			need := 0
			lat := false
			for _, q := range d.dpb {
				if q.needOutput {
					need++
					if sps.maxLatencyPlus1[hi] != 0 && q.latency >= sps.maxNumReorder[hi]+sps.maxLatencyPlus1[hi]-1 {
						lat = true
					}
				}
			}
			if need > sps.maxNumReorder[hi] || (lat && need > 0) {
				d.bump()
				continue
			}
			break
		}
	}
	d.removeUnusedKeep(p)
}

func (d *Decoder) removeUnusedKeep(p *Picture) {
	k := 0
	for _, q := range d.dpb {
		if q.refShort || q.refLong || q.needOutput {
			d.dpb[k] = q
			k++
		}
	}
	for i := k; i < len(d.dpb); i++ {
		d.dpb[i] = nil
	}
	d.dpb = d.dpb[:k]
}

// frameOf wraps a picture as a cropped output frame.
func (d *Decoder) frameOf(p *Picture) *yuv.Frame {
	if p.frame != nil {
		return p.frame
	}
	sps := p.sps
	f := &yuv.Frame{Depth: sps.bitDepthY, Matrix: sps.matrix, FullRange: sps.fullRange}
	f.Width = p.w - sps.cropL - sps.cropR
	f.Height = p.h - sps.cropT - sps.cropB
	f.Planes[0] = p.planes[0][sps.cropT*p.stride[0]+sps.cropL:]
	f.Stride[0] = p.stride[0]
	switch sps.chromaFormat {
	case 0:
		f.Format = yuv.F400
	case 1:
		f.Format = yuv.F420
	case 2:
		f.Format = yuv.F422
	default:
		f.Format = yuv.F444
	}
	if sps.chromaFormat != 0 {
		sx, sy := f.Format.Subsampling()
		f.CWidth, f.CHeight = f.Width>>sx, f.Height>>sy
		co := (sps.cropT>>sy)*p.stride[1] + sps.cropL>>sx
		f.Planes[1], f.Planes[2] = p.planes[1][co:], p.planes[2][co:]
		f.Stride[1], f.Stride[2] = p.stride[1], p.stride[2]
	}
	p.frame = f
	return f
}
