package engine

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="#ff0000"/></svg>`

// A fetched .svg paints through <img>, and drawImage() of it works on canvas.
func TestSVGImageAndCanvasDrawImage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/r.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(testSVG))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body style="margin:0"><img id="i" src="/r.svg" style="display:block"><canvas id="c" width="40" height="40" style="display:block"></canvas></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	var w float64
	evalJSON(t, tab, `document.getElementById('i').getBoundingClientRect().width`, &w)
	if w != 40 {
		t.Fatalf("svg <img> should size to its intrinsic 40px, got %v", w)
	}
	img, err := png.Decode(bytes.NewReader(tab.PNGScreenshot(200, 200, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := img.At(20, 20).RGBA(); r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Fatalf("svg <img> not painted red: %d %d %d", r>>8, g>>8, b>>8)
	}
	var px string
	evalJSON(t, tab, `window.__p='';var im=new Image();im.onload=function(){var x=document.getElementById('c').getContext('2d');x.drawImage(im,0,0);var d=x.getImageData(20,20,1,1).data;window.__p=d[0]+','+d[1]+','+d[2]};im.src='/r.svg';true`, nil)
	tab.settle(1e9)
	evalJSON(t, tab, `window.__p`, &px)
	if px != "255,0,0" {
		t.Fatalf("canvas drawImage(svg) pixel = %q", px)
	}
}
