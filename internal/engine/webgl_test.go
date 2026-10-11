package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestWebGLSmoke(t *testing.T) {
	_, tab := newTestTab(t)
	tab.SetDocument(`<!doctype html><canvas id=c width=64 height=64></canvas><script>
var c = document.getElementById('c');
var gl = c.getContext('webgl', {preserveDrawingBuffer:true});
window.res = [typeof gl, gl && gl.getParameter(gl.VERSION)].join('|');
gl.clearColor(1, 0.5, 0, 1); gl.clear(gl.COLOR_BUFFER_BIT);
var px = new Uint8Array(4); gl.readPixels(3, 3, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, px);
window.px = Array.prototype.join.call(px, ',');
window.err = gl.getError();
</script>`, "https://example.test/")
	if got := evalStr(t, tab, `window.res`); !strings.Contains(got, "WebGL 1.0") {
		t.Fatalf("res %q\n%s", got, consoleText(tab))
	}
	if got := evalStr(t, tab, `window.px+'|'+window.err`); got != "255,128,0,255|0" {
		t.Fatalf("px %q\n%s", got, consoleText(tab))
	}
}

// ---- Chromium comparison ----------------------------------------------------------

func glCmp(a, b *image.NRGBA, tol int) (within, exact float64, mae float64) {
	if a.Bounds() != b.Bounds() {
		return 0, 0, 255
	}
	n := a.Bounds().Dx() * a.Bounds().Dy()
	ok, ex := 0, 0
	var sum float64
	for i := 0; i < n; i++ {
		pa, pb := a.Pix[i*4:i*4+4], b.Pix[i*4:i*4+4]
		good, same := true, true
		for c := 0; c < 4; c++ {
			va, vb := int(pa[c]), int(pb[c])
			if c < 3 {
				va, vb = va*int(pa[3])/255, vb*int(pb[3])/255
			}
			d := va - vb
			if d < 0 {
				d = -d
			}
			sum += float64(d)
			if d > tol {
				good = false
			}
			if d != 0 {
				same = false
			}
		}
		if good {
			ok++
		}
		if same {
			ex++
		}
	}
	return float64(ok) / float64(n), float64(ex) / float64(n), sum / float64(n*4)
}

type glRef struct {
	Result string `json:"result"`
	Error  string `json:"error"`
}

