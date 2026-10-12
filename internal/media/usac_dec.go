package media

import (
	"errors"
	"fmt"
)

// USAC decoder: access unit parsing, channel elements, stereo processing and
// output assembly (ISO/IEC 23003-3). The decoding process of the elements
// FFmpeg implements follows libavcodec/aac/aacdec_usac.c (LGPL-2.1-or-later,
// see NOTICE).

// usacPrimePreroll makes the decoder decode the AudioPreRoll frames of the
// first access unit even when they belong to the stream's own configuration
// (libxaac does; the output of the first frame then carries the overlap of
// the pre-roll frames). Tests against libxaac set it.
var usacPrimePreroll = false

var errUSACUnsupported = fmt.Errorf("%w: usac feature", ErrUnsupported)

// nativeUSAC reports whether the pure-Go USAC decoder handles the track.
func nativeUSAC(a *Track) bool {
	return a != nil && a.CodecName == "aac" && a.Codec == "mp4a.40.42"
}

type usacElem struct {
	cfg   *usacElemCfg
	idx   int
	nch   int
	ch    [2]usacCh
	st    usacStereo
	first int // index of the first output channel
}

type usacDec struct {
	cfg        *usacConfig
	sfi        int
	elems      []*usacElem
	nOut       int
	order      []int
	mdctLong   *usacMDCT
	mdctShort  *usacMDCT
	buf, tmp   []float64
	pcm        [][]float32
	frameOut   [][]float32 // per output channel samples of the frame being decoded
	cfgBytes   []byte
	frames     int
	discard    bool // preroll frame: state update only
	outFrame   int
	prerollSig string
	gain       float64
	stats      usacStats
	haveOutput bool
	err        error
}

type usacStats struct{ short, start, stop, ms, pred, cpred, tns, noise, lpd, acelp, tcx, sbr, mps, fac int }

var lastUSACStats usacStats

func newUSACDecoder(cfg *usacConfig) (*usacDec, error) {
	d := &usacDec{gain: 1}
	if err := d.configure(cfg); err != nil {
		return nil, err
	}
	d.pcm = make([][]float32, d.nOut)
	return d, nil
}

// reconfigure applies a new configuration in the middle of a stream.
func (d *usacDec) reconfigure(cfg *usacConfig) error {
	if cfg.rate != d.cfg.rate || cfg.nOutCh != d.cfg.nOutCh || cfg.outFrame != d.cfg.outFrame {
		return fmt.Errorf("%w: usac stream changes its output format", ErrUnsupported)
	}
	return d.configure(cfg)
}

func (d *usacDec) configure(cfg *usacConfig) error {
	usacInitSwb()
	aacInitHuff()
	d.cfg = cfg
	d.elems = nil
	d.frameOut = nil
	d.sfi = usacSfIndex(cfg.coreRate)
	if cfg.sbrRatioIdx != 0 {
		return fmt.Errorf("%w: usac eSBR", ErrUnsupported)
	}
	if d.sfi < 0 {
		return fmt.Errorf("%w: usac sampling rate %d", ErrUnsupported, cfg.coreRate)
	}
	n := cfg.coreLen
	d.mdctLong = newUsacMDCT(n)
	d.mdctShort = newUsacMDCT(n / 8)
	d.buf = make([]float64, 2*n)
	d.tmp = make([]float64, 2*n)
	d.outFrame = cfg.outFrame
	d.nOut = cfg.nOutCh
	// output channel order: element order -> L R C LFE Ls Rs ...
	d.order = make([]int, cfg.nCoreCh)
	for i := range d.order {
		d.order[i] = i
	}
	if o, ok := aacChanOrder[cfg.chCfgIdx]; ok && len(o) == cfg.nCoreCh {
		copy(d.order, o)
	}
	ch := 0
	for i := range cfg.elems {
		e := &cfg.elems[i]
		el := &usacElem{cfg: e, idx: i, first: ch}
		switch e.typ {
		case usacElemSCE, usacElemLFE:
			el.nch = 1
		case usacElemCPE:
			el.nch = 2
		default:
			d.elems = append(d.elems, el)
			continue
		}
		ch += el.nch
		el.ch[0].noiseSeed = 0x3039
		el.ch[1].noiseSeed = 0x10932
		d.elems = append(d.elems, el)
	}
	return nil
}

