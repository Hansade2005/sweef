package style

import (
	"strconv"
	"strings"

	"github.com/sweefbrowser/sweef/internal/font"
)

// fontface.go: the descriptors of an @font-face rule (CSS Fonts 4, section 4):
// font-family, src (url() with format() and tech() hints, local()),
// font-weight and font-stretch (single values or ranges, for variable fonts),
// font-style, unicode-range, font-display, font-feature-settings,
// font-variation-settings and the metric descriptors size-adjust and
// ascent/descent/line-gap-override. An invalid descriptor is ignored; a rule
// without a usable family is dropped (a rule without src is kept parsed but
// the loader has nothing to fetch).

var genericFamilies = map[string]bool{
	"serif": true, "sans-serif": true, "monospace": true, "cursive": true, "fantasy": true, "system-ui": true,
	"ui-serif": true, "ui-sans-serif": true, "ui-monospace": true, "ui-rounded": true, "math": true, "emoji": true,
	"fangsong": true, "inherit": true, "initial": true, "unset": true, "revert": true, "default": true,
}

// parseFontFaceDecls turns the declarations of one @font-face block into a FontFace.
func parseFontFaceDecls(decls []Decl) (FontFace, bool) {
	ff := FontFace{}
	d := font.FaceDesc{}
	for _, dc := range decls {
		switch dc.Name {
		case "font-family":
			ff.Family = fontFaceFamily(dc.Val)
		case "font-weight":
			ff.Weight = dc.L
			if lo, hi, ok := parseWeightRange(dc.L); ok {
				d.WMin, d.WMax = lo, hi
			}
		case "font-style":
			ff.Style = dc.L
			if st, ok := parseFaceStyle(dc.L); ok {
				d.Style = st
			}
		case "font-stretch":
			if lo, hi, ok := parseStretchRange(dc.L); ok {
				d.SMin, d.SMax = lo, hi
			}
		case "src":
			ff.Src = dc.Val
			d.Src = parseFaceSrc(dc.Val)
		case "unicode-range":
			if r, ok := parseUnicodeRange(dc.L); ok {
				d.Ranges = r
			}
		case "font-display":
			d.Display = strings.TrimSpace(dc.L)
		case "font-feature-settings":
			d.Features = strings.TrimSpace(dc.Val)
		case "font-variation-settings":
			d.Variations = strings.TrimSpace(dc.Val)
		case "size-adjust":
			if v, ok := parsePctFraction(dc.L); ok && v > 0 {
				d.SizeAdjust = v
			}
		case "ascent-override":
			if v, ok := parsePctFraction(dc.L); ok && v >= 0 {
				d.AscentOverride, d.Overrides = v, d.Overrides|font.OvrAscent
			}
		case "descent-override":
			if v, ok := parsePctFraction(dc.L); ok && v >= 0 {
				d.DescentOverride, d.Overrides = v, d.Overrides|font.OvrDescent
			}
		case "line-gap-override":
			if v, ok := parsePctFraction(dc.L); ok && v >= 0 {
				d.LineGapOverride, d.Overrides = v, d.Overrides|font.OvrLineGap
			}
		}
	}
	if ff.Family == "" {
		return ff, false
	}
	d.Family = ff.Family
	d.Normalize()
	ff.Desc = d
	return ff, true
}

// fontFaceFamily reads the single family name of the font-family descriptor.
func fontFaceFamily(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if v[0] == '"' || v[0] == '\'' {
		return strings.Join(strings.Fields(unquote(v)), " ")
	}
	if strings.ContainsAny(v, ",()") {
		return ""
	}
	name := strings.Join(strings.Fields(v), " ")
	if genericFamilies[strings.ToLower(name)] {
		return ""
	}
	return name
}

