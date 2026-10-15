package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Regression tests for the DOM shape that web-component polyfills and component frameworks (the Shady DOM and
// Polymer Resin code YouTube ships) probe and patch. Each case is a behaviour of the DOM, not of any one site.

func polyTab(t *testing.T, body string) *Tab {
	t.Helper()
	mux := http.NewServeMux()
	page := func(path, html string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, html)
		})
	}
	page("/", `<!doctype html><html><body>`+body+`</body></html>`)
	page("/frame", `<!doctype html><html><body><p id="p" onclick="window.clicked=1">frame</p><script>window.ran = 1;</script></body></html>`)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e := newFeatureEngine(t)
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

// The Node.prototype accessors work on a DocumentFragment. The Shady DOM polyfill copies Node.prototype's
// firstChild/lastChild/nextSibling/childNodes/textContent descriptors and applies them to the fragments it turns into
// shadow roots; a Node getter that only understood element ids made every shadow root look empty.
func TestNodeAccessorsWorkOnFragments(t *testing.T) {
	tab := polyTab(t, `<p>x</p>`)
	var r map[string]any
	evalJSON(t, tab, `(function(){
	  var f = document.createDocumentFragment();
	  var a = document.createElement('a'), b = document.createElement('b');
	  f.appendChild(a); f.appendChild(document.createTextNode('t')); f.appendChild(b);
	  var N = Object.getOwnPropertyDescriptor.bind(Object, Node.prototype);
	  var g = function (n) { return N(n).get.call(f); };
	  var nodeFirst = g('firstChild'), nodeLast = g('lastChild'), kids = g('childNodes');
	  return {
	    first: nodeFirst === a, last: nodeLast === b, kids: kids.length, parent: g('parentNode'), sib: g('nextSibling'),
	    type: g('nodeType'), name: g('nodeName'), text: g('textContent'), conn: g('isConnected'),
	    aNext: N('nextSibling').get.call(a).nodeType, bPrev: N('previousSibling').get.call(b).nodeType,
	    own: Object.getOwnPropertyNames(DocumentFragment.prototype).filter(function (n) { return /^(firstChild|childNodes|host|mode)$/.test(n); }).join()
	  };
	})()`, &r)
	if r["first"] != true || r["last"] != true || r["kids"] != 3.0 || r["parent"] != nil || r["sib"] != nil || r["type"] != 11.0 ||
		r["name"] != "#document-fragment" || r["text"] != "t" || r["conn"] != false || r["aNext"] != 3.0 || r["bPrev"] != 3.0 || r["own"] != "" {
		t.Fatalf("fragment accessors via Node.prototype: %v", r)
	}
}

// host and mode are ShadowRoot's, not DocumentFragment's: a script that subclasses DocumentFragment (the Shady DOM
// polyfill's own ShadowRoot) assigns this.host, which a getter-only accessor inherited from DocumentFragment swallowed
// (the shadow root then had host === null and attachShadow threw an empty-looking TypeError).
func TestShadowRootHostIsAssignableOnSubclasses(t *testing.T) {
	tab := polyTab(t, `<div id="h"></div>`)
	var r map[string]any
	evalJSON(t, tab, `(function(){
	  function Poly() {} Poly.prototype = Object.create(DocumentFragment.prototype);
	  var f = document.createDocumentFragment(); Object.setPrototypeOf(f, Poly.prototype);
	  var h = document.getElementById('h'); f.host = h; f.mode = 'open';
	  return {assigned: f.host === h && f.mode === 'open', notOnFragment: !('host' in DocumentFragment.prototype) && !('mode' in DocumentFragment.prototype), onShadowRoot: 'host' in ShadowRoot.prototype && 'mode' in ShadowRoot.prototype};
	})()`, &r)
	if r["assigned"] != true || r["notOnFragment"] != true || r["onShadowRoot"] != true {
		t.Fatalf("ShadowRoot host: %v", r)
	}
}

