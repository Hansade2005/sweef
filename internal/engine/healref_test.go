package engine

import (
	"regexp"
	"testing"
)

// A widget that swaps its element when used (Wikipedia's search box becomes a combobox on click) must not
// strand the agent's snapshot ref: fill and press on the old ref reach the control that took its place.
func TestStaleRefHealsToReplacementControl(t *testing.T) {
	e := New(Options{CacheDir: "off"})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetDocument(`<!doctype html><body><div id="box"><input type="search" aria-label="Search site" id="old"></div>
<script>
document.getElementById('old').addEventListener('click', function () {
  var n = document.createElement('input'); n.setAttribute('role', 'combobox'); n.setAttribute('aria-label', 'Search site'); n.id = 'new';
  this.style.display = 'none'; document.getElementById('box').appendChild(n); n.focus();
});
</script></body>`, "https://heal.test/")
	snap, err := tab.AriaSnapshotYAML()
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`searchbox "Search site" \[ref=(e\d+)\]`).FindStringSubmatch(snap)
	if m == nil {
		t.Fatalf("no searchbox ref in snapshot:\n%s", snap)
	}
	ref := m[1]
	if err := tab.Click(ref); err != nil {
		t.Fatal(err)
	}
	if err := tab.Fill(ref, "ada"); err != nil {
		t.Fatalf("fill on the replaced ref: %v", err)
	}
	if v := mustEval(t, tab, `document.getElementById('new').value + '|' + document.getElementById('old').value`); v != `"ada|"` {
		t.Fatalf("values: %s", v)
	}
}
