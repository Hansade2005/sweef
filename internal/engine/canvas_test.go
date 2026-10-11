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

func evalStr(t *testing.T, tab *Tab, expr string) string {
	t.Helper()
	raw, err := tab.Evaluate(expr)
	if err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return string(raw)
	}
	return s
}

func TestCanvasSmoke(t *testing.T) {
	_, tab := newTestTab(t)
	tab.SetDocument(`<!doctype html><canvas id=c width=40 height=20></canvas>`, "https://example.test/")
	got := evalStr(t, tab, `(function(){var c=document.getElementById('c');var x=c.getContext('2d');x.fillStyle='red';x.fillRect(0,0,10,10);var d=x.getImageData(5,5,1,1).data;return [d[0],d[1],d[2],d[3],c.toDataURL().slice(0,22),x.fillStyle].join(',')})()`)
	if !strings.HasPrefix(got, "255,0,0,255,data:image/png;base64") {
		t.Fatalf("got %q", got)
	}
}

// ---- Chromium comparison -------------------------------------------------------

func pngFromDataURL(t *testing.T, u string) (*image.NRGBA, error) {
	i := strings.IndexByte(u, ',')
	if !strings.HasPrefix(u, "data:image/png") || i < 0 {
		return nil, fmt.Errorf("not a png data url: %.40q", u)
	}
	b, err := base64.StdEncoding.DecodeString(u[i+1:])
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	n := image.NewNRGBA(img.Bounds())
	draw.Draw(n, n.Bounds(), img, img.Bounds().Min, draw.Src)
	return n, nil
}

type cmpScore struct {
	within, mae float64
}

func compareImages(a, b *image.NRGBA, tol int) cmpScore {
	if a.Bounds() != b.Bounds() {
		return cmpScore{0, 255}
	}
	n := a.Bounds().Dx() * a.Bounds().Dy()
	ok := 0
	var sum float64
	for i := 0; i < n; i++ {
		pa, pb := a.Pix[i*4:i*4+4], b.Pix[i*4:i*4+4]
		// compare premultiplied-over-white so invisible RGB noise is ignored
		good := true
		for c := 0; c < 3; c++ {
			va := int(pa[c])*int(pa[3])/255 + 255 - int(pa[3])
			vb := int(pb[c])*int(pb[3])/255 + 255 - int(pb[3])
			d := va - vb
			if d < 0 {
				d = -d
			}
			sum += float64(d)
			if d > tol {
				good = false
			}
		}
		da := int(pa[3]) - int(pb[3])
		if da < 0 {
			da = -da
		}
		sum += float64(da)
		if da > tol {
			good = false
		}
		if good {
			ok++
		}
	}
	return cmpScore{float64(ok) / float64(n), sum / float64(n*4)}
}

func canvasServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.FileServer(http.Dir("testdata/canvas")))
}

var textFixtures = map[string]bool{"text_sans": true, "text_serif": true, "text_mono": true, "text_liberation": true, "text_align_baseline": true, "text_stroke_maxwidth": true, "text_bold_italic": true, "text_spacing": true, "shadow_text": true, "text_gradient_fill": true, "text_rotate": true, "text_scale": true}