// ---- ics_info -----------------------------------------------------------------

func (d *usacDec) readICSInfo(r *aacBits, c *usacCh, ics *usacICS) {
	ics.seqPrev = ics.seq
	ics.seqPrevEff = ics.seqEff
	ics.kbPrev = ics.kb
	ics.seq = r.bits(2)
	ics.kb = r.bits(1)
	ics.seqEff = ics.seq
	if ics.seq == usacLongStart && (ics.seqPrevEff == usacLongStart || ics.seqPrevEff == usacEightShort || ics.seqPrevEff == usacStopStart) {
		ics.seqEff = usacStopStart
	}
	if ics.seq == usacEightShort {
		ics.maxSfb = r.bits(4)
		ics.grouping = r.bits(7)
	} else {
		ics.maxSfb = r.bits(6)
	}
}

// ---- core coder ---------------------------------------------------------------

func (d *usacDec) coreElement(r *aacBits, el *usacElem, indep bool) error {
	ec := el.cfg
	nch := el.nch
	st := &el.st
	lfe := ec.typ == usacElemLFE
	coreCh := nch
	st.commonWindow, st.commonTW = false, false
	st.tnsOnLR = false
	for i := 0; i < nch; i++ {
		c := &el.ch[i]
		c.tns.present = false
		c.tnsData = false
		c.coreMode = 0
		if !lfe {
			c.coreMode = r.bits(1)
		}
	}
	if nch > 1 && ec.stereoCfg == 1 {
		coreCh = 1
	}
	if coreCh == 2 {
		if err := d.stereoInfo(r, el, indep); err != nil {
			return err
		}
	}
	for i := 0; i < coreCh; i++ {
		c := &el.ch[i]
		ics := &c.ics
		if c.coreMode != 0 {
			if err := d.lpdChannel(r, el, i, indep); err != nil {
				return err
			}
			continue
		}
		if c.tdPrev && c.lpd != nil {
			d.lpdUpdate(c)
		}
		if !lfe && (coreCh == 1 || el.ch[0].coreMode != el.ch[1].coreMode) {
			c.tnsData = r.bit()
		}
		global := r.bits(8)
		c.noiseLevel = 0
		c.noiseFill = ec.noiseFill
		if ec.noiseFill {
			c.noiseLevel = r.bits(3)
			c.noiseOffset = r.bits(5)
		}
		if !st.commonWindow {
			d.readICSInfo(r, c, ics)
			if err := d.setupICS(ics); err != nil {
				return err
			}
		}
		if ec.twMDCT && !st.commonTW {
			if r.bit() { // tw_data_present
				return fmt.Errorf("%w: usac time-warped MDCT", ErrUnsupported)
			}
		}
		if err := d.readScaleFactors(r, c, global); err != nil {
			return err
		}
		if c.tnsData {
			if err := d.readTNS(r, &c.tns, ics); err != nil {
				return err
			}
		}
		// ac_spectral_data
		reset := indep
		if !indep {
			reset = r.bit()
		}
		for k := range c.q {
			c.q[k] = 0
		}
		stride := d.cfg.coreLen
		if ics.seq == usacEightShort {
			stride = d.cfg.coreLen / 8
		}
		lg := int(ics.swb[ics.maxSfb])
		for w := 0; w < ics.numWindows; w++ {
			if !usacDecodeSpectrumAC(c.q[w*stride:(w+1)*stride], r, &c.arith, reset && w == 0, lg, stride) {
				return fmt.Errorf("media: usac bad spectral data")
			}
		}
		c.facPresent = r.bit()
		if c.facPresent {
			facLen := d.cfg.coreLen / 8
			if ics.seq == usacEightShort {
				facLen = d.cfg.coreLen / 16
			}
			c.facData[0] = r.bits(7)
			lpdFacDecode(facLen, c.facData[1:], r)
		}
		if r.err {
			return fmt.Errorf("media: usac truncated frame")
		}
	}
	// the dequantisation does not depend on the stereo mode
	for i := 0; i < coreCh; i++ {
		if el.ch[i].coreMode == 0 {
			d.dequantize(&el.ch[i])
		}
	}
	if el.cfg.hasSBR {
		return fmt.Errorf("%w: usac eSBR", ErrUnsupported)
	}
	d.spectrumDecode(el, coreCh, indep)
	for i := 0; i < coreCh; i++ {
		el.ch[i].tdPrev = el.ch[i].coreMode != 0
	}
	return nil
}

