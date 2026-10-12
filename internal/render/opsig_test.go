package render

import (
	"image"
	"reflect"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
)

// deepCopy clones structs, arrays, slices and pointers-to-struct reachable from v.
func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Struct:
		o := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				o.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
		return o
	case reflect.Array:
		o := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			o.Index(i).Set(deepCopy(v.Index(i)))
		}
		return o
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		o := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			o.Index(i).Set(deepCopy(v.Index(i)))
		}
		return o
	case reflect.Ptr:
		if v.IsNil() {
			return v
		}
		if v.Elem().Kind() == reflect.Struct && v.Type() != reflect.TypeOf((*font.Face)(nil)) && v.Type() != reflect.TypeOf((*SVGDoc)(nil)) {
			o := reflect.New(v.Type().Elem())
			o.Elem().Set(deepCopy(v.Elem()))
			return o
		}
		return v
	}
	return v
}

type leaf struct {
	path string
	set  func(root reflect.Value) // mutates a deep copy
}

func leaves(t reflect.Type, get func(root reflect.Value) reflect.Value, path string, out *[]leaf) {
	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			i := i
			leaves(f.Type, func(r reflect.Value) reflect.Value { return get(r).Field(i) }, path+"."+f.Name, out)
		}
	case reflect.Array:
		for i := 0; i < t.Len(); i++ {
			i := i
			leaves(t.Elem(), func(r reflect.Value) reflect.Value { return get(r).Index(i) }, path+"[]", out)
		}
	case reflect.Slice:
		// the samples carry two elements; mutate each, and test length via a separate leaf
		for i := 0; i < 2; i++ {
			i := i
			leaves(t.Elem(), func(r reflect.Value) reflect.Value { return get(r).Index(i) }, path+"[]", out)
		}
		*out = append(*out, leaf{path + ".len", func(r reflect.Value) {
			s := get(r)
			s.Set(s.Slice(0, 1))
		}})
	case reflect.Ptr:
		if t.Elem().Kind() == reflect.Struct && t != reflect.TypeOf((*font.Face)(nil)) && t != reflect.TypeOf((*SVGDoc)(nil)) {
			leaves(t.Elem(), func(r reflect.Value) reflect.Value { return get(r).Elem() }, path+"->", out)
		} else {
			*out = append(*out, leaf{path, func(r reflect.Value) {
				n := reflect.New(t.Elem())
				if t == reflect.TypeOf((*SVGDoc)(nil)) {
					n.Interface().(*SVGDoc).Root = &SVGNode{Tag: "other"}
				}
				get(r).Set(n)
			}})
		}
	case reflect.Interface:
		if t == reflect.TypeOf((*Op)(nil)).Elem() {
			*out = append(*out, leaf{path, func(r reflect.Value) { get(r).Set(reflect.ValueOf(FillRect{Rect: Rect{9, 9, 9, 9}})) }})
		} else {
			*out = append(*out, leaf{path, func(r reflect.Value) { get(r).Set(reflect.ValueOf(image.NewRGBA(image.Rect(0, 0, 3, 3)))) }})
		}
	default:
		*out = append(*out, leaf{path, func(r reflect.Value) {
			f := get(r)
			switch f.Kind() {
			case reflect.Float64:
				f.SetFloat(f.Float() + 1.25)
			case reflect.Int, reflect.Int32, reflect.Int64:
				f.SetInt(f.Int() + 1)
			case reflect.Uint8, reflect.Uint32:
				f.SetUint(f.Uint() + 1)
			case reflect.String:
				f.SetString(f.String() + "x")
			case reflect.Bool:
				f.SetBool(!f.Bool())
			default:
				panic("kind " + f.Kind().String())
			}
		}})
	}
}

func sampleOps() []Op {
	face := &font.Face{}
	tile := &Tile{Origin: [2]float64{1, 2}, Size: [2]float64{3, 4}, Step: [2]float64{5, 6}}
	rad := Radii{{1, 2}, {3, 4}, {5, 6}, {7, 8}}
	stops := []GradientStop{{0, Color{1, 2, 3, 4}}, {1, Color{5, 6, 7, 8}}}
	doc := &SVGDoc{Root: &SVGNode{Tag: "svg", A: map[string]string{"a": "b"}}, ByID: map[string]*SVGNode{}}
	return []Op{
		FillRect{Rect: Rect{1, 2, 3, 4}, Color: Color{1, 2, 3, 4}, Radii: rad},
		Border{Rect: Rect{1, 2, 3, 4}, Widths: [4]float64{1, 2, 3, 4}, Colors: [4]Color{{1, 2, 3, 4}, {1, 2, 3, 5}, {1, 2, 3, 6}, {1, 2, 3, 7}}, Styles: [4]string{"solid", "dashed", "dotted", "double"}, Radii: rad},
		TextRun{X: 1, Y: 2, Size: 3, Color: Color{1, 2, 3, 4}, Glyphs: []Glyph{{R: 'a', X: 1, Face: face, GID: 2}, {R: 'b', X: 2, Face: face}}, Width: 9, DecoStyle: "solid", DecoColor: Color{9, 9, 9, 9},
			Shadows: []TextShadow{{1, 2, 3, Color{1, 2, 3, 4}}, {4, 5, 6, Color{4, 5, 6, 7}}}, Stroke: 1, StrokeColor: Color{3, 3, 3, 3}},
		Image{Img: image.NewRGBA(image.Rect(0, 0, 2, 2)), Dst: Rect{1, 2, 3, 4}, Src: Rect{5, 6, 7, 8}, Radii: rad, Smooth: true, Tile: tile},
		Gradient{Rect: Rect{1, 2, 3, 4}, Kind: "linear", Radii: rad, Stops: stops, Angle: 3, CX: 1, CY: 2, RX: 3, RY: 4, FromAngle: 5, Tile: tile},
		BoxShadow{Rect: Rect{1, 2, 3, 4}, Radii: rad, DX: 1, DY: 2, Blur: 3, Spread: 4, Color: Color{1, 2, 3, 4}},
		PushClip{Rect: Rect{1, 2, 3, 4}, Radii: rad},
		PushLayer{Opacity: 0.5, Blend: "multiply", Filter: []Filter{{Name: "blur", Args: []float64{1, 2}, Color: Color{1, 2, 3, 4}}, {Name: "x", Args: []float64{3, 4}}}, Bounds: Rect{1, 2, 3, 4}},
		PushTransform{M: [6]float64{1, 2, 3, 4, 5, 6}, Lattice: true, LatX: 1, LatY: 2},
		Scrollbar{Rect: Rect{1, 2, 3, 4}, Vertical: true, ThumbOff: 1, ThumbLen: 2, Dark: true},
		ScrollCorner{Rect: Rect{1, 2, 3, 4}, Dark: true},
		SVGDraw{Dst: Rect{1, 2, 3, 4}, Doc: doc, Color: Color{1, 2, 3, 4}, FontSize: 3, Family: "serif", Weight: 400, Italic: true},
		PushMask{Bounds: Rect{1, 2, 3, 4}, Layers: []MaskLayerOps{{Ops: []Op{FillRect{Rect: Rect{1, 1, 1, 1}}, FillRect{Rect: Rect{2, 2, 2, 2}}}, Mode: "alpha", Composite: "add"}, {Mode: "luminance", Composite: "subtract", Ops: []Op{FillRect{Rect: Rect{3, 3, 3, 3}}, FillRect{Rect: Rect{4, 4, 4, 4}}}}}},
		PopClip{}, PopLayer{}, PopTransform{}, PopMask{},
	}
}

