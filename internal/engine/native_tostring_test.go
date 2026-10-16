package engine

import "testing"

// The platform prints as built-in code; the page's own functions keep their source. Optimizely's "Cleanse"
// deletes every accessor from HTMLElement.prototype that does not, which took element.style away and broke
// jQuery on the-internet.herokuapp.com.
func TestPlatformFunctionsPrintAsNative(t *testing.T) {
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab("data:text/html,<p>x")
	if err != nil {
		t.Fatal(err)
	}
	var got []any
	evalJSON(t, tab, `(function(){
	  var nat = function (f) { return typeof f === 'function' && String(f).indexOf('[native code]') >= 0; };
	  var d = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'style');
	  function mine(a, b) { return a + b; }
	  return [nat(d && d.get), nat(document.querySelector), nat(Element.prototype.closest), nat(setTimeout), nat(HTMLElement), nat(Function.prototype.toString), nat(mine), String(mine).indexOf('a + b') >= 0, nat(function(){})];
	})()`, &got)
	want := []any{true, true, true, true, true, true, false, true, false}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