// lpdChannel parses and decodes the LPD channel stream of channel i.
func (d *usacDec) lpdChannel(r *aacBits, el *usacElem, i int, indep bool) error {
	c := &el.ch[i]
	if c.lpd == nil {
		c.lpd = newLPDState(d.cfg.coreRate)
	}
	d.stats.lpd++
	if !c.tdPrev {
		d.lpdResetFromFD(c)
	}
	f := &c.lpdF
	if err := d.lpdParse(r, c, f, indep); err != nil {
		return err
	}
	n := d.cfg.coreLen
	out := make([]float64, n+lpdSuper)
	if err := d.lpdDecode(c, f, !f.coreModeLast, out); err != nil {
		return err
	}
	d.lpdTDFrame(c, f.mod[0], out)
	fo := d.frameOut[el.first+i]
	for k := 0; k < n; k++ {
		fo[k] = float32(out[k])
	}
	c.ics.seq, c.ics.seqEff = usacEightShort, usacEightShort
	c.ics.kb = 0
	return nil
}

func (d *usacDec) stereoInfo(r *aacBits, el *usacElem, indep bool) error {
	ec := el.cfg
	st := &el.st
	c0, c1 := &el.ch[0], &el.ch[1]
	for i := range st.alphaRe {
		st.alphaRe[i], st.alphaIm[i] = 0, 0
	}
	if c0.coreMode != 0 || c1.coreMode != 0 {
		return nil
	}
	tnsActive := r.bit()
	st.commonWindow = r.bit()
	if !st.commonWindow || indep {
		st.prevAlphaRe, st.prevAlphaIm = [64]float64{}, [64]float64{}
	}
	st.maxSfbSte = 0
	if st.commonWindow {
		d.readICSInfo(r, c0, &c0.ics)
		c1.ics.seqPrev, c1.ics.seqPrevEff, c1.ics.kbPrev = c1.ics.seq, c1.ics.seqEff, c1.ics.kb
		c1.ics.seq, c1.ics.seqEff, c1.ics.kb = c0.ics.seq, c0.ics.seqEff, c0.ics.kb
		c1.ics.maxSfb = c0.ics.maxSfb
		c1.ics.grouping = c0.ics.grouping
		if !r.bit() { // common_max_sfb
			if c1.ics.seq == usacEightShort {
				c1.ics.maxSfb = r.bits(4)
			} else {
				c1.ics.maxSfb = r.bits(6)
			}
		}
		if err := d.setupICS(&c0.ics); err != nil {
			return err
		}
		if err := d.setupICS(&c1.ics); err != nil {
			return err
		}
		st.maxSfbSte = c0.ics.maxSfb
		if c1.ics.maxSfb > st.maxSfbSte {
			st.maxSfbSte = c1.ics.maxSfb
		}
		st.msMaskMode = r.bits(2)
		if st.msMaskMode != 3 {
			st.prevAlphaRe, st.prevAlphaIm = [64]float64{}, [64]float64{}
		}
		for i := range st.msMask {
			st.msMask[i] = false
		}
		switch {
		case st.msMaskMode == 1:
			for g := 0; g < c0.ics.numGroups; g++ {
				for sfb := 0; sfb < st.maxSfbSte; sfb++ {
					st.msMask[g*st.maxSfbSte+sfb] = r.bit()
				}
			}
		case st.msMaskMode == 2:
			for i := range st.msMask {
				st.msMask[i] = true
			}
		case st.msMaskMode == 3 && ec.stereoCfg == 0:
			d.complexPredData(r, el, indep)
		}
	}
	if ec.twMDCT {
		st.commonTW = r.bit()
		return fmt.Errorf("%w: usac time-warped MDCT", ErrUnsupported)
	}
	st.tnsOnLR = false
	c0.tnsData, c1.tnsData = false, false
	if tnsActive {
		commonTNS := false
		if st.commonWindow {
			commonTNS = r.bit()
		}
		st.tnsOnLR = r.bit()
		if commonTNS {
			if err := d.readTNS(r, &c0.tns, &c0.ics); err != nil {
				return err
			}
			c1.tns = c0.tns
			c0.tnsData, c1.tnsData = false, false
		} else if r.bit() {
			c0.tnsData, c1.tnsData = true, true
		} else {
			c1.tnsData = r.bit()
			c0.tnsData = !c1.tnsData
		}
	}
	return nil
}

