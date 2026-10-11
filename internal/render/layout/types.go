// Package layout turns styled DOM into a tree of positioned box fragments:
// CSS 2.2 block and inline formatting contexts (line boxes, floats,
// clearance, margin collapsing), relative/absolute/fixed/sticky positioning,
// flexbox, grid, tables, replaced elements, list markers and generated
// content. Text is measured with render/font so painted text lands exactly
// where it was laid out.
//
// This file is the contract with render/paint; layout.go implements Layout.
package layout

import (
	"image"

	"github.com/sweefbrowser/sweef/internal/render/font"
	"github.com/sweefbrowser/sweef/internal/render/style"
	"golang.org/x/net/html"
)

// Kind is what a Box represents.
type Kind uint8

const (
	// KindBlock is a block-level box or block container (including the
	// root, flex/grid/table containers and their items, table cells,
	// inline-blocks' own box).
	KindBlock Kind = iota
	// KindInline is one fragment of an inline box on one line; an inline
	// element that wraps produces one fragment per line.
	KindInline
	// KindText is a run of text on one line (a child of a line or inline
	// fragment).
	KindText
	// KindLine is a line box inside a block container with inline content.
	KindLine
	// KindReplaced is an image, canvas, video, iframe, embedded SVG or form
	// control painted from Replaced.
	KindReplaced
	// KindMarker is a list item's outside marker.
	KindMarker
)

// Box is one fragment. Geometry is in page CSS px (origin at the top-left
// of the initial containing block, y down), includes relative/sticky
// offsets, and excludes transforms (paint applies Style.Transform about
// the border box).
type Box struct {
	Kind  Kind
	Node  *html.Node // element (or text node for KindText); nil when anonymous
	Style *style.ComputedStyle
	// Pseudo is "before", "after", "marker", ... for generated boxes.
	Pseudo string

	// Border box.
	X, Y, W, H float64
	// Used margin, border and padding widths (top, right, bottom, left).
	Margin, Border, Padding [4]float64

	// Children in paint/tree order: block children, line boxes, inline
	// fragments, text runs, absolutely positioned descendants whose
	// containing block is this box (flagged by Style.Position).
	Children []*Box

	// Text is set for KindText.
	Text *Text
	// Replaced is set for KindReplaced.
	Replaced *Replaced

	// For KindInline fragments: whether this fragment holds the inline
	// box's start / end edge (borders, padding and margins only apply on
	// those sides when the box is split across lines).
	First, Last bool

	// Baseline is the y of the first line's baseline (page px); 0 when the
	// box has no baseline.
	Baseline float64

	// Scrollable overflow extent of a scroll container's content, in page
	// px relative to the padding box origin (for overflow clipping and
	// scrollWidth/scrollHeight).
	ScrollW, ScrollH float64
	// ScrollX/ScrollY are the container's scroll offsets (element.scrollTop).
	ScrollX, ScrollY float64

	// Fixed is set for position: fixed boxes (painted relative to the
	// viewport rather than the page).
	Fixed bool
}

// PaddingBox returns the padding box rectangle.
func (b *Box) PaddingBox() (x, y, w, h float64) {
	return b.X + b.Border[3], b.Y + b.Border[0], b.W - b.Border[1] - b.Border[3], b.H - b.Border[0] - b.Border[2]
}

// ContentBox returns the content box rectangle.
func (b *Box) ContentBox() (x, y, w, h float64) {
	px, py, pw, ph := b.PaddingBox()
	return px + b.Padding[3], py + b.Padding[0], pw - b.Padding[1] - b.Padding[3], ph - b.Padding[0] - b.Padding[2]
}

// Text is a shaped run on one line.
type Text struct {
	Face   *font.Face
	Glyphs []font.Glyph // X relative to the run start
	// Origin of the run: X of the first glyph's pen position and the
	// baseline Y, page px.
	X, Baseline float64
	Width       float64
	// Str is the text after white-space processing and text-transform.
	Str string
	// Ellipsis marks a run truncated by text-overflow: ellipsis (the
	// ellipsis glyphs are already appended to Glyphs).
	Ellipsis bool
}

// ReplacedKind says what a replaced box draws.
type ReplacedKind uint8

const (
	ReplacedImage ReplacedKind = iota // <img>, <input type=image>, <svg> rendered to a bitmap
	ReplacedCanvas
	ReplacedVideo
	ReplacedIframe
	ReplacedControl // form controls (input, button, select, textarea, progress, meter)
	ReplacedBroken  // image failed to load: alt text / broken icon
)

// Replaced is the content of a replaced box.
type Replaced struct {
	Kind ReplacedKind
	// Image is the bitmap to draw (nil when not loaded).
	Image image.Image
	// Content rect where the image is drawn after object-fit and
	// object-position (page px, may exceed the content box: paint clips).
	DX, DY, DW, DH float64
	// Control describes form controls for paint.
	Control *Control
	// AltText for broken images.
	AltText string
}

// Control is a form control's paint-relevant state.
type Control struct {
	Type          string // text, password, checkbox, radio, button, submit, reset, select, textarea, range, color, file, progress, meter, date, number, search, email, url, tel
	Value         string // displayed value (password masked)
	Placeholder   bool   // Value is the placeholder text
	Checked       bool
	Disabled      bool
	Indeterminate bool
	// Fraction for range/progress/meter (0..1); -1 when indeterminate.
	Fraction float64
	// Text is the shaped value/label (laid out inside the content box).
	Text *Text
}

// Result is a layout of one document at one viewport.
type Result struct {
	Root *Box
	// Document scroll size (page px).
	DocW, DocH float64
	ViewportW  float64
	ViewportH  float64
	// Fragments lists every box generated for each element, in order.
	Fragments map[*html.Node][]*Box
}

// BorderBox returns the union of an element's fragment border boxes, the
// value getBoundingClientRect reports (before transforms).
func (r *Result) BorderBox(n *html.Node) (x, y, w, h float64, ok bool) {
	fs := r.Fragments[n]
	if len(fs) == 0 {
		return 0, 0, 0, 0, false
	}
	x0, y0, x1, y1 := fs[0].X, fs[0].Y, fs[0].X+fs[0].W, fs[0].Y+fs[0].H
	for _, f := range fs[1:] {
		if f.X < x0 {
			x0 = f.X
		}
		if f.Y < y0 {
			y0 = f.Y
		}
		if f.X+f.W > x1 {
			x1 = f.X + f.W
		}
		if f.Y+f.H > y1 {
			y1 = f.Y + f.H
		}
	}
	return x0, y0, x1 - x0, y1 - y0, true
}
