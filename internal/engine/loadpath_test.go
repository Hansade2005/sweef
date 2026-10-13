package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests of the page-load path: bare window methods, about:blank, detached
// images, duplicate requests, progressive loading.

func TestBareWindowMethodsAreCallableAsGlobals(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body><script>
var log = [];
function tryit(name, f) { try { var r = f(); log.push(name + ':ok'); return r; } catch (e) { log.push(name + ':' + e.name + ':' + e.message); } }
var loaded = 0, custom = 0;
tryit('addEventListener', function () { addEventListener('load', function () { loaded++; }); });
tryit('addEventListener-opts', function () { addEventListener('load', function () { loaded += 10; }, { once: true }); });
var h = function () { custom++; };
tryit('addEventListener-custom', function () { addEventListener('custom', h); });
tryit('dispatchEvent', function () { return dispatchEvent(new Event('custom')); });
tryit('removeEventListener', function () { removeEventListener('custom', h); });
tryit('dispatchEvent-removed', function () { dispatchEvent(new Event('custom')); });
tryit('setTimeout', function () { setTimeout(function () { window.timerRan = true; }, 0); });
tryit('clearTimeout', function () { clearTimeout(setTimeout(function () {}, 1000)); });
tryit('setInterval', function () { clearInterval(setInterval(function () {}, 1000)); });
tryit('requestAnimationFrame', function () { cancelAnimationFrame(requestAnimationFrame(function () {})); });
tryit('queueMicrotask', function () { queueMicrotask(function () { window.microRan = true; }); });
tryit('getComputedStyle', function () { return getComputedStyle(document.body); });
tryit('matchMedia', function () { return matchMedia('(min-width: 1px)').matches; });
tryit('postMessage', function () { postMessage('x', '*'); });
tryit('scrollTo', function () { scrollTo(0, 0); });
tryit('scrollBy', function () { scrollBy(0, 0); });
tryit('atob', function () { return atob(btoa('x')); });
tryit('fetch', function () { return typeof fetch; });
tryit('structuredClone', function () { return structuredClone({ a: 1 }); });
tryit('alert', function () { alert('x'); });
tryit('getSelection', function () { return getSelection(); });
tryit('focus', function () { focus(); blur(); });
tryit('open', function () { return typeof open; });
tryit('requestIdleCallback', function () { return requestIdleCallback(function () {}); });
tryit('reportError', function () { return typeof reportError; });
</script></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newJSTab(t, &httptest.Server{URL: srv.URL}, "/")
	tab.Settle(200 * time.Millisecond)
	got := mustEval(t, tab, `log.join('\n')`)
	var bad []string
	for _, l := range strings.Split(strings.Trim(got, `"`), `\n`) {
		if !strings.HasSuffix(l, ":ok") {
			bad = append(bad, l)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("bare window methods that threw:\n%s", strings.Join(bad, "\n"))
	}
	if v := mustEval(t, tab, `loaded`); v != "11" {
		t.Fatalf("load listeners registered through the bare global: loaded=%s", v)
	}
	if v := mustEval(t, tab, `custom`); v != "1" {
		t.Fatalf("dispatchEvent/removeEventListener as globals: custom=%s", v)
	}
	if v := mustEval(t, tab, `[window.timerRan, window.microRan].join()`); v != `"true,true"` {
		t.Fatalf("timers/microtasks: %s", v)
	}
}

var _ = sync.Mutex{}
var _ atomic.Int32
