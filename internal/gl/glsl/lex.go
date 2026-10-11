// Package glsl implements a GLSL ES 1.00 / 3.00 front end (lexer,
// preprocessor, parser, type checker) and a closure-compiled executor for
// software rendering.
package glsl

import (
	"fmt"
	"strings"
)

// TokKind classifies a lexical token.
type TokKind uint8

const (
	TEOF TokKind = iota
	TIdent
	TInt
	TUint
	TFloat
	TPunct
)

// Tok is one lexical token.
type Tok struct {
	K     TokKind
	S     string
	Line  int
	BOL   bool // first token on its line
	Space bool // whitespace precedes the token
	hide  map[string]bool
}

type lexer struct {
	src  string
	pos  int
	line int
	toks []Tok
	err  error
}

var puncts3 = []string{"<<=", ">>=", "..."}
var puncts2 = []string{"++", "--", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<", ">>", "<=", ">=", "==", "!=", "&&", "||", "^^", "##"}

func isIdStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool   { return c >= '0' && c <= '9' }
func isHex(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// lex tokenizes src. Comments are removed; line splices (backslash-newline)
// are honoured.
func lex(src string) ([]Tok, error) {
	l := &lexer{src: src, line: 1}
	bol, space := true, false
	n := len(src)
	for l.pos < n {
		c := src[l.pos]
		switch {
		case c == '\n':
			l.line++
			l.pos++
			bol, space = true, false
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f':
			l.pos++
			space = true
			continue
		case c == '\\' && l.pos+1 < n && src[l.pos+1] == '\n':
			l.pos += 2
			l.line++
			space = true
			continue
		case c == '\\' && l.pos+2 < n && src[l.pos+1] == '\r' && src[l.pos+2] == '\n':
			l.pos += 3
			l.line++
			space = true
			continue
		case c == '/' && l.pos+1 < n && src[l.pos+1] == '/':
			for l.pos < n && src[l.pos] != '\n' {
				l.pos++
			}
			space = true
			continue
		case c == '/' && l.pos+1 < n && src[l.pos+1] == '*':
			end := strings.Index(src[l.pos+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("ERROR: 0:%d: '/*' : unterminated comment\n", l.line)
			}
			l.line += strings.Count(src[l.pos:l.pos+2+end+2], "\n")
			l.pos += 2 + end + 2
			space = true
			continue
		}
		t := Tok{Line: l.line, BOL: bol, Space: space}
		bol, space = false, false
		switch {
		case isIdStart(c):
			s := l.pos
			for l.pos < n && (isIdStart(src[l.pos]) || isDigit(src[l.pos])) {
				l.pos++
			}
			t.K, t.S = TIdent, src[s:l.pos]
		case isDigit(c) || c == '.' && l.pos+1 < n && isDigit(src[l.pos+1]):
			s := l.pos
			isFloat := false
			if c == '0' && l.pos+1 < n && (src[l.pos+1] == 'x' || src[l.pos+1] == 'X') {
				l.pos += 2
				for l.pos < n && isHex(src[l.pos]) {
					l.pos++
				}
			} else {
				for l.pos < n && isDigit(src[l.pos]) {
					l.pos++
				}
				if l.pos < n && src[l.pos] == '.' {
					isFloat = true
					l.pos++
					for l.pos < n && isDigit(src[l.pos]) {
						l.pos++
					}
				}
				if l.pos < n && (src[l.pos] == 'e' || src[l.pos] == 'E') {
					p := l.pos + 1
					if p < n && (src[p] == '+' || src[p] == '-') {
						p++
					}
					if p < n && isDigit(src[p]) {
						isFloat = true
						for p < n && isDigit(src[p]) {
							p++
						}
						l.pos = p
					}
				}
			}
			t.S = src[s:l.pos]
			switch {
			case isFloat:
				t.K = TFloat
				if l.pos < n && (src[l.pos] == 'f' || src[l.pos] == 'F') {
					l.pos++
				}
			case l.pos < n && (src[l.pos] == 'u' || src[l.pos] == 'U'):
				t.K = TUint
				l.pos++
			case l.pos < n && (src[l.pos] == 'f' || src[l.pos] == 'F') && !strings.HasPrefix(t.S, "0x"):
				t.K = TFloat
				l.pos++
			default:
				t.K = TInt
			}
		default:
			t.K = TPunct
			rest := src[l.pos:]
			matched := false
			for _, p := range puncts3 {
				if strings.HasPrefix(rest, p) {
					t.S, matched = p, true
					break
				}
			}
			if !matched {
				for _, p := range puncts2 {
					if strings.HasPrefix(rest, p) {
						t.S, matched = p, true
						break
					}
				}
			}
			if !matched {
				if strings.IndexByte("+-*/%<>=!&|^~?:;,.()[]{}#", c) < 0 {
					return nil, fmt.Errorf("ERROR: 0:%d: '%c' : unexpected token\n", l.line, c)
				}
				t.S = string(c)
			}
			l.pos += len(t.S)
		}
		l.toks = append(l.toks, t)
	}
	l.toks = append(l.toks, Tok{K: TEOF, Line: l.line, BOL: true})
	return l.toks, nil
}
