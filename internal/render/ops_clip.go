package render

// Additive display-list ops for CSS clip-path and mask. They are emitted
// around the painting of an element (after its transform has been pushed) and
// affect everything painted until the matching Pop op.

// ClipSub is one filled shape of a clip path. Coordinates are CSS px in the
// current (possibly transformed) space.
type ClipSub struct {
	Kind string // rrect | ellipse | polygon | path
	// rrect
	Rect  Rect
	Radii Radii
	// ellipse
	CX, CY, RX, RY float64
	// polygon
	Pts [][2]float64
	// path: SVG path data transformed by M (HasM) before filling
	D       string
	M       [6]float64
	HasM    bool
	EvenOdd bool
}

// PushClipPath clips painting to the union of Subs until PopClipPath.
type PushClipPath struct {
	Subs   []ClipSub
	Bounds Rect // area the clip can affect (the element's painted area)
}

// PopClipPath ends the innermost PushClipPath.
type PopClipPath struct{}

// MaskLayerOps is one mask layer: Ops paint the layer's image (clipped to the
// layer's painting area) onto a transparent surface; Mode picks whether its
// alpha or its luminance is the mask value, Composite how it combines with
// the layers below it.
type MaskLayerOps struct {
	Ops       []Op
	Mode      string // alpha | luminance
	Composite string // add | subtract | intersect | exclude
}

// PushMask paints everything until PopMask into an isolated group and
// multiplies it with the composite of Layers (top-most first, as written in
// CSS). Outside Bounds nothing is painted.
type PushMask struct {
	Bounds Rect
	Layers []MaskLayerOps
}

// PopMask ends the innermost PushMask and composites the masked group.
type PopMask struct{}

func (PushClipPath) isOp() {}
func (PopClipPath) isOp()  {}
func (PushMask) isOp()     {}
func (PopMask) isOp()      {}
