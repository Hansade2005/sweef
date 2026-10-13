package render

import "testing"

// A text run that starts with a forced line break (preserved newline at offset 0) produced an empty
// segment, and the soft-hyphen probe read rs[end-1] with end == 0. go.dev/doc/effective_go crashed on it.
func TestLeadingForcedBreakDoesNotPanic(t *testing.T) {
	for _, ws := range []string{"pre", "pre-wrap", "pre-line", "break-spaces"} {
		r := layoutHTML(t, `<div style="white-space:`+ws+`">`+"\n"+`first line`+"\n\n"+`third</div>`)
		if r == nil {
			t.Fatalf("%s: no layout", ws)
		}
	}
}
