package gfx

import "image"

// DisplayList is a recorded sequence of drawing commands in CSS px (page
// coordinates). The painter (render/paint) produces one per frame; Replay
// rasterises it at any device scale and scroll offset, so a viewport
// screenshot and a full-page screenshot come from the same list.
//
// Commands nest: every Push* has a matching Pop. State pushed is the
// current transform, clip, and layer (opacity, blend, filter).
type DisplayList struct {
	Items []Item
	// Bounds is the page's scrollable area (document width/height).
	Bounds Rect
	// Background is painted under everything (the canvas background).
	Background Color
}

// Item is one display list command. Exactly one of the pointer fields is
// set; Kind says which.
type Item struct {
	Kind ItemKind

	Rect   *RectItem
	Border *BorderItem
	Shadow *BoxShadowItem
	Text   *TextItem
	Image  *ImageItem
	Path   *PathItem
	Clip   *ClipItem
	Layer  *LayerItem
	Xform  *TransformItem
	// NodeID identifies the DOM element (backend id) that produced the
	// item, for debugging and hit-testing overlays; 0 when anonymous.
	NodeID int64
}

// ItemKind tags an Item.
type ItemKind uint8

const (
	KindRect ItemKind = iota
	KindBorder
	KindBoxShadow
	KindText
	KindImage
	KindPath
	KindPushClip
	KindPopClip
	KindPushLayer
	KindPopLayer
	KindPushTransform
	KindPopTransform
)

// RectItem fills a (possibly rounded) rectangle with a paint: background
// colours, background images and gradients (with Paint.Matrix placing the
// tile), selection highlights.
type RectItem struct {
	Rect  Rect
	Radii Radii
	Paint Paint
}

// BorderStyle is a CSS border-style.
type BorderStyle uint8

const (
	BorderNone BorderStyle = iota
	BorderHidden
	BorderSolid
	BorderDashed
	BorderDotted
	BorderDouble
	BorderGroove
	BorderRidge
	BorderInset
	BorderOutset
)

// BorderItem paints a box's four borders. Rect is the border box; Widths
// are top, right, bottom, left; Radii are the outer radii.
type BorderItem struct {
	Rect   Rect
	Widths [4]float64
	Colors [4]Color
	Styles [4]BorderStyle
	Radii  Radii
}

// BoxShadowItem paints one CSS box-shadow for a border box Rect with outer
// Radii. Inset shadows paint inside the padding box; outset ones outside
// the border box only (the box itself is cut out).
type BoxShadowItem struct {
	Rect    Rect
	Radii   Radii
	OffsetX float64
	OffsetY float64
	Blur    float64
	Spread  float64
	Color   Color
	Inset   bool
	// Borders lets inset shadows find the padding box.
	Borders [4]float64
}

// TextDecoration lines.
type TextDecoration uint8

const (
	DecoUnderline TextDecoration = 1 << iota
	DecoOverline
	DecoLineThrough
)

// TextItem paints one shaped run on a baseline.
type TextItem struct {
	Run   GlyphRun
	Color Color
	// Paint, when its Kind is not solid, overrides Color (background-clip:
	// text and -webkit-text-fill-color with gradients).
	Paint   *Paint
	Shadows []Shadow
	// Decorations are drawn by the painter as Rect/Path items already; the
	// width here is the run's advance, for selection and debugging.
	Width float64
	// StrokeWidth and StrokeColor implement -webkit-text-stroke.
	StrokeWidth float64
	StrokeColor Color
}

// ImageRendering selects sampling.
type ImageRendering uint8

const (
	RenderAuto ImageRendering = iota
	RenderPixelated
)

// ImageItem draws Src (whole image when Src is empty) into Dst, clipped to
// Clip when non-empty (object-fit: cover overflow) and to Radii.
type ImageItem struct {
	Image     image.Image
	Src       Rect
	Dst       Rect
	Clip      Rect
	Radii     Radii
	Rendering ImageRendering
}

// PathItem fills or strokes an arbitrary path (SVG shapes, list markers,
// form control glyphs, dotted/dashed decorations).
type PathItem struct {
	Path   *Path
	Rule   FillRule
	Paint  Paint
	Stroke *StrokeStyle // nil = fill
}

// ClipItem pushes a clip: a rounded rectangle, or an arbitrary path when
// Path is set (clip-path).
type ClipItem struct {
	Rect  Rect
	Radii Radii
	Path  *Path
	Rule  FillRule
}

// FilterOp is one CSS filter function.
type FilterOp struct {
	Kind   string  // blur, brightness, contrast, grayscale, hue-rotate, invert, opacity, saturate, sepia, drop-shadow
	Amount float64 // px for blur, radians for hue-rotate, fraction otherwise
	Shadow Shadow  // for drop-shadow
}

// LayerItem starts an isolated group composited back with Opacity, Blend
// and Filters when popped (stacking contexts that need a layer).
type LayerItem struct {
	Opacity float64
	Blend   CompositeOp
	Filters []FilterOp
	// Bounds bounds the layer's content (page px); empty = whole surface.
	Bounds Rect
}

// TransformItem pre-multiplies the current transform by Matrix.
type TransformItem struct {
	Matrix Matrix
}

// Builder helpers keep painter code short.

func (d *DisplayList) push(it Item) { d.Items = append(d.Items, it) }

func (d *DisplayList) FillRect(r Rect, radii Radii, p Paint, node int64) {
	d.push(Item{Kind: KindRect, Rect: &RectItem{r, radii, p}, NodeID: node})
}
func (d *DisplayList) Border(b *BorderItem, node int64) {
	d.push(Item{Kind: KindBorder, Border: b, NodeID: node})
}
func (d *DisplayList) BoxShadow(b *BoxShadowItem, node int64) {
	d.push(Item{Kind: KindBoxShadow, Shadow: b, NodeID: node})
}
func (d *DisplayList) Text(t *TextItem, node int64) {
	d.push(Item{Kind: KindText, Text: t, NodeID: node})
}
func (d *DisplayList) Image(i *ImageItem, node int64) {
	d.push(Item{Kind: KindImage, Image: i, NodeID: node})
}
func (d *DisplayList) Path(p *PathItem, node int64) {
	d.push(Item{Kind: KindPath, Path: p, NodeID: node})
}
func (d *DisplayList) PushClip(c *ClipItem) { d.push(Item{Kind: KindPushClip, Clip: c}) }
func (d *DisplayList) PopClip()             { d.push(Item{Kind: KindPopClip}) }
func (d *DisplayList) PushLayer(l *LayerItem) {
	d.push(Item{Kind: KindPushLayer, Layer: l})
}
func (d *DisplayList) PopLayer() { d.push(Item{Kind: KindPopLayer}) }
func (d *DisplayList) PushTransform(m Matrix) {
	d.push(Item{Kind: KindPushTransform, Xform: &TransformItem{m}})
}
func (d *DisplayList) PopTransform() { d.push(Item{Kind: KindPopTransform}) }

// Replay rasterises the list onto a new surface of w x h device px. The
// page point (scrollX, scrollY) maps to the surface origin and every page
// px is dpr device px. Implemented in replay.go.
func (d *DisplayList) Replay(w, h int, scrollX, scrollY, dpr float64) *Surface {
	return replay(d, w, h, scrollX, scrollY, dpr)
}
