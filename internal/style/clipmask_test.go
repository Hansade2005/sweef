package style

import "testing"

func computeOne(t *testing.T, css, inline string) *Computed {
	t.Helper()
	doc := parse(t, `<html><body><div id=x style="`+inline+`">x</div></body></html>`)
	e := NewEngine(&Env{Width: 1280, Height: 800}, []SheetIn{{Text: css}})
	hc := e.Compute(find(doc, "html"), nil, "")
	body := e.Compute(find(doc, "body"), hc, "")
	return e.Compute(find(doc, "div"), body, "")
}

func TestClipPathParse(t *testing.T) {
	cases := []struct {
		v    string
		kind string
		box  string
		url  string
	}{
		{"inset(10px 20px round 5px)", "inset", "", ""},
		{"circle(50% at 20% 30%)", "circle", "", ""},
		{"ellipse(40px 20px)", "ellipse", "", ""},
		{"polygon(evenodd, 0 0, 100% 0, 50% 100%)", "polygon", "", ""},
		{"path('M0 0 L10 10 Z')", "path", "", ""},
		{"padding-box", "", "padding-box", ""},
		{"circle(10px) content-box", "circle", "content-box", ""},
		{"margin-box inset(1px)", "inset", "margin-box", ""},
		{"url(#c)", "", "", "#c"},
		{"rect(auto 50% 50% auto round 4px)", "rect", "", ""},
		{"xywh(1px 2px 30px 40px)", "xywh", "", ""},
	}
	for _, c := range cases {
		st := computeOne(t, "", "clip-path:"+c.v)
		cp := st.ClipPathV
		if cp == nil || !st.ClipPath {
			t.Errorf("%s: not parsed", c.v)
			continue
		}
		k := ""
		if cp.Shape != nil {
			k = cp.Shape.Kind
		}
		if k != c.kind || cp.Box != c.box || cp.URL != c.url {
			t.Errorf("%s: got kind=%q box=%q url=%q", c.v, k, cp.Box, cp.URL)
		}
	}
	for _, bad := range []string{"circle(", "polygon()", "inset()", "foo(1px)", "url(#a) url(#b)", "circle(1px) inset(1px)"} {
		if st := computeOne(t, "", "clip-path:none;clip-path:"+bad); st.ClipPathV != nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if st := computeOne(t, "", "clip-path:circle(5px);clip-path:none"); st.ClipPathV != nil || st.ClipPath {
		t.Error("none did not reset")
	}
	sh := computeOne(t, "", "clip-path:inset(1px 2px 3px 4px round 5px 6px / 7px 8px)").ClipPathV.Shape
	if sh.Edges[0].V != 1 || sh.Edges[1].V != 2 || sh.Edges[2].V != 3 || sh.Edges[3].V != 4 {
		t.Errorf("edges %v", sh.Edges)
	}
	if !sh.HasRound || sh.Round[0][0].V != 5 || sh.Round[0][1].V != 7 || sh.Round[1][0].V != 6 || sh.Round[1][1].V != 8 {
		t.Errorf("round %v", sh.Round)
	}
}

func TestMaskParse(t *testing.T) {
	st := computeOne(t, "", "mask: linear-gradient(red, blue) 10px 20px / 30px 40px no-repeat content-box, url(#m) luminance")
	if !st.MaskImage || len(st.Mask) != 2 {
		t.Fatalf("layers %d image=%v", len(st.Mask), st.MaskImage)
	}
	a, b := st.Mask[0], st.Mask[1]
	if a.Image.Grad == nil || a.RepeatX != "no-repeat" || a.Origin != "content-box" || a.Clip != "content-box" || a.SizeW.V != 30 || a.SizeH.V != 40 {
		t.Errorf("layer0 %+v", a)
	}
	if b.Image.URL != "#m" || b.Mode != "luminance" || b.Origin != "border-box" || b.Composite != "add" {
		t.Errorf("layer1 %+v", b)
	}
	st = computeOne(t, "", "mask-image: linear-gradient(#000,#000), linear-gradient(#fff,#fff); mask-composite: subtract; mask-mode: alpha, luminance; mask-size: cover; -webkit-mask-clip: padding-box")
	if len(st.Mask) != 2 || st.Mask[0].Composite != "subtract" || st.Mask[1].Composite != "subtract" || st.Mask[1].Mode != "luminance" || st.Mask[0].SizeKind != "cover" || st.Mask[1].Clip != "padding-box" {
		t.Errorf("longhands %+v", st.Mask)
	}
	st = computeOne(t, "", "-webkit-mask-image: url(a.png); -webkit-mask-composite: xor; -webkit-mask-position-x: 5px")
	if len(st.Mask) != 1 || st.Mask[0].Composite != "exclude" || st.Mask[0].PosX.V != 5 {
		t.Errorf("webkit %+v", st.Mask)
	}
	// Chromium has no unprefixed mask-position-x / -y
	if Supports("mask-position-x", "5px") || !Supports("-webkit-mask-position-x", "5px") {
		t.Error("mask-position-x support")
	}
	if st := computeOne(t, "", "mask-image: linear-gradient(red, blue); mask: none"); st.MaskImage || len(st.Mask) != 0 {
		t.Error("mask:none")
	}
	if st := computeOne(t, "", "mask-image: none"); st.MaskImage {
		t.Error("mask-image:none sets MaskImage")
	}
}

func TestBoxDecorationBreak(t *testing.T) {
	if computeOne(t, "", "box-decoration-break: clone").BoxDecorationBreak != "clone" {
		t.Error("clone")
	}
	if computeOne(t, "", "-webkit-box-decoration-break: clone").BoxDecorationBreak != "clone" {
		t.Error("webkit clone")
	}
	if computeOne(t, "", "box-decoration-break: bogus").BoxDecorationBreak != "" {
		t.Error("bogus")
	}
}
