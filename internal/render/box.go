package render

import (
	"image"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

type boxKind uint8

const (
	kBlock  boxKind = iota // block-level box (any inner display)
	kInline                // inline-level non-atomic box (span, a, ...)
	kText                  // text run
	kAtomic                // atomic inline-level box (inline-block, replaced inline, inline-flex...)
	kBreak                 // <br>
	kMarker                // list marker (outside or inside)
)

// inner display types
const (
	iFlow = iota
	iFlex
	iGrid
	iTable
	iRow
	iRowGroup
	iCell
	iCaption
	iColGroup
	iCol
	iReplaced
	iControl
	iMulticol
)

// replacedInfo describes replaced and form-control content.
type replacedInfo struct {
	kind   string // img canvas video iframe svg embed object audio input select textarea progress meter
	iw, ih float64
	hasIW  bool
	hasIH  bool
	ratio  float64 // intrinsic aspect ratio (w/h), 0 none
	img    image.Image
	url    string
	bitmap bool
	ctrl   string // control subtype: text, checkbox, radio, button, range, color, file...
}

type lbox struct {
	n      *html.Node
	st     *style.Computed
	parent *lbox
	kids   []*lbox
	kind   boxKind
	inner  int
	text   string
	pseudo string
	// classification
	floated       bool
	oof           bool // absolutely positioned or fixed
	inlineLv      bool
	ifc           bool // block container whose children are inline-level (inline formatting context)
	anon          bool
	rep           *replacedInfo
	cont          int  // continuation index for split inlines (0 = first piece)
	lastPart      bool // last piece of an inline that was split around blocks
	split         bool
	markerOutside bool
	liIndex       int

	// resolved box model (px)
	mar, bor, pad [4]float64
	marAuto       [4]bool

	// layout results; x,y are relative to the layout parent's border-box origin
	x, y, w, h       float64
	lp               *lbox   // layout parent whose coordinate system x,y uses
	ax, ay           float64 // absolute border-box origin (after placement)
	relX, relY       float64
	mtopOut, mbotOut float64 // collapsed margins exposed to the parent
	through          bool
	hasFloat         bool
	baseline         float64 // first-line baseline from border-box top (for inline-block / flex / table alignment)
	lastBase         float64
	hasBase          bool
	contentH         float64 // content height (auto height result)
	scrollW, scrollH float64
	vscroll, hscroll bool // scrollbars take space
	sbW, sbH         float64

	// inline layout results
	lines     []*line
	frags     []Rect  // inline element fragments in IFC root coordinates (block-relative)
	listItems []*lbox // flattened atomic/float boxes of an IFC for placement

	// intrinsic size cache
	minW, maxW float64
	hasIntr    bool

	// layout cache
	lastIn  layoutIn
	hasLast bool

	// abspos static position (relative to containing block's lp coordinate system)
	staticX, staticY                 float64
	staticSet                        bool
	oofKids                          []*lbox // abs-positioned descendants whose containing block is this box
	txs                              *tx
	node                             *Box
	zIdx                             int
	stacking                         bool
	flexOrder                        int
	gridCell                         *gridPlace
	extra                            any
	atoms                            []atom
	atomsDone                        bool
	balancing                        bool
	touchesBFC                       bool
	oofDone                          bool
	cbBox                            *lbox
	ctrlBase                         float64
	marker                           *lbox
	ovProp                           bool
	ovRight, ovBottom, ovLeft, ovTop float64
	scrollR, scrollB                 float64
	inlineKids                       []*lbox
	absFrags                         []Rect
	fragsAbs                         []Rect
	isBtn                            bool
	contBlocks                       []*lbox
	hiddenFlow                       bool
	mcN                              int
	mcSegs                           []mcSeg // vertical ranges where the columns apply (column-span splits them)
	mcFrags                          []Rect  // per-column fragments of a block split across columns, relative to its bounding box origin
	mcW, mcGap                       float64
	legend                           *lbox
	legendPad                        float64
	legendBase                       float64
	borOv, borMin                    [4]float64
	hasBorOv                         bool
	cl                               *collapsed
	flSt                             *style.Computed // ::first-line style of an inline formatting context root
	atomsFL                          []atom
	flMap                            map[*lbox]*style.Computed
	vflow, vt, vmode                 int8 // orthogonal flow root mode / mode of the logical frame being laid out / mode of a converted IFC root
	ost                              *style.Computed
	noFrags                          bool // synthetic box: its inline boxes report no fragments
	vlines                           []*line
	force                            map[int]bool // atom indices that must start a new line (text-wrap: balance / pretty)
	quirkMT, quirkMB                 bool         // quirks mode: UA margin ignored at the start/end of body or a table cell
	forceCols, forceRows             []float64    // subgrid: track sizes taken from the parent grid
	forceCGap, forceRGap             float64
	natColN, natRowN                 int // subgrid intrinsic pass: number of auto tracks
	gridNat                          bool
	gridColSz, gridRowSz             []float64 // track sizes after the last grid layout
}

func (b *lbox) shiftFrags(dx, dy float64) {
	for i := range b.absFrags {
		b.absFrags[i].X += dx
		b.absFrags[i].Y += dy
	}
}

type tx struct {
	m      [6]float64
	origin [2]float64
}

func (b *lbox) isBlockLevel() bool { return b.kind == kBlock }

// display returns the (possibly blockified) display of b.
func (b *lbox) display() string { return b.st.Display }

func newBox(n *html.Node, st *style.Computed, k boxKind) *lbox {
	return &lbox{n: n, st: st, kind: k}
}

func (b *lbox) addKid(k *lbox) {
	k.parent = b
	b.kids = append(b.kids, k)
}

// Rect helpers.
func (r Rect) union(o Rect) Rect {
	if r.W <= 0 && r.H <= 0 {
		return o
	}
	if o.W <= 0 && o.H <= 0 {
		return r
	}
	x0, y0 := minf(r.X, o.X), minf(r.Y, o.Y)
	x1, y1 := maxf(r.Right(), o.Right()), maxf(r.Bottom(), o.Bottom())
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func clampf(v, lo, hi float64) float64 {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}
