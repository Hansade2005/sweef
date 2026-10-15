//go:build !(386 || arm)

package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Modules and classic scripts compiled by the precompiler run from their bytecode: the module loader reads
// it (relative imports, import.meta.url and shared module identity intact), and a syntax error in a module
// the precompiler could not compile is still reported by the page's own compile.
func TestPrecompiledModulesAndScriptsRun(t *testing.T) {
	pad := "\n//" + strings.Repeat("x", precompileMin) + "\n"
	files := map[string]string{
		"/m/a.js":    `import {b} from './b.js'; import {b as b2} from '/m/b.js'; export const a = 'a' + b + (b === b2) + new URL(import.meta.url).pathname;` + pad,
		"/m/b.js":    `globalThis.bRuns = (globalThis.bRuns || 0) + 1; export const b = 'b';` + pad,
		"/m/main.js": `import {a} from './a.js'; document.title = a + ':' + globalThis.bRuns;` + pad,
		"/s.js":      `window.classic = 'c';` + pad,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if src, ok := files[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, src)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><script src="/s.js"></script><script type=module src="/m/main.js"></script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	pc := tab.eng.pc
	for p, src := range files {
		if strings.HasPrefix(p, "/m/") {
			pc.submitModule(srv.URL+p, src)
		} else {
			pc.submitClassic(src)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		pc.mu.Lock()
		n := len(pc.inflight)
		pc.mu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r0 := bcLoaderReads.Load()
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	if got := mustEval(t, tab, `document.title + '|' + window.classic`); got != `"abtrue/m/a.js:1|c"` {
		t.Fatalf("page state %s", got)
	}
	if bcLoaderReads.Load()-r0 < 2 {
		t.Errorf("the loader read %d modules from bytecode, want a.js and b.js", bcLoaderReads.Load()-r0)
	}
}
