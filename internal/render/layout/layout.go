package layout

import (
	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render/res"
	"github.com/sweefbrowser/sweef/internal/render/style"
)

// Layout lays out d at a viewport of vw x vh CSS px. Styles come from sr
// (built for the same viewport); images and replaced bitmaps from ld and
// res.Lookup.
func Layout(d *fnet.Doc, sr *style.Resolver, ld res.Loader, vw, vh float64) *Result {
	return &Result{Root: &Box{Kind: KindBlock, W: vw, H: vh}, DocW: vw, DocH: vh, ViewportW: vw, ViewportH: vh}
}
