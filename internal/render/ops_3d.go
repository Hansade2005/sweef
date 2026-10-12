package render

// Additive display-list ops for CSS 3D transforms.
//
// PushTransform3D opens a layer: everything painted until the matching
// PopTransform3D is rendered in the element's own local plane (the ops use the
// same document coordinates as everywhere else) at a raster scale the
// rasterizer chooses, and the finished layer is then drawn into the enclosing
// target through the projective mapping of the plane z = 0. This is how
// Chromium's compositor draws a layer with a perspective or 3D transform: the
// layer is rasterized in its own space and drawn as a textured quad.
//
// Depth ordering inside a 3D rendering context (transform-style: preserve-3d)
// is resolved by the display-list producer: the layers of a context are emitted
// back to front (BSP order, with layers split where planes intersect), so the
// rasterizer needs no depth buffer.

// PushTransform3D draws the ops up to PopTransform3D as one projectively
// transformed layer.
type PushTransform3D struct {
	// M is the layer's matrix, row-major 4x4, acting on column vectors
	// (x, y, z, 1) of the layer's coordinates, relative to the coordinate space
	// the op itself is emitted in (the enclosing layer or the page). Only the
	// z = 0 plane (rows and columns 0, 1, 3) is needed to draw; z and the full
	// matrix are informative (depth, backface tests).
	M [16]float64
	// Bounds is the part of the layer's plane that can carry ink, in the ops'
	// coordinates. Nothing outside it is drawn.
	Bounds Rect
	// Clip, when not empty, restricts the layer to this convex polygon (in the
	// ops' coordinates). The splitter of a 3D rendering context uses it to draw
	// the two halves of a layer that a plane cuts apart.
	Clip [][2]float64
	// Lattice anchors the layer's pixel grid at (LatX, LatY) in the ops'
	// coordinates: the layer is rasterized with that point on a pixel
	// boundary, and rectangles snap to the grid (see PushTransform.Lattice).
	Lattice    bool
	LatX, LatY float64
}

// PopTransform3D ends the innermost PushTransform3D and composites the layer.
type PopTransform3D struct{}

func (PushTransform3D) isOp() {}
func (PopTransform3D) isOp()  {}