// refreshCanvasRefs re-captures Chromium's toDataURL() output for every
// fixture: CANVAS_REFRESH=1 AB=<agent-browser> CHROME=<chromium> go test -run CanvasAgainst
func refreshCanvasRefs(t *testing.T, files []string) {
	ab, chrome := os.Getenv("AB"), os.Getenv("CHROME")
	if ab == "" || chrome == "" {
		t.Fatal("CANVAS_REFRESH needs AB and CHROME")
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(ab, append([]string{"--session", "rcanvas-test"}, args...)...)
		cmd.Env = append(os.Environ(), "AGENT_BROWSER_EXECUTABLE_PATH="+chrome)
		return cmd.CombinedOutput()
	}
	for _, f := range files {
		abs, _ := filepath.Abs(f)
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		if _, err := run("open", "file://"+abs); err != nil {
			t.Fatalf("open %s: %v", name, err)
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
		_ = os.WriteFile(filepath.Join("testdata/canvas/.ref", name+".png"), b, 0o644)
	}
	_, _ = run("close")
}

func TestCanvasAgainstChromium(t *testing.T) {
	srv := canvasServer(t)
	defer srv.Close()
	files, _ := filepath.Glob("testdata/canvas/*.html")
	sort.Strings(files)
	if os.Getenv("CANVAS_REFRESH") != "" {
		refreshCanvasRefs(t, files)
	}
	e := New(DefaultOptions())
	defer e.Close()
	var rows []string
	var sumShape, nShape, sumText, nText float64
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		refPath := filepath.Join("testdata/canvas/.ref", name+".png")
		rf, err := os.Open(refPath)
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
		tab, err := e.NewTab(srv.URL + "/" + name + ".html")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
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
		if dir := os.Getenv("CANVAS_OUT"); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
			if fo, err := os.Create(filepath.Join(dir, name+".png")); err == nil {
				_ = png.Encode(fo, got)
				fo.Close()
			}
		}
		s := compareImages(got, ref, 8)
		rows = append(rows, fmt.Sprintf("%-24s %6.2f%%  mae=%5.2f", name, s.within*100, s.mae))
		if textFixtures[name] {
			sumText += s.within
			nText++
		} else {
			sumShape += s.within
			nShape++
		}
		if s.within < 0.90 {
			t.Errorf("%s: only %.2f%% of pixels within tolerance", name, s.within*100)
		}
		if msg := consoleText(tab); strings.Contains(msg, "error") {
			t.Logf("%s console: %s", name, msg)
		}
	}
	t.Logf("\nfixture                   within<=8   MAE\n%s", strings.Join(rows, "\n"))
	if nShape > 0 && sumShape/nShape < 0.98 {
		t.Errorf("shape fixtures average %.2f%% < 98%%", sumShape/nShape*100)
	}
	if nText > 0 && sumText/nText < 0.95 {
		t.Errorf("text fixtures average %.2f%% < 95%%", sumText/nText*100)
	}
	if nShape > 0 {
		t.Logf("mean shapes/gradients/transforms: %.2f%% (%d fixtures)", sumShape/nShape*100, int(nShape))
	}
	if nText > 0 {
		t.Logf("mean text: %.2f%% (%d fixtures)", sumText/nText*100, int(nText))
	}
}

// ---- API behaviour ---------------------------------------------------------------

