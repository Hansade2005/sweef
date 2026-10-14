package raster

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

// The FreeType-exact path reads the raw glyf table, which holds only a variable font's
// default instance. A face instantiated at another weight must not take it, or every
// variable web font paints at its default weight (Fraunces, whose default is 900, drew
// all text Black).
func TestVariableInstanceSkipsRawGlyf(t *testing.T) {
	sfnt, _, err := font.Decode(fonttest.VarBoxes("Var", true).SFNT(), font.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wf := font.NewWebFace(font.FaceDesc{Family: "V", WMin: 100, WMax: 900})
	if err := wf.SetData(sfnt); err != nil {
		t.Fatal(err)
	}
	f := wf.Face(900)
	if !f.Varied() {
		t.Fatal("weight 900 should be a varied instance")
	}
	if glyfFor(f) != nil {
		t.Error("a varied instance must use the font's own (delta-applied) outlines, not the raw glyf table")
	}
	if _, ok := fxContoursTT(f, 1, 1<<16, 1<<16, 0, fxMat{}); ok {
		t.Error("fxContoursTT must decline a varied instance so the caller falls back to OutlineGID")
	}
}
