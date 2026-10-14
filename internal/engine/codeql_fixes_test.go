package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Ids and names with backslashes, quotes or line breaks must still match when the prelude
// builds an attribute selector for them.
func TestSelectorStringEscaping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><body>x</body>`))
	}))
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	var got []bool
	evalJSON(t, tab, `(function(){var out=[];['a\\b','q"x','n\nl','plain'].forEach(function(id){
var f=document.createDocumentFragment(),d=document.createElement('div');d.id=id;f.appendChild(d);
out.push(f.getElementById(id)===d);
var host=document.createElement('div');var r=document.createElement('input');r.type='radio';r.name=id;host.appendChild(r);
var l=document.createElement('label');l.htmlFor=id;var i=document.createElement('input');i.id=id;host.appendChild(l);host.appendChild(i);document.body.appendChild(host);
out.push(i.labels.length===1);host.remove();});return out})()`, &got)
	for i, ok := range got {
		if !ok {
			t.Errorf("case %d did not match: %v", i, got)
		}
	}
	if len(got) != 8 {
		t.Fatalf("results %v", got)
	}
}

func TestSafeFileName(t *testing.T) {
	for in, want := range map[string]string{"report.pdf": "report.pdf", "../../etc/passwd": "passwd", `..\..\x.exe`: "x.exe", "..": "download", "": "download", "a:b?.txt": "a_b_.txt"} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsScriptURL(t *testing.T) {
	for in, want := range map[string]bool{"javascript:x()": true, " JavaScript:x": true, "vbscript:msgbox": true, "\tVBScript:x": true, "https://a/javascript:": false, "java": false, "/p": false} {
		if got := isScriptURL(in); got != want {
			t.Errorf("isScriptURL(%q) = %v", in, got)
		}
	}
	if !hasScheme("DATA:text/html,x", "data") || hasScheme("database", "data") {
		t.Error("hasScheme data")
	}
}
