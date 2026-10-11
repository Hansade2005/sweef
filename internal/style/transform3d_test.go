package style

import "testing"

func compute3D(t *testing.T, css string) *Computed {
	doc := parse(t, `<html><head><style>`+css+`</style></head><body><div id=d></div></body></html>`)
	e := NewEngine(&Env{Width: 1280, Height: 800}, []SheetIn{{Text: css}})
	hc := e.Compute(find(doc, "html"), nil, "")
	body := e.Compute(find(doc, "body"), hc, "")
	return e.Compute(find(doc, "div"), body, "")
}

func TestTransform3DParsing(t *testing.T) {
	c := compute3D(t, `div { transform: rotate3d(1,1,0,45deg) translateZ(10px) matrix3d(1,0,0,0, 0,1,0,0, 0,0,1,-0.002, 5,6,7,1) perspective(300px);
		transform-origin: 10px 20px 30px; perspective: 500px; perspective-origin: 20% 80%; transform-style: preserve-3d;
		backface-visibility: hidden; transform-box: content-box }`)
	if len(c.Transform) != 4 {
		t.Fatalf("ops %v", c.Transform)
	}
	if op := c.Transform[0]; op.Fn != "rotate3d" || op.N != 4 || op.A[3].V != 45 {
		t.Errorf("rotate3d %+v", op)
	}
	if op := c.Transform[2]; op.Fn != "matrix3d" || op.M == nil || op.M[11] != -0.002 || op.M[12] != 5 {
		t.Errorf("matrix3d %+v", op)
	}
	if c.TOriginZ != 30 || c.Perspective != 500 || c.PerspectiveOrigin[0].V != 20 || c.TransformStyle != "preserve-3d" || !c.BackfaceHidden || c.TransformBox != "content-box" {
		t.Errorf("props %v %v %v %v %v %v", c.TOriginZ, c.Perspective, c.PerspectiveOrigin, c.TransformStyle, c.BackfaceHidden, c.TransformBox)
	}
}

func TestIndividualTransform3D(t *testing.T) {
	c := compute3D(t, `div { translate: 1px 2px 3px; rotate: 1 1 0 45deg; scale: 1 2 3 }`)
	if len(c.Translate) != 1 || c.Translate[0].Fn != "translate3d" || c.Translate[0].A[2].V != 3 {
		t.Errorf("translate %+v", c.Translate)
	}
	if len(c.Rotate) != 1 || c.Rotate[0].Fn != "rotate3d" || c.Rotate[0].A[0].V != 1 || c.Rotate[0].A[3].V != 45 {
		t.Errorf("rotate %+v", c.Rotate)
	}
	if len(c.Scale) != 1 || c.Scale[0].Fn != "scale3d" || c.Scale[0].A[2].V != 3 {
		t.Errorf("scale %+v", c.Scale)
	}
	c = compute3D(t, `div { rotate: x 30deg }`)
	if len(c.Rotate) != 1 || c.Rotate[0].Fn != "rotate3d" || c.Rotate[0].A[0].V != 1 || c.Rotate[0].A[1].V != 0 {
		t.Errorf("rotate x %+v", c.Rotate)
	}
	c = compute3D(t, `div { rotate: 0 1 0 0.5turn }`)
	if len(c.Rotate) != 1 || c.Rotate[0].A[1].V != 1 || c.Rotate[0].A[3].V != 180 {
		t.Errorf("rotate axis %+v", c.Rotate)
	}
	c = compute3D(t, `div { rotate: 90deg }`)
	if len(c.Rotate) != 1 || c.Rotate[0].Fn != "rotate" {
		t.Errorf("rotate %+v", c.Rotate)
	}
}