// NamedNodeMap is array-like for the Array.prototype methods: map/forEach/slice.call(el.attributes) skip an index that
// `in` denies, and used to return arrays of holes.
func TestAttributesAreArrayLike(t *testing.T) {
	tab := polyTab(t, `<div id="d" a="1" data-b="2"></div>`)
	var r map[string]any
	evalJSON(t, tab, `(function(){
	  var at = document.getElementById('d').attributes;
	  return {
	    map: Array.prototype.map.call(at, function (x) { return x.name + '=' + x.value; }).join('|'),
	    slice: Array.prototype.slice.call(at).length, from: Array.from(at).length,
	    has: (0 in at) && !(3 in at), keys: Object.keys(at).join(), len: at.length,
	    forEach: (function () { var n = []; Array.prototype.forEach.call(at, function (x) { n.push(x.name); }); return n.join(); })()
	  };
	})()`, &r)
	if r["map"] != "id=d|a=1|data-b=2" || r["slice"] != 3.0 || r["from"] != 3.0 || r["has"] != true || r["keys"] != "0,1,2" || r["len"] != 3.0 || r["forEach"] != "id,a,data-b" {
		t.Fatalf("attributes as an array-like: %v", r)
	}
}

// Template contents are inert: a custom element inside <template> is not constructed when a script walks
// template.content, and nodes made by the template's owner document are not upgraded either; importNode into the
// document upgrades them. Polymer parses its templates this way, with bindings such as items="[[x]]" still in the
// attributes, and a constructed element would deserialise them (and run its observers).
func TestTemplateContentIsInert(t *testing.T) {
	tab := polyTab(t, `<template id="t"><x-widget a="[[b]]"><x-widget></x-widget></x-widget></template>`)
	var r map[string]any
	evalJSON(t, tab, `(function(){
	  var made = [];
	  customElements.define('x-widget', class extends HTMLElement { constructor() { super(); made.push(this.getAttribute('a')); } });
	  var t = document.getElementById('t');
	  var first = t.content.firstChild;
	  var walked = t.content.querySelectorAll('x-widget').length;
	  var owner = first.ownerDocument;
	  var viaOwner = owner.createElement('x-widget');
	  var afterWalk = made.length, ownerIsDoc = owner === document;
	  var copy = document.importNode(t.content, true);
	  var upgraded = copy.firstChild instanceof customElements.get('x-widget');
	  document.body.appendChild(viaOwner);
	  var adopted = viaOwner instanceof customElements.get('x-widget');
	  return {afterWalk: afterWalk, walked: walked, ownerIsDoc: ownerIsDoc, firstUp: first instanceof customElements.get('x-widget'), upgraded: upgraded, adopted: adopted, importedBinding: copy.firstChild.getAttribute('a')};
	})()`, &r)
	if r["afterWalk"] != 0.0 || r["walked"] != 2.0 || r["ownerIsDoc"] != false || r["firstUp"] != false || r["upgraded"] != true || r["adopted"] != true || r["importedBinding"] != "[[b]]" {
		t.Fatalf("inert template content: %v", r)
	}
}

// Properties exist on the interfaces that own them, not on every HTMLElement. Resin-style sanitisers and feature tests
// probe `prop in element`: 'labels', 'form' and 'validity' belong to form controls, 'data' to <object>, 'href' to links,
// and the `is` value passed to createElement is not an attribute.
func TestPropertiesLiveOnTheirOwnInterfaces(t *testing.T) {
	tab := polyTab(t, `<form id="f"><input id="i"><label id="l" for="i">x</label></form>`)
	var r map[string]any
	evalJSON(t, tab, `(function(){
	  var div = document.createElement('div'), inp = document.getElementById('i'), custom = document.createElement('x-thing');
	  var isEl = document.createElement('div', {is: 'x-y'});
	  custom.is = 'x-thing';
	  return {
	    divNo: ['labels', 'form', 'validity', 'willValidate', 'checkValidity', 'data', 'href', 'src', 'value', 'name', 'type', 'is', 'alt', 'disabled'].filter(function (p) { return p in div; }).join(),
	    customNo: ['labels', 'data', 'href', 'page', 'pageSubtype'].filter(function (p) { return p in custom; }).join(),
	    inputYes: ['labels', 'form', 'validity', 'willValidate', 'checkValidity', 'setCustomValidity', 'name', 'type', 'disabled', 'value'].filter(function (p) { return !(p in inp); }).join(),
	    labelForm: document.getElementById('l').form === document.getElementById('f'), inputForm: inp.form === document.getElementById('f'), labels: inp.labels.length,
	    global: ['title', 'hidden', 'tabIndex', 'lang', 'dir', 'draggable', 'popover', 'inert'].filter(function (p) { return !(p in div); }).join(),
	    isAttr: isEl.getAttribute('is'), isProp: custom.getAttribute('is'),
	    a: ['href', 'target', 'rel', 'download'].filter(function (p) { return !(p in document.createElement('a')); }).join(),
	    img: ['src', 'alt', 'width', 'srcset'].filter(function (p) { return !(p in document.createElement('img')); }).join()
	  };
	})()`, &r)
	if r["divNo"] != "" || r["customNo"] != "" || r["inputYes"] != "" || r["labelForm"] != true || r["inputForm"] != true || r["labels"] != 1.0 ||
		r["global"] != "" || r["isAttr"] != nil || r["isProp"] != nil || r["a"] != "" || r["img"] != "" {
		t.Fatalf("property placement: %v", r)
	}
}

