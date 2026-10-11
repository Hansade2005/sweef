package style

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// uaCSS follows Chromium's html.css (the parts that affect layout and paint).
const uaCSS = `
html, address, blockquote, body, center, dialog, div, figure, figcaption, footer, form, header, hr, legend, listing, main, p, plaintext, pre, search, xmp,
article, aside, h1, h2, h3, h4, h5, h6, hgroup, nav, section, dir, dd, dl, dt, menu, ol, ul, details, summary, fieldset, optgroup, option, slot[name] { display: block }
slot { display: contents }
head, link, meta, script, style, title, base, area, datalist, noembed, noframes, param, rp, template, noscript, source, track { display: none }
[hidden]:not([hidden="until-found" i]):not(embed) { display: none }
dialog:not([open]) { display: none }
html { color: black }
body { margin: 8px }
p, dl, multicol { margin: 1em 0 }
blockquote, figure { margin: 1em 40px }
center { text-align: -webkit-center }
hr { overflow: hidden; margin: 0.5em auto; border-style: inset; border-width: 1px; color: gray }
legend { padding-left: 2px; padding-right: 2px }
fieldset { margin: 0 2px; padding: 0.35em 0.75em 0.625em; border: 2px groove rgb(192,192,192); min-width: min-content }
details > summary:first-of-type { display: list-item; list-style-type: disclosure-closed; list-style-position: inside }
details[open] > summary:first-of-type { list-style-type: disclosure-open }
details:not([open]) > :not(summary):not(slot) { display: none }
h1 { display: block; font-size: 2em; margin: 0.67em 0; font-weight: bold }
h2 { display: block; font-size: 1.5em; margin: 0.83em 0; font-weight: bold }
h3 { display: block; font-size: 1.17em; margin: 1em 0; font-weight: bold }
h4 { display: block; margin: 1.33em 0; font-weight: bold }
h5 { display: block; font-size: 0.83em; margin: 1.67em 0; font-weight: bold }
h6 { display: block; font-size: 0.67em; margin: 2.33em 0; font-weight: bold }
:is(article, aside, nav, section) h1 { font-size: 1.5em; margin: 0.83em 0 }
:is(article, aside, nav, section) :is(article, aside, nav, section) h1 { font-size: 1.17em; margin: 1em 0 }
:is(article, aside, nav, section) :is(article, aside, nav, section) :is(article, aside, nav, section) h1 { font-size: 1em; margin: 1.33em 0 }
:is(article, aside, nav, section) :is(article, aside, nav, section) :is(article, aside, nav, section) :is(article, aside, nav, section) h1 { font-size: 0.83em; margin: 1.67em 0 }
:is(article, aside, nav, section) :is(article, aside, nav, section) :is(article, aside, nav, section) :is(article, aside, nav, section) :is(article, aside, nav, section) h1 { font-size: 0.67em; margin: 2.33em 0 }
dd { margin-left: 40px }
dir, menu, ul { list-style-type: disc; margin: 1em 0; padding-left: 40px }
ol { list-style-type: decimal; margin: 1em 0; padding-left: 40px }
:is(dir, dl, menu, ol, ul) :is(dir, dl, menu, ol, ul) { margin: 0 }
:is(dir, menu, ul) :is(dir, menu, ul) { list-style-type: circle }
:is(dir, menu, ul) :is(dir, menu, ul) :is(dir, menu, ul) { list-style-type: square }
:is(ol, ul, menu, dir) :is(dl) { margin: 0 }
li { display: list-item }
pre, listing, xmp, plaintext { font-family: monospace; white-space: pre; margin: 1em 0 }
code, kbd, samp, tt { font-family: monospace }
address, cite, dfn, em, i, var { font-style: italic }
b, strong { font-weight: bolder }
big { font-size: larger }
small { font-size: smaller }
sub { vertical-align: sub; font-size: smaller }
sup { vertical-align: super; font-size: smaller }
s, strike, del { text-decoration: line-through }
u, ins { text-decoration: underline }
mark { background-color: yellow; color: black }
abbr[title], acronym[title] { text-decoration: underline dotted }
q::before { content: open-quote }
q::after { content: close-quote }
a:any-link { color: #0000ee; text-decoration: underline; cursor: pointer }

img { display: inline }
video { object-fit: contain }
audio:not([controls]) { display: none }
iframe { border: 2px inset }
iframe[frameborder="0"], iframe[frameborder="no" i] { border: none }
embed, object { display: inline }
br { display: inline }
nobr { white-space: nowrap }
wbr { display: inline }
marquee { display: inline-block; text-align: initial }
ruby { display: ruby }
rt { display: ruby-text }
table { display: table; box-sizing: border-box; border-collapse: separate; border-spacing: 2px; border-color: gray; text-indent: initial }
caption { display: table-caption; text-align: -webkit-center }
colgroup { display: table-column-group }
col { display: table-column }
thead { display: table-header-group; vertical-align: middle; border-color: inherit }
tbody { display: table-row-group; vertical-align: middle; border-color: inherit }
tfoot { display: table-footer-group; vertical-align: middle; border-color: inherit }
tr { display: table-row; vertical-align: inherit; border-color: inherit }
td, th { display: table-cell; vertical-align: inherit; padding: 1px }
th { font-weight: bold; text-align: -internal-center }
table > tr { vertical-align: middle }
input, textarea, select, button { margin: 0em; font: -webkit-small-control; text-rendering: auto; color: fieldtext; letter-spacing: normal; word-spacing: normal; line-height: normal; text-transform: none; text-indent: 0; text-shadow: none; display: inline-block; text-align: start }
input, input:is([type="password" i], [type="search" i], [type="email" i], [type="url" i], [type="tel" i], [type="number" i]) { padding: 1px 2px; border: 2px inset rgb(118,118,118); background-color: field; cursor: text; appearance: textfield }
input[type="hidden" i] { display: none }
input[type="image" i] { cursor: pointer; padding: 0; border: none; background-color: transparent }
input:is([type="button" i], [type="submit" i], [type="reset" i], [type="file" i])::file-selector-button, input:is([type="button" i], [type="submit" i], [type="reset" i]), button {
  appearance: auto; padding: 1px 6px; border: 2px outset buttonborder; background-color: buttonface; color: buttontext; box-sizing: border-box; text-align: center; cursor: default; align-items: flex-start }
input[type="checkbox" i], input[type="radio" i] { margin: 3px 3px 3px 4px; padding: 0; border: none; background-color: initial; box-sizing: border-box; cursor: default; appearance: auto }
input[type="radio" i] { margin: 3px 3px 0 5px }
input[type="range" i] { margin: 2px; padding: 0; border: none; background-color: transparent; cursor: default; appearance: auto }
input[type="color" i] { padding: 1px 2px; border: 1px solid rgb(118,118,118); background-color: buttonface; cursor: default; box-sizing: border-box; appearance: auto }
input[type="file" i] { background-color: transparent; border: none; padding: 0; cursor: default; appearance: auto; align-items: baseline }
input::placeholder, textarea::placeholder { color: darkGray; pointer-events: none }
textarea { font-family: monospace; appearance: auto; padding: 2px; border: 1px solid rgb(118,118,118); background-color: field; cursor: text; white-space: pre-wrap; word-wrap: break-word; overflow: auto; resize: both; box-sizing: content-box }
select { box-sizing: border-box; border: 1px solid rgb(118,118,118); background-color: field; cursor: default; white-space: pre; appearance: auto; border-radius: 0; align-items: center }
select:is([multiple], [size]:not([size="1"])) { appearance: auto }
optgroup { font-weight: bolder; display: block }
option { font-weight: normal; display: block; white-space: pre; min-height: 1.2em; padding: 0 2px 1px 2px }
progress { appearance: auto; display: inline-block; box-sizing: border-box; vertical-align: -0.2em; width: 10em; height: 1em }
meter { appearance: auto; display: inline-block; box-sizing: border-box; vertical-align: -0.2em; width: 5em; height: 1em }
button, input[type="button" i], input[type="submit" i], input[type="reset" i], select { cursor: default }
`

