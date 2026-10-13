package engine

import (
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

func TestLeadingImports(t *testing.T) {
	rules, rest := leadingImports(`@charset "utf-8"; /* c */ @import url("a.css") screen and (min-width:1px);
@IMPORT 'b.css';  @import url(c.css) layer(base) supports(display:grid) print;
@import url("https://fonts.googleapis.com/css2?family=Inter:wght@400;700&display=swap");
body{margin:0}@import url(late.css);`)
	want := []importRule{{"a.css", "screen and (min-width:1px)"}, {"b.css", ""}, {"c.css", "print"}, {"https://fonts.googleapis.com/css2?family=Inter:wght@400;700&display=swap", ""}}
	if len(rules) != len(want) {
		t.Fatalf("rules %+v", rules)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d: %+v want %+v", i, rules[i], want[i])
		}
	}
	if !strings.HasPrefix(rest, "body{margin:0}") {
		t.Errorf("rest %q", rest)
	}
	if r, _ := leadingImports("a{color:red}"); len(r) != 0 {
		t.Errorf("no imports expected: %v", r)
	}
}

func TestImportedSheetsApplyInOrderAndDeclareFonts(t *testing.T) {
	s := newFontSite(t)
	s.pages["/"] = `<!doctype html><html><head><style>@import url("/a.css"); @import "/loop.css";
#s{font-family:Boxy,serif;font-size:100px}</style></head><body style="margin:0"><span id="s">Hello</span><p id="p">x</p></body></html>`
	s.pages["/a.css"] = `@import url(/b.css);
@font-face{font-family:Boxy;src:url(/boxy.woff2)}
#p{color:rgb(1,2,3);margin:0;height:11px}`
	s.pages["/b.css"] = `#p{height:22px;display:block}`
	s.pages["/loop.css"] = `@import "/loop.css"; #p{width:33px}`
	s.files["/boxy.woff2"] = fontFile{data: fonttest.BoxesWOFF2}
	_, tab, got := loadWidth(t, s, DefaultOptions())
	if want := boxesWidth("Hello", 100, false); !within(got, want, 1) {
		t.Fatalf("a font declared by an imported sheet: width %.1f want %.1f (%v)", got, want, s.requested())
	}
	b, err := tab.Locator("#p").BoundingBox()
	if err != nil || b[3] != 11 || b[2] != 33 {
		t.Fatalf("an importing sheet overrides what it imports: box %v err %v", b, err)
	}
	if s.count("/loop.css") != 1 {
		t.Fatalf("an import cycle is cut: %v", s.requested())
	}
}
