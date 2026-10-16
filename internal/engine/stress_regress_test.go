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