// needsHints reports whether presentational attributes may affect n.
func needsHints(n *html.Node) bool {
	for i := range n.Attr {
		switch n.Attr[i].Key {
		case "width", "height", "bgcolor", "align", "valign", "border", "cellpadding", "cellspacing", "nowrap", "color", "face", "size", "text", "link",
			"background", "hspace", "vspace", "type", "dir", "frameborder", "marginwidth", "marginheight", "rows", "cols", "span", "noshade", "start", "value":
			return true
		}
	}
	switch n.Data {
	case "td", "th":
		return true
	}
	return false
}

func hd(name, val string) Decl {
	return Decl{Name: name, Val: val, L: strings.ToLower(val)}
}

func attrLen(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasSuffix(v, "%") {
		if _, ok := parseNumber(strings.TrimSuffix(v, "%")); ok {
			return v
		}
		return ""
	}
	// leading digits
	i := 0
	for i < len(v) && (v[i] >= '0' && v[i] <= '9' || v[i] == '.') {
		i++
	}
	if i == 0 {
		return ""
	}
	return v[:i] + "px"
}

func ancestorTable(n *html.Node) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "table" {
			return p
		}
	}
	return nil
}

// hints returns declarations for presentational attributes.
func (e *Engine) hints(n *html.Node) []Decl {
	if n.Type != html.ElementNode || len(n.Attr) == 0 && n.Data != "td" && n.Data != "th" {
		return nil
	}
	var out []Decl
	get := func(k string) (string, bool) { return attrOf(n, k) }
	tag := n.Data
	if v, ok := get("width"); ok {
		switch tag {
		case "img", "table", "td", "th", "col", "colgroup", "iframe", "canvas", "video", "embed", "object", "hr", "input", "svg", "pre":
			if tag == "input" {
				if t, _ := get("type"); !strings.EqualFold(t, "image") {
					break
				}
			}
			if l := attrLen(v); l != "" {
				out = append(out, hd("width", l))
			}
		}
	}
	if v, ok := get("height"); ok {
		switch tag {
		case "img", "table", "td", "th", "tr", "iframe", "canvas", "video", "embed", "object", "input", "svg", "tbody", "thead", "tfoot":
			if tag == "input" {
				if t, _ := get("type"); !strings.EqualFold(t, "image") {
					break
				}
			}
			if l := attrLen(v); l != "" {
				out = append(out, hd("height", l))
			}
		}
	}
	if v, ok := get("bgcolor"); ok {
		switch tag {
		case "body", "table", "tr", "td", "th", "tbody", "thead", "tfoot":
			if c := legacyColor(v); c != "" {
				out = append(out, hd("background-color", c))
			}
		}
	}
	if v, ok := get("text"); ok && tag == "body" {
		if c := legacyColor(v); c != "" {
			out = append(out, hd("color", c))
		}
	}
	if v, ok := get("color"); ok && tag == "font" {
		if c := legacyColor(v); c != "" {
			out = append(out, hd("color", c))
		}
	}
	if v, ok := get("face"); ok && tag == "font" {
		out = append(out, hd("font-family", v))
	}
	if v, ok := get("size"); ok && tag == "font" {
		v = strings.TrimSpace(v)
		rel := 0
		if strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-") {
			rel = 1
		}
		if k, err := strconv.Atoi(strings.TrimPrefix(v, "+")); err == nil {
			if rel == 1 {
				k += 3
			}
			if k < 1 {
				k = 1
			}
			if k > 7 {
				k = 7
			}
			out = append(out, hd("font-size", []string{"", "x-small", "small", "medium", "large", "x-large", "xx-large", "xxx-large"}[k]))
		}
	}
	if v, ok := get("align"); ok {
		v = strings.ToLower(strings.TrimSpace(v))
		switch tag {
		case "div", "p", "h1", "h2", "h3", "h4", "h5", "h6", "td", "th", "tr", "tbody", "thead", "tfoot", "caption", "col", "colgroup", "center", "legend":
			switch v {
			case "left", "right", "center", "justify":
				out = append(out, hd("text-align", "-webkit-"+v))
				if v == "justify" {
					out[len(out)-1] = hd("text-align", "justify")
				}
			}
		case "table":
			switch v {
			case "left":
				out = append(out, hd("float", "left"))
			case "right":
				out = append(out, hd("float", "right"))
			case "center":
				out = append(out, hd("margin-left", "auto"), hd("margin-right", "auto"))
			}
		case "img", "iframe", "embed", "object", "input":
			switch v {
			case "left", "right":
				out = append(out, hd("float", v))
			case "top", "middle", "bottom":
				out = append(out, hd("vertical-align", v))
			case "absmiddle":
				out = append(out, hd("vertical-align", "middle"))
			}
		case "hr":
			switch v {
			case "left":
				out = append(out, hd("margin-left", "0"), hd("margin-right", "auto"))
			case "right":
				out = append(out, hd("margin-left", "auto"), hd("margin-right", "0"))
			case "center":
				out = append(out, hd("margin-left", "auto"), hd("margin-right", "auto"))
			}
		}
	}
	if v, ok := get("valign"); ok {
		switch tag {
		case "td", "th", "tr", "tbody", "thead", "tfoot":
			out = append(out, hd("vertical-align", strings.ToLower(v)))
		}
	}
	if _, ok := get("nowrap"); ok && (tag == "td" || tag == "th") {
		out = append(out, hd("white-space", "nowrap"))
	}
	switch tag {
	case "table":
		if v, ok := get("border"); ok {
			w := "1px"
			if k, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				w = strconv.Itoa(k) + "px"
				if k == 0 {
					w = "0"
				}
			}
			out = append(out, hd("border-width", w), hd("border-style", "outset"))
		}
		if v, ok := get("cellspacing"); ok {
			if l := attrLen(v); l != "" {
				out = append(out, hd("border-spacing", l))
			}
		}
		if v, ok := get("bordercolor"); ok {
			if c := legacyColor(v); c != "" {
				out = append(out, hd("border-color", c))
			}
		}
	case "td", "th":
		if t := ancestorTable(n); t != nil {
			if v, ok := attrOf(t, "cellpadding"); ok {
				if l := attrLen(v); l != "" {
					out = append(out, hd("padding", l))
				}
			}
			if v, ok := attrOf(t, "border"); ok {
				if k, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || k > 0 {
					out = append(out, hd("border-width", "1px"), hd("border-style", "inset"), hd("border-color", "gray"))
				}
			}
		}
	case "img":
		if v, ok := get("border"); ok {
			if l := attrLen(v); l != "" {
				out = append(out, hd("border-width", l), hd("border-style", "solid"))
			}
		}
		if v, ok := get("hspace"); ok {
			if l := attrLen(v); l != "" {
				out = append(out, hd("margin-left", l), hd("margin-right", l))
			}
		}
		if v, ok := get("vspace"); ok {
			if l := attrLen(v); l != "" {
				out = append(out, hd("margin-top", l), hd("margin-bottom", l))
			}
		}
	case "iframe":
		if v, ok := get("frameborder"); ok && strings.TrimSpace(v) == "0" {
			out = append(out, hd("border-width", "0"))
		}
	case "hr":
		if _, ok := get("noshade"); ok {
			out = append(out, hd("border-style", "solid"), hd("border-color", "gray"), hd("background-color", "gray"))
		}
		if v, ok := get("size"); ok {
			if k, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && k > 1 {
				out = append(out, hd("height", strconv.Itoa(k-2)+"px"))
			}
		}
	case "ol", "ul", "li":
		if v, ok := get("type"); ok {
			m := map[string]string{"1": "decimal", "a": "lower-alpha", "A": "upper-alpha", "i": "lower-roman", "I": "upper-roman",
				"disc": "disc", "circle": "circle", "square": "square"}
			if t, ok := m[strings.TrimSpace(v)]; ok {
				out = append(out, hd("list-style-type", t))
			} else if t, ok := m[strings.ToLower(strings.TrimSpace(v))]; ok && (v == "DISC" || v == "CIRCLE" || v == "SQUARE") {
				out = append(out, hd("list-style-type", t))
			}
		}
	case "body":
		if v, ok := get("marginwidth"); ok {
			if l := attrLen(v); l != "" {
				out = append(out, hd("margin-left", l), hd("margin-right", l))
			}
		}
		if v, ok := get("marginheight"); ok {
			if l := attrLen(v); l != "" {
				out = append(out, hd("margin-top", l), hd("margin-bottom", l))
			}
		}
		if v, ok := get("leftmargin"); ok {
			if l := attrLen(v); l != "" {
				out = append(out, hd("margin-left", l))
			}
		}
		if v, ok := get("topmargin"); ok {
			if l := attrLen(v); l != "" {
				out = append(out, hd("margin-top", l))
			}
		}
	}
	if v, ok := get("dir"); ok {
		v = strings.ToLower(v)
		if v == "ltr" || v == "rtl" {
			out = append(out, hd("direction", v))
		}
	}
	return out
}