// complexPredData reads cplx_pred_data().
func (d *usacDec) complexPredData(r *aacBits, el *usacElem, indep bool) {
	st := &el.st
	c0 := &el.ch[0]
	ics := &c0.ics
	ng := ics.numGroups
	ste := st.maxSfbSte
	for i := range st.predUsed {
		st.predUsed[i] = false
	}
	if !r.bit() { // cplx_pred_all
		for g := 0; g < ng; g++ {
			for sfb := 0; sfb < ste; sfb += 2 {
				v := r.bit()
				st.predUsed[g*ste+sfb] = v
				if sfb+1 < ste {
					st.predUsed[g*ste+sfb+1] = v
				}
			}
		}
	} else {
		for g := 0; g < ng; g++ {
			for sfb := 0; sfb < ste; sfb++ {
				st.predUsed[g*ste+sfb] = true
			}
		}
	}
	st.predDir = r.bits(1)
	st.complexCoef = r.bit()
	st.usePrevFrame = false
	if st.complexCoef && !indep {
		st.usePrevFrame = r.bit()
	}
	deltaTime := false
	if !indep {
		deltaTime = r.bit()
	}
	for g := 0; g < ng; g++ {
		for sfb := 0; sfb < ste; sfb += 2 {
			var lastRe, lastIm float64
			if deltaTime {
				lastRe, lastIm = st.prevAlphaRe[sfb], st.prevAlphaIm[sfb]
			} else if sfb > 0 {
				lastRe, lastIm = st.alphaRe[g*ste+sfb-1], st.alphaIm[g*ste+sfb-1]
			}
			if st.predUsed[g*ste+sfb] {
				v := -(r.huff(aacSFHuff) - 60)
				st.alphaRe[g*ste+sfb] = lastRe + float64(v)*0.1
				st.alphaIm[g*ste+sfb] = 0
				if st.complexCoef {
					v = -(r.huff(aacSFHuff) - 60)
					st.alphaIm[g*ste+sfb] = lastIm + float64(v)*0.1
				}
			} else {
				st.alphaRe[g*ste+sfb], st.alphaIm[g*ste+sfb] = 0, 0
			}
			if sfb+1 < ste {
				st.alphaRe[g*ste+sfb+1] = st.alphaRe[g*ste+sfb]
				st.alphaIm[g*ste+sfb+1] = st.alphaIm[g*ste+sfb]
			}
			st.prevAlphaRe[sfb] = st.alphaRe[g*ste+sfb]
			st.prevAlphaIm[sfb] = st.alphaIm[g*ste+sfb]
		}
	}
}