func refreshWebGLRefs(t *testing.T, files []string) {
	ab, chrome := os.Getenv("AB"), os.Getenv("CHROME")
	if ab == "" || chrome == "" {
		t.Fatal("WEBGL_REFRESH needs AB and CHROME")
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(ab, append([]string{"--session", "webgl-agent-ref"}, args...)...)
		cmd.Env = append(os.Environ(), "AGENT_BROWSER_EXECUTABLE_PATH="+chrome)
		return cmd.CombinedOutput()
	}
	_ = os.MkdirAll("testdata/webgl/.ref", 0o755)
	for _, f := range files {
		abs, _ := filepath.Abs(f)
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		if out, err := run("open", "file://"+abs); err != nil {
			t.Fatalf("open %s: %v %s", name, err, out)
		}
		for i := 0; i < 40; i++ {
			w, _ := run("eval", `String(!window.__wait || !!window.__done)`)
			if strings.Contains(string(w), "true") {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		out, err := run("eval", `document.querySelector("canvas").toDataURL()`)
		if err != nil {
			t.Fatalf("eval %s: %v %s", name, err, out)
		}
		var u string
		if err := json.Unmarshal(bytes.TrimSpace(out), &u); err != nil {
			t.Fatalf("%s: %v: %.80s", name, err, out)
		}
		b, err := base64.StdEncoding.DecodeString(u[strings.IndexByte(u, ',')+1:])
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join("testdata/webgl/.ref", name+".png"), b, 0o644)
		var ref glRef
		for _, q := range []struct {
			expr string
			dst  *string
		}{{`String(window.__result||"")`, &ref.Result}, {`String(window.__error||"")`, &ref.Error}} {
			o, err := run("eval", q.expr)
			if err != nil {
				t.Fatalf("eval %s: %v", name, err)
			}
			_ = json.Unmarshal(bytes.TrimSpace(o), q.dst)
		}
		jb, _ := json.Marshal(ref)
		_ = os.WriteFile(filepath.Join("testdata/webgl/.ref", name+".json"), jb, 0o644)
	}
	_, _ = run("close")
}

func TestWebGLAgainstChromium(t *testing.T) {
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata/webgl")))
	defer srv.Close()
	files, _ := filepath.Glob("testdata/webgl/*.html")
	sort.Strings(files)
	if os.Getenv("WEBGL_REFRESH") != "" {
		refreshWebGLRefs(t, files)
	}
	e := New(DefaultOptions())
	defer e.Close()
	var rows []string
	var sum, n float64
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		rf, err := os.Open(filepath.Join("testdata/webgl/.ref", name+".png"))
		if err != nil {
			t.Errorf("%s: no reference (%v)", name, err)
			continue
		}
		refImg, err := png.Decode(rf)
		rf.Close()
		if err != nil {
			t.Errorf("%s: bad reference: %v", name, err)
			continue
		}
		ref := image.NewNRGBA(refImg.Bounds())
		draw.Draw(ref, ref.Bounds(), refImg, refImg.Bounds().Min, draw.Src)
		var refInfo glRef
		if b, err := os.ReadFile(filepath.Join("testdata/webgl/.ref", name+".json")); err == nil {
			_ = json.Unmarshal(b, &refInfo)
		}
		tab, err := e.NewTab(srv.URL + "/" + name + ".html")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for i := 0; i < 60; i++ {
			if evalStr(t, tab, `String(!window.__wait || !!window.__done)`) == "true" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		raw, err := tab.Evaluate(`document.querySelector('canvas').toDataURL()`)
		var u string
		if err == nil {
			_ = json.Unmarshal(raw, &u)
		}
		got, perr := pngFromDataURL(t, u)
		if perr != nil {
			t.Errorf("%s: %v (eval err %v)\n%s", name, perr, err, consoleText(tab))
			continue
		}
		var gotInfo glRef
		gotInfo.Result = evalStr(t, tab, `String(window.__result||"")`)
		gotInfo.Error = evalStr(t, tab, `String(window.__error||"")`)
		if dir := os.Getenv("WEBGL_OUT"); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
			if fo, err := os.Create(filepath.Join(dir, name+".png")); err == nil {
				_ = png.Encode(fo, got)
				fo.Close()
			}
			jb, _ := json.Marshal(gotInfo)
			_ = os.WriteFile(filepath.Join(dir, name+".json"), jb, 0o644)
		}
		w, ex, mae := glCmp(got, ref, 4)
		note := ""
		if gotInfo.Error != "" || refInfo.Error != "" {
			note += fmt.Sprintf(" err(sweef=%q chromium=%q)", trim(gotInfo.Error, 60), trim(refInfo.Error, 60))
		}
		if gotInfo.Result != refInfo.Result {
			note += fmt.Sprintf(" result differs: sweef=%q chromium=%q", trim(gotInfo.Result, 160), trim(refInfo.Result, 160))
		}
		rows = append(rows, fmt.Sprintf("%-26s within4=%6.2f%% exact=%6.2f%% mae=%5.2f%s", name, w*100, ex*100, mae, note))
		sum += w
		n++
		if w < 0.90 {
			t.Errorf("%s: only %.2f%% of pixels within tolerance", name, w*100)
		}
	}
	t.Logf("\n%s\nmean within4: %.2f%% over %d fixtures", strings.Join(rows, "\n"), sum/n*100, int(n))
}

