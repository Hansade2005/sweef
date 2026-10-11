// Package av1 is a pure-Go AV1 video decoder (AV1 Bitstream & Decoding
// Process Specification v1.0.0 errata 1). It decodes Main profile streams
// (8 and 10 bit, 4:2:0 and monochrome; other chroma formats are decoded by
// the same code paths but are not part of the tested feature set) and
// reproduces the normative output of the reference decoders sample for
// sample.
package av1

import (
	"errors"
	"fmt"
)

// Picture is a decoded output picture. Samples are stored as 16-bit values
// regardless of bit depth.
type Picture struct {
	Width, Height int // luma dimensions (UpscaledWidth x FrameHeight)
	BitDepth      int
	SubX, SubY    int
	Mono          bool
	Planes        [3][]uint16
	Stride        [3]int
	// RenderWidth/RenderHeight are the render_size hint of the frame.
	RenderWidth, RenderHeight int
	// Color description from the sequence header.
	ColorPrimaries, TransferCharacteristics, MatrixCoefficients int
	FullRange                                                   bool
	// Layer of the OBU the picture was coded in (scalable streams).
	SpatialID, TemporalID int
}

// frameBuf is a set of sample planes.
type frameBuf struct {
	pl     [3][]uint16
	stride [3]int
	w, h   [3]int // allocated dimensions
}

func newFrameBuf(w, h, subX, subY, numPlanes int) *frameBuf {
	fb := &frameBuf{}
	for p := 0; p < numPlanes; p++ {
		pw, ph := w, h
		if p > 0 {
			pw = (w + subX) >> uint(subX)
			ph = (h + subY) >> uint(subY)
		}
		fb.w[p], fb.h[p] = pw, ph
		fb.stride[p] = pw
		fb.pl[p] = make([]uint16, pw*ph)
	}
	return fb
}

// refFrame is the saved state of a decoded frame (spec 7.20). It is
// immutable once stored.
type refFrame struct {
	frameID         int
	upscaledWidth   int
	frameWidth      int
	frameHeight     int
	renderWidth     int
	renderHeight    int
	miCols, miRows  int
	frameType       int
	subX, subY      int
	bitDepth        int
	orderHint       int
	savedOrderHints [totalRefsPerFrame]int
	buf             *frameBuf // LrFrame, upscaled; valid area upscaledWidth x frameHeight
	savedRefFrames  []int8
	savedMvs        []mvec
	gmParams        [totalRefsPerFrame][6]int
	segIDs          []uint8
	cdf             *cdfCtx
	film            filmGrain
	lfRefDeltas     [totalRefsPerFrame]int
	lfModeDeltas    [2]int
	featEnabled     [maxSegments][segLvlMax]bool
	featData        [maxSegments][segLvlMax]int
	showable        bool
	mono            bool
	spatialID       int
	temporalID      int
}

// Decoder decodes a sequence of temporal units.
type Decoder struct {
	seq          *seqHdr
	ref          [numRefFrames]*refFrame
	refValid     [numRefFrames]bool
	refOrderHint [numRefFrames]int
	refFrameID   [numRefFrames]int
	orderHints   [totalRefsPerFrame]int

	hdrCDF        *cdfCtx
	prevSegIDsFor *refFrame

	seenFrameHeader bool
	fh              *frameHdr
	fd              *frameDec
	tileNum         int

	temporalID, spatialID int
	out                   []*Picture

	// OperatingPoint selects the operating point of a scalable stream (0 is
	// the highest quality one in practice) and AllLayers makes every shown
	// frame an output; by default only the highest spatial layer of the
	// operating point is output for each temporal unit, like dav1d.
	OperatingPoint int
	AllLayers      bool

	newTU bool     // the next output picture is the first of a temporal unit
	cache *Picture // spatial layer picture waiting for a higher layer
}

// opIdc returns operating_point_idc of the selected operating point.
func (d *Decoder) opIdc() int {
	if d.seq == nil {
		return 0
	}
	op := d.OperatingPoint
	if op < 0 || op >= d.seq.opCnt {
		op = 0
	}
	return d.seq.opIdc[op]
}

// maxSpatialID is the highest spatial layer of the operating point.
func (d *Decoder) maxSpatialID() int {
	m := d.opIdc() >> 8
	n := 0
	for m > 1 {
		m >>= 1
		n++
	}
	return n
}

// NewDecoder returns a decoder with no state.
func NewDecoder() *Decoder { return &Decoder{} }

// Reset discards all decoding state (call before decoding from a key frame).
func (d *Decoder) Reset() {
	*d = Decoder{OperatingPoint: d.OperatingPoint, AllLayers: d.AllLayers}
}

// Decode decodes one temporal unit (a sequence of OBUs in the low-overhead
// format) and returns the pictures that are output by it.
func (d *Decoder) Decode(data []byte) (pics []*Picture, err error) {
	d.out = nil
	defer func() {
		defer func() { d.endTemporalUnit(); pics = d.out }()
		if r := recover(); r != nil {
			// Corrupt streams can index outside the tables; report an error
			// rather than crashing the caller.
			d.seenFrameHeader = false
			d.fd = nil
			pics, err = d.out, fmt.Errorf("av1: corrupt stream: %v", r)
		}
	}()
	if IsAnnexB(data) {
		err := d.decodeAnnexB(data)
		return d.out, err
	}
	pos := 0
	for pos < len(data) {
		n, err := d.decodeOBU(data[pos:])
		if err != nil {
			d.seenFrameHeader = false
			d.fd = nil
			return d.out, err
		}
		pos += n
	}
	return d.out, nil
}