// spectrumDecode runs TNS, stereo processing and the inverse transform of the
// channels of an element and appends the output.
func (d *usacDec) spectrumDecode(el *usacElem, coreCh int, indep bool) {
	st := &el.st
	for i := 0; i < coreCh; i++ {
		c := &el.ch[i]
		switch c.ics.seq {
		case usacEightShort:
			d.stats.short++
		case usacLongStart:
			d.stats.start++
		case usacLongStop:
			d.stats.stop++
		}
	}
	bothFD := coreCh == 2 && el.ch[0].coreMode == 0 && el.ch[1].coreMode == 0
	if bothFD {
		beforeStereo := st.commonWindow && !st.tnsOnLR
		if beforeStereo {
			for i := 0; i < 2; i++ {
				if el.ch[i].tns.present {
					d.stats.tns++
					d.applyTNS(&el.ch[i])
				}
			}
		}
		if st.commonWindow {
			switch {
			case st.msMaskMode == 3:
				d.stats.cpred++
				d.complexStereo(el)
			case st.msMaskMode > 0:
				d.stats.ms++
				d.midSide(el)
			}
		}
		if !beforeStereo {
			for i := 0; i < 2; i++ {
				if el.ch[i].tns.present {
					d.stats.tns++
					d.applyTNS(&el.ch[i])
				}
			}
		}
		d.savePrevDmx(el)
	} else {
		for i := 0; i < coreCh; i++ {
			if el.ch[i].tns.present {
				d.stats.tns++
				d.applyTNS(&el.ch[i])
			}
		}
	}
	n := d.cfg.coreLen
	for i := 0; i < coreCh; i++ {
		if el.ch[i].coreMode != 0 {
			continue
		}
		out := d.frameOut[el.first+i]
		var tmp [1024]float64
		d.synth(&el.ch[i], tmp[:n])
		for k := 0; k < n; k++ {
			out[k] = float32(tmp[k])
		}
	}
	if coreCh == 1 && el.nch == 2 {
		// MPS212 elements are handled by the caller
		_ = indep
	}
}

func (d *usacDec) midSide(el *usacElem) {
	st := &el.st
	c0, c1 := &el.ch[0], &el.ch[1]
	ics := &c0.ics
	stride := d.cfg.coreLen
	if ics.seq == usacEightShort {
		stride = d.cfg.coreLen / 8
	}
	base := 0
	for g := 0; g < ics.numGroups; g++ {
		for sfb := 0; sfb < st.maxSfbSte; sfb++ {
			if !st.msMask[g*st.maxSfbSte+sfb] {
				continue
			}
			s, e := int(ics.swb[sfb]), int(ics.swb[sfb+1])
			for w := 0; w < ics.groupLen[g]; w++ {
				a := c0.coef[base+w*stride+s : base+w*stride+e]
				b := c1.coef[base+w*stride+s : base+w*stride+e]
				for z := range a {
					a[z], b[z] = a[z]+b[z], a[z]-b[z]
				}
			}
		}
		base += ics.groupLen[g] * stride
	}
}

// ---- access unit ----------------------------------------------------------------

var errUSACEnd = errors.New("usac: end")

// decodeAU decodes one access unit and appends its samples to d.pcm.
func (d *usacDec) decodeAU(data []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("media: usac decoder fault: %v", r)
		}
	}()
	r := &aacBits{b: data}
	if d.frameOut == nil {
		d.frameOut = make([][]float32, d.nOut)
		for i := range d.frameOut {
			d.frameOut[i] = make([]float32, d.outFrame)
		}
	}
	indep := r.bit()
	audio := false
	for _, el := range d.elems {
		switch el.cfg.typ {
		case usacElemSCE, usacElemCPE, usacElemLFE:
			if err := d.coreElement(r, el, indep); err != nil {
				return err
			}
			audio = true
		case usacElemEXT:
			if err := d.extElement(r, el); err != nil {
				return err
			}
		}
		if r.err {
			return fmt.Errorf("media: usac truncated access unit")
		}
	}
	if d.err != nil {
		e := d.err
		d.err = nil
		return e
	}
	if !audio || d.discard {
		return nil
	}
	for c := 0; c < d.nOut && c < len(d.order); c++ {
		o := d.order[c]
		if o >= len(d.pcm) {
			o = c
		}
		d.pcm[o] = append(d.pcm[o], d.frameOut[c][:d.outFrame]...)
	}
	d.frames++
	return nil
}

