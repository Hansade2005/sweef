package engine

import (
	"testing"
	"time"
)

// DOM-bridge workloads: page script patterns that crossed the JS/Go bridge once per node or rebuilt live
// collections on every read. Each case has a generous ceiling (about 10x what it takes after the fix, far
// below the quadratic cost it had before) so a regression fails instead of only slowing pages down.
var domBridgeCases = []struct {
	name, setup, js string
	maxMs           float64 // 0 = timing logged only
}{
	{"create1000", ``, `(function(){var r=document.createElement('div');document.body.appendChild(r);for(var i=0;i<1000;i++){var d=document.createElement('div');d.className='c'+(i%10);d.setAttribute('data-i',i);d.textContent='x'+i;r.appendChild(d)}return 1})()`, 0},
	{"children_loop1000", `window.R=document.createElement('div');document.body.appendChild(R);for(var i=0;i<1000;i++){var d=document.createElement('div');d.setAttribute('data-i',i);R.appendChild(d)}`, `(function(){var n=0,els=R.children;for(var j=0;j<els.length;j++){els[j].style.color='red';n+=els[j].getAttribute('data-i').length}return n})()`, 800},
	{"prop20k", ``, `(function(){var n=0;for(var m=0;m<20000;m++){n+=document.body.firstChild?1:0}return n})()`, 1500},
	{"innerHTML300", `window.Q=document.createElement('div');document.body.appendChild(Q)`, `(function(){Q.innerHTML='';for(var q=0;q<300;q++)Q.innerHTML+='<span>a</span>';return Q.children.length})()`, 800},
	{"qsa200", `window.S=document.createElement('div');document.body.appendChild(S);for(var i=0;i<1000;i++){var d=document.createElement('div');d.className='c'+(i%10);S.appendChild(d)}`, `(function(){var n=0;for(var k=0;k<200;k++)n+=document.querySelectorAll('.c3').length;return n})()`, 0},
	{"x_styleSet1000", `window.E=document.createElement('div');document.body.appendChild(E)`, `(function(){for(var i=0;i<1000;i++)E.style.color='red';return 1})()`, 0},
	{"x_setAttr1000", `window.E=document.createElement('div');document.body.appendChild(E)`, `(function(){for(var i=0;i<1000;i++)E.setAttribute('data-x',i);return 1})()`, 0},
	{"x_getAttr1000", `window.E=document.createElement('div');E.setAttribute('a','b');document.body.appendChild(E)`, `(function(){var n=0;for(var i=0;i<1000;i++)n+=E.getAttribute('a').length;return n})()`, 0},
	{"x_body20k", ``, `(function(){var n=0;for(var m=0;m<20000;m++){n+=document.body?1:0}return n})()`, 1000},
	{"x_firstChild20k", `window.B=document.body`, `(function(){var n=0;for(var m=0;m<20000;m++){n+=B.firstChild?1:0}return n})()`, 0},
	{"x_innerHTMLget300", `window.Q=document.createElement('div');document.body.appendChild(Q);for(var i=0;i<300;i++)Q.appendChild(document.createElement('span'))`, `(function(){var n=0;for(var q=0;q<300;q++)n+=Q.innerHTML.length;return n})()`, 0},
	{"x_innerHTMLset300", `window.Q=document.createElement('div');document.body.appendChild(Q);window.H=new Array(150).join('<span>a</span>')`, `(function(){for(var q=0;q<300;q++)Q.innerHTML=H;return Q.children.length})()`, 800},
	{"x_createOnly1000", ``, `(function(){for(var i=0;i<1000;i++)document.createElement('div');return 1})()`, 0},
	{"x_appendOnly1000", `window.R=document.createElement('div');document.body.appendChild(R);window.L=[];for(var i=0;i<1000;i++)L.push(document.createElement('div'))`, `(function(){for(var i=0;i<1000;i++)R.appendChild(L[i]);return 1})()`, 0},
	{"x_textContent1000", `window.L=[];for(var i=0;i<1000;i++)L.push(document.createElement('div'))`, `(function(){for(var i=0;i<1000;i++)L[i].textContent='x'+i;return 1})()`, 0},
}

func TestDOMBridgeSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	e := New(DefaultOptions())
	defer e.Close()
	for _, c := range domBridgeCases {
		tab, err := e.NewTab("about:blank")
		if err != nil {
			t.Fatal(err)
		}
		if c.setup != "" {
			evalJSON(t, tab, c.setup+";1", nil)
		}
		start := time.Now()
		var got any
		evalJSON(t, tab, c.js, &got)
		ms := float64(time.Since(start).Microseconds()) / 1000
		t.Logf("DOMBRIDGE %-18s %8.1f ms  result=%v", c.name, ms, got)
		if c.maxMs > 0 && ms > c.maxMs {
			t.Errorf("%s took %.0f ms, over its %.0f ms ceiling: a DOM bridge path regressed", c.name, ms, c.maxMs)
		}
		tab.close()
	}
}
