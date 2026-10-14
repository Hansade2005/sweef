package engine

import "testing"

// navigator.userAgentData exists (feature detection) and is truthful: the brand is the browser's own.
func TestUserAgentData(t *testing.T) {
	tab := platformTab(t)
	runJSCases(t, tab, []jsCase{
		{"shape", `return [typeof navigator.userAgentData, Object.prototype.toString.call(navigator.userAgentData), typeof navigator.userAgentData.getHighEntropyValues].join()`, `"object,[object NavigatorUAData],function"`},
		{"brands are not Chromium", `const b = navigator.userAgentData.brands; return JSON.stringify([b.length >= 1, b.some(x => /chrom/i.test(x.brand)), Object.isFrozen(b)])`, `"[true,false,true]"`},
		{"mobile and platform", `const d = navigator.userAgentData; return JSON.stringify([d.mobile, ['Linux','Windows','macOS','Android','Unknown'].includes(d.platform)])`, `"[false,true]"`},
		{"high entropy", `const v = await navigator.userAgentData.getHighEntropyValues(['architecture','bitness','platformVersion','fullVersionList','nope']); return JSON.stringify([v.bitness, typeof v.architecture, 'nope' in v, Array.isArray(v.fullVersionList), v.wow64 === undefined])`, `"[\"64\",\"string\",false,true,true]"`},
		{"toJSON", `return JSON.stringify(Object.keys(navigator.userAgentData.toJSON()))`, `"[\"brands\",\"mobile\",\"platform\"]"`},
		{"bad hints", `try { await navigator.userAgentData.getHighEntropyValues(); } catch (e) { return e.name } return 'resolved'`, `"TypeError"`},
	})
}
