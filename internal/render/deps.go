// Package render is Sweef's pixel pipeline: computed style, CSS box layout,
// painting to a display list and rasterising it, plus the canvas 2D, WebGL
// and media element stacks that draw into it.
//
// Each stage lives in its own subpackage; see font, gfx, style, layout,
// paint, canvas, webgl and media.
package render

// Pin modules the subpackages rely on so go mod tidy keeps them while
// stages are built out in parallel.
import (
	_ "golang.org/x/image/font/opentype"
	_ "golang.org/x/image/vector"
)
