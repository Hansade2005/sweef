package net

import (
	"reflect"
	"testing"
)

func TestSplitChain(t *testing.T) {
	for in, want := range map[string][]string{
		"a":                             {"a"},
		"input[type=checkbox] >> nth=0": {"input[type=checkbox]", "nth=0"},
		`text="a >> b" >> nth=-1`:       {`text="a >> b"`, "nth=-1"},
		"[data-x='>>'] >> span":         {"[data-x='>>']", "span"},
		"a >>":                          {"a >>"},
	} {
		if got := splitChain(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitChain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveChain(t *testing.T) {
	src := `<html><body>
<form id="checkboxes"><input type="checkbox" id="c1"> checkbox 1<br><input type="checkbox" id="c2" checked> checkbox 2</form>
<ul><li id="l1">alpha <b>x</b></li><li id="l2" style="display:none">beta <b>y</b></li><li id="l3">gamma <b>z</b></li></ul>
<div class="card"><button id="b1">Buy</button></div><div class="card"><button id="b2">Buy</button></div>
</body></html>`
	d, _ := ParseDocument(src, "https://t.test/")
	ids := func(sel string) []string {
		infos, err := d.ResolveEngine(sel)
		if err != nil {
			return nil
		}
		var out []string
		for _, i := range infos {
			out = append(out, i.ID)
		}
		return out
	}
	for sel, want := range map[string][]string{
		"input[type=checkbox] >> nth=0":  {"c1"},
		"input[type=checkbox] >> nth=1":  {"c2"},
		"input[type=checkbox] >> nth=-1": {"c2"},
		"input[type=checkbox] >> nth=2":  nil,
		"li >> visible=true":             {"l1", "l3"},
		"li >> visible=false":            {"l2"},
		"li >> has-text=GAM":             {"l3"},
		"li >> b >> nth=1":               {""}, // the <b> in l2 (no id)
		".card >> nth=1 >> button":       {"b2"},
		".card >> text=Buy":              {"b1", "b2"},
		".card >> nth=-1 >> role=button": {"b2"},
		"#checkboxes >> input >> nth=0":  {"c1"},
		"ul >> button":                   nil,
	} {
		if got := ids(sel); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q want %q", sel, got, want)
		}
	}
}
