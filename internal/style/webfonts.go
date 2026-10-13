package style

import "github.com/sweefbrowser/sweef/internal/font"

// SetWebFonts attaches the document's web font set: from now on the family
// names of font-family declarations that the set declares are replaced by
// their scoped form (font.WebSet.Scope), which is how layout and painting
// find the right faces of the right document. nil detaches.
func (e *Engine) SetWebFonts(w *font.WebSet) { e.web = w }

// scopeFamilies rewrites declared web font families of a parsed font-family list.
func (e *Engine) scopeFamilies(fam []string) []string {
	if e.web == nil {
		return fam
	}
	var out []string
	for i, f := range fam {
		if sc, ok := e.web.Scope(f); ok {
			if out == nil {
				out = append([]string(nil), fam...)
			}
			out[i] = sc
		}
	}
	if out != nil {
		return out
	}
	return fam
}

// noteWebMetrics records that a computed length (ex, ch, cap, lh) was resolved
// with the metrics of a web font family: such a style is out of date once a
// face of the set loads, so the cross-pass cache must not keep it (persist.go).
func (e *Engine) noteWebMetrics(fam []string) {
	if e.web != nil && font.HasScoped(fam) {
		e.usesWebMetrics = true
	}
}