// A same-origin iframe sandboxed without allow-scripts still has a window the embedder can read (a page parks an
// <iframe sandbox="allow-same-origin" src="about:blank"> to borrow its History object): contentWindow works. The frame's
// own scripts and inline handlers stay blocked.
func TestSandboxedFrameWindowIsReachable(t *testing.T) {
	tab := polyTab(t, `<p>host</p>`)
	var r map[string]any
	evalJSON(t, tab, `(function(){
	  var blank = document.createElement('iframe'); blank.sandbox = 'allow-same-origin'; blank.src = 'about:blank'; document.body.appendChild(blank);
	  var out = {};
	  try { var h = blank.contentWindow.history; out.blankHistory = typeof h.pushState + '/' + typeof h.replaceState; } catch (e) { out.blankErr = e.name + ': ' + e.message; }
	  try {
	    // borrow the frame's pristine method and bind it to this window's own history, as YouTube's navigation code does
	    var borrowed = blank.contentWindow.history.replaceState.bind(window.history);
	    borrowed({borrowed: 1}, '', location.href);
	    out.borrowedState = window.history.state && window.history.state.borrowed === 1 && blank.contentWindow.history.state === null;
	  } catch (e) { out.borrowedErr = e.name + ': ' + e.message; }
	  var f = document.createElement('iframe'); f.id = 'fr'; f.sandbox = 'allow-same-origin'; f.src = '/frame'; document.body.appendChild(f);
	  return out;
	})()`, &r)
	if r["blankHistory"] != "function/function" || r["blankErr"] != nil || r["borrowedErr"] != nil || r["borrowedState"] != true {
		t.Fatalf("sandboxed about:blank frame: %v", r)
	}
	// the frame loads on its own goroutine: a fixed 500 ms settle was not always enough on a loaded machine
	// (the test failed about one run in ten under the full suite); wait for the frame load itself
	tab.Settle(100 * time.Millisecond)
	if !tab.WaitFrames(10 * time.Second) {
		t.Fatal("the sandboxed frame did not finish loading")
	}
	tab.Settle(50 * time.Millisecond)
	evalJSON(t, tab, `(function(){
	  var w = document.getElementById('fr').contentWindow, out = {};
	  try { out.ran = w.ran === undefined ? 'blocked' : 'ran'; out.text = w.document.getElementById('p').textContent; w.document.getElementById('p').click(); out.clicked = w.clicked === undefined ? 'blocked' : 'ran'; } catch (e) { out.err = e.name + ': ' + e.message; }
	  return out;
	})()`, &r)
	if r["err"] != nil || r["ran"] != "blocked" || r["text"] != "frame" || r["clicked"] != "blocked" {
		t.Fatalf("sandboxed frame without allow-scripts: %v", r)
	}
}

