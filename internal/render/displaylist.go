// Package render turns an HTML document plus stylesheets into geometry and a
// display list: CSS cascade and computed values, box-model layout (block,
// inline, inline-block, flex, grid, table, float, positioned), text shaping
// with real font metrics, and paint operations. Rasterization lives in
// internal/raster, which consumes the display list defined here.
//
// This file is the contract between the layout engine (producer) and the
// rasterizer / canvas packages (consumers). Coordinates are CSS pixels in
// document space (origin top-left of the page, y down) unless stated.
package render

import (
	"image"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/style"
)

// Color is non-premultiplied 8-bit RGBA (an alias of style.Color so the
// style engine and the display list share one type).
type Color = style.Color

// Rect is an axis-aligned rectangle.
type Rect struct{ X, Y, W, H float64 }

// Right and Bottom are the far edges.
func (r Rect) Right() float64  { return r.X + r.W }
func (r Rect) Bottom() float64 { return r.Y + r.H }

// Radii are the four corner radii (top-left, top-right, bottom-right,
// bottom-left), each with horizontal and vertical extent (elliptical corners).
type Radii [4][2]float64

// IsZero reports whether all corners are square.
func (r Radii) IsZero() bool {
	for _, c := range r {
		if c[0] > 0 || c[1] > 0 {
			return false
		}
	}
	return true
}

// Op is one paint operation. Ops are applied in order (painter's algorithm);
// layout emits them in CSS paint order (backgrounds, borders, floats, inline
// content, positioned layers by z-index).
type Op interface{ isOp() }

// FillRect fills a (possibly rounded) rectangle with a solid color.
type FillRect struct {
	Rect  Rect
	Color Color
	Radii Radii
}

// Border strokes the four sides of a border box. Widths and Styles are in
// top, right, bottom, left order. Styles: solid, dashed, dotted, double,
// groove, ridge, inset, outset, none, hidden.
type Border struct {
	Rect   Rect // border box
	Widths [4]float64
	Colors [4]Color
	Styles [4]string
	Radii  Radii
}

// Glyph is one positioned character in a TextRun; X is the pen offset from
// the run origin in px (already includes kerning, letter- and word-spacing).
type Glyph struct {
	R    rune
	X    float64
	Face *font.Face // face that has the glyph (fallback may differ per glyph)
	GID  int        // additive: glyph id to draw instead of R's glyph (ligatures); 0 = use R
}

// TextRun draws shaped text. X, Y is the baseline origin.
type TextRun struct {
	X, Y        float64
	Size        float64
	Color       Color
	Glyphs      []Glyph
	Width       float64 // total advance
	Bold        bool    // synthetic bold when the face has no bold variant
	Italic      bool    // synthetic oblique when the face has no italic variant
	Underline   bool
	Overline    bool
	LineThrough bool
	DecoStyle   string // solid, double, dotted, dashed, wavy
	DecoColor   Color
	DecoThick   float64 // text-decoration-thickness in px (0 = auto); additive
	UnderOffset float64 // text-underline-offset in px (0 = auto); additive
	Shadows     []TextShadow
	Stroke      float64 // -webkit-text-stroke width
	StrokeColor Color
}

// TextShadow is one text-shadow entry.
type TextShadow struct {
	DX, DY, Blur float64
	Color        Color
}

// Image draws a decoded bitmap into Dst, optionally cropping Src (zero Src =
// whole image). Used for <img>, <canvas>, <video> frames and CSS images.
type Image struct {
	Img    image.Image
	Dst    Rect
	Src    Rect
	Radii  Radii
	Smooth bool  // bilinear; false = nearest (image-rendering: pixelated)
	Tile   *Tile // when set, the image repeats (background-repeat)
}

// Tile describes background tiling: the image is drawn at Size, repeated
// every Step starting at Origin, clipped to Dst.
type Tile struct {
	Origin           [2]float64
	Size             [2]float64
	Step             [2]float64
	RepeatX, RepeatY bool
}

// GradientStop is a color stop at Pos in [0,1] along the gradient line.
type GradientStop struct {
	Pos   float64
	Color Color
}

// Gradient paints linear, radial or conic gradients into Rect.
type Gradient struct {
	Rect  Rect
	Kind  string // linear, radial, conic
	Radii Radii
	Stops []GradientStop
	// linear: Angle in degrees (CSS convention, 0 = to top, 90 = to right).
	Angle float64
	// radial: center and radii in px relative to Rect's top-left; conic: center + FromAngle.
	CX, CY, RX, RY float64
	FromAngle      float64
	Repeating      bool
	Tile           *Tile
}

// BoxShadow paints an outer or inset shadow for a (rounded) box.
type BoxShadow struct {
	Rect         Rect // the box casting the shadow (border box)
	Radii        Radii
	DX, DY       float64
	Blur, Spread float64
	Color        Color
	Inset        bool
}

// PushClip restricts painting to a (rounded) rectangle until PopClip.
type PushClip struct {
	Rect  Rect
	Radii Radii
}

// PopClip ends the innermost clip.
type PopClip struct{}

// PushLayer starts an isolated group composited back with Opacity and an
// optional blend mode when popped (opacity, mix-blend-mode, filter, transforms
// that need a layer).
type PushLayer struct {
	Opacity float64
	Blend   string // normal, multiply, screen, overlay, darken, lighten, ...
	Filter  []Filter
	Bounds  Rect
}

