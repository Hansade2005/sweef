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
