package engine

import "testing"

// btoa and atob run in Go (b64host.go); they must keep the HTML spec's forgiving-base64 behaviour.
func TestBtoaAtob(t *testing.T) {
	tab := platformTab(t)
	runJSCases(t, tab, []jsCase{
		{"encode", `return [btoa(''), btoa('hello'), btoa('hi'), btoa('h'), btoa('\xff\x00\xe9'), btoa(String.fromCharCode(0,1,2,254,255))].join('|')`, `"|aGVsbG8=|aGk=|aA==|/wDp|AAEC/v8="`},
		{"encode outside latin1", `try { btoa('€'); } catch (e) { return e.name } return 'ok'`, `"InvalidCharacterError"`},
		{"encode coerces", `return btoa(12) + '|' + btoa(null)`, `"MTI=|bnVsbA=="`},
		{"decode", `return [atob('aGVsbG8='), atob('aGVsbG8'), atob(' aGVs\n bG8= '), atob(''), atob('aGk='), atob('aGk')].join('|')`, `"hello|hello|hello||hi|hi"`},
		{"decode bytes", `const s = atob('/wDp'); return [s.length, s.charCodeAt(0), s.charCodeAt(1), s.charCodeAt(2)].join()`, `"3,255,0,233"`},
		{"decode errors", `const r = []; for (const bad of ['a', 'aGVsbG8===', '=a', '!!!!', 'aG=sbG8=', 'a$b']) { try { atob(bad); r.push('ok:' + bad); } catch (e) { r.push(e.name); } } return r.join()`, `"InvalidCharacterError,InvalidCharacterError,InvalidCharacterError,InvalidCharacterError,InvalidCharacterError,InvalidCharacterError"`},
		{"arguments", `const r = []; try { btoa(); } catch (e) { r.push(e.name); } try { atob(); } catch (e) { r.push(e.name); } return r.join()`, `"TypeError,TypeError"`},
		{"round trip a megabyte", `const u = new Uint8Array(1 << 20); for (let i = 0; i < u.length; i++) u[i] = i * 7; let s = ''; for (let i = 0; i < u.length; i += 0x8000) s += String.fromCharCode.apply(null, u.subarray(i, i + 0x8000)); const t0 = Date.now(); const back = atob(btoa(s)); const ms = Date.now() - t0; return [back === s, ms < 500].join()`, `"true,true"`},
	})
}