// Filter is one CSS filter function with its numeric argument(s).
type Filter struct {
	Name  string // blur, brightness, contrast, grayscale, hue-rotate, invert, opacity, saturate, sepia, drop-shadow
	Args  []float64
	Color Color // drop-shadow
}

// PopLayer ends the innermost layer.
type PopLayer struct{}

// PushTransform applies an affine matrix [a b c d e f] (CSS matrix()) until PopTransform.
//
// Lattice, when set, says the rasterizer must snap rectangles (backgrounds,
// borders, images, clips) to a whole-pixel grid anchored at (LatX, LatY) in the
// coordinates of the ops that follow, instead of snapping in device space.
// Chromium snaps in the local space of the nearest paint-offset container and
// transforms the snapped geometry afterwards, so a scaled or translated frame
// keeps its box edges on its own pixel grid. Without Lattice the old
// device-space snapping applies.
type PushTransform struct {
	M          [6]float64
	Lattice    bool
	LatX, LatY float64
}

// PopTransform ends the innermost transform.
type PopTransform struct{}

// Bitmap draws live pixel content owned by something other than layout
// (canvas, WebGL, video frame, SVG raster). Read at paint time.
type Bitmap struct {
	Node  *html.Node
	Dst   Rect
	Fit   string // object-fit: fill, contain, cover, none, scale-down
	Pos   [2]float64
	Radii Radii
}

func (FillRect) isOp()      {}
func (Border) isOp()        {}
func (TextRun) isOp()       {}
func (Image) isOp()         {}
func (Gradient) isOp()      {}
func (BoxShadow) isOp()     {}
func (PushClip) isOp()      {}
func (PopClip) isOp()       {}
func (PushLayer) isOp()     {}
func (PopLayer) isOp()      {}
func (PushTransform) isOp() {}
func (PopTransform) isOp()  {}
func (Bitmap) isOp()        {}

// Box is the geometry of one element, in document coordinates.
type Box struct {
	Border  Rect       // border box: what getBoundingClientRect reports (before transforms)
	Padding Rect       // padding box
	Content Rect       // content box
	Margin  [4]float64 // top, right, bottom, left
	Frags   []Rect     // one border-box fragment per line box for inline elements, per column for blocks split across columns (getClientRects)
	// Scroll sizes for overflow containers and the viewport.
	ScrollW, ScrollH float64
	ClientW, ClientH float64 // padding box minus scrollbars
	Display          string
	Position         string
	Visible          bool       // false for display:none / visibility:hidden
	Transform        [6]float64 // identity when no transform
	HasTransform     bool
	Xf3              *Xf3D // 3D transform data (nil without 3D involvement)
	OffsetParent     *html.Node
	ZIndex           int
	Style            *style.Computed // the element's computed style (getComputedStyle)
}

// Result is the output of Layout.
type Result struct {
	Width, Height float64 // document scroll size (>= viewport)
	Viewport      Rect
	Boxes         map[*html.Node]*Box
	List          []Op
	// Hit returns the topmost element at a document point, honoring paint
	// order, pointer-events and clipping; nil when the point hits only the canvas.
	Hit func(x, y float64) *html.Node
}

// Sheet is one stylesheet in cascade order.
type Sheet struct {
	Text      string
	BaseURL   string // for resolving url() references
	Media     string // media query from the <link>/<style> media attribute
	UserAgent bool
}

// Env supplies resources and live state to layout and paint.
type Env interface {
	// Image returns a decoded image for a resolved URL, or nil when it is not
	// available (not fetched, failed). Intrinsic sizes come from the bitmap.
	Image(url string) image.Image
	// Bitmap returns live pixels for <canvas>, <video> and similar nodes, or nil.
	Bitmap(n *html.Node) image.Image
	// IntrinsicSize returns the natural size of replaced content when known
	// (canvas width/height, video videoWidth/videoHeight); ok false if unknown.
	IntrinsicSize(n *html.Node) (w, h float64, ok bool)
	// Focused is the element with focus (for :focus and focus rings), or nil.
	Focused() *html.Node
}

// Options configure one layout pass.
type Options struct {
	Width, Height float64 // viewport in CSS px
	DPR           float64 // device pixel ratio (for image-set, resolution media)
	ColorScheme   string  // "light" | "dark"
	BaseURL       string
	Sheets        []Sheet // author sheets in order; the UA sheet is built in
	Env           Env
	ReducedMotion bool
	Print         bool // media type print instead of screen
	// ScrollX, ScrollY is the viewport's scroll position, used to decide which
	// content-visibility:auto subtrees are on screen.
	ScrollX, ScrollY float64
	// CVRelevant, when set, reports content-visibility:auto elements that are
	// relevant to the user for another reason (they contain focus or the
	// selection); those are never skipped.
	CVRelevant func(n *html.Node) bool
	// Session, when set, carries styles and SVG scenes from one pass over a
	// document to the next (see Session). The caller owns it and must not run
	// two passes at once.
	Session *Session
	// Fonts is the document's web font set (see font.WebSet). Family names it
	// declares resolve to its faces, which are fetched when the text needs them.
	Fonts *font.WebSet
}
