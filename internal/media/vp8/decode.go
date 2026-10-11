// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file (LICENSE.x-image).

// Package vp8 is a complete VP8 video decoder (RFC 6386): key frames and
// inter frames with motion compensation, golden and altref references,
// segmentation, probability updates and both loop filters. It began as a
// fork of golang.org/x/image/vp8 (key frames only) and keeps its intra
// prediction, residual and filter kernels.
package vp8

import (
	"errors"
	"image"
)

// FrameHeader is the uncompressed data chunk at the start of every frame.
type FrameHeader struct {
	KeyFrame          bool
	Version           uint8
	ShowFrame         bool
	FirstPartitionLen uint32
	Width             int
	Height            int
	XScale            uint8
	YScale            uint8
}

const (
	nSegment = 4
)

type segmentHeader struct {
	enabled   bool
	updateMap bool
	absDelta  bool
	quant     [nSegment]int8
	lf        [nSegment]int8
	prob      [3]uint8
}

type filterHeader struct {
	simple       bool
	level        int
	sharp        int
	deltaEnabled bool
	ref          [4]int8
	mode         [4]int8
}

// probSet is the entropy context that frames may update and (when
// refresh_entropy_probs is 0) discard.
type probSet struct {
	token  [nPlane][nBand][nContext][nProb]uint8
	ymode  [4]uint8
	uvmode [3]uint8
	mv     [2][19]uint8
}

// mb is the per-macroblock context of the neighbouring macroblocks.
type mb struct {
	pred   [4]uint8
	nzMask uint8
	nzY16  uint8
}

// Decoder decodes a VP8 stream frame by frame.
type Decoder struct {
	hdr          FrameHeader
	w, h         int
	mbw, mbh     int
	miStride     int
	mbs          []mbInfo
	last, golden *image.YCbCr
	alt          *image.YCbCr
	cur          *image.YCbCr
	prev         *image.YCbCr // last shown frame, for dropped frames

	fp  partition
	op  [8]partition
	nOP int

	seg       segmentHeader
	lf        filterHeader
	quant     [nSegment]quant
	fc, lfc   probSet
	signBias  [4]bool
	bilinear  bool
	fullPixel bool

	useSkipProb      bool
	skipProb         uint8
	probIntra        uint8
	probLast, probGF uint8
	refreshGolden    bool
	refreshAlt       bool
	refreshLast      bool
	copyToGolden     int
	copyToAlt        int
	refreshEntropy   bool
	haveKey          bool

	segment int
	leftMB  mb
	upMB    []mb

	nzDCMask, nzACMask uint32
	usePredY16         bool
	predY16            uint8
	predC8             uint8
	predY4             [4][4]uint8

	coeff [1*16*16 + 2*8*8 + 1*4*4]int16
	ybr   [1 + 16 + 1 + 8][32]uint8

	mcWin [21 * 21]byte
	mcTmp [21 * 16]byte
}

// NewDecoder returns a new Decoder.
func NewDecoder() *Decoder { return &Decoder{} }

// Reset discards all reference frames (used when seeking).
func (d *Decoder) Reset() {
	d.last, d.golden, d.alt, d.prev = nil, nil, nil, nil
	d.haveKey = false
}

// Width and Height are the display dimensions of the last key frame.
func (d *Decoder) Width() int  { return d.w }
func (d *Decoder) Height() int { return d.h }

var (
	errShort  = errors.New("vp8: truncated frame")
	errFormat = errors.New("vp8: invalid format")
	errNoKey  = errors.New("vp8: no key frame yet")
)

// IsKeyFrame reports whether data starts a key frame.
func IsKeyFrame(data []byte) bool { return len(data) > 0 && data[0]&1 == 0 }

