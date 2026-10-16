package engine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression tests for failures an external tester hit driving real sites
// (the-internet.herokuapp.com and friends) through the SDKs. Each fixture
// reproduces the page's mechanism offline.

func serveFixtures(t *testing.T, pages map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		ct := "text/html; charset=utf-8"
		if strings.HasSuffix(r.URL.Path, ".js") {
			ct = "application/javascript"
		}
		w.Header().Set("Content-Type", ct)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func fixtureTab(t *testing.T, base, path string) *Tab {
	t.Helper()
	e := New(DefaultOptions())
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab(base + path)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

// herokuapp /dropdown: the change handler strips every `selected` attribute
// and then reads select.value; selectedness must survive that (option
// dirtiness), so select_option sticks.
const dropdownFixture = `<!doctype html><html><body>
<select id="dropdown">
  <option value="" disabled="disabled" selected="selected"
    >Please select an option</option
  >
  <option value="1">Option 1</option>
  <option value="2">Option 2</option>
  <optgroup label="More"><option value="3">  Option
   Three </option></optgroup>
</select>
<script>
  window.LOG = [];
  var dropdown = document.getElementById('dropdown');
  dropdown.addEventListener('input', function (e) { LOG.push('input:' + e.target.value); });
  dropdown.onchange = function(event) {
    LOG.push('change:' + event.target.value);
    var options = dropdown.getElementsByTagName('option');
    for (var i = 0; i < options.length; i++) {
      options[i].removeAttribute('selected');
    }
    document
      .querySelector("#dropdown option[value='" + event.target.value + "']")
      .setAttribute('selected', 'selected');
  };
</script></body></html>`

func TestSelectOptionSurvivesAttributeRewritingHandler(t *testing.T) {
	base := serveFixtures(t, map[string]string{"/dropdown": dropdownFixture})
	tab := fixtureTab(t, base, "/dropdown")
	state := `(function(){var s=document.getElementById('dropdown');return s.value+'|'+s.selectedIndex+'|'+[].map.call(s.options,function(o){return o.selected?1:0}).join('')+'|'+LOG.join(',')})()`
	for _, tc := range []struct{ pick, want string }{
		{"2", `"2|2|0010|input:2,change:2"`},
		{"Option 1", `"1|1|0100|input:1,change:1"`},
		{"Option Three", `"3|3|0001|input:3,change:3"`},
	} {
		mustEval(t, tab, `LOG.length=0`)
		if err := tab.SelectOption("#dropdown", tc.pick); err != nil {
			t.Fatalf("select %q: %v", tc.pick, err)
		}
		if got := mustEval(t, tab, state); got != tc.want {
			t.Fatalf("select %q: got %s want %s", tc.pick, got, tc.want)
		}
	}
	// Script-side: once dirty, attribute writes don't move the selection,
	// but getAttribute/hasAttribute still report what the script wrote.
	got := mustEval(t, tab, `(function(){var s=document.getElementById('dropdown');s.selectedIndex=1;
		s.options[1].removeAttribute('selected'); s.options[2].setAttribute('selected','');
		return s.selectedIndex+'|'+s.options[1].hasAttribute('selected')+'|'+s.options[2].hasAttribute('selected')+'|'+s.value})()`)
	if got != `"1|false|true|1"` {
		t.Fatalf("dirty option attribute writes: %s", got)
	}
}

// A clean option gaining `selected` in a single select becomes the
// selection, the others keep their attribute.
func TestOptionSelectedAttributeOnCleanSelect(t *testing.T) {
	base := serveFixtures(t, map[string]string{"/s": `<!doctype html><select id=s><option selected>a</option><option>b</option></select>`})
	tab := fixtureTab(t, base, "/s")
	got := mustEval(t, tab, `(function(){var s=document.getElementById('s');s.options[1].setAttribute('selected','');
		return s.selectedIndex+'|'+s.value+'|'+s.options[0].hasAttribute('selected')})()`)
	if got != `"1|b|true"` {
		t.Fatalf("got %s", got)
	}
}

// Page.title (Tab.Title) must match document.title (strip and collapse ASCII
// whitespace) and never hand tab state a control or line-separator character.
func TestTitleStripsAndCollapsesWhitespace(t *testing.T) {
	base := serveFixtures(t, map[string]string{
		"/t": "<!doctype html><title>\n    All products | Books\tto Scrape\r\n</title><p>x",
		"/c": "<!doctype html><title>a b\u0001c\u007f d e</title>",
	})
	tab := fixtureTab(t, base, "/t")
	if got, want := tab.Title(), "All products | Books to Scrape"; got != want {
		t.Fatalf("Title() = %q, want %q", got, want)
	}
	if got := mustEval(t, tab, `document.title`); got != `"All products | Books to Scrape"` {
		t.Fatalf("document.title = %s", got)
	}
	if err := tab.Navigate(base+"/c", nil); err != nil {
		t.Fatal(err)
	}
	if got, want := tab.Title(), "a b c d e"; got != want {
		t.Fatalf("control characters: Title() = %q, want %q", got, want)
	}
	// a title set by script goes through the same path
	mustEval(t, tab, `document.title = "  x\n\ny  "`)
	if got := tab.Title(); got != "x y" {
		t.Fatalf("script title: %q", got)
	}
	if got := CleanTabURL("https://e.test/a b\x01"); got != "https://e.test/a%E2%80%A8b%01" {
		t.Fatalf("CleanTabURL: %q", got)
	}
}

const typingFixture = `<!doctype html><html><head><title>F</title></head><body style="margin:0">
<input id="a" style="position:absolute;left:10px;top:10px;width:200px;height:30px">
<input id="b" style="position:absolute;left:10px;top:60px;width:200px;height:30px">
<input id="skip" tabindex="-1" style="position:absolute;left:300px;top:10px;width:50px;height:30px">
<textarea id="ta" style="position:absolute;left:10px;top:110px;width:200px;height:60px"></textarea>
<div id="ce" contenteditable="true" style="position:absolute;left:10px;top:200px;width:200px;height:40px"></div>
SCRIPT</body></html>`

// Click (by coordinates or selector) then type with no selector: the most
// common agent pattern. It must reach the clicked field with and without a
// script VM, and Tab / Shift+Tab must move focus.
func TestClickThenTypeReachesFocusedField(t *testing.T) {
	base := serveFixtures(t, map[string]string{
		"/vm":   strings.Replace(typingFixture, "SCRIPT", `<script>window.K=[];document.addEventListener('keydown',function(e){K.push(e.key)})</script>`, 1),
		"/novm": strings.Replace(typingFixture, "SCRIPT", "", 1),
	})
	for _, path := range []string{"/vm", "/novm"} {
		t.Run(path, func(t *testing.T) {
			tab := fixtureTab(t, base, path)
			if got, want := tab.hasVM(), path == "/vm"; got != want {
				t.Fatalf("hasVM = %v, want %v", got, want)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(tab.MouseClick(50, 25)) // #a
			must(tab.KeyboardType("AA", 0))
			must(tab.Keyboard("Tab")) // -> #b (#skip has tabindex=-1)
			must(tab.KeyboardType("BB", 0))
			must(tab.Keyboard("Shift+Tab")) // -> #a; a browser selects the field's text on tab focus
			must(tab.Keyboard("Tab"))
			must(tab.MouseClick(50, 130)) // textarea: a newline is text there
			must(tab.KeyboardType("T1\nT2", 0))
			must(tab.MouseClick(50, 215)) // contenteditable
			must(tab.KeyboardType("CE", 0))
			tab.MouseDown(50, 70) // a press alone focuses too (computer-use left_mouse_down/up)
			tab.MouseUp(50, 70)
			must(tab.KeyboardType("+", 0))
			must(tab.Keyboard("Backspace"))
			must(tab.Click("#a"))
			must(tab.KeyboardType("x", 0))
			want := map[string]string{"#a": "AAx", "#b": "BB", "#ta": "T1\nT2"}
			for sel, w := range want {
				bid, err := tab.Locator(sel).BackendID()
				must(err)
				got, _, _ := tab.GetAttributeByBackend(bid, "value")
				if path == "/vm" {
					got = strings.Trim(mustEval(t, tab, `document.querySelector('`+sel+`').value`), `"`)
					got = strings.ReplaceAll(got, `\n`, "\n")
				}
				if got != w {
					t.Errorf("%s = %q, want %q", sel, got, w)
				}
			}
			if txt, _ := tab.Locator("#ce").TextContent(); txt != "CE" {
				t.Errorf("#ce = %q, want CE", txt)
			}
		})
	}
}

// Without a VM, typing with nothing focused is an error (with a VM keys go to
// the body, as in a browser).
func TestKeyboardTypeWithoutFocusNoVM(t *testing.T) {
	base := serveFixtures(t, map[string]string{"/p": strings.Replace(typingFixture, "SCRIPT", "", 1)})
	tab := fixtureTab(t, base, "/p")
	if err := tab.KeyboardType("x", 0); err == nil {
		t.Fatal("typing with nothing focused should fail on a script-less page")
	}
}

// the-internet.herokuapp.com loads an Optimizely snippet whose "Cleanse"
// step walks HTMLElement.prototype and Object.prototype and deletes every
// property whose getter, setter or value does not print as "[native code]"
// (to hide page polyfills while it runs). When the engine's DOM, written in
// JS, printed its source, Cleanse deleted Element.style and the rest: the
// snippet's own jQuery 1.6 support probe threw "cannot read property
// 'opacity' of undefined", jQuery 1.11's support tests threw on cssText,
// and jQuery UI, Foundation and the page's handlers never ran. The fixture
// runs Cleanse's algorithm, then both support probes. The probes also read
// style.opacity back after writing ".5": a value not serialized as "0.5"
// makes jQuery fall back to IE filters.
const supportProbeFixture = `<!doctype html><html><head><script>
window.R = [];
HTMLElement.prototype.pagePolyfill = function () {}; // Cleanse must remove this one
(function () { // optly.Cleanse.start, as shipped
  var native = function (f) { return f && String(f).indexOf("[native code]") !== -1; }, cleansed = 0;
  [HTMLElement, Object].forEach(function (T) {
    var p = T.prototype;
    for (var k in p) if (p.hasOwnProperty(k)) {
      var g = p.__lookupGetter__(k), st = p.__lookupSetter__(k), v = g ? null : p[k];
      if (native(v) || native(g) || native(st)) continue;
      delete p[k]; cleansed++;
    }
  });
  R.push("cleansed:" + cleansed + "," + typeof HTMLElement.prototype.pagePolyfill);
})();
(function () {
  try {
    var o = document.createElement("div");
    o.setAttribute("className", "t");
    o.innerHTML = "   <link/><table></table><a href='/a' style='top:1px;float:left;opacity:.55;'>a</a><input type='checkbox'/>";
    var C = o.getElementsByTagName("a")[0];
    R.push("leading:" + (o.firstChild.nodeType === 3));
    R.push("link:" + !!o.getElementsByTagName("link").length);
    R.push("style:" + /top/.test(C.getAttribute("style")));
    R.push("opacity:" + /^0.55$/.test(C.style.opacity));
    R.push("cssFloat:" + !!C.style.cssFloat);
    var d = document.createElement("div");
    d.innerHTML = "  <link/><table></table><a href='/a'>a</a><input type='checkbox'/>";
    var c = d.getElementsByTagName("a")[0].style;
    c.cssText = "float:left;opacity:.5";
    R.push("cssText:" + (c.opacity === "0.5") + "," + !!c.cssFloat);
    var frag = document.createDocumentFragment(), x = document.createElement("div");
    frag.appendChild(x);
    x.innerHTML = "<textarea>x</textarea><span>s</span>";
    var m = document.createElement("div").style;
    m.cssText = "margin: -.5em 1.50px +2px 0; background: url(a.5.png); transition: opacity .3s";
    R.push("canon:" + m.margin + "/" + m.transitionDuration + m.transition.slice(-5) + "/" + (m.background.indexOf("a.5.png") > 0));
    R.push("frag:" + (typeof x.lastChild.style) + "," + (typeof getComputedStyle(document.documentElement).opacity));
  } catch (e) { R.push("ERR " + e); }
})();
</script></head><body><button onclick="document.getElementById('out').textContent = typeof window.R">go</button><p id="out"></p></body></html>`

func TestInnerHTMLElementsHaveStyle(t *testing.T) {
	base := serveFixtures(t, map[string]string{"/p": supportProbeFixture})
	tab := fixtureTab(t, base, "/p")
	got := mustEval(t, tab, `R.join(" ")`)
	want := `"cleansed:1,undefined leading:true link:true style:true opacity:true cssFloat:true cssText:true,true canon:-0.5em 1.5px 2px 0/ 0.3s/true frag:object,string"`
	if got != want {
		t.Fatalf("support probe:\n got %s\nwant %s", got, want)
	}
	if err := tab.Click("button"); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Locator("#out").TextContent(); txt != "object" {
		t.Fatalf("handlers after the probe: %q", txt)
	}
}