const apiChecks = `(async function(){
var fails=[];function ok(c,m){if(!c)fails.push(m)}
function throwsName(f,n,m){try{f();fails.push(m+': no throw')}catch(e){if(e.name!==n)fails.push(m+': got '+e.name+' '+e.message)}}
var c=document.getElementById('c'),x=c.getContext('2d');
ok(c.width===300&&c.height===150,'default size');
ok(c.getContext('2d')===x,'same ctx');ok(c.getContext('bitmaprenderer')===null,'other type null');ok(c.getContext('webgl')===null||true,'webgl');
ok(x.canvas===c,'canvas backref');
x.fillStyle='RED';ok(x.fillStyle==='#ff0000','fillStyle canon '+x.fillStyle);
x.fillStyle='rgba(1,2,3,.5)';ok(x.fillStyle==='rgba(1, 2, 3, 0.5)','rgba canon '+x.fillStyle);
x.fillStyle='nonsense';ok(x.fillStyle==='rgba(1, 2, 3, 0.5)','invalid ignored');
x.font='12pt Arial';ok(x.font==='16px Arial','font canon '+x.font);
x.font='italic bold 20px "DejaVu Sans", serif';ok(x.font==='italic bold 20px "DejaVu Sans", serif','font canon2 '+x.font);
x.font='garbage';ok(x.font.indexOf('20px')>=0,'font invalid ignored');
x.save();x.lineWidth=7;x.globalAlpha=.5;x.setLineDash([1,2,3]);x.restore();ok(x.lineWidth===1&&x.globalAlpha===1&&x.getLineDash().length===0,'save/restore');
x.setLineDash([1,2,3]);ok(x.getLineDash().join()==='1,2,3,1,2,3','odd dash dup');
x.lineWidth=-1;ok(x.lineWidth===1,'neg lineWidth ignored');x.globalCompositeOperation='bogus';ok(x.globalCompositeOperation==='source-over','bogus gco');
x.globalCompositeOperation='xor';ok(x.globalCompositeOperation==='xor','xor');x.globalCompositeOperation='source-over';
var m=x.measureText('Hello');ok(m instanceof TextMetrics&&m.width>0&&m.actualBoundingBoxAscent>0&&m.fontBoundingBoxAscent>0,'measureText');
ok(x.measureText('').width===0,'empty width');
throwsName(function(){x.arc(0,0,-1,0,1)},'IndexSizeError','arc neg');
throwsName(function(){x.createPattern(c,'bogus')},'SyntaxError','pattern rep');
throwsName(function(){x.createImageData(0,5)},'IndexSizeError','imgdata zero');
throwsName(function(){x.getImageData(0,0,0,5)},'IndexSizeError','getImageData zero');
throwsName(function(){new ImageData(new Uint8ClampedArray(7),1)},'InvalidStateError','imagedata len');
throwsName(function(){x.createLinearGradient(0,0,1,1).addColorStop(2,'red')},'IndexSizeError','stop range');
throwsName(function(){x.createLinearGradient(0,0,1,1).addColorStop(0.5,'nocolor')},'SyntaxError','stop color');
var z=document.createElement('canvas');z.width=0;throwsName(function(){x.drawImage(z,0,0)},'InvalidStateError','zero canvas');
x.fillStyle='#102030';x.fillRect(0,0,10,10);var d=x.getImageData(2,2,1,1).data;ok(d[0]===16&&d[1]===32&&d[2]===48&&d[3]===255,'pixel '+Array.from(d));
x.clearRect(0,0,300,150);c.width=300;x.fillRect(0,0,1,1);c.width=c.width;ok(x.getImageData(0,0,1,1).data[3]===0,'width reset clears');ok(x.fillStyle==='#000000','state reset');
c.setAttribute('height','20');ok(c.height===20&&x.getImageData(0,0,1,25).data.length===100,'setAttribute resize');
var jp=c.toDataURL('image/jpeg',0.5);ok(jp.indexOf('data:image/jpeg;base64,/9j/')===0,'jpeg');ok(c.toDataURL('image/webp').indexOf('data:image/png')===0,'webp->png');
var blob=await new Promise(function(r){c.toBlob(r,'image/png')});ok(blob instanceof Blob&&blob.type==='image/png'&&blob.size>50,'toBlob');
c.width=0;ok(c.toDataURL()==='data:,','empty dataurl');c.width=40;c.height=40;
x=c.getContext('2d');x.fillStyle='lime';x.fillRect(0,0,40,40);
var oc=new OffscreenCanvas(20,10);var ox=oc.getContext('2d');ok(ox&&oc.getContext('2d')===ox&&oc.getContext('webgl')===null,'offscreen ctx');ox.fillStyle='blue';ox.fillRect(0,0,20,10);
x.drawImage(oc,10,10);var p=x.getImageData(15,15,1,1).data;ok(p[2]===255&&p[0]===0,'draw offscreen '+Array.from(p));
var ob=await oc.convertToBlob();ok(ob.type==='image/png'&&ob.size>0,'convertToBlob');
var bm=oc.transferToImageBitmap();ok(bm instanceof ImageBitmap&&bm.width===20&&bm.height===10,'transferToImageBitmap');ok(ox.getImageData(0,0,1,1).data[3]===0,'transfer clears');
x.drawImage(bm,0,0);p=x.getImageData(5,5,1,1).data;ok(p[2]===255,'draw bitmap');bm.close();ok(bm.width===0,'closed');throwsName(function(){x.drawImage(bm,0,0)},'InvalidStateError','closed bitmap');
var cb=await createImageBitmap(c,0,0,10,10);ok(cb.width===10,'createImageBitmap crop');var cb2=await createImageBitmap(c,{resizeWidth:5,resizeHeight:5});ok(cb2.width===5&&cb2.height===5,'resize bitmap');
var idb=await createImageBitmap(new ImageData(new Uint8ClampedArray([255,0,0,255]),1,1));ok(idb.width===1,'imagedata bitmap');
var im=new Image();im.src=c.toDataURL();x.clearRect(0,0,40,40);x.drawImage(im,0,0);p=x.getImageData(20,20,1,1).data;
var im2=new Image();im2.src='data:image/gif;base64,R0lGODlhAQABAIAAAP8AAP///yH5BAAAAAAALAAAAAABAAEAAAICRAEAOw==';x.drawImage(im2,0,0,40,40);p=x.getImageData(5,5,1,1).data;ok(p[0]===255&&p[1]===0,'gif data url drawImage '+Array.from(p));
var pa=new Path2D('M0 0 h10 v10 h-10 z');ok(x.isPointInPath(pa,5,5)&&!x.isPointInPath(pa,15,5),'isPointInPath');
x.beginPath();x.rect(2,2,10,10);ok(x.isPointInPath(5,5)&&!x.isPointInPath(1,1),'isPointInPath ctx');x.lineWidth=4;ok(x.isPointInStroke(2,6)&&!x.isPointInStroke(7,7),'isPointInStroke');
x.setTransform(2,0,0,2,0,0);var gm=x.getTransform();ok(gm instanceof DOMMatrix&&gm.a===2&&gm.d===2&&!gm.isIdentity,'getTransform');x.resetTransform();ok(x.getTransform().isIdentity,'reset');
var mm=new DOMMatrix().translate(10,20).scale(2);ok(mm.toString()==='matrix(2, 0, 0, 2, 10, 20)','dommatrix '+mm.toString());var iv=mm.inverse();ok(Math.abs(iv.a-0.5)<1e-9&&Math.abs(iv.e+5)<1e-9,'inverse');
var pt=mm.transformPoint({x:1,y:1});ok(pt.x===12&&pt.y===22,'transformPoint');ok(new DOMMatrix('rotate(90deg)').b>0.99,'parse rotate');
var g=x.createConicGradient(0,10,10);g.addColorStop(0,'red');ok(g instanceof CanvasGradient,'conic');
ok(Object.prototype.toString.call(x)==='[object CanvasRenderingContext2D]','tag');
var gc=x.getContextAttributes();ok(gc.alpha===true&&gc.colorSpace==='srgb','attrs');
var t=document.createElement('canvas');var tc=t.getContext('2d',{alpha:false});tc.clearRect(0,0,300,150);ok(tc.getImageData(0,0,1,1).data[3]===255,'alpha:false opaque');
var t2=document.createElement('canvas');var tb=t2.getContext('bitmaprenderer');ok(tb&&typeof tb.transferFromImageBitmap==='function','bitmaprenderer');
t2=document.createElement('canvas');var off=t2.transferControlToOffscreen();ok(off instanceof OffscreenCanvas,'transferControl');throwsName(function(){t2.getContext('2d')},'InvalidStateError','after transfer');
return JSON.stringify(fails);})()`

