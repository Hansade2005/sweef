package ui_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io/fs"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/ui"
	"github.com/sweefbrowser/sweef/internal/ui/preview"
)

var refRe = regexp.MustCompile(`(?:src|href)="([^"#]+\.(?:js|css))"`)

const (
	// Budgets (uncompressed bytes). The chrome page is parsed on every start, so its eager set
	// (what chrome/index.html references) is held to the 150 KB aim; the sidebar, palette and the
	// data popover load lazily. Other pages load on demand. The total is a ceiling on everything embedded.
	//
	// The total was raised from 245 KB to 295 KB on purpose for the Data Saver UI (popover, Settings ->
	// Data with its plain-language level cards, the hard-stop page, the onboarding step, about 46 KB, of
	// which about 9 KB are copy in strings.js). The chrome page's eager set went from about 122 KB to
	// about 134 KB: the meter chip is eager (tiny), the popover is lazy, and the copy deck is shared.
	budgetPage  = 150 * 1024
	budgetTotal = 295 * 1024
)

// pageWeight sums the JS and CSS an HTML page loads eagerly.
func pageWeight(t *testing.T, html string) int {
	b, err := fs.ReadFile(ui.FS, html)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range refRe.FindAllStringSubmatch(string(b), -1) {
		ref := m[1]
		target := ref[1:]
		if !strings.HasPrefix(ref, "/") {
			target = filepath.ToSlash(filepath.Join(filepath.Dir(html), ref))
		}
		d, err := fs.ReadFile(ui.FS, target)
		if err != nil {
			t.Fatalf("%s: %s: %v", html, ref, err)
		}
		n += len(d)
	}
	return n
}

