package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/ui/preview"
)

// The cross-pass caches (computed styles, SVG scenes) must never change a
// result: after any sequence of DOM changes the layout of the cached pass has
// to equal a layout from scratch (engine.SetLayoutCheck compares every box and
// display-list op and panics on a difference). This test mutates real pages
// at random between passes: class and attribute changes, inline styles,
// inserted, cloned, moved and removed elements, text edits, HTML insertion.

const fuzzJS = `(function(S){
  function rnd(n){ S=(S*1103515245+12345)&0x7fffffff; return (S>>8)%n; }
  var all=Array.prototype.slice.call(document.body.querySelectorAll('*'));
  if(!all.length) return 'empty';
  var el=all[rnd(all.length)], other=all[rnd(all.length)];
  var cs=window.__fzC; if(!cs){ var m={}; document.querySelectorAll('[class]').forEach(function(e){String(e.getAttribute('class')).split(/\s+/).forEach(function(c){if(c)m[c]=1;});}); cs=window.__fzC=Object.keys(m); }
  var attrs=['id','title','hidden','disabled','checked','dir','lang','data-x','aria-selected','style','type','value','width','height','class','open','href','selected','readonly','required','role'];
  var vals=['','a','1','x y','display:none','color:red','width:50px','margin:3px','position:relative;top:4px','float:left','font-size:20px','flex','grid','background:#08f','opacity:.5','transform:rotate(3deg)'];
  var props=[['display','none'],['display','block'],['display','flex'],['display','inline'],['position','absolute'],['position','relative'],['width','40px'],['height','30px'],['color','red'],['margin','4px'],['padding','6px'],['float','left'],['overflow','hidden'],['font-size','22px'],['opacity','0.6'],['visibility','hidden'],['order','2'],['transform','scale(1.1)'],['--x','5px'],['content','"x"']];
  var op=rnd(15), tag='';
  if(all.length>350 && (op===4||op===6||op===10)) op=3;
  try{
  switch(op){
   case 0: if(cs.length) el.classList.toggle(cs[rnd(cs.length)]); tag='class'; break;
   case 1: el.setAttribute(attrs[rnd(attrs.length)], vals[rnd(vals.length)]); tag='setattr'; break;
   case 2: el.removeAttribute(attrs[rnd(attrs.length)]); tag='rmattr'; break;
   case 3: if(el.parentNode && el!==document.body) el.parentNode.removeChild(el); tag='remove'; break;
   case 4: var c=el.cloneNode(true); (rnd(2)?other:el.parentNode||other).appendChild(c); tag='clone'; break;
   case 5: var t=[]; (function w(n){ for(var k=n.firstChild;k;k=k.nextSibling){ if(k.nodeType===3) t.push(k); else w(k);} })(el); if(t.length) t[rnd(t.length)].data=['', 'x', 'edited text longer than before', ' '][rnd(4)]; tag='text'; break;
   case 6: var s=document.createElement(['span','div','p','b','li','svg','button'][rnd(7)]); if(cs.length&&rnd(2)) s.className=cs[rnd(cs.length)]; s.textContent='n'+rnd(100); el.appendChild(s); tag='append'; break;
   case 7: if(!el.contains(other) && other!==document.body) el.appendChild(other); tag='move'; break;
   case 8: var p=props[rnd(props.length)]; el.style.setProperty(p[0], p[1]); tag='style'; break;
   case 9: if(el.hasAttribute('hidden')) el.removeAttribute('hidden'); else el.setAttribute('hidden',''); tag='hidden'; break;
   case 10: el.insertAdjacentHTML(['beforeend','afterbegin','beforebegin','afterend'][rnd(4)], ['<div class="'+(cs.length?cs[rnd(cs.length)]:'q')+'">t</div>','<span>a</span><span>b</span>','<svg width="16" height="16" viewBox="0 0 24 24"><path d="M5 12h14" stroke="currentColor"/></svg>','<ul><li>1</li><li>2</li></ul>'][rnd(4)]); tag='html'; break;
   case 11: if(el.nextSibling && el.parentNode) el.parentNode.insertBefore(el.nextSibling, el); tag='swap'; break;
   case 12: if(el.firstChild) el.insertBefore(document.createTextNode('ins'+rnd(9)), el.firstChild); tag='textins'; break;
   case 13: if(cs.length){ el.className = cs[rnd(cs.length)]+' '+cs[rnd(cs.length)]; } tag='classname'; break;
   case 14: el.textContent = ['', 'replaced', 'a b c d e f g h i j k l m n o p'][rnd(3)]; tag='textcontent'; break;
  }
  }catch(e){ return 'err '+e; }
  return tag;
})(`

func fuzzPass(t *testing.T, name string, tab *engine.Tab, seed int, steps int, w int) {
	t.Helper()
	opts := engine.PaintOptions{Width: w, Height: 800, DSF: 1, Transparent: true, Incremental: true}
	tab.PaintRGBAOpts(opts)
	for i := 0; i < steps; i++ {
		s := seed*1000 + i
		var tag json_
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s seed %d step %d (replay: SWEEF_FUZZ_SEED=%d): %v", name, seed, i, seed, r)
				}
			}()
			raw, _ := tab.Evaluate(fuzzJS + fmt.Sprint(s) + `)`)
			tag = json_(raw)
			tab.PaintRGBAOpts(opts)
			if i%7 == 3 { // a second pass over the same tree must be a pure cache hit
				tab.PaintRGBAOpts(opts)
			}
		}()
		_ = tag
	}
}

type json_ []byte

func fuzzCount(def int) int {
	if os.Getenv("SWEEF_FUZZ_FULL") != "" {
		return def * 6
	}
	return def
}

func TestLayoutCacheFuzzFidelityPages(t *testing.T) {
	if testing.Short() && os.Getenv("SWEEF_FUZZ_FULL") == "" {
		t.Skip("-short: set SWEEF_FUZZ_FULL=1 to fuzz the fidelity pages")
	}
	engine.SetLayoutCheck(true)
	defer engine.SetLayoutCheck(false)
	files, _ := filepath.Glob("../../testdata/fidelity/*.html")
	if len(files) == 0 {
		t.Skip("no fidelity fixtures")
	}
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	step := 1
	if os.Getenv("SWEEF_FUZZ_FULL") == "" {
		step = 5
	}
	for fi := 0; fi < len(files); fi += step {
		f := files[fi]
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			tab, err := e.NewTab("about:blank")
			if err != nil {
				t.Fatal(err)
			}
			tab.SetViewport(1280, 800, 1, false, false)
			tab.SetDocument(string(src), "https://fuzz.test/"+name+".html")
			for seed := 1; seed <= fuzzCount(2); seed++ {
				fuzzPass(t, name, tab, seed, 40, 1280)
			}
		})
	}
}

func TestLayoutCacheFuzzUIPages(t *testing.T) {
	engine.SetLayoutCheck(true)
	defer engine.SetLayoutCheck(false)
	srv := preview.Server()
	defer srv.Close()
	e := preview.Engine()
	defer e.Close()
	n := 0
	for _, sc := range preview.Scenarios() {
		if sc.Skip || !strings.HasPrefix(sc.Name, "chrome-") {
			continue
		}
		n++
		if testing.Short() && n%3 != 0 {
			continue
		}
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			s, err := preview.Open(e, srv.URL, sc, 1)
			if err != nil {
				t.Fatal(err)
			}
			s.Run(sc.Setup)
			s.Settle()
			for seed := 1; seed <= fuzzCount(2); seed++ {
				fuzzPass(t, sc.Name, s.Tab, seed, 30, 1280)
			}
		})
	}
}