func parseWeightRange(l string) (lo, hi int, ok bool) {
	f := strings.Fields(l)
	one := func(s string) (int, bool) {
		switch s {
		case "normal", "auto":
			return 400, true
		case "bold":
			return 700, true
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || n < 1 || n > 1000 {
			return 0, false
		}
		return int(n + 0.5), true
	}
	switch len(f) {
	case 1:
		v, ok := one(f[0])
		return v, v, ok
	case 2:
		a, ok1 := one(f[0])
		b, ok2 := one(f[1])
		if f[0] == "normal" || f[0] == "bold" || f[1] == "normal" || f[1] == "bold" {
			return 0, 0, false // keywords are only valid alone
		}
		if a > b {
			a, b = b, a
		}
		return a, b, ok1 && ok2
	}
	return 0, 0, false
}

func parseFaceStyle(l string) (uint8, bool) {
	f := strings.Fields(l)
	if len(f) == 0 {
		return 0, false
	}
	switch f[0] {
	case "normal", "auto":
		return font.StyleNormal, len(f) == 1
	case "italic":
		return font.StyleItalic, len(f) == 1
	case "oblique":
		return font.StyleOblique, true
	}
	return 0, false
}

var stretchKeywords = map[string]float64{
	"ultra-condensed": 50, "extra-condensed": 62.5, "condensed": 75, "semi-condensed": 87.5, "normal": 100,
	"semi-expanded": 112.5, "expanded": 125, "extra-expanded": 150, "ultra-expanded": 200,
}

func parseStretchRange(l string) (lo, hi float64, ok bool) {
	one := func(s string) (float64, bool) {
		if v, ok := stretchKeywords[s]; ok {
			return v, true
		}
		if strings.HasSuffix(s, "%") {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
			if err == nil && n >= 0 {
				return n, true
			}
		}
		return 0, false
	}
	f := strings.Fields(l)
	switch len(f) {
	case 1:
		v, ok := one(f[0])
		return v, v, ok
	case 2:
		a, ok1 := one(f[0])
		b, ok2 := one(f[1])
		if a > b {
			a, b = b, a
		}
		return a, b, ok1 && ok2
	}
	return 0, 0, false
}

func parsePctFraction(l string) (float64, bool) {
	l = strings.TrimSpace(l)
	if !strings.HasSuffix(l, "%") {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.TrimSuffix(l, "%"), 64)
	if err != nil {
		return 0, false
	}
	return n / 100, true
}

// parseUnicodeRange parses "U+0025-00FF, U+4??, U+26"; ok is false when any
// item is invalid (the descriptor is then ignored).
func parseUnicodeRange(l string) ([]font.URange, bool) {
	var out []font.URange
	for _, item := range strings.Split(l, ",") {
		item = strings.TrimSpace(item)
		if len(item) < 3 || item[0] != 'u' || item[1] != '+' {
			return nil, false
		}
		item = item[2:]
		var lo, hi int64
		var err error
		switch {
		case strings.Contains(item, "?"):
			if strings.Contains(strings.TrimRight(item, "?"), "?") || len(item) > 6 {
				return nil, false
			}
			lo, err = strconv.ParseInt(strings.ReplaceAll(item, "?", "0"), 16, 32)
			if err != nil {
				return nil, false
			}
			hi, err = strconv.ParseInt(strings.ReplaceAll(item, "?", "f"), 16, 32)
			if err != nil {
				return nil, false
			}
		case strings.Contains(item, "-"):
			p := strings.SplitN(item, "-", 2)
			if len(p[0]) > 6 || len(p[1]) > 6 {
				return nil, false
			}
			if lo, err = strconv.ParseInt(p[0], 16, 32); err != nil {
				return nil, false
			}
			if hi, err = strconv.ParseInt(p[1], 16, 32); err != nil {
				return nil, false
			}
		default:
			if len(item) > 6 {
				return nil, false
			}
			if lo, err = strconv.ParseInt(item, 16, 32); err != nil {
				return nil, false
			}
			hi = lo
		}
		if lo > hi || hi > 0x10ffff || lo < 0 {
			return nil, false
		}
		out = append(out, font.URange{Lo: rune(lo), Hi: rune(hi)})
		if len(out) > 8192 {
			return nil, false // nobody writes this; a hostile sheet could make every lookup slow
		}
	}
	return out, len(out) > 0
}

// parseFaceSrc parses the src descriptor: a comma separated list of
// url("...") [format("...")] [tech(...)] and local("...") entries. Entries
// that do not parse are skipped.
func parseFaceSrc(v string) []font.Source {
	var out []font.Source
	for _, item := range splitTop(v, ',') {
		toks := fields(strings.TrimSpace(item))
		if len(toks) == 0 {
			continue
		}
		var s font.Source
		ok := false
		for _, tk := range toks {
			lt := strings.ToLower(tk)
			switch {
			case strings.HasPrefix(lt, "url(") && strings.HasSuffix(tk, ")"):
				u := strings.TrimSpace(tk[4 : len(tk)-1])
				s.URL = strings.TrimSpace(unquote(u))
				ok = s.URL != ""
			case strings.HasPrefix(lt, "local(") && strings.HasSuffix(tk, ")"):
				s.Local = strings.Join(strings.Fields(unquote(strings.TrimSpace(tk[6:len(tk)-1]))), " ")
				ok = s.Local != ""
			case strings.HasPrefix(lt, "format(") && strings.HasSuffix(tk, ")"):
				s.Format = strings.ToLower(strings.TrimSpace(unquote(strings.TrimSpace(tk[7 : len(tk)-1]))))
			case strings.HasPrefix(lt, "tech(") && strings.HasSuffix(tk, ")"):
				for _, t := range strings.Split(tk[5:len(tk)-1], ",") {
					if t = strings.ToLower(strings.TrimSpace(unquote(strings.TrimSpace(t)))); t != "" {
						s.Tech = append(s.Tech, t)
					}
				}
			default:
				ok = false
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// ParseFontFaces returns the @font-face rules of one stylesheet text whose
// conditions (@media, @supports) hold in env, in order. Source URLs are as
// written (relative to the stylesheet).
func ParseFontFaces(css string, env *Env) []FontFace {
	order := 0
	p := ParseSheet(css, env, OriginAuthor, &order, map[string]int{})
	return p.Fonts
}

// NewFaceDesc builds a descriptor the way the FontFace constructor does: the
// family, the src descriptor text (empty when the source is binary data) and
// the descriptors keyed by their CSS names ("font-weight", "unicode-range",
// ...). An invalid value is reported as an error (SyntaxError in script).
func NewFaceDesc(family, src string, props map[string]string) (font.FaceDesc, error) {
	decls := []Decl{makeDecl("font-family", family)}
	if strings.TrimSpace(src) != "" {
		decls = append(decls, makeDecl("src", src))
	}
	for _, name := range []string{"font-style", "font-weight", "font-stretch", "unicode-range", "font-display", "font-feature-settings", "font-variation-settings"} {
		v, ok := props[name]
		if !ok || strings.TrimSpace(v) == "" || strings.EqualFold(strings.TrimSpace(v), "normal") && name != "font-style" && name != "font-weight" && name != "font-stretch" {
			continue
		}
		// validate the descriptors that have a syntax of their own
		lv := strings.ToLower(strings.TrimSpace(v))
		switch name {
		case "font-weight":
			if _, _, ok := parseWeightRange(lv); !ok {
				return font.FaceDesc{}, errBadDescriptor(name, v)
			}
		case "font-style":
			if _, ok := parseFaceStyle(lv); !ok {
				return font.FaceDesc{}, errBadDescriptor(name, v)
			}
		case "font-stretch":
			if _, _, ok := parseStretchRange(lv); !ok {
				return font.FaceDesc{}, errBadDescriptor(name, v)
			}
		case "unicode-range":
			if _, ok := parseUnicodeRange(lv); !ok {
				return font.FaceDesc{}, errBadDescriptor(name, v)
			}
		}
		decls = append(decls, makeDecl(name, v))
	}
	ff, ok := parseFontFaceDecls(decls)
	if !ok {
		return font.FaceDesc{}, errBadDescriptor("font-family", family)
	}
	return ff.Desc, nil
}

type descriptorError string

func (e descriptorError) Error() string { return string(e) }

func errBadDescriptor(name, v string) error {
	return descriptorError("The value provided ('" + v + "') could not be parsed as a valid " + name + ".")
}