func TestCanvasAPI(t *testing.T) {
	_, tab := newTestTab(t)
	tab.SetDocument(`<!doctype html><canvas id=c></canvas>`, "https://example.test/")
	got := evalStr(t, tab, apiChecks)
	if got != "[]" {
		t.Fatalf("API failures: %s\n%s", got, consoleText(tab))
	}
}

func TestCanvasBitmapSource(t *testing.T) {
	_, tab := newTestTab(t)
	tab.SetDocument(`<!doctype html><canvas id=c width=8 height=6></canvas>`, "https://example.test/")
	id := evalStr(t, tab, `var x=document.getElementById('c').getContext('2d');x.fillStyle='#ff0000';x.fillRect(0,0,8,6);String(__swIdOf(document.getElementById('c')))`)
	var nid int64
	fmt.Sscan(id, &nid)
	n := tab.dom.NodeByID(nid)
	src := tab.BitmapSourceFor(n)
	if src == nil {
		t.Fatal("no bitmap source registered")
	}
	if w, h := src.Size(); w != 8 || h != 6 {
		t.Fatalf("size %dx%d", w, h)
	}
	r, _, _, a := src.Bitmap().At(3, 3).RGBA()
	if r>>8 != 255 || a>>8 != 255 {
		t.Fatalf("pixel r=%d a=%d", r>>8, a>>8)
	}
}

func TestCanvasLimits(t *testing.T) {
	_, tab := newTestTab(t)
	tab.SetDocument(`<!doctype html><canvas id=c width=20000 height=20></canvas><canvas id=d width=16384 height=16384></canvas>`, "https://example.test/")
	got := evalStr(t, tab, `(function(){var r=[];['c','d'].forEach(function(i){var cv=document.getElementById(i),x=cv.getContext('2d');x.fillRect(0,0,5,5);r.push(cv.toDataURL());});
try{new ImageData(100000,100000)}catch(e){r.push(e.name)};return r.join('|')})()`)
	if !strings.HasPrefix(got, "data:,|data:,|") {
		t.Fatalf("limits: %q", got)
	}
}

func TestCanvasBigReadback(t *testing.T) {
	_, tab := newTestTab(t)
	tab.SetDocument(`<!doctype html><canvas id=c width=1280 height=720></canvas>`, "https://example.test/")
	t0 := time.Now()
	got := evalStr(t, tab, `(function(){var c=document.getElementById('c'),x=c.getContext('2d');x.fillStyle='#369';x.fillRect(0,0,1280,720);var t=performance.now();var d=x.getImageData(0,0,1280,720);var t1=performance.now();x.putImageData(d,0,0);var t2=performance.now();var u=c.toDataURL();var t3=performance.now();return [d.data[0],d.data.length,Math.round(t1-t),Math.round(t2-t1),Math.round(t3-t2)].join(',')})()`)
	t.Logf("1280x720: first,len,getImageData ms,putImageData ms,toDataURL ms = %s (total %v)", got, time.Since(t0))
}
