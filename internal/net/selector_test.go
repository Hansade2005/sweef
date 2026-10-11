package net

import "testing"

func TestSelectorEngine(t *testing.T) {
	src := `<html><body>
<div id="a" class="x y" data-k="v1-foo"><p>one</p><p class="z">two</p><span>s</span></div>
<ul><li>1</li><li class="sel">2</li><li>3</li><li>4</li></ul>
<form><input type="text" name="q" required><input type="checkbox" checked disabled><select><option>a</option><option selected>b</option></select></form>
<a href="/x">link</a><a>nolink</a>
</body></html>`
	d, _ := ParseDocument(src, "https://t.test/")
	cases := map[string]int{
		"div#a p":                           2,
		"div > p":                           2,
		"p + p":                             1,
		"p ~ span":                          1,
		"[data-k^=v1]":                      1,
		"[data-k$=foo]":                     1,
		"[data-k*='-f']":                    1,
		"[data-k|=v1]":                      1,
		"[class~=y]":                        1,
		"[CLASS~=Y i]":                      1,
		"li:nth-child(2n)":                  2,
		"li:nth-child(odd)":                 2,
		"li:first-child":                    1,
		"li:last-child":                     1,
		"li:nth-last-child(1)":              1,
		"li:not(.sel)":                      3,
		"li:not(:first-child, :last-child)": 2,
		"ul:has(> li.sel)":                  1,
		"div:has(span)":                     1,
		"input:checked":                     1,
		"input:disabled":                    1,
		"input:enabled":                     1,
		"input:required":                    1,
		"a:any-link":                        1,
		"a:link":                            1,
		":is(p, span).z":                    1,
		"p:first-of-type":                   1,
		"span:only-of-type":                 1,
		"body > *":                          5,
		"li:empty":                          0,
		"option:checked":                    1,
		"html:root":                         1,
		"input[type=\"text\"][name=q]":      1,
	}
	for sel, want := range cases {
		if got := len(d.queryAll(sel)); got != want {
			t.Errorf("%s: got %d want %d", sel, got, want)
		}
	}
	if _, err := compileSelector("div >"); err == nil {
		t.Error("expected syntax error")
	}
	if _, err := compileSelector("[foo"); err == nil {
		t.Error("expected syntax error for unclosed attr")
	}
	ids, err := d.QueryAllFrom(d.ElementByID("a"), ":scope > p")
	if err != nil || len(ids) != 2 {
		t.Errorf("scoped: %v %v", ids, err)
	}
}
