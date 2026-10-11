package engine

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestWebGLDebug evaluates WEBGL_EVAL on a fixture (WEBGL_DEBUG=name) and
// writes the string result to WEBGL_EVAL_OUT; used while developing.
func TestWebGLDebug(t *testing.T) {
	name := os.Getenv("WEBGL_DEBUG")
	if name == "" {
		t.Skip()
	}
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata/webgl")))
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, _ := e.NewTab(srv.URL + "/" + name + ".html")
	if ex := os.Getenv("WEBGL_EVAL"); ex != "" {
		out := evalStr(t, tab, ex)
		if f := os.Getenv("WEBGL_EVAL_OUT"); f != "" {
			_ = os.WriteFile(f, []byte(out), 0o644)
		} else {
			t.Log(out)
		}
		return
	}
	t.Logf("error=%s\nresult=%s\nconsole=%s", evalStr(t, tab, `String(window.__error||"")`), evalStr(t, tab, `String(window.__result||"")`), consoleText(tab))
}
