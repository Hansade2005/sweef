// Package policy decides whether a document or frame may use camera, microphone and display capture under
// Permissions-Policy (the response header) and the iframe allow attribute.
//
// Rules implemented (Permissions Policy spec, the parts these three features need): the default allowlist is
// 'self'; the header can widen it to "*" or a list of origins or close it with (); a cross-origin frame may use a
// feature only when its parent's policy allows the frame's origin AND the iframe's allow attribute names the
// feature (bare name means 'src', the frame's own origin).
//
// Note on this engine: iframes do not run scripts yet, so only the top-level document ever asks. FrameAllowed is
// the rule the engine will apply when frames get their own JavaScript; it is tested here so that does not have to
// be written later under pressure.
package policy

import "strings"

// Features this package knows by their policy names.
const (
	Camera         = "camera"
	Microphone     = "microphone"
	DisplayCapture = "display-capture"
)

// Policy is a parsed Permissions-Policy header: for each feature, its allowlist.
type Policy struct {
	lists map[string]allowlist
}

type allowlist struct {
	star    bool
	self    bool
	src     bool
	origins []string
}

func (a allowlist) matches(origin, self string) bool {
	if a.star {
		return true
	}
	if a.self && sameOrigin(origin, self) {
		return true
	}
	for _, o := range a.origins {
		if sameOrigin(o, origin) {
			return true
		}
	}
	return false
}

func sameOrigin(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}

// ParseHeader reads a Permissions-Policy header value ("camera=(self \"https://a.example\"), microphone=()"). The
// legacy Feature-Policy syntax ("camera 'self' https://a.example; microphone 'none'") is accepted too. Unknown
// features are ignored.
func ParseHeader(h string) Policy {
	p := Policy{lists: map[string]allowlist{}}
	h = strings.TrimSpace(h)
	if h == "" {
		return p
	}
	if strings.Contains(h, "=") {
		for _, item := range splitTop(h, ',') {
			item = strings.TrimSpace(item)
			eq := strings.IndexByte(item, '=')
			if eq <= 0 {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(item[:eq]))
			val := strings.TrimSpace(item[eq+1:])
			if i := strings.IndexByte(val, ';'); i >= 0 { // parameters
				val = strings.TrimSpace(val[:i])
			}
			val = strings.TrimSuffix(strings.TrimPrefix(val, "("), ")")
			var toks []string
			for _, f := range strings.Fields(val) {
				toks = append(toks, strings.Trim(f, `"`))
			}
			p.lists[name] = lexList(toks)
		}
		return p
	}
	for _, d := range strings.Split(h, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		p.lists[strings.ToLower(f[0])] = lexList(f[1:])
	}
	return p
}

func lexList(toks []string) allowlist {
	var a allowlist
	for _, t := range toks {
		switch strings.Trim(t, "'") {
		case "*":
			a.star = true
		case "self":
			a.self = true
		case "src":
			a.src = true
		case "none":
		default:
			if t != "" {
				a.origins = append(a.origins, t)
			}
		}
	}
	return a
}

// splitTop splits s at sep outside parentheses and quotes.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, q, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			q = !q
		case q:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// Allowed reports whether the document at origin may use feature under its own header policy.
func (p Policy) Allowed(feature, origin string) bool {
	a, ok := p.lists[feature]
	if !ok {
		return true // default allowlist 'self': the document itself
	}
	return a.matches(origin, origin)
}

// AllowAttr is a parsed iframe allow attribute.
type AllowAttr struct {
	lists map[string]allowlist
}

// ParseAllowAttr parses allow="camera; microphone 'none'; display-capture https://meet.example".
func ParseAllowAttr(attr string) AllowAttr {
	a := AllowAttr{lists: map[string]allowlist{}}
	for _, d := range strings.Split(attr, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		l := lexList(f[1:])
		if len(f) == 1 {
			l.src = true // bare feature name == 'src'
		}
		a.lists[strings.ToLower(f[0])] = l
	}
	return a
}

// FrameAllowed decides for a frame whose document origin is frameOrigin and whose <iframe src> origin is srcOrigin.
// parent is the parent document's header policy and parentOrigin its origin.
func FrameAllowed(feature string, parent Policy, parentOrigin string, attr AllowAttr, frameOrigin, srcOrigin string, frame Policy) bool {
	if sameOrigin(frameOrigin, parentOrigin) {
		// same-origin frames inherit the parent's decision
		return parent.Allowed(feature, parentOrigin) && frame.Allowed(feature, frameOrigin)
	}
	// the parent's own policy must allow the frame's origin to use the feature
	if pl, ok := parent.lists[feature]; ok {
		if !pl.matches(frameOrigin, parentOrigin) {
			return false
		}
	}
	// without a header entry the default allowlist is 'self'; delegation then happens through the allow attribute
	l, ok := attr.lists[feature]
	if !ok {
		return false // not delegated
	}
	delegated := l.star || (l.self && sameOrigin(frameOrigin, parentOrigin))
	if l.src && sameOrigin(frameOrigin, srcOrigin) {
		delegated = true
	}
	for _, o := range l.origins {
		if sameOrigin(o, frameOrigin) {
			delegated = true
		}
	}
	if !delegated {
		return false
	}
	return frame.Allowed(feature, frameOrigin)
}