var errNoSeq = errors.New("av1: frame before sequence header")

func (d *Decoder) decodeOBU(data []byte) (int, error) {
	b := &bitReader{buf: data}
	b.f(1) // forbidden
	obuType := b.f(4)
	extFlag := b.flag()
	hasSize := b.flag()
	b.f(1)
	temporalID, spatialID := 0, 0
	if extFlag {
		temporalID = b.f(3)
		spatialID = b.f(2)
		b.f(3)
	}
	var obuSize int
	if hasSize {
		obuSize = b.leb128()
	} else {
		obuSize = len(data) - 1 - b2i(extFlag)
	}
	if b.err {
		return 0, errBitstream
	}
	hdrBytes := b.pos >> 3
	if obuSize < 0 || hdrBytes+obuSize > len(data) {
		return 0, errBitstream
	}
	payload := data[hdrBytes : hdrBytes+obuSize]
	total := hdrBytes + obuSize
	if obuType != obuSequenceHeader && obuType != obuTemporalDelimiter && d.seq != nil &&
		d.opIdc() != 0 && extFlag {
		idc := d.opIdc()
		inT := (idc >> uint(temporalID)) & 1
		inS := (idc >> uint(spatialID+8)) & 1
		if inT == 0 || inS == 0 {
			return total, nil
		}
	}
	d.temporalID, d.spatialID = temporalID, spatialID
	switch obuType {
	case obuSequenceHeader:
		s := &seqHdr{}
		pb := &bitReader{buf: payload}
		if err := s.parse(pb); err != nil {
			return 0, err
		}
		d.seq = s
	case obuTemporalDelimiter:
		d.seenFrameHeader = false
		d.newTU = true
	case obuFrameHeader, obuRedundantFrameHeader:
		if d.seq == nil {
			return 0, errNoSeq
		}
		if d.seenFrameHeader {
			break // frame_header_copy
		}
		pb := &bitReader{buf: payload}
		if err := d.frameHeaderOBU(pb); err != nil {
			return 0, err
		}
	case obuFrame:
		if d.seq == nil {
			return 0, errNoSeq
		}
		pb := &bitReader{buf: payload}
		if err := d.frameHeaderOBU(pb); err != nil {
			return 0, err
		}
		pb.byteAlign()
		if d.fd == nil {
			return 0, errBitstream
		}
		if err := d.tileGroupOBU(payload[pb.pos>>3:]); err != nil {
			return 0, err
		}
	case obuTileGroup:
		if d.fd == nil {
			return 0, errBitstream
		}
		if err := d.tileGroupOBU(payload); err != nil {
			return 0, err
		}
	default:
		// metadata, padding, tile list, reserved: ignored
	}
	return total, nil
}

// frameHeaderOBU parses a frame header and prepares frame decoding.
func (d *Decoder) frameHeaderOBU(pb *bitReader) error {
	d.seenFrameHeader = true
	fh, err := d.parseFrameHeader(pb, d.temporalID, d.spatialID)
	if err != nil {
		d.seenFrameHeader = false
		return err
	}
	d.fh = fh
	if fh.showExisting {
		err := d.showExistingFrame(fh)
		d.seenFrameHeader = false
		return err
	}
	d.orderHints = fh.orderHints
	if err := d.setupFrame(fh); err != nil {
		d.seenFrameHeader = false
		return err
	}
	d.tileNum = 0
	return nil
}

func (d *Decoder) tileGroupOBU(data []byte) error {
	fh := d.fh
	fd := d.fd
	b := &bitReader{buf: data}
	numTiles := fh.tileCols * fh.tileRows
	startAndEnd := false
	if numTiles > 1 {
		startAndEnd = b.flag()
	}
	tgStart, tgEnd := 0, numTiles-1
	if startAndEnd {
		tileBits := fh.tileColsLog2 + fh.tileRowsLog2
		tgStart = b.f(tileBits)
		tgEnd = b.f(tileBits)
	}
	b.byteAlign()
	if b.err || tgEnd >= numTiles || tgStart > tgEnd {
		return errBitstream
	}
	pos := b.pos >> 3
	for tn := tgStart; tn <= tgEnd; tn++ {
		tileRow := tn / fh.tileCols
		tileCol := tn % fh.tileCols
		var tileSize int
		if tn == tgEnd {
			tileSize = len(data) - pos
		} else {
			if pos+fh.tileSizeBytes > len(data) {
				return errBitstream
			}
			tileSize = 0
			for i := 0; i < fh.tileSizeBytes; i++ {
				tileSize |= int(data[pos+i]) << uint(8*i)
			}
			tileSize++
			pos += fh.tileSizeBytes
		}
		if tileSize < 0 || pos+tileSize > len(data) {
			return errBitstream
		}
		if err := fd.decodeTile(data[pos:pos+tileSize], tn, tileRow, tileCol); err != nil {
			return err
		}
		pos += tileSize
	}
	d.tileNum = tgEnd + 1
	if tgEnd == numTiles-1 {
		d.seenFrameHeader = false
		err := d.finishFrame()
		d.fd = nil
		return err
	}
	return nil
}
