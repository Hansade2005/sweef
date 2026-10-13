package render

import (
	"fmt"
	"reflect"
)

// DiffResults compares two layout results of the same tree and returns a
// description of the first difference ("" when they are identical): document
// size, every box, and every display-list op (by content signature, falling
// back to deep comparison for ops without one). It backs SWEEF_LAYOUT_CHECK,
// which compares an incremental layout with a from-scratch one.
func DiffResults(a, b *Result) string {
	if a == nil || b == nil {
		if a == b {
			return ""
		}
		return "one result is nil"
	}
	if a.Width != b.Width || a.Height != b.Height || a.Viewport != b.Viewport {
		return fmt.Sprintf("size %vx%v %v vs %vx%v %v", a.Width, a.Height, a.Viewport, b.Width, b.Height, b.Viewport)
	}
	if len(a.Boxes) != len(b.Boxes) {
		return fmt.Sprintf("box count %d vs %d", len(a.Boxes), len(b.Boxes))
	}
	for n, x := range a.Boxes {
		y, ok := b.Boxes[n]
		if !ok {
			return fmt.Sprintf("box of <%s> missing", n.Data)
		}
		if !reflect.DeepEqual(x, y) {
			return fmt.Sprintf("box of <%s> differs: %+v vs %+v", n.Data, x, y)
		}
	}
	if len(a.List) != len(b.List) {
		return fmt.Sprintf("display list length %d vs %d", len(a.List), len(b.List))
	}
	for i := range a.List {
		oa, ob := normalizeRefs(a.List[i]), normalizeRefs(b.List[i])
		sa, oka := OpSig(oa)
		sb, okb := OpSig(ob)
		if oka && okb {
			if sa != sb {
				if x, ok := a.List[i].(SVGDraw); ok {
					y := b.List[i].(SVGDraw)
					rs1, _ := x.Doc.Root.sig(0)
					rs2, _ := y.Doc.Root.sig(0)
					return fmt.Sprintf("op %d SVGDraw doc differs: root %v vs %v, byid %d vs %d, inherit %v vs %v, base %q vs %q src %q %q  intr %v %v %v / %v %v %v", i, rs1, rs2, len(x.Doc.ByID), len(y.Doc.ByID), x.Doc.Inherit, y.Doc.Inherit, x.Doc.Base, y.Doc.Base, x.Doc.Src, y.Doc.Src, x.Doc.IntrW, x.Doc.IntrH, x.Doc.Ratio, y.Doc.IntrW, y.Doc.IntrH, y.Doc.Ratio)
				}
				return fmt.Sprintf("op %d differs: %#v vs %#v", i, a.List[i], b.List[i])
			}
			continue
		}
		if oka != okb || !reflect.DeepEqual(oa, ob) {
			return fmt.Sprintf("op %d differs: %#v vs %#v", i, a.List[i], b.List[i])
		}
	}
	return ""
}

// normalizeRefs replaces the per-pass key of an SVG filter reference
// ("svgref:<seq>") by what it resolves to, so two passes that reference the
// same filter compare equal.
func normalizeRefs(op Op) Op {
	l, ok := op.(PushLayer)
	if !ok {
		return op
	}
	var fs []Filter
	for i, f := range l.Filter {
		if !IsSVGFilterRef(f.Name) {
			continue
		}
		if fs == nil {
			fs = append([]Filter(nil), l.Filter...)
		}
		name := "svgref:?"
		if ref, ok := LookupSVGFilter(f.Name); ok && ref != nil {
			sig, _ := ref.Node.sig(0)
			name = fmt.Sprintf("svgref:%s:%x", ref.ID, sig)
		}
		fs[i].Name = name
	}
	if fs != nil {
		l.Filter = fs
		return l
	}
	return op
}
