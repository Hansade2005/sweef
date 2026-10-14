package vp9

import (
	"errors"
	"image"

	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// ErrUnsupported is returned for streams using features this decoder lacks.
var ErrUnsupported = errors.New("vp9: unsupported stream feature")

var errBitstream = errors.New("vp9: invalid bitstream")

// Frame is a decoded picture (also used as a reference).
type Frame struct {
	w, h   int // FrameWidth, FrameHeight
	pw, ph [3]int
	stride [3]int
	pix    [3][]uint16
	subX   int
	subY   int
	bd     int // bit depth
	cs     int // color_space
	full   bool
}

type segParams struct {
	enabled     bool
	updateMap   bool
	temporal    bool
	updateData  bool
	absDelta    bool
	treeProbs   [7]uint8
	predProbs   [3]uint8
	featEnabled [8][4]bool
	featData    [8][4]int
}

// modeInfo is the per 8x8 block information (spec 6.4.4).
type modeInfo struct {
	size     uint8
	skip     bool
	txSize   uint8
	yMode    uint8
	subModes [4]uint8
	refFrame [2]int8
	filter   uint8
	segID    uint8
	mv       [2]mv    // Mvs[..][list] = BlockMvs[list][3]
	subMvs   [2][4]mv // SubMvs
	isInter  bool
}

type mv struct{ row, col int16 }

// Decoder decodes a VP9 stream, one chunk (frame or superframe) at a time.
type Decoder struct {
	refs         [8]*Frame
	ctxs         [4]probs
	p            probs
	cnt          counts
	lfRefDeltas  [4]int
	lfModeDeltas [2]int
	seg          segParams
	prevSegIDs   []uint8
	segIDs       []uint8
	prevMi       []modeInfo // PrevMvs / PrevRefFrames
	mi           []modeInfo

	// previous-frame state
	haveSize      bool
	lastW, lastH  int
	lastShow      bool
	lastFrameType int
	frameType     int

	// current frame header
	profile         int
	showFrame       bool
	errorRes        bool
	intraOnly       bool
	frameIsIntra    bool
	resetFrameCtx   int
	refreshFlags    int
	refFrameIdx     [3]int
	refValid        [3]bool
	signBias        [4]bool
	allowHp         bool
	interpFilter    int
	refreshFrameCtx bool
	frameParallel   bool
	frameCtxIdx     int
	lfLevel         int
	lfSharp         int
	lfDeltaEnabled  bool
	baseQIdx        int
	deltaQYDc       int
	deltaQUVDc      int
	deltaQUVAc      int
	lossless        bool
	tileColsLog2    int
	tileRowsLog2    int
	headerSize      int
	txMode          int
	referenceMode   int
	compFixedRef    int
	compVarRef      [2]int
	usePrevMvs      bool

	width, height      int
	miCols, miRows     int
	sb64Cols, sb64Rows int

	cur *Frame

	// colour configuration of the current frame
	bitDepth int
	maxV     int32
	subX     int
	subY     int
	colorSp  int
	fullRng  bool

	// tile/block decode state
	bd                   boolDec
	miRowStart, miRowEnd int
	miColStart, miColEnd int
	aboveNZ              [3][]uint8
	leftNZ               [3][16]uint8
	abovePart            []uint8
	leftPart             [8]uint8
	aboveSegPred         []uint8
	leftSegPred          [8]uint8
	blk                  blockState
	tokens               [1024]int32
	tokenCache           [1024]uint8
	eobTotal             int
	lvlLookup            [8][4][2]uint8
	fp                   [4][4][3]int32 // dequant cache

	workers []*Decoder // tile column workers (decodeTiles), reused between frames

	predBuf   [2][64 * 64]uint16
	tmpBuf    [(64 + 7) * (64 + 7)]uint16
	predTmp   [(64 + 7) * 64]uint16
	scaledTmp [330 * 64]uint16
}

// NewDecoder returns a new decoder.
func NewDecoder() *Decoder { return &Decoder{} }

// Reset drops all reference frames (used when seeking).
func (d *Decoder) Reset() {
	d.refs = [8]*Frame{}
	d.haveSize = false
	d.prevMi = nil
}

// Decode decodes one chunk of data (a frame or a superframe) and returns the
// last frame it displays as an 8-bit YCbCr image, or nil when it displays
// none. Streams that do not fit an 8-bit image.YCbCr (high bit depth, 4:4:4,
// ...) return ErrUnsupported; use DecodeFrame for those.
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

// DecodeFrame decodes one chunk of data (a frame or a superframe) and returns
// the last frame it displays in its native sample format, or nil when it
// displays none.
func (d *Decoder) DecodeFrame(data []byte) (img *yuv.Frame, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, errBitstream
		}
	}()
	frames, err := splitSuperframe(data)
	if err != nil {
		return nil, err
	}
	for _, f := range frames {
		out, e := d.decodeFrame(f)
		if e != nil {
			return img, e
		}
		if out != nil {
			img = out
		}
	}
	return img, nil
}

// splitSuperframe parses the superframe index (Annex B).
func splitSuperframe(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	last := data[len(data)-1]
	if last&0xe0 == 0xc0 {
		frames := int(last&7) + 1
		mag := int(last>>3&3) + 1
		idxSize := 2 + mag*frames
		if len(data) >= idxSize && data[len(data)-idxSize] == last {
			p := len(data) - idxSize + 1
			var out [][]byte
			off := 0
			for i := 0; i < frames; i++ {
				sz := 0
				for b := 0; b < mag; b++ {
					sz |= int(data[p]) << uint(8*b)
					p++
				}
				if off+sz > len(data)-idxSize {
					return nil, errBitstream
				}
				if sz > 0 {
					out = append(out, data[off:off+sz])
				}
				off += sz
			}
			return out, nil
		}
	}
	return [][]byte{data}, nil
}