// legacyColor parses an HTML legacy color attribute value into a CSS color.
func legacyColor(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if _, _, ok := parseColor(v, colorCtx{}); ok && v[0] != '#' {
		return v
	}
	h := strings.TrimPrefix(v, "#")
	if len(h) == 3 || len(h) == 6 {
		valid := true
		for i := 0; i < len(h); i++ {
			if !isHex(h[i]) {
				valid = false
			}
		}
		if valid {
			return "#" + h
		}
	}
	// legacy: pad / truncate non-hex
	b := []byte{}
	for i := 0; i < len(h); i++ {
		if isHex(h[i]) {
			b = append(b, h[i])
		} else {
			b = append(b, '0')
		}
	}
	for len(b)%3 != 0 {
		b = append(b, '0')
	}
	n := len(b) / 3
	if n > 8 {
		n = 8
	}
	if n > 2 {
		for len(b) > 0 && n > 2 && b[0] == '0' && b[n] == '0' && b[2*n] == '0' {
			b2 := []byte{}
			for k := 0; k < 3; k++ {
				b2 = append(b2, b[k*n+1:(k+1)*n]...)
			}
			b = b2
			n--
		}
	}
	if n < 2 {
		return ""
	}
	return "#" + string(b[0:2]) + string(b[n:n+2]) + string(b[2*n:2*n+2])
}
