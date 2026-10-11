package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

const testHTML = `<!doctype html><html><head><title>Hello Sweef</title></head>
<body>
<h1>Welcome</h1>
<a href="/next">More information</a>
<form action="/post" method="post">
  <input name="q" type="text">
  <select name="lang"><option value="go">Go</option><option value="py">Python</option></select>
  <input type="checkbox" name="agree">
  <button type="submit" role="button" aria-label="Send it">OK</button>
</form>
<div id="hidden-div" style="display:none">secret</div>
</body></html>`

func newTestTab(t *testing.T) (*Engine, *Tab) {
	t.Helper()
	e := New(DefaultOptions())
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	tab.SetDocument(testHTML, "https://example.test/")
	return e, tab
}

func TestLocatorEngines(t *testing.T) {
	_, tab := newTestTab(t)
	for _, sel := range []string{
		"text=More information",
		`role=button[name="Send it"]`,
		"#hidden-div",
		"input[name=q]",
	} {
		n, err := tab.Locator(sel).Count()
		if err != nil || n != 1 {
			t.Fatalf("%s: n=%d err=%v", sel, n, err)
		}
	}
}

func TestVisibilityAndActions(t *testing.T) {
	_, tab := newTestTab(t)
	v, err := tab.Locator("#hidden-div").IsVisible()
	if err != nil {
		t.Fatal(err)
	}
	if v {
		t.Fatal("display:none should be invisible")
	}
	if err := tab.Fill("input[name=q]", "hello"); err != nil {
		t.Fatal(err)
	}
	bid, err := tab.Locator("input[name=q]").BackendID()
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := tab.GetAttributeByBackend(bid, "value")
	if err != nil || !ok || got != "hello" {
		t.Fatalf("fill: %q ok=%v err=%v", got, ok, err)
	}
	if err := tab.Check("input[type=checkbox]"); err != nil {
		t.Fatal(err)
	}
	ck, err := tab.Locator("input[type=checkbox]").IsChecked()
	if err != nil || !ck {
		t.Fatalf("check: %v %v", ck, err)
	}
	if err := tab.SelectOption("select[name=lang]", "Python"); err != nil {
		t.Fatal(err)
	}
	opts, err := tab.SelectedOptions("select[name=lang]")
	if err != nil || len(opts) != 1 || opts[0] != "py" {
		t.Fatalf("select by label: %v err=%v", opts, err)
	}
}

func TestEvaluateAndConsole(t *testing.T) {
	_, tab := newTestTab(t)
	res, err := tab.Evaluate("1+2")
	if err != nil {
		t.Fatal(err)
	}
	var f float64
	if json.Unmarshal(res, &f); f != 3 {
		t.Fatalf("eval: %s", res)
	}
	if _, err := tab.Evaluate("console.log('hi from test')"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range tab.ConsoleLog(0) {
		if b, _ := json.Marshal(ev.Data); strings.Contains(string(b), "hi from test") {
			found = true
		}
	}
	if !found {
		t.Fatal("console log missing")
	}
}

func TestSnapshotAndScreenshot(t *testing.T) {
	_, tab := newTestTab(t)
	nodes, err := tab.Snapshot()
	if err != nil || len(nodes) == 0 {
		t.Fatalf("snapshot: %d nodes err=%v", len(nodes), err)
	}
	pic := tab.Screenshot(80)
	if !strings.Contains(pic, "Welcome") {
		t.Fatalf("screenshot missing text:\n%s", pic)
	}
}

func TestHistoryAndTabs(t *testing.T) {
	e, tab := newTestTab(t)
	if u := tab.URL(); u != "https://example.test/" {
		t.Fatalf("url: %s", u)
	}
	if got, err := e.Tab(tab.ID); err != nil || got != tab {
		t.Fatalf("engine.Tab lookup: %v %v", got, err)
	}
	act, err := e.ActiveTab()
	if err != nil || act == nil {
		t.Fatalf("ActiveTab: %v", err)
	}
}
