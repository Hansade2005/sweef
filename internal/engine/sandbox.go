package engine

import "strings"

// sandbox is the parsed form of an <iframe sandbox="..."> attribute (and of what a
// nested frame or a popup inherits from a sandboxed creator). A nil *sandbox means
// "not sandboxed". Only the flags that change behaviour in this engine are kept:
//
//	scripts      allow-scripts: the document gets a JavaScript VM at all
//	sameOrigin   allow-same-origin: otherwise the document has an opaque origin
//	popups       allow-popups: window.open and target=_blank may create windows
//	popupsEscape allow-popups-to-escape-sandbox: popups are not sandboxed themselves
//	forms        allow-forms: form submission
//	topNav       allow-top-navigation: the frame may navigate the top level
//	topNavUser   allow-top-navigation-by-user-activation: ... but only during a user gesture
//
// Not implemented: allow-modals (dialogs are auto-handled for every tab), allow-pointer-lock,
// allow-orientation-lock, allow-presentation, allow-downloads (downloads are not gated here).
type sandbox struct {
	scripts, sameOrigin, popups, popupsEscape, forms, topNav, topNavUser bool
}

// parseSandbox builds the sandbox for a frame whose <iframe> element has the sandbox
// attribute value val (has says whether the attribute is present) inside a document that
// is itself sandboxed by parent (nil when it is not). Restrictions only accumulate: a
// nested frame can never be more permissive than its embedder.
func parseSandbox(val string, has bool, parent *sandbox) *sandbox {
	if !has {
		return parent
	}
	s := &sandbox{}
	for _, tok := range strings.Fields(strings.ToLower(val)) {
		switch tok {
		case "allow-scripts":
			s.scripts = true
		case "allow-same-origin":
			s.sameOrigin = true
		case "allow-popups":
			s.popups = true
		case "allow-popups-to-escape-sandbox":
			s.popupsEscape = true
		case "allow-forms":
			s.forms = true
		case "allow-top-navigation":
			s.topNav = true
		case "allow-top-navigation-by-user-activation":
			s.topNavUser = true
		}
	}
	if parent != nil {
		s.scripts = s.scripts && parent.scripts
		s.sameOrigin = s.sameOrigin && parent.sameOrigin
		s.popups = s.popups && parent.popups
		s.popupsEscape = s.popupsEscape && parent.popupsEscape
		s.forms = s.forms && parent.forms
		s.topNav = s.topNav && parent.topNav
		s.topNavUser = s.topNavUser && parent.topNavUser
	}
	return s
}

// Sandboxed reports whether the document is inside a sandboxed frame (or was opened by
// one without allow-popups-to-escape-sandbox).
func (t *Tab) Sandboxed() bool { return t.win.sbx != nil }

func (t *Tab) scriptsAllowed() bool { return t.win.sbx == nil || t.win.sbx.scripts }

// opaqueOrigin: a sandbox without allow-same-origin gives the document a unique origin.
func (t *Tab) opaqueOrigin() bool { return t.win.sbx != nil && !t.win.sbx.sameOrigin }
