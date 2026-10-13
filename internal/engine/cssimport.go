package engine

// cssimport.go: @import in author style sheets. A sheet's leading @import rules
// are read, the imported sheets (with their own imports) are fetched through
// the usual resource path and placed before the importing sheet, in the order
// they are imported, as the cascade requires. Web fonts depend on it: Google
// Fonts is as often imported as linked.

import (
	"strings"

	"github.com/sweefbrowser/sweef/internal/render"
)

const (
	maxImportDepth     = 4
	maxImportsPerSheet = 8
)

// withImports returns sh preceded by the sheets it imports. The text of sh has its
// @import rules removed (the style engine does not know them).
func (e *tabEnv) withImports(sh render.Sheet, depth int, chain map[string]bool) []render.Sheet {
	if depth > maxImportDepth || !strings.Contains(sh.Text, "@import") && !strings.Contains(sh.Text, "@IMPORT") {
		return []render.Sheet{sh}
	}
	rules, rest := leadingImports(sh.Text)
	if len(rules) == 0 {
		return []render.Sheet{sh}
	}
	var out []render.Sheet
	for i, r := range rules {
		if i >= maxImportsPerSheet {
			break
		}
		abs, err := resolveURL(sh.BaseURL, r.href)
		if err != nil || chain[abs] || !strings.HasPrefix(abs, "http") && !strings.HasPrefix(abs, "file:") {
			continue
		}
		body, status, err := e.t.fetchResourceKind(abs, KindStyle)
		if err != nil || status >= 400 {
			continue
		}
		child := render.Sheet{Text: body, BaseURL: abs, Media: r.media}
		if child.Media == "" {
			child.Media = sh.Media
		}
		chain[abs] = true
		out = append(out, e.withImports(child, depth+1, chain)...)
		delete(chain, abs)
	}
	sh.Text = rest
	return append(out, sh)
}

type importRule struct{ href, media string }

// leadingImports parses the @import rules at the start of a style sheet (after
// comments, whitespace and @charset) and returns them with the text that follows.
func leadingImports(css string) ([]importRule, string) {
	var rules []importRule
	i := 0
	skip := func() {
		for i < len(css) {
			switch {
			case css[i] == ' ' || css[i] == '\t' || css[i] == '\n' || css[i] == '\r' || css[i] == '\f':
				i++
			case strings.HasPrefix(css[i:], "/*"):
				j := strings.Index(css[i+2:], "*/")
				if j < 0 {
					i = len(css)
					return
				}
				i += j + 4
			case strings.HasPrefix(css[i:], "<!--") || strings.HasPrefix(css[i:], "-->"):
				i += 3
			default:
				return
			}
		}
	}
	for {
		skip()
		switch {
		case hasPrefixFold(css[i:], "@charset"):
			j := strings.IndexByte(css[i:], ';')
			if j < 0 {
				return rules, css[i:]
			}
			i += j + 1
			continue
		case hasPrefixFold(css[i:], "@import"):
			end := importEnd(css, i)
			if end < 0 {
				return rules, css[i:]
			}
			if r, ok := parseImport(css[i+len("@import") : end]); ok {
				rules = append(rules, r)
			}
			i = end + 1
			continue
		}
		return rules, css[i:]
	}
}

func hasPrefixFold(s, p string) bool { return len(s) >= len(p) && strings.EqualFold(s[:len(p)], p) }

// importEnd finds the ';' that ends the rule starting at i, outside strings and parentheses.
func importEnd(css string, i int) int {
	depth := 0
	for j := i; j < len(css); j++ {
		switch c := css[j]; {
		case c == '"' || c == '\'':
			for j++; j < len(css) && css[j] != c; j++ {
				if css[j] == '\\' {
					j++
				}
			}
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ';' && depth == 0:
			return j
		}
	}
	return -1
}

// parseImport reads `url("x") layer(a) supports(...) media list` or `"x" media list`.
func parseImport(s string) (importRule, bool) {
	s = strings.TrimSpace(s)
	var href string
	switch {
	case hasPrefixFold(s, "url("):
		end := strings.IndexByte(s, ')')
		if end < 0 {
			return importRule{}, false
		}
		href = strings.Trim(strings.TrimSpace(s[4:end]), `"'`)
		s = s[end+1:]
	case len(s) > 0 && (s[0] == '"' || s[0] == '\''):
		end := strings.IndexByte(s[1:], s[0])
		if end < 0 {
			return importRule{}, false
		}
		href = s[1 : 1+end]
		s = s[end+2:]
	default:
		return importRule{}, false
	}
	// layer(...) and supports(...) are accepted and ignored; what is left is the media list
	s = strings.TrimSpace(s)
	for {
		l := strings.ToLower(s)
		if strings.HasPrefix(l, "layer") || strings.HasPrefix(l, "supports(") {
			j := strings.IndexByte(s, '(')
			k := -1
			if j >= 0 && (strings.HasPrefix(l, "supports(") || strings.HasPrefix(l, "layer(")) {
				depth := 0
				for m := j; m < len(s); m++ {
					if s[m] == '(' {
						depth++
					} else if s[m] == ')' {
						if depth--; depth == 0 {
							k = m
							break
						}
					}
				}
				if k < 0 {
					return importRule{}, false
				}
				s = strings.TrimSpace(s[k+1:])
				continue
			}
			if strings.HasPrefix(l, "layer") && (len(l) == 5 || l[5] == ' ') {
				s = strings.TrimSpace(s[5:])
				continue
			}
		}
		break
	}
	return importRule{href: strings.TrimSpace(href), media: strings.TrimSpace(s)}, href != ""
}
