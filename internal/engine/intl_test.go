package engine

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestIntlGolden evaluates a few hundred Intl expressions in the engine and compares them with what Node 22 (ICU 77,
// CLDR 47) gives (tools/intlgen/golden.js writes testdata/intl_golden.json). Chromium turns the narrow no-break space
// ICU 72+ puts before AM/PM into a plain space; so does the engine, and so is the expectation.
func TestIntlGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/intl_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		E string `json:"e"`
		V any    `json:"v"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	// Known differences from Node, each a documented limit (docs/web-platform.md, Intl): the expressions marked "dictionary" segment Thai,
	// Chinese, Japanese, Lao, Khmer and Burmese text into words, which needs ICU's break dictionaries (93 KB to 1.6 MB gzipped each,
	// measured by tools/intlgen/icudict.js) that the engine does not ship; it cuts such text where UAX #29 does.
	isKnown := func(e string) bool { return strings.Contains(e, "/* dictionary */") }
	tab := platformTab(t)
	var cases []jsCase
	for _, g := range golden {
		if isKnown(g.E) {
			continue
		}
		want, _ := json.Marshal(g.V)
		w := string(want)
		if strings.Contains(g.E, "DateTimeFormat") || strings.Contains(g.E, "Date(") {
			w = strings.ReplaceAll(strings.ReplaceAll(w, "\u202f", " "), `\u202f`, " ")
		}
		cases = append(cases, jsCase{g.E, "return " + g.E + ";", w})
	}
	if len(cases) < 300 {
		t.Fatalf("only %d golden cases", len(cases))
	}
	runJSCases(t, tab, cases)
}

func TestIntlBasics(t *testing.T) {
	tab := platformTab(t)
	runJSCases(t, tab, []jsCase{
		{"shape", `return ['NumberFormat','DateTimeFormat','PluralRules','RelativeTimeFormat','ListFormat','DisplayNames','Collator','Segmenter','Locale','DurationFormat','getCanonicalLocales','supportedValuesOf'].map(k => typeof Intl[k]);`, `["function","function","function","function","function","function","function","function","function","function","function","function"]`},
		{"toLocale methods are cached and consistent", `const a = (12345.678).toLocaleString('de'); const b = (12345.678).toLocaleString('de'); return [a, a === b, new Date(0).toLocaleDateString('en-US', {timeZone: 'UTC'}), ['b','a'].toLocaleString('en')];`, `["12.345,678",true,"1/1/1970","b,a"]`},
		{"local time zone resolves", `const z = new Intl.DateTimeFormat().resolvedOptions().timeZone; return [typeof z, z.length > 0, Intl.supportedValuesOf('timeZone').includes('Europe/Paris')];`, `["string",true,true]`},
		{"duration format", `return new Intl.DurationFormat('en', {style:'long'}).format({hours: 1, minutes: 2, seconds: 3});`, `"1 hour, 2 minutes, 3 seconds"`},
		{"duration format digital", `return new Intl.DurationFormat('en', {style:'digital'}).format({hours: 1, minutes: 2, seconds: 3});`, `"1:02:03"`},
	})
}

func TestIntlInWorker(t *testing.T) {
	tab := platformTab(t)
	runJSCases(t, tab, []jsCase{
		{"worker has Intl", `const w = new Worker(URL.createObjectURL(new Blob(["postMessage([new Intl.NumberFormat('de').format(1234.5), new Intl.PluralRules('en').select(1), (5).toLocaleString('fr')])"]))); const r = await new Promise(res => { w.onmessage = e => res(e.data); setTimeout(() => res('timeout'), 3000); }); w.terminate(); return r;`, `["1.234,5","one","5"]`},
	})
}