// Every field of every op must feed its signature: a missing field would
// leave stale pixels in an incremental repaint.
func TestOpSigCoversEveryField(t *testing.T) {
	for _, op := range sampleOps() {
		base, ok := OpSig(op)
		if !ok {
			t.Fatalf("%T: not signable", op)
		}
		if b2, _ := OpSig(op); b2 != base {
			t.Fatalf("%T: unstable signature", op)
		}
		typ := reflect.TypeOf(op)
		var ls []leaf
		leaves(typ, func(r reflect.Value) reflect.Value { return r }, typ.Name(), &ls)
		t.Logf("%s: %d leaves", typ.Name(), len(ls))
		for _, l := range ls {
			cp := reflect.New(typ).Elem()
			cp.Set(deepCopy(reflect.ValueOf(op)))
			l.set(cp)
			got, ok := OpSig(cp.Interface().(Op))
			if !ok {
				t.Errorf("%s: mutated op not signable", l.path)
				continue
			}
			if got == base {
				t.Errorf("%s: field not covered by OpSig", l.path)
			}
		}
	}
}

func TestOpSigUnknownOps(t *testing.T) {
	for _, op := range []Op{Bitmap{}, BackdropFilter{}, PushClipPath{}, PopClipPath{}, PushTransform3D{}, PopTransform3D{}} {
		if _, ok := OpSig(op); ok {
			t.Errorf("%T must not be signable", op)
		}
	}
}

func TestSVGDocSigCoversContent(t *testing.T) {
	mk := func() *SVGDoc {
		k := &SVGNode{Tag: "path", A: map[string]string{"d": "M0 0"}}
		return &SVGDoc{Root: &SVGNode{Tag: "svg", A: map[string]string{"viewBox": "0 0 1 1"}, Kids: []*SVGNode{k}, Text: "t"}, ByID: map[string]*SVGNode{"i": k},
			Inherit: map[string]string{"fill": "red"}, IntrW: 1, IntrH: 2, Ratio: 3, Base: "b", Src: "s"}
	}
	base, ok := mk().sig()
	if !ok {
		t.Fatal("not signable")
	}
	if b2, _ := mk().sig(); b2 != base {
		t.Fatal("equal docs differ")
	}
	muts := map[string]func(d *SVGDoc){
		"root attr": func(d *SVGDoc) { d.Root.A["viewBox"] = "0 0 2 2" },
		"kid attr":  func(d *SVGDoc) { d.Root.Kids[0].A["d"] = "M1 1" },
		"kid tag":   func(d *SVGDoc) { d.Root.Kids[0].Tag = "rect" },
		"text":      func(d *SVGDoc) { d.Root.Text = "u" },
		"byid":      func(d *SVGDoc) { d.ByID["j"] = &SVGNode{Tag: "g"} },
		"inherit":   func(d *SVGDoc) { d.Inherit["fill"] = "blue" },
		"intr":      func(d *SVGDoc) { d.IntrW = 5 },
		"ratio":     func(d *SVGDoc) { d.Ratio = 5 },
		"base":      func(d *SVGDoc) { d.Base = "c" },
		"src":       func(d *SVGDoc) { d.Src = "z" },
		"media":     func(d *SVGDoc) { d.HasMedia = true },
		"img":       func(d *SVGDoc) { d.Root.Img = image.NewRGBA(image.Rect(0, 0, 1, 1)) },
		"subid":     func(d *SVGDoc) { d.Root.SubID = "q" },
		"ops":       func(d *SVGDoc) { d.Root.Ops = []Op{FillRect{}} },
	}
	for name, m := range muts {
		d := mk()
		m(d)
		if s, ok := d.sig(); !ok || s == base {
			t.Errorf("%s: not covered", name)
		}
	}
}