// Decode decodes one frame. The returned image is the frame to display and
// is never modified afterwards; it is nil (with a nil error) for a frame
// whose show_frame flag is clear. An empty packet repeats the previous
// frame.
func (d *Decoder) Decode(data []byte) (*image.YCbCr, error) {
	if len(data) == 0 {
		return d.prev, nil
	}
	if len(data) < 3 {
		return nil, errShort
	}
	h := &d.hdr
	h.KeyFrame = data[0]&1 == 0
	h.Version = (data[0] >> 1) & 7
	h.ShowFrame = (data[0]>>4)&1 == 1
	h.FirstPartitionLen = uint32(data[0])>>5 | uint32(data[1])<<3 | uint32(data[2])<<11
	data = data[3:]
	if h.KeyFrame {
		if len(data) < 7 {
			return nil, errShort
		}
		if data[0] != 0x9d || data[1] != 0x01 || data[2] != 0x2a {
			return nil, errFormat
		}
		w := int(data[4]&0x3f)<<8 | int(data[3])
		hh := int(data[6]&0x3f)<<8 | int(data[5])
		if w == 0 || hh == 0 {
			return nil, errFormat
		}
		h.XScale, h.YScale = data[4]>>6, data[6]>>6
		data = data[7:]
		if w != d.w || hh != d.h || d.mbs == nil {
			d.w, d.h = w, hh
			d.mbw, d.mbh = (w+15)>>4, (hh+15)>>4
			d.miStride = d.mbw + 1
			d.mbs = make([]mbInfo, d.miStride*(d.mbh+1))
			d.upMB = make([]mb, d.mbw)
		}
		d.haveKey = true
	} else if !d.haveKey {
		return nil, errNoKey
	}
	h.Width, h.Height = d.w, d.h
	if int(h.FirstPartitionLen) > len(data) {
		return nil, errShort
	}
	d.bilinear = h.Version != 0
	d.fullPixel = h.Version == 3

	d.fp.init(data[:h.FirstPartitionLen])
	rest := data[h.FirstPartitionLen:]
	if h.KeyFrame {
		d.fp.readFlag() // color space
		d.fp.readFlag() // clamping type
		d.fc.token = defaultTokenProb
		d.fc.ymode = defaultYModeProb
		d.fc.uvmode = defaultUVModeProb
		d.fc.mv = defaultMVProb
		d.seg = segmentHeader{prob: [3]uint8{255, 255, 255}}
		d.lf.ref = [4]int8{}
		d.lf.mode = [4]int8{}
		d.signBias = [4]bool{}
	}
	d.parseSegmentHeader()
	d.parseFilterHeader()
	if err := d.parsePartitions(rest); err != nil {
		return nil, err
	}
	d.parseQuant()
	if h.KeyFrame {
		d.refreshGolden, d.refreshAlt, d.refreshLast = true, true, true
		d.copyToGolden, d.copyToAlt = 0, 0
		d.refreshEntropy = d.fp.readFlag()
	} else {
		d.refreshGolden = d.fp.readFlag()
		d.refreshAlt = d.fp.readFlag()
		d.copyToGolden, d.copyToAlt = 0, 0
		if !d.refreshGolden {
			d.copyToGolden = int(d.fp.readUint(uniformProb, 2))
		}
		if !d.refreshAlt {
			d.copyToAlt = int(d.fp.readUint(uniformProb, 2))
		}
		d.signBias[refGolden] = d.fp.readFlag()
		d.signBias[refAlt] = d.fp.readFlag()
		d.refreshEntropy = d.fp.readFlag()
		d.refreshLast = d.fp.readFlag()
	}
	if !d.refreshEntropy {
		d.lfc = d.fc
	}
	d.parseTokenProb()
	d.useSkipProb = d.fp.readFlag()
	d.skipProb = 0
	if d.useSkipProb {
		d.skipProb = uint8(d.fp.readUint(uniformProb, 8))
	}
	if !h.KeyFrame {
		d.probIntra = uint8(d.fp.readUint(uniformProb, 8))
		d.probLast = uint8(d.fp.readUint(uniformProb, 8))
		d.probGF = uint8(d.fp.readUint(uniformProb, 8))
		if d.fp.readFlag() {
			for i := range d.fc.ymode {
				d.fc.ymode[i] = uint8(d.fp.readUint(uniformProb, 8))
			}
		}
		if d.fp.readFlag() {
			for i := range d.fc.uvmode {
				d.fc.uvmode[i] = uint8(d.fp.readUint(uniformProb, 8))
			}
		}
		for i := 0; i < 2; i++ {
			for j := 0; j < 19; j++ {
				if d.fp.readBit(mvUpdateProb[i][j]) {
					x := uint8(d.fp.readUint(uniformProb, 7))
					if x != 0 {
						d.fc.mv[i][j] = x << 1
					} else {
						d.fc.mv[i][j] = 1
					}
				}
			}
		}
	}

	d.cur = image.NewYCbCr(image.Rect(0, 0, 16*d.mbw, 16*d.mbh), image.YCbCrSubsampleRatio420)
	for i := range d.upMB {
		d.upMB[i] = mb{}
	}
	for mby := 0; mby < d.mbh; mby++ {
		d.leftMB = mb{}
		for mbx := 0; mbx < d.mbw; mbx++ {
			d.decodeMB(mbx, mby)
		}
	}
	if d.lf.level != 0 {
		d.frameFilter(d.cur)
	}

	// Reference buffer updates (libvpx swap_frame_buffers order).
	cur := d.cur
	if h.KeyFrame {
		d.last, d.golden, d.alt = cur, cur, cur
	} else {
		switch d.copyToAlt {
		case 1:
			d.alt = d.last
		case 2:
			d.alt = d.golden
		}
		switch d.copyToGolden {
		case 1:
			d.golden = d.last
		case 2:
			d.golden = d.alt
		}
		if d.refreshGolden {
			d.golden = cur
		}
		if d.refreshAlt {
			d.alt = cur
		}
		if d.refreshLast {
			d.last = cur
		}
	}
	if !d.refreshEntropy {
		d.fc = d.lfc
	}
	d.cur = nil
	if !h.ShowFrame {
		return nil, nil
	}
	out := cur.SubImage(image.Rect(0, 0, d.w, d.h)).(*image.YCbCr)
	d.prev = out
	return out, nil
}

