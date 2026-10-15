package render

import (
	"reflect"
	"testing"

	"github.com/sweefbrowser/sweef/internal/style"
)

// inhDeclsFor (the inherited-property index) must give the inherited declarations declsFor gives, in order;
// needsForeignIDs must see references to ids of other svg elements, in attributes and in cascaded properties.
func TestSVGCascadeInheritedIndexAndForeignIDs(t *testing.T) {
	css := `.box .wrap { fill: red; color: blue } .wrap { stroke: green; margin: 0 } #w { fill-opacity: .5 }
	.wrap span { fill: black } .grad path { fill: url(#g) } div { stroke-width: 3 }`
	doc := parseHTML(t, `<body><div class="box"><p class="wrap" id="w" style="stroke-linecap: round"><svg id="a"><path d="M0 0h1"/></svg></p></div>
<div class="grad"><svg id="b"><path d="M0 0h1"/></svg></div>
<svg id="c"><defs><linearGradient id="g"/></defs><path fill="url(#g)" d="M0 0h1"/></svg>
<svg id="d"><use href="#g"/></svg></body>`)
	sc := newSVGCascade([]Sheet{{Text: css}}, &style.Env{Width: 800, Height: 600}, nil)
	for _, id := range []string{"w", "a"} {
		n := findByID(doc, id)
		for a := n; a != nil && a.Parent != nil; a = a.Parent {
			var want []style.Decl
			for _, d := range sc.declsFor(a) {
				if svgInheritedProps[d.Name] {
					want = append(want, d)
				}
			}
			if got := sc.inhDeclsFor(a); !reflect.DeepEqual(got, want) {
				t.Fatalf("<%s>: inhDeclsFor %v, declsFor %v", a.Data, got, want)
			}
		}
	}
	for id, want := range map[string]bool{"a": false, "b": true, "c": false, "d": true} {
		d := buildSVGScene(findByID(doc, id), sc)
		if d == nil {
			t.Fatalf("%s: no scene", id)
		}
		if got := d.needsForeignIDs(); got != want {
			t.Errorf("svg %s: needsForeignIDs %v, want %v", id, got, want)
		}
	}
}