// extElement parses an extension element of the access unit.
func (d *usacDec) extElement(r *aacBits, el *usacElem) error {
	ec := el.cfg
	if !r.bit() { // usacExtElementPresent
		return nil
	}
	var length int
	if r.bit() { // usacExtElementUseDefaultLength
		length = ec.extDefLen
	} else {
		length = r.bits(8)
		if length == 255 {
			length += r.bits(16) - 2
		}
	}
	if length == 0 {
		return nil
	}
	start, end := true, true
	if ec.extFrag {
		start = r.bit()
		end = r.bit()
	}
	if r.pos+8*length > len(r.b)*8 {
		return fmt.Errorf("media: usac extension element exceeds the access unit")
	}
	payload := r.b[0:0]
	if start {
		ec.extPayload = ec.extPayload[:0]
	}
	// the payload is byte aligned relative to the reader position only if the
	// reader is; copy it bit by bit otherwise
	for i := 0; i < length; i++ {
		ec.extPayload = append(ec.extPayload, byte(r.bits(8)))
	}
	_ = payload
	if !end {
		return nil
	}
	pl := ec.extPayload
	switch ec.extType {
	case usacExtPreroll:
		return d.audioPreRoll(pl)
	}
	return nil
}

// audioPreRoll handles an AudioPreRoll() extension payload (ISO/IEC 23003-3
// 5.5.6): the UsacConfig the following frames belong to and the access units
// that precede the current one. A decoder that starts (or whose configuration
// changed) decodes them to establish its state; when the same configuration
// was announced before, they are redundant and skipped.
func (d *usacDec) audioPreRoll(pl []byte) error {
	r := &aacBits{b: pl}
	cl := usacEscaped(r, 4, 4, 8)
	cs := r.pos
	cfg, err := parseUsacConfig(&aacBits{b: pl[:min(len(pl), cs/8+cl)], pos: cs})
	if err != nil {
		return nil // unparsable pre-roll configuration: ignore the pre-roll
	}
	r.pos = cs + 8*cl
	r.bit() // applyCrossfade
	r.bit()
	n := usacEscaped(r, 2, 4, 0)
	sig := cfg.signature()
	if d.prerollSig != "" && d.prerollSig == sig {
		return nil
	}
	d.prerollSig = sig
	if sig != d.cfg.signature() {
		if err := d.reconfigure(cfg); err != nil {
			return err
		}
	} else if !usacPrimePreroll {
		// Same configuration as the stream's: like FFmpeg, do not decode the
		// pre-roll frames (the first frame is then decoded without the
		// overlap of the frames before it, which is what the conformance
		// reference output of such streams contains).
		return nil
	}
	for i := 0; i < n; i++ {
		l := usacEscaped(r, 16, 16, 0)
		if r.pos+8*l > len(pl)*8 || l == 0 {
			return nil
		}
		// the access units are not byte aligned inside the payload
		au := make([]byte, l)
		for k := range au {
			au[k] = byte(r.bits(8))
		}
		saved := d.discard
		d.discard = true
		err := d.decodeAU(au)
		d.discard = saved
		if err != nil {
			if errors.Is(err, ErrUnsupported) {
				return err
			}
			// a damaged pre-roll frame only costs the state it would have set
		}
	}
	return nil
}