func (d *Decoder) parseSegmentHeader() {
	s := &d.seg
	s.enabled = d.fp.readFlag()
	s.updateMap = false
	if !s.enabled {
		return
	}
	s.updateMap = d.fp.readFlag()
	if d.fp.readFlag() {
		s.absDelta = d.fp.readFlag()
		for i := range s.quant {
			s.quant[i] = int8(d.fp.readOptionalInt(uniformProb, 7))
		}
		for i := range s.lf {
			s.lf[i] = int8(d.fp.readOptionalInt(uniformProb, 6))
		}
	}
	if s.updateMap {
		for i := range s.prob {
			if d.fp.readFlag() {
				s.prob[i] = uint8(d.fp.readUint(uniformProb, 8))
			} else {
				s.prob[i] = 255
			}
		}
	}
}

func (d *Decoder) parseFilterHeader() {
	f := &d.lf
	f.simple = d.fp.readFlag()
	f.level = int(d.fp.readUint(uniformProb, 6))
	f.sharp = int(d.fp.readUint(uniformProb, 3))
	f.deltaEnabled = d.fp.readFlag()
	if f.deltaEnabled && d.fp.readFlag() {
		for i := range f.ref {
			if d.fp.readFlag() {
				f.ref[i] = int8(d.fp.readInt(uniformProb, 6))
			}
		}
		for i := range f.mode {
			if d.fp.readFlag() {
				f.mode[i] = int8(d.fp.readInt(uniformProb, 6))
			}
		}
	}
}

func (d *Decoder) parsePartitions(rest []byte) error {
	d.nOP = 1 << d.fp.readUint(uniformProb, 2)
	n := 3 * (d.nOP - 1)
	if len(rest) < n {
		return errShort
	}
	sizes := rest[:n]
	rest = rest[n:]
	for i := 0; i < d.nOP; i++ {
		l := len(rest)
		if i < d.nOP-1 {
			pl := int(sizes[3*i]) | int(sizes[3*i+1])<<8 | int(sizes[3*i+2])<<16
			if pl < l {
				l = pl
			}
		}
		d.op[i].init(rest[:l])
		rest = rest[l:]
	}
	return nil
}

func (d *Decoder) decodeMB(mbx, mby int) {
	mi := &d.mbs[(mby+1)*d.miStride+mbx+1]
	if d.seg.updateMap {
		if !d.fp.readBit(d.seg.prob[0]) {
			mi.segment = uint8(d.fp.readUint(d.seg.prob[1], 1))
		} else {
			mi.segment = uint8(d.fp.readUint(d.seg.prob[2], 1)) + 2
		}
	}
	d.segment = 0
	if d.seg.enabled {
		d.segment = int(mi.segment)
	}
	skip := false
	if d.useSkipProb {
		skip = d.fp.readBit(d.skipProb)
	}
	for i := range d.coeff {
		d.coeff[i] = 0
	}
	d.prepareYBR(mbx, mby)

	inter := false
	if d.hdr.KeyFrame {
		d.usePredY16 = d.fp.readBit(145)
		if d.usePredY16 {
			d.parsePredModeY16(mbx)
		} else {
			d.parsePredModeY4(mbx)
		}
		d.parsePredModeC8()
		mi.ref, mi.mv = refIntra, mv{}
	} else if d.fp.readBit(d.probIntra) {
		inter = true
		d.parseInterMB(mbx, mby, mi)
	} else {
		d.parseIntraModesInter()
		mi.ref, mi.mv = refIntra, mv{}
	}
	if !inter {
		if !d.usePredY16 {
			mi.mode = mbB
		} else {
			switch d.predY16 {
			case predDC:
				mi.mode = mbDC
			case predVE:
				mi.mode = mbV
			case predHE:
				mi.mode = mbH
			default:
				mi.mode = mbTM
			}
		}
	}
	hasY2 := !(inter && mi.mode == mbSplit) && !(!inter && !d.usePredY16)
	var coeffs bool
	if !skip {
		coeffs = d.parseResiduals(mbx, mby, hasY2)
	} else {
		if hasY2 {
			d.leftMB.nzY16 = 0
			d.upMB[mbx].nzY16 = 0
		}
		d.leftMB.nzMask = 0
		d.upMB[mbx].nzMask = 0
		d.nzDCMask = 0
		d.nzACMask = 0
	}
	if inter {
		d.predictInter(mbx, mby, mi)
		d.addResidual()
	} else {
		d.reconstructMacroblock(mbx, mby)
	}
	cur := d.cur
	for i, y := (mby*cur.YStride+mbx)*16, 0; y < 16; i, y = i+cur.YStride, y+1 {
		copy(cur.Y[i:i+16], d.ybr[ybrYY+y][ybrYX:ybrYX+16])
	}
	for i, y := (mby*cur.CStride+mbx)*8, 0; y < 8; i, y = i+cur.CStride, y+1 {
		copy(cur.Cb[i:i+8], d.ybr[ybrBY+y][ybrBX:ybrBX+8])
		copy(cur.Cr[i:i+8], d.ybr[ybrRY+y][ybrRX:ybrRX+8])
	}
	mi.lfInner = coeffs || mi.mode == mbB || mi.mode == mbSplit
	if d.lf.level != 0 {
		mi.lfLevel = d.mbFilterLevel(d.segment, mi.ref, mi.mode)
	}
}
