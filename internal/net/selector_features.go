package net

// selector_features.go: what a compiled selector looks at. The style engine
// uses it to decide which DOM changes can alter a cached computed style (see
// internal/style/persist.go).

// SelFeatures describes the inputs of a selector alternative.
type SelFeatures struct {
	// Attrs are the attribute names the selector (or the pseudo-classes it
	// uses) reads, lower case. "class" and "id" are included when used.
	Attrs []string
	// Sib: the element the selector is matched against depends on its
	// siblings (+ ~ :nth-child :first-child :last-child :only-child
	// :*-of-type, or the same inside :not()/:is() arguments).
	Sib bool
	// AncSib: an ancestor-side compound depends on its siblings, so the
	// match can change when an ancestor's siblings change.
	AncSib bool
	// Empty: :empty is used (depends on the element's children).
	Empty bool
	// Complex: :has() or :scope; callers should not cache across mutations.
	Complex bool
	// Has: :has() is used (the match depends on descendants or later siblings); Scope: :scope is used.
	Has, Scope bool
}

// Features reports what the alternative reads.
func (e SelectorEntry) Features() SelFeatures {
	var f SelFeatures
	seen := map[string]bool{}
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			f.Attrs = append(f.Attrs, a)
		}
	}
	featComplex(e.c, &f, add, false)
	return f
}

// featComplex walks one complex selector. nested is true inside :not()/:is()
// arguments, where any sibling dependence counts for both flags.
func featComplex(c complexSel, f *SelFeatures, add func(string), nested bool) {
	last := len(c.parts) - 1
	for i, p := range c.parts {
		sibHere := false
		if i > 0 && (p.comb == '+' || p.comb == '~') {
			sibHere = true
		}
		if p.id != "" {
			add("id")
		}
		if len(p.classes) > 0 {
			add("class")
		}
		for _, a := range p.attrs {
			add(a.name)
		}
		for _, ps := range p.pseudos {
			switch ps.name {
			case "first-child", "last-child", "only-child", "nth-child", "nth-last-child",
				"nth-of-type", "nth-last-of-type", "first-of-type", "last-of-type", "only-of-type":
				sibHere = true
			case "empty":
				f.Empty = true
			case "has", "scope":
				f.Complex = true
				if ps.name == "has" {
					f.Has = true
				} else {
					f.Scope = true
				}
			case "checked":
				add("checked")
				add("selected")
			case "disabled", "enabled":
				add("disabled")
			case "required", "optional":
				add("required")
			case "link", "any-link":
				add("href")
			case "focus", "focus-visible", "focus-within":
				add("data-sw-focused")
			case "fullscreen", "-webkit-full-screen":
				add("data-sw-fullscreen")
			case "popover-open":
				add("data-sw-popover-open")
			case "read-only", "read-write":
				add("readonly")
			case "lang", "dir":
				add("lang")
				add("dir")
			}
			for _, sc := range ps.sub {
				featComplex(sc, f, add, true)
			}
			for _, r := range ps.rel {
				featComplex(r.sel, f, add, true)
			}
		}
		if sibHere {
			if i == last && !nested {
				f.Sib = true
			} else {
				f.AncSib = true
				if nested {
					f.Sib = true
				}
			}
		}
	}
}