func TestEmbeddedBudget(t *testing.T) {
	var jscss, total int
	err := fs.WalkDir(ui.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(ui.FS, p)
		if err != nil {
			return err
		}
		total += len(b)
		if ext := filepath.Ext(p); ext == ".js" || ext == ".css" {
			jscss += len(b)
		}
		if strings.HasSuffix(p, ".html") {
			w := pageWeight(t, p)
			t.Logf("%-28s eager JS+CSS %6d B", p, w)
			if w > budgetPage {
				t.Errorf("%s loads %d bytes of JS+CSS eagerly, budget %d", p, w, budgetPage)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("embedded: js+css=%d B, everything=%d B (budget %d)", jscss, total, budgetTotal)
	if total > budgetTotal {
		t.Errorf("embedded UI is %d bytes, budget %d", total, budgetTotal)
	}
}

func TestRoute(t *testing.T) {
	cases := map[string]string{
		"sweef://newtab":                  "newtab/index.html",
		"sweef://settings/":               "settings/index.html",
		"sweef://settings/?s=agent#x":     "settings/index.html",
		"sweef://chrome/overlay.html":     "chrome/overlay.html",
		"sweef://settings/settings.js":    "settings/settings.js",
		"sweef://history/shared/base.css": "shared/base.css",
		"sweef://ui/shared/tokens.css":    "shared/tokens.css",
		"sweef://newtab/../../etc/passwd": "newtab/etc/passwd",
	}
	for in, want := range cases {
		u, _ := url.Parse(in)
		got, ok := ui.Route(u)
		if !ok || got != want {
			t.Errorf("Route(%s) = %q,%v want %q", in, got, ok, want)
		}
		if ok {
			if _, err := fs.Stat(ui.FS, got); err != nil && !strings.Contains(in, "passwd") {
				t.Errorf("Route(%s) -> %s does not exist in FS", in, got)
			}
		}
	}
	for _, bad := range []string{"https://newtab/", "sweef://nope/", "sweef://ui"} {
		u, _ := url.Parse(bad)
		if p, ok := ui.Route(u); ok {
			t.Errorf("Route(%s) should fail, got %s", bad, p)
		}
	}
}

// Every HTML page's references must exist in FS (absolute /shared/... or page-relative).
func TestReferencesResolve(t *testing.T) {
	_ = fs.WalkDir(ui.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		b, _ := fs.ReadFile(ui.FS, p)
		for _, m := range refRe.FindAllStringSubmatch(string(b), -1) {
			ref := m[1]
			var target string
			if strings.HasPrefix(ref, "/") {
				target = ref[1:]
			} else {
				target = filepath.ToSlash(filepath.Join(filepath.Dir(p), ref))
			}
			if _, err := fs.Stat(ui.FS, target); err != nil {
				t.Errorf("%s references %s which does not exist", p, ref)
			}
		}
		return nil
	})
}

func TestPagesRenderWithoutErrors(t *testing.T) {
	srv := preview.Server()
	defer srv.Close()
	e := preview.Engine()
	defer e.Close()
	for _, sc := range preview.Scenarios() {
		sc := sc
		if sc.Skip {
			continue
		}
		t.Run(sc.Name, func(t *testing.T) {
			r, err := preview.Render(e, srv.URL, sc, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range r.Errors {
				t.Errorf("page error: %s", m)
			}
			img, err := png.Decode(bytes.NewReader(r.PNG))
			if err != nil {
				t.Fatalf("png: %v", err)
			}
			b := img.Bounds()
			seen := map[uint32]int{}
			for y := b.Min.Y; y < b.Max.Y; y += 7 {
				for x := b.Min.X; x < b.Max.X; x += 7 {
					r, g, bl, _ := img.At(x, y).RGBA()
					seen[(r>>10)<<12|(g>>10)<<6|bl>>10]++
				}
			}
			if len(seen) < 8 {
				t.Errorf("page looks blank: only %d distinct colours", len(seen))
			}
		})
	}
}

// ---- behaviour -----------------------------------------------------------

type rig struct {
	t   *testing.T
	e   *engine.Engine
	srv string
}

func newRig(t *testing.T) (*rig, func()) {
	srv := preview.Server()
	e := preview.Engine()
	return &rig{t: t, e: e, srv: srv.URL}, func() { e.Close(); srv.Close() }
}

func (r *rig) open(name string) *preview.Session {
	r.t.Helper()
	for _, sc := range preview.Scenarios() {
		if sc.Name == name {
			s, err := preview.Open(r.e, r.srv, sc, 1)
			if err != nil {
				r.t.Fatal(err)
			}
			return s
		}
	}
	r.t.Fatalf("no scenario %q", name)
	return nil
}

// openSet opens a scenario and also runs its Setup, like the preview does (open alone skips it).
func (r *rig) openSet(name string) *preview.Session {
	r.t.Helper()
	s := r.open(name)
	for _, sc := range preview.Scenarios() {
		if sc.Name == name {
			if sc.Setup != "" {
				s.Run(sc.Setup)
				s.Settle()
			}
			if sc.Then != "" {
				s.Run(sc.Then)
				s.Settle()
			}
		}
	}
	return s
}

func (r *rig) run(s *preview.Session, js string) {
	s.Run(js)
	s.Settle()
}

// post returns the first recorded post of the given type, or nil.
func post(s *preview.Session, typ string) map[string]any {
	for _, p := range s.Posts() {
		if len(p) == 2 && p[0] == typ {
			m, _ := p[1].(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			return m
		}
	}
	return nil
}

func evalS(t *testing.T, s *preview.Session, js string) any {
	t.Helper()
	v, err := s.Eval(js)
	if err != nil {
		t.Fatalf("eval %q: %v", js, err)
	}
	return v
}

const typeInto = `window.__type=function(el,v,k){el.focus();el.value=v;try{el.setSelectionRange(v.length,v.length)}catch(e){};el.dispatchEvent(new Event('input',{bubbles:true}))};
window.__key=function(el,key,o){o=o||{};o.key=key;o.bubbles=true;o.cancelable=true;el.dispatchEvent(new KeyboardEvent('keydown',o))};
window.__md=function(el,t,o){o=o||{};o.bubbles=true;o.cancelable=true;el.dispatchEvent(new MouseEvent(t,o))};`

func TestMarkdownIsSafe(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("chrome-light-empty")
	sample := "<img src=x onerror=alert(1)> <script>alert(1)</script> [x](javascript:alert(1)) [d](data:text/html,hi) **b** ~~s~~ `c<d>` [ok](https://a.com/p?q=1) https://b.org/z\n\n- one\n  - two\n\n1. a\n2. b\n\n| h1 | h2 |\n|---|---|\n| x | y |\n\n```js\n<b>raw</b>\n```"
	q, _ := json.Marshal(sample)
	v := evalS(t, s, `(function(){var f=SW.md.render(`+string(q)+`); var d=document.createElement('div'); d.appendChild(f); return {bad:d.querySelectorAll('img,script,iframe,object,style').length, jsA:d.querySelectorAll('a[href^=javascript],a[href^=data]').length, text:d.textContent, links:d.querySelectorAll('a[data-href]').length, li:d.querySelectorAll('li').length, strong:d.querySelectorAll('strong').length, table:d.querySelectorAll('table td').length, pre:d.querySelectorAll('pre code')[0].textContent}})()`).(map[string]any)
	if v["bad"].(float64) != 0 || v["jsA"].(float64) != 0 {
		t.Errorf("unsafe nodes survived: %v", v)
	}
	if !strings.Contains(v["text"].(string), "<img src=x onerror=alert(1)>") {
		t.Errorf("model text should be shown literally: %v", v["text"])
	}
	if v["links"].(float64) != 2 || v["li"].(float64) != 4 || v["strong"].(float64) != 1 || v["table"].(float64) != 2 || strings.TrimSpace(v["pre"].(string)) != "<b>raw</b>" {
		t.Errorf("markdown structure wrong: %v", v)
	}
	// half-written (streaming) input must not hang or throw
	partial, _ := json.Marshal("**bold and `code [link](http://x ```\n| a | b\n- ")
	evalS(t, s, `(function(){SW.md.render(`+string(partial)+`);return 1})()`)
	if errs := s.Errors(); len(errs) > 0 {
		t.Errorf("errors: %v", errs)
	}
}

func TestOmniboxFlow(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("chrome-light-empty")
	r.run(s, typeInto)
	r.run(s, `__type(document.getElementById('url'),'github')`)
	p := post(s, "omni.input")
	if p == nil || p["text"] == nil {
		t.Fatalf("typing must post omni.input, got %v", s.Posts())
	}
	// rows exist instantly (before Go answers): top hit + Ask row
	if n := evalS(t, s, `document.querySelectorAll('#od .orow').length`).(float64); n < 2 {
		t.Errorf("expected instant local rows, got %v", n)
	}
	// remote answer with inline completion shows ghost text; previous rows stay until replaced
	r.run(s, `__send('omni.suggestions',{id:SW.omniSeq,items:[{type:'history',title:'GitHub',url:'https://github.com/',inline:'github.com/'},{type:'search',text:'github actions'}]})`)
	if g := evalS(t, s, `document.getElementById('ghost').textContent`).(string); !strings.Contains(g, "github.com/") {
		t.Errorf("ghost completion missing: %q", g)
	}
	// Tab accepts the completion without navigating
	s.ClearPosts()
	r.run(s, `__key(document.getElementById('url'),'Tab')`)
	if v := evalS(t, s, `document.getElementById('url').value`).(string); v != "github.com/" {
		t.Errorf("Tab should accept ghost, value=%q", v)
	}
	// Enter navigates the active tab
	s.ClearPosts()
	r.run(s, `__key(document.getElementById('url'),'Enter')`)
	nav := post(s, "nav.go")
	if nav == nil || nav["tab"] != "t2" {
		t.Fatalf("Enter must post nav.go for the active tab, got %v", s.Posts())
	}
	// Ctrl+Enter asks the agent instead of searching
	r2 := r.open("chrome-light-empty")
	r.run(r2, typeInto)
	r.run(r2, `__type(document.getElementById('url'),'how do heat pumps work?')`)
	r2.ClearPosts()
	r.run(r2, `__key(document.getElementById('url'),'Enter',{ctrlKey:true})`)
	ask := post(r2, "agent.send")
	if ask == nil || ask["mode"] != "ask" || !strings.Contains(fmt.Sprint(ask["text"]), "heat pumps") {
		t.Fatalf("Ctrl+Enter must post agent.send, got %v", r2.Posts())
	}
	if post(r2, "nav.go") != nil {
		t.Errorf("Ctrl+Enter must not navigate")
	}
	// Escape reverts the text
	r.run(r2, `var u=document.getElementById('url');u.focus();__type(u,'junk');__key(u,'Escape');__key(u,'Escape')`)
	if v := evalS(t, r2, `document.getElementById('url').value`).(string); !strings.HasPrefix(v, "https://grocer") {
		t.Errorf("Escape should restore the URL, got %q", v)
	}
	// plain words search; a URL goes to nav.go
	r3 := r.open("chrome-light-empty")
	r.run(r3, typeInto)
	r.run(r3, `__type(document.getElementById('url'),'weather lisbon');__key(document.getElementById('url'),'Enter')`)
	if n := post(r3, "nav.go"); n == nil || n["input"] != "weather lisbon" {
		t.Errorf("search text must reach nav.go verbatim, got %v", r3.Posts())
	}
	for _, x := range []*preview.Session{s, r2, r3} {
		if e := x.Errors(); len(e) > 0 {
			t.Errorf("errors: %v", e)
		}
	}
}

func TestTabsAndToolbar(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("chrome-light-chat")
	r.run(s, typeInto)
	if n := evalS(t, s, `document.querySelectorAll('.tab').length`).(float64); n != 7 {
		t.Fatalf("expected 7 tabs, got %v", n)
	}
	r.run(s, `__md(document.querySelector('.tab[data-id=t5]'),'mousedown',{button:0})`)
	if p := post(s, "tab.activate"); p == nil || p["id"] != "t5" {
		t.Errorf("mousedown on tab must activate it: %v", s.Posts())
	}
	s.ClearPosts()
	r.run(s, `document.querySelector('.tab[data-id=t5] .tx').click()`)
	if p := post(s, "tab.close"); p == nil || p["id"] != "t5" {
		t.Errorf("close button: %v", s.Posts())
	}
	s.ClearPosts()
	r.run(s, `__md(document.querySelector('.tab[data-id=t3]'),'mousedown',{button:1})`)
	if p := post(s, "tab.close"); p == nil || p["id"] != "t3" {
		t.Errorf("middle click closes: %v", s.Posts())
	}
	// state.patch (RFC 7386): add a tab, remove one with null, rename another
	r.run(s, `__send('state.patch',{tabs:[{id:'a',title:'Alpha',url:'https://a.test/'},{id:'b',title:'Beta',url:'https://b.test/'}],active:'b'})`)
	if n := evalS(t, s, `document.querySelectorAll('.tab').length`).(float64); n != 2 {
		t.Errorf("patch should replace tabs array, got %v tabs", n)
	}
	if v := evalS(t, s, `document.querySelector('.tab[aria-selected=true]').getAttribute('data-id')`); v != "b" {
		t.Errorf("active tab: %v", v)
	}
	s.ClearPosts()
	r.run(s, `document.getElementById('reload').click();document.getElementById('back').click();document.getElementById('sbtoggle').click()`)
	for _, typ := range []string{"nav.reload", "nav.back", "settings.set"} {
		if post(s, typ) == nil {
			t.Errorf("toolbar should post %s, got %v", typ, s.Posts())
		}
	}
	if e := s.Errors(); len(e) > 0 {
		t.Errorf("errors: %v", e)
	}
}

func TestTabDragReorder(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("chrome-light-chat")
	r.run(s, typeInto)
	// t1 is index 2 (after two pinned tabs); drag it right by ~two tab widths
	r.run(s, `var t=document.querySelector('.tab[data-id=t1]'),b=t.getBoundingClientRect();
	__md(t,'mousedown',{button:0,clientX:b.left+20,clientY:b.top+10});
	__md(window,'mousemove',{clientX:b.left+20+40,clientY:b.top+10});
	__md(window,'mousemove',{clientX:b.left+20+2*(b.width+2)+10,clientY:b.top+10});
	__md(window,'mouseup',{clientX:b.left+20+2*(b.width+2)+10,clientY:b.top+10});`)
	p := post(s, "tab.move")
	if p == nil || p["id"] != "t1" || p["to"] != 4.0 {
		t.Fatalf("drag should post tab.move{id:t1,to:4}, got %v", s.Posts())
	}
	if v := evalS(t, s, `Array.prototype.map.call(document.querySelectorAll('.tab'),function(e){return e.getAttribute('data-id')}).join()`); v != "p1,p2,t2,t3,t1,t4,t5" {
		t.Errorf("DOM order after drop: %v", v)
	}
	// a pinned tab cannot be dragged into the unpinned group
	s.ClearPosts()
	r.run(s, `var t=document.querySelector('.tab[data-id=p2]'),b=t.getBoundingClientRect();
	__md(t,'mousedown',{button:0,clientX:b.left+10,clientY:b.top+10});__md(window,'mousemove',{clientX:b.left+400,clientY:b.top+10});__md(window,'mouseup',{clientX:b.left+400,clientY:b.top+10});`)
	if p := post(s, "tab.move"); p != nil && p["to"].(float64) >= 2 {
		t.Errorf("pinned tab dragged out of its group: %v", p)
	}
	if e := s.Errors(); len(e) > 0 {
		t.Errorf("errors: %v", e)
	}
}

func TestSidebarResizePersists(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("chrome-light-chat")
	r.run(s, typeInto)
	r.run(s, `var z=document.getElementById('rz');__md(z,'mousedown',{button:0,clientX:900});__md(window,'mousemove',{clientX:700});__md(window,'mouseup',{clientX:700});`)
	found := false
	for _, p := range s.Posts() {
		if p[0] == "settings.set" {
			m := p[1].(map[string]any)
			if m["key"] == "sidebar.width" {
				found = true
				if w := m["value"].(float64); w < 320 || w > 560 {
					t.Errorf("width %v outside 320..560", w)
				}
			}
		}
	}
	if !found {
		t.Errorf("resize must persist via settings.set sidebar.width, got %v", s.Posts())
	}
	// clamped at both ends
	r.run(s, `var z=document.getElementById('rz');__md(z,'mousedown',{button:0,clientX:900});__md(window,'mousemove',{clientX:0});__md(window,'mouseup',{clientX:0});`)
	if v := evalS(t, s, `document.documentElement.style.getPropertyValue('--sidebar-w')`); v != "560px" {
		t.Errorf("max width should clamp to 560px, got %v", v)
	}
}

func TestSiteInfoVisibilityToggle(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("chrome-light-chat")
	r.run(s, `document.getElementById('secbtn').click();document.querySelector('.pop .mi').click();`)
	r.run(s, `var o=document.querySelectorAll('.vischoice .mi');o[1].click();`)
	p := post(s, "agent.pageVisibility")
	if p == nil || p["allowed"] != false || p["origin"] != "https://www.wsj.com" {
		t.Fatalf("expected agent.pageVisibility{origin,allowed:false}, got %v", s.Posts())
	}
	if v := evalS(t, s, `document.querySelector('.pagechip').className`).(string); !strings.Contains(v, "off") {
		t.Errorf("page chip should show blocked state, got %q", v)
	}
	if e := s.Errors(); len(e) > 0 {
		t.Errorf("errors: %v", e)
	}
}

func TestSidekickConversation(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("chrome-light-empty")
	r.run(s, typeInto)
	r.run(s, `var i=document.getElementById('skin');__type(i,'Compare these products');__key(i,'Enter')`)
	snd := post(s, "agent.send")
	if snd == nil || snd["text"] != "Compare these products" || snd["mode"] != "ask" {
		t.Fatalf("Enter should send, got %v", s.Posts())
	}
	if v := evalS(t, s, `document.getElementById('skin').value`); v != "" {
		t.Errorf("composer should clear, got %v", v)
	}
	// Shift+Enter must not send
	s.ClearPosts()
	r.run(s, `var i=document.getElementById('skin');__type(i,'line one');__key(i,'Enter',{shiftKey:true})`)
	if post(s, "agent.send") != nil {
		t.Errorf("Shift+Enter must insert a newline, not send")
	}
	// Send button becomes Stop while running, Stop posts agent.stop
	r.run(s, `__send('agent.event',{runId:'r9',kind:'text',text:'Hello **world**'})`)
	if v := evalS(t, s, `document.getElementById('sksend').className`).(string); !strings.Contains(v, "stop") {
		t.Errorf("send button should be Stop while running: %q", v)
	}
	if v := evalS(t, s, `document.querySelector('.am .txt strong') && document.querySelector('.am .txt strong').textContent`); v != "world" {
		t.Errorf("streamed markdown should render, got %v", v)
	}
	r.run(s, `document.getElementById('sksend').click()`)
	if post(s, "agent.stop") == nil {
		t.Errorf("Stop should post agent.stop: %v", s.Posts())
	}
	// confirm card posts the decision with the exact ids
	s2 := r.open("chrome-light-empty")
	r.run(s2, `SW.sk.send('buy it');__send('agent.event',{runId:'r1',kind:'confirm',id:'c1',title:'Place the order',action:{click:'Place order',total:'$39.98'}})`)
	if v := evalS(t, s2, `document.querySelector('.cf pre').textContent`).(string); !strings.Contains(v, "Place order") || !strings.Contains(v, "$39.98") {
		t.Errorf("confirm card must show the exact action: %q", v)
	}
	r.run(s2, `document.querySelector('.cf .btn.primary').click()`)
	if c := post(s2, "agent.confirm"); c == nil || c["id"] != "c1" || c["approved"] != true || c["runId"] != "r1" {
		t.Errorf("approve: %v", s2.Posts())
	}
	// errors: rate limit and missing key states
	s3 := r.open("chrome-light-empty")
	r.run(s3, `SW.sk.send('x');__send('agent.event',{runId:'r1',kind:'error',code:'rate_limit',retryAfter:20})`)
	if v := evalS(t, s3, `document.querySelector('.errc').textContent`).(string); !strings.Contains(v, "rate limit") {
		t.Errorf("rate limit state: %q", v)
	}
	s4 := r.open("chrome-light-empty")
	r.run(s4, `SW.sk.send('x');__send('agent.event',{runId:'r1',kind:'error',code:'auth'})`)
	r.run(s4, `document.querySelector('.errc .btn').click()`)
	if p := post(s4, "tab.new"); p == nil || !strings.Contains(fmt.Sprint(p["url"]), "settings") {
		t.Errorf("auth error should offer to open settings: %v", s4.Posts())
	}
	// starters depend on page type; clicking one sends
	if v := evalS(t, s, `1`); v == nil {
		t.Fatal("eval")
	}
	s5 := r.open("chrome-light-empty")
	if v := evalS(t, s5, `document.querySelector('.starter b').textContent`); v != "Compare options" {
		t.Errorf("shop starters expected, got %v", v)
	}
	r.run(s5, `__send('state.patch',{page:{type:'form'}})`)
	if v := evalS(t, s5, `document.querySelector('.starter b').textContent`); v != "Fill it for me" {
		t.Errorf("form starters expected after page change, got %v", v)
	}
	for _, x := range []*preview.Session{s, s2, s3, s4, s5} {
		if e := x.Errors(); len(e) > 0 {
			t.Errorf("errors: %v", e)
		}
	}
}

func TestPaletteFindAndShortcuts(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("chrome-light-chat")
	r.run(s, typeInto)
	r.run(s, `__key(document.body,'k',{ctrlKey:true})`)
	if n := evalS(t, s, `document.querySelectorAll('#pal').length`); n != 1.0 {
		t.Fatalf("Ctrl+K should open the palette")
	}
	r.run(s, `var i=document.querySelector('#pal input');__type(i,'lo-fi');`)
	s.ClearPosts()
	r.run(s, `__key(document.querySelector('#pal input'),'Enter')`)
	if p := post(s, "tab.activate"); p == nil || p["id"] != "t3" {
		t.Errorf("palette should switch to the matching tab: %v", s.Posts())
	}
	if n := evalS(t, s, `document.querySelectorAll('#pal').length`); n != 0.0 {
		t.Errorf("palette should close after running")
	}
	r.run(s, `__key(document.body,'f',{ctrlKey:true})`)
	if v := evalS(t, s, `document.getElementById('find').className`).(string); strings.Contains(v, "hide") {
		t.Errorf("Ctrl+F should show the find bar")
	}
	s.ClearPosts()
	r.run(s, `__type(document.querySelector('#find input'),'bracket')`)
	if p := post(s, "find.query"); p == nil || p["text"] != "bracket" {
		t.Errorf("find.query: %v", s.Posts())
	}
	r.run(s, `__send('find.result',{count:7,index:2})`)
	if v := evalS(t, s, `document.querySelector('#find .cnt').textContent`); v != "2/7" {
		t.Errorf("find count: %v", v)
	}
	r.run(s, `__send('ui.command',{name:'sidebar.toggle'})`)
	if post(s, "settings.set") == nil {
		t.Errorf("ui.command sidebar.toggle should persist via settings.set")
	}
	if e := s.Errors(); len(e) > 0 {
		t.Errorf("errors: %v", e)
	}
}

func TestNewTabOnboardingSettingsOverlay(t *testing.T) {
	r, done := newRig(t)
	defer done()
	// new tab: a question asks the agent, a URL navigates, keyed to the sender's tab
	s := r.open("newtab-light")
	r.run(s, typeInto)
	r.run(s, `__type(document.getElementById('q'),'explain how heat pumps work');__key(document.getElementById('q'),'Enter')`)
	if p := post(s, "agent.send"); p == nil || p["source"] != "newtab" {
		t.Errorf("natural-language question on the new tab should ask: %v", s.Posts())
	}
	s.ClearPosts()
	r.run(s, `__type(document.getElementById('q'),'example.org/path');__key(document.getElementById('q'),'Enter')`)
	if p := post(s, "nav.go"); p == nil || p["input"] != "example.org/path" {
		t.Errorf("URL should navigate: %v", s.Posts())
	}
	if n := evalS(t, s, `document.querySelectorAll('.site').length`); n != 6.0 {
		t.Errorf("top sites tiles: %v", n)
	}
	if v := evalS(t, s, `document.getElementById('stats').textContent`).(string); !strings.Contains(v, "38 MB") {
		t.Errorf("stats card: %q", v)
	}
	noStats := r.open("newtab-light")
	r.run(noStats, `__send('state.patch',{stats:null})`)
	if v := evalS(t, noStats, `document.getElementById('stats').className`).(string); !strings.Contains(v, "hide") {
		t.Errorf("stats must not render without data")
	}

	// onboarding
	o := r.open("onboarding-1")
	r.run(o, `__send("import.unavailable",{source:"safari"})`)
	r.run(o, `document.querySelectorAll('.src')[1].click()`)
	if p := post(o, "import.start"); p == nil || p["source"] != "edge" {
		t.Errorf("import.start: %v", o.Posts())
	}
	if v := evalS(t, o, `document.querySelectorAll('.src')[3].getAttribute('aria-disabled')`); v != "true" {
		t.Errorf("import.unavailable should disable the card: %v", v)
	}
	r.run(o, `document.getElementById('skip').click()`)
	if post(o, "onboarding.skip") == nil {
		t.Errorf("skip always available")
	}
	o.ClearPosts()
	r.run(o, `__send('onboarding.step',{n:2});var i=document.querySelector('.keybox input');i.value='not a key';document.querySelector('.keybox .btn.primary').click()`)
	if post(o, "agent.setKey") != nil {
		t.Errorf("invalid key must not be posted")
	}
	r.run(o, `var i=document.querySelector('.keybox input');i.value='sk-ant-api03-abcdefghijklmnop';document.querySelector('.keybox .btn.primary').click()`)
	if p := post(o, "agent.setKey"); p == nil || p["key"] != "sk-ant-api03-abcdefghijklmnop" {
		t.Errorf("agent.setKey: %v", o.Posts())
	}
	r.run(o, `__send('onboarding.step',{n:4});document.querySelectorAll('.ch')[1].click()`) // step 3 is the data plan, step 4 privacy
	if p := post(o, "settings.set"); p == nil || p["key"] != "privacy.trackers" || p["value"] != "strict" {
		t.Errorf("privacy choice: %v", o.Posts())
	}
	r.run(o, `document.querySelector('#foot .btn.primary').click()`)
	if post(o, "onboarding.done") == nil {
		t.Errorf("finish: %v", o.Posts())
	}

	// settings: a toggle writes settings.set with the key and the new value
	st := r.open("settings-agent")
	r.run(st, `document.querySelector('.switch').click()`)
	if p := post(st, "settings.set"); p == nil || p["key"] != "agent.confirmations" || p["value"] != false {
		t.Errorf("settings toggle: %v", st.Posts())
	}

	// overlay: takeover buttons and click-through rectangles
	ov := r.open("overlay-acting")
	r.run(ov, `__send("overlay",{state:"acting",viewport:{x:0,y:88,w:900,h:712},task:"beach",cursor:{x:10,y:10}})`)
	if p := post(ov, "overlay.hit"); p == nil || len(p["rects"].([]any)) != 1 {
		t.Errorf("overlay must report exactly one interactive rect: %v", ov.Posts())
	}
	r.run(ov, `document.querySelectorAll('#pill button')[0].click();document.querySelector('#pill button.stop').click()`)
	if post(ov, "agent.takeControl") == nil || post(ov, "agent.stop") == nil {
		t.Errorf("overlay buttons: %v", ov.Posts())
	}
	r.run(ov, `__send("overlay",{state:"control",viewport:{x:0,y:88,w:900,h:712}})`)
	r.run(ov, `document.querySelector('#pill button.go').click()`)
	if post(ov, "agent.resume") == nil {
		t.Errorf("hand back: %v", ov.Posts())
	}
	r.run(ov, `__send("overlay",{state:"off"})`)
	if p := post(ov, "overlay.hit"); p == nil {
		t.Errorf("no hit post")
	}
	for _, x := range []*preview.Session{s, noStats, o, st, ov} {
		if e := x.Errors(); len(e) > 0 {
			t.Errorf("errors: %v", e)
		}
	}
}

// Go may send partial snapshots (or nulls) early in start-up: no page may throw.
func TestToleratesSparseState(t *testing.T) {
	r, done := newRig(t)
	defer done()
	states := []string{`{}`, `{"tabs":[],"agent":null,"settings":null,"security":null,"downloads":null,"topSites":null,"stats":null}`, `{"tabs":[{"id":"x"}],"active":"nope","sidebar":{"open":true},"incognito":true,"platform":"windows","agent":{"state":"working"}}`}
	for _, sc := range preview.Scenarios() {
		if sc.Skip {
			continue
		}
		for i, st := range states {
			sc, st := sc, st
			sc.NoState = true
			s, err := preview.Open(r.e, r.srv, sc, 1)
			if err != nil {
				t.Fatal(err)
			}
			s.Run(`__send("state", ` + st + `)`)
			s.Run(`__send("state.patch", {"tabs":[{"id":"y","title":"Y","url":"https://y.test/"}],"agent":{"pageVisibility":"blocked"},"downloads":[{"id":"d","name":"x"}]})`)
			s.Settle()
			for _, m := range s.Errors() {
				t.Errorf("%s with sparse state #%d: %s", sc.Name, i, m)
			}
			s.Tab.Navigate("about:blank", nil)
		}
	}
}

func TestErrorPageActions(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.open("error-dns")
	r.run(s, `var b=document.querySelectorAll('.acts .btn');b[1].click();b[2].click()`)
	if p := post(s, "openExternal"); p == nil || !strings.Contains(fmt.Sprint(p["url"]), "example-typo.test") {
		t.Errorf("Open in system browser: %v", s.Posts())
	}
	if p := post(s, "agent.send"); p == nil || p["source"] != "error" || !strings.Contains(fmt.Sprint(p["text"]), "DNS_NOT_FOUND") {
		t.Errorf("Ask to fix: %v", s.Posts())
	}
}

// ---- Data Saver ------------------------------------------------------------------------

func TestDataPopoverTalksToGo(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.openSet("data-popover-light")
	// the measured and estimated numbers are visibly different things
	rows := evalS(t, s, `Array.prototype.map.call(document.querySelectorAll('.dpop .drow'), function(r){var p=r.querySelector('.dpill');return p.className+'|'+p.textContent+'|'+r.querySelector('.dv').textContent})`).([]any)
	if len(rows) != 3 || !strings.HasPrefix(rows[0].(string), "dpill m|Measured|640 KB") || !strings.HasPrefix(rows[1].(string), "dpill m|Measured|310 KB") || !strings.HasPrefix(rows[2].(string), "dpill e|Estimate|about 1.9 MB") {
		t.Fatalf("rows: %v", rows)
	}
	if n := evalS(t, s, `document.querySelectorAll('.dpop .tsite').length`).(float64); n != 5 {
		t.Errorf("top five sites, got %v", n)
	}
	r.run(s, `document.querySelectorAll('.dpop .dacts .btn')[0].click()`)
	if p := post(s, "data.loadSkipped"); p == nil {
		t.Errorf("Load images must post data.loadSkipped, got %v", s.Posts())
	}
	s.ClearPosts()
	r.run(s, `document.querySelectorAll('.dpop .dseg')[1].querySelectorAll('button')[1].click()`) // Always full
	if p := post(s, "data.siteRule"); p == nil || p["rule"] != "full" || p["origin"] != "https://www.wsj.com" {
		t.Errorf("site rule post: %v", s.Posts())
	}
	s.ClearPosts()
	r.run(s, `document.querySelectorAll('.dpop .dseg')[0].querySelectorAll('button')[2].click()`) // Text first
	if p := post(s, "data.setLevel"); p == nil || p["level"] != "strict" {
		t.Errorf("level post: %v", s.Posts())
	}
	s.ClearPosts()
	r.run(s, `document.querySelectorAll('.dpop .dacts .btn')[1].click()`)
	if post(s, "data.loadFully") == nil {
		t.Errorf("Load fully, once must post data.loadFully, got %v", s.Posts())
	}
	if e := s.Errors(); len(e) > 0 {
		t.Errorf("errors: %v", e)
	}
	// a private window never lists the sites seen in normal ones
	p := r.openSet("data-popover-incognito")
	if n := evalS(t, p, `document.querySelectorAll('.dpop .tsite').length`).(float64); n != 0 {
		t.Errorf("private popover lists sites: %v", n)
	}
}

func TestDataChipToneIsCalmUntilWarned(t *testing.T) {
	r, done := newRig(t)
	defer done()
	cls := func(name string) string {
		s := r.openSet(name)
		return evalS(t, s, `document.getElementById('databtn').className+'|'+document.getElementById('databtn').textContent`).(string)
	}
	if c := cls("data-popover-light"); strings.Contains(c, " w1") || strings.Contains(c, " w2") || strings.Contains(c, " x") || !strings.Contains(c, "39 MB / 100 MB") {
		t.Errorf("39%%: %q", c)
	}
	if c := cls("data-popover-warn"); !strings.Contains(c, " w1") || strings.Contains(c, " x") {
		t.Errorf("78%%: %q", c)
	}
	if c := cls("data-popover-exhausted"); !strings.Contains(c, " x") {
		t.Errorf("exhausted: %q", c)
	}
	// no budget: only what was used, no limit and no fill
	if c := cls("data-popover-nolimit"); strings.Contains(c, "/") || !strings.Contains(c, "datachip") {
		t.Errorf("unlimited: %q", c)
	}
	// the chip hides when the meter is switched off or counting is unavailable
	s := r.openSet("data-popover-light")
	r.run(s, `__send('state.patch',{data:{showMeter:false}})`)
	if v := evalS(t, s, `document.getElementById('databtn').classList.contains('hide')`); v != true {
		t.Errorf("showMeter=false should hide the chip")
	}
	r.run(s, `__send('state.patch',{data:{showMeter:true,available:false}})`)
	if v := evalS(t, s, `document.getElementById('databtn').classList.contains('hide')`); v != true {
		t.Errorf("counting unavailable should hide the chip (no fake zeros)")
	}
}

func TestDataSettingsControls(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.openSet("data-settings-light")
	if n := evalS(t, s, `document.querySelectorAll('.lvl').length`).(float64); n != 3 {
		t.Fatalf("level cards: %v", n)
	}
	text := evalS(t, s, `document.body.innerText`).(string)
	for _, want := range []string{"Counts the data you use", "Does not send pages through another company's servers", "Pauses background tabs", "Shows no images until you tap them", "What it does not do", "Never added", "Measured", "Estimate", "Reset counter", "maps.example.com"} {
		if !strings.Contains(text, want) {
			t.Errorf("settings lacks %q", want)
		}
	}
	click := func(js string) { s.ClearPosts(); r.run(s, js) }
	click(`document.querySelectorAll('.pchip')[1].click()`)
	if p := post(s, "data.setBudget"); p == nil || p["limitMB"] != float64(500) || p["enabled"] != true {
		t.Errorf("500 MB preset: %v", s.Posts())
	}
	click(`document.querySelectorAll('.pchip')[2].click()`)
	if p := post(s, "data.setBudget"); p == nil || p["limitMB"] != float64(1024) {
		t.Errorf("1 GB preset: %v", s.Posts())
	}
	click(`document.querySelectorAll('.pchip')[3].click()`)
	if p := post(s, "data.setBudget"); p == nil || p["enabled"] != false {
		t.Errorf("No limit: %v", s.Posts())
	}
	click(`document.querySelectorAll('.lvl')[2].click()`)
	if p := post(s, "data.setLevel"); p == nil || p["level"] != "strict" {
		t.Errorf("level card: %v", s.Posts())
	}
	// period, reset day and hard stop
	r2 := r.openSet("data-settings-light")
	r.run(r2, `__send('state.patch',{data:{budget:{enabled:true}}})`)
	r2.ClearPosts()
	r.run(r2, `Array.prototype.filter.call(document.querySelectorAll('.seg button'), function(b){return b.textContent==='Week'})[0].click()`)
	if p := post(r2, "data.setBudget"); p == nil || p["period"] != "week" {
		t.Errorf("period: %v", r2.Posts())
	}
	if v := evalS(t, r2, `document.querySelector('select.field').options[1].textContent`); v != "Monday" {
		t.Errorf("week reset day should be a weekday, got %v", v)
	}
	r2.ClearPosts()
	r.run(r2, `document.querySelector('.switch[aria-disabled]') ? 0 : 0; var sw=document.querySelectorAll('.srow .switch')[0]; sw.click()`)
	if p := post(r2, "data.setBudget"); p == nil || p["hardStop"] != true {
		t.Errorf("hard stop: %v", r2.Posts())
	}
	// reset asks first
	r2.ClearPosts()
	r.run(r2, `Array.prototype.filter.call(document.querySelectorAll('button'), function(b){return b.textContent==='Reset counter'})[0].click()`)
	if post(r2, "data.reset") != nil {
		t.Errorf("reset must ask for confirmation first")
	}
	r.run(r2, `Array.prototype.filter.call(document.querySelectorAll('button'), function(b){return b.textContent==='Yes, reset'})[0].click()`)
	if post(r2, "data.reset") == nil {
		t.Errorf("confirmed reset should post data.reset")
	}
	// forgetting a site rule
	r2.ClearPosts()
	r.run(r2, `Array.prototype.filter.call(document.querySelectorAll('button'), function(b){return b.textContent==='Forget'})[0].click()`)
	if p := post(r2, "data.siteRule"); p == nil || p["rule"] != "" {
		t.Errorf("forget: %v", r2.Posts())
	}
	for _, x := range []*preview.Session{s, r2} {
		if e := x.Errors(); len(e) > 0 {
			t.Errorf("errors: %v", e)
		}
	}
}

func TestDataStopPage(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.openSet("data-stop-light")
	text := evalS(t, s, `document.body.innerText`).(string)
	for _, want := range []string{"You have used your 100 MB this month", "resets in 19 days", "grocer.example.com", "Load this page once", "Raise limit", "Wait until reset"} {
		if !strings.Contains(text, want) {
			t.Errorf("stop page lacks %q in %q", want, text)
		}
	}
	for _, bad := range []string{"!", "warning", "careful", "stop"} {
		if strings.Contains(strings.ToLower(text), bad) && bad != "stop" {
			t.Errorf("copy should stay calm, found %q", bad)
		}
	}
	s.ClearPosts()
	r.run(s, `document.querySelectorAll('.acts .btn')[0].click()`)
	if p := post(s, "data.loadOnce"); p == nil || !strings.HasPrefix(fmt.Sprint(p["url"]), "https://grocer.example.com") {
		t.Errorf("load once: %v", s.Posts())
	}
	s.ClearPosts()
	r.run(s, `document.querySelectorAll('.acts .btn')[2].click()`)
	if post(s, "data.wait") == nil {
		t.Errorf("wait: %v", s.Posts())
	}
	// raise limit: presets above the current limit, then raise and reload
	s.ClearPosts()
	r.run(s, `document.querySelectorAll('.acts .btn')[1].click()`)
	if n := evalS(t, s, `document.querySelectorAll('.raise .pchip').length`).(float64); n < 2 {
		t.Fatalf("raise presets: %v", n)
	}
	r.run(s, `document.querySelector('.raise .btn.primary').click()`)
	if p := post(s, "data.setBudget"); p == nil || p["limitMB"] != float64(250) {
		t.Errorf("raise: %v", s.Posts())
	}
	if post(s, "nav.reload") == nil {
		t.Errorf("raising the limit should reload the page: %v", s.Posts())
	}
	if e := s.Errors(); len(e) > 0 {
		t.Errorf("errors: %v", e)
	}
}

func TestOnboardingDataPlanStep(t *testing.T) {
	r, done := newRig(t)
	defer done()
	s := r.openSet("onboarding-3-data-picked")
	if n := evalS(t, s, `document.querySelectorAll('.plan').length`).(float64); n != 4 {
		t.Fatalf("plans: %v", n)
	}
	var seenBudget, seenLevel bool
	for _, p := range s.Posts() {
		if p[0] == "data.setBudget" && p[1].(map[string]any)["limitMB"] == float64(100) {
			seenBudget = true
		}
		if p[0] == "data.setLevel" && p[1].(map[string]any)["level"] == "strict" {
			seenLevel = true
		}
	}
	if !seenBudget || !seenLevel {
		t.Errorf("a 100 MB plan should set the budget and start in Text first: %v", s.Posts())
	}
	if v := evalS(t, s, `document.getElementById('dots').children.length`); v != float64(4) {
		t.Errorf("onboarding should have 4 steps, got %v", v)
	}
	if v := evalS(t, s, `document.querySelector('.plannote').textContent`).(string); !strings.Contains(v, "Starting with Text first") {
		t.Errorf("confirmation line: %q", v)
	}
	if e := s.Errors(); len(e) > 0 {
		t.Errorf("errors: %v", e)
	}
}

func TestNewTabDataLineAndSidekickResearchLine(t *testing.T) {
	r, done := newRig(t)
	defer done()
	nt := r.openSet("data-newtab-light")
	if v := evalS(t, nt, `document.getElementById('dataline').textContent`); v != "61 MB left this month" {
		t.Errorf("new tab line: %q", v)
	}
	r.run(nt, `__send('state.patch',{data:{budget:{enabled:false}}})`)
	if v := evalS(t, nt, `document.getElementById('dataline').classList.contains('hide')`); v != true {
		t.Errorf("no budget: the line must disappear")
	}
	sk := r.openSet("data-sidekick-light")
	if v := evalS(t, sk, `(document.querySelector('.amdata')||{textContent:''}).textContent`).(string); !strings.Contains(v, "Research used 412 KB") || !strings.Contains(v, "About 1.3 MB not loaded (estimate)") {
		t.Errorf("sidekick line: %q", v)
	}
	// when the engine could not count, no line is invented
	r.run(sk, `SW.sk.send("again");__send("agent.event",{runId:"r9",kind:"text",text:"ok"});__send("agent.event",{runId:"r9",kind:"done",ttft:300,data:{bytes:0,available:false}})`)
	if n := evalS(t, sk, `document.querySelectorAll('.amdata').length`).(float64); n != 1 {
		t.Errorf("unavailable counting must not add a line, have %v", n)
	}
	for _, x := range []*preview.Session{nt, sk} {
		if e := x.Errors(); len(e) > 0 {
			t.Errorf("errors: %v", e)
		}
	}
}