// The Popover API: a closed popover is not rendered, showPopover/hidePopover/togglePopover drive :popover-open and
// the beforetoggle/toggle events, an auto popover closes the others and light-dismisses on Escape, a popovertarget
// button invokes it, and a popover removed from the document closes quietly.
func TestPopoverAPI(t *testing.T) {
	tab := polyTab(t, `<style>#p1,#p2,#m { width: 100px; height: 40px }</style><div id="p1" popover>one</div><div id="p2" popover="auto">two</div><div id="m" popover="manual">man</div><button id="b" popovertarget="p1">t</button><p id="plain">x</p>`)
	var r map[string]any
	evalJSON(t, tab, `(function(){
	  var p1 = document.getElementById('p1'), p2 = document.getElementById('p2'), m = document.getElementById('m'), out = {};
	  var shown = function (e) { return getComputedStyle(e).display !== 'none' && e.getBoundingClientRect().width > 0; };
	  out.initial = [shown(p1), shown(p2), shown(m), p1.matches(':popover-open')].join();
	  out.types = [p1.popover, p2.popover, m.popover, document.getElementById('plain').popover].join();
	  window.ev = []; ['beforetoggle', 'toggle'].forEach(function (n) { p1.addEventListener(n, function (e) { ev.push(n + ':' + e.oldState + '>' + e.newState + ':' + e.constructor.name); }); });
	  p1.showPopover();
	  out.afterShow = [shown(p1), p1.matches(':popover-open')].join();
	  try { document.getElementById('plain').showPopover(); out.plain = 'no throw'; } catch (e) { out.plain = e.name; }
	  return out;
	})()`, &r)
	if r["initial"] != "false,false,false,false" || r["types"] != "auto,auto,manual," || r["afterShow"] != "true,true" || r["plain"] != "NotSupportedError" {
		t.Fatalf("popover state: %v", r)
	}
	tab.Settle(60 * time.Millisecond)
	evalJSON(t, tab, `(function(){
	  var p1 = document.getElementById('p1'), p2 = document.getElementById('p2'), m = document.getElementById('m'), out = {};
	  out.events = ev.join();
	  p2.showPopover();
	  out.autoClosesOther = [p1.matches(':popover-open'), p2.matches(':popover-open')].join();
	  m.showPopover(); p2.showPopover();
	  out.manualStays = m.matches(':popover-open');
	  document.dispatchEvent(new KeyboardEvent('keydown', {key: 'Escape', bubbles: true}));
	  out.escape = [p2.matches(':popover-open'), m.matches(':popover-open')].join();
	  m.hidePopover(); out.toggle = [p1.togglePopover(), p1.togglePopover(), p1.togglePopover(true), p1.togglePopover(false)].join();
	  document.getElementById('b').click();
	  out.invoker = p1.matches(':popover-open');
	  p1.remove(); out.removed = p1.matches(':popover-open');
	  return out;
	})()`, &r)
	if !strings.HasPrefix(r["events"].(string), "beforetoggle:closed>open:BeforeToggleEvent,toggle:closed>open:ToggleEvent") || r["autoClosesOther"] != "false,true" || r["manualStays"] != true ||
		r["escape"] != "false,true" || r["toggle"] != "true,false,true,false" || r["invoker"] != true || r["removed"] != false {
		t.Fatalf("popover behaviour: %v", r)
	}
}

// The console says what was thrown: an Error as "Name: message" plus its stack (the engine's stacks have no header,
// which made YouTube's failure read as an empty "Uncaught"), and a non-Error value with its type.
func TestUncaughtReportsThrownValueAndType(t *testing.T) {
	tab := polyTab(t, `<p>x</p>`)
	evalJSON(t, tab, `(function(){
	  setTimeout(function () { throw new TypeError('boom here'); }, 0);
	  setTimeout(function () { throw 'a plain string'; }, 0);
	  setTimeout(function () { throw 42; }, 0);
	  setTimeout(function () { throw {code: 7}; }, 0);
	  setTimeout(function () { throw null; }, 0);
	  setTimeout(function () { var e = new Error(''); throw e; }, 0);
	  return 1; })()`, nil)
	tab.Settle(100 * time.Millisecond)
	var all []string
	for _, ev := range tab.ConsoleLog(0) {
		if m, ok := ev.Data.(map[string]string); ok && strings.HasPrefix(m["text"], "Uncaught") {
			all = append(all, m["text"])
		}
	}
	joined := strings.Join(all, "\n---\n")
	for _, want := range []string{
		"Uncaught TypeError: boom here\n    at ",
		`Uncaught non-Error string thrown: "a plain string"`,
		"Uncaught non-Error number thrown: 42",
		`Uncaught non-Error object thrown (Object): {"code":7}`,
		"Uncaught non-Error object thrown: null",
		"Uncaught Error: \n    at ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("console lacks %q in:\n%s", want, joined)
		}
	}
}