func trim(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func TestWebGLBitmapSource(t *testing.T) {
	_, tab := newTestTab(t)
	tab.SetDocument(`<!doctype html><canvas id=c width=8 height=6></canvas>`, "https://example.test/")
	id := evalStr(t, tab, `var gl=document.getElementById('c').getContext('webgl');gl.enable(gl.SCISSOR_TEST);gl.scissor(0,0,8,2);gl.clearColor(1,0,0,1);gl.clear(gl.COLOR_BUFFER_BIT);String(__swIdOf(document.getElementById('c')))`)
	var nid int64
	fmt.Sscan(id, &nid)
	src := tab.BitmapSourceFor(tab.dom.NodeByID(nid))
	if src == nil {
		t.Fatal("no bitmap source registered")
	}
	if w, h := src.Size(); w != 8 || h != 6 {
		t.Fatalf("size %dx%d", w, h)
	}
	// WebGL's origin is bottom-left: the cleared strip is at the bottom of the bitmap.
	r, _, _, a := src.Bitmap().At(3, 5).RGBA()
	if r>>8 != 255 || a>>8 != 255 {
		t.Fatalf("bottom row pixel r=%d a=%d", r>>8, a>>8)
	}
	if _, _, _, a := src.Bitmap().At(3, 0).RGBA(); a != 0 {
		t.Fatalf("top row should be transparent, a=%d", a>>8)
	}
	// The frame is still visible after it has been presented (preserveDrawingBuffer=false).
	tab.Settle(100 * time.Millisecond)
	if r, _, _, _ := src.Bitmap().At(3, 5).RGBA(); r>>8 != 255 {
		t.Fatalf("presented frame lost: r=%d", r>>8)
	}
	// the drawing buffer itself was cleared at presentation
	if got := evalStr(t, tab, `var p=new Uint8Array(4);gl.readPixels(3,0,1,1,gl.RGBA,gl.UNSIGNED_BYTE,p);Array.prototype.join.call(p)`); got != "0,0,0,0" {
		t.Fatalf("drawing buffer after present: %s", got)
	}
}

func TestWebGLTransferAndDrawPerf(t *testing.T) {
	_, tab := newTestTab(t)
	tab.SetDocument(`<!doctype html><canvas id=c width=512 height=512></canvas>`, "https://example.test/")
	got := evalStr(t, tab, `(function(){
var c=document.getElementById('c'),gl=c.getContext('webgl',{antialias:false,preserveDrawingBuffer:true});
function sh(t,s){var o=gl.createShader(t);gl.shaderSource(o,s);gl.compileShader(o);if(!gl.getShaderParameter(o,gl.COMPILE_STATUS))throw new Error(gl.getShaderInfoLog(o));return o;}
var p=gl.createProgram();gl.attachShader(p,sh(gl.VERTEX_SHADER,'attribute vec2 a;attribute vec3 c;varying vec3 v;void main(){v=c;gl_Position=vec4(a,0.,1.);}'));gl.attachShader(p,sh(gl.FRAGMENT_SHADER,'precision mediump float;varying vec3 v;void main(){gl_FragColor=vec4(v,1.);}'));gl.linkProgram(p);gl.useProgram(p);
var n=100000,d=new Float32Array(n*3*5),r=1;function rnd(){r=(r*1664525+1013904223)>>>0;return r/4294967296;}
for(var i=0;i<n;i++){var cx=rnd()*2-1,cy=rnd()*2-1;for(var k=0;k<3;k++){var o=(i*3+k)*5;d[o]=cx+(rnd()-0.5)*0.03;d[o+1]=cy+(rnd()-0.5)*0.03;d[o+2]=rnd();d[o+3]=rnd();d[o+4]=rnd();}}
var t0=performance.now();var b=gl.createBuffer();gl.bindBuffer(gl.ARRAY_BUFFER,b);gl.bufferData(gl.ARRAY_BUFFER,d,gl.STATIC_DRAW);var t1=performance.now();
var a=gl.getAttribLocation(p,'a'),cc=gl.getAttribLocation(p,'c');gl.enableVertexAttribArray(a);gl.vertexAttribPointer(a,2,gl.FLOAT,false,20,0);gl.enableVertexAttribArray(cc);gl.vertexAttribPointer(cc,3,gl.FLOAT,false,20,8);
gl.clear(gl.COLOR_BUFFER_BIT);var t2=performance.now();gl.drawArrays(gl.TRIANGLES,0,n*3);var t3=performance.now();
var px=new Uint8Array(512*512*4);gl.readPixels(0,0,512,512,gl.RGBA,gl.UNSIGNED_BYTE,px);var t4=performance.now();
var tex=gl.createTexture();gl.bindTexture(gl.TEXTURE_2D,tex);gl.texImage2D(gl.TEXTURE_2D,0,gl.RGBA,512,512,0,gl.RGBA,gl.UNSIGNED_BYTE,px);var t5=performance.now();
var f=new Float32Array(16),loc=gl.getUniformLocation(p,'nope');var t6=performance.now();for(var i=0;i<2000;i++){gl.uniform4fv(loc,[1,2,3,4]);gl.getError();}var t7=performance.now();
return [Math.round(t1-t0),Math.round(t3-t2),Math.round(t4-t3),Math.round(t5-t4),((t7-t6)/2000*1000).toFixed(1)].join(',');})()`)
	t.Logf("bufferData 6MB ms, 100k triangle draw ms, readPixels 1MB ms, texImage2D 1MB ms, us per gl call pair: %s", got)
}
