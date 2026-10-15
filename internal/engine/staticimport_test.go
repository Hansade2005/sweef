package engine

import (
	"math/rand"
	"reflect"
	"testing"
)

// staticImportSpecs must capture exactly what staticImportRE does.
func TestStaticImportSpecsMatchesRegexp(t *testing.T) {
	cases := []string{
		`import a from "./a.js"; import {b} from './b.js'; export * from "./c.js"; import "./d.css.js";`,
		`import(x); important stuff from 'x'; reimport from "y"; export default 1; export{a as b}from"z"`,
		"import\n  {\n a,\n b } from\n 'multi.js'\nconst s = \"import 'no' \"",
		`importfrom "q"; import x from'r' ; import y fromage 's'; export const f = () => import("./lazy.js")`,
		`import a from "unterminated` + "\n" + `"; import b from ""; import c from "./ok.js"`,
		`x.import = 1; y.export from "w"; _import from "v"; $import from "u"`,
	}
	r := rand.New(rand.NewSource(1))
	toks := []string{"import", "export", "from", " ", "\"", "'", "(", ")", ";", "=", "\n", "a", "./x.js", "{", "}", "*", "as", "_", "x", "\t"}
	for n := 0; n < 20000; n++ {
		var b []byte
		for i := 5 + r.Intn(30); i > 0; i-- {
			b = append(b, toks[r.Intn(len(toks))]...)
		}
		cases = append(cases, string(b))
	}
	for _, c := range cases {
		var want []string
		for _, m := range staticImportRE.FindAllStringSubmatch(c, -1) {
			want = append(want, m[1])
		}
		if got := staticImportSpecs(c); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q:\n got %q\nwant %q", c, got, want)
		}
	}
}
