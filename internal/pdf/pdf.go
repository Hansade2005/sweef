// Package pdf renders structured text lines into a minimal, valid PDF 1.4
// document using only the standard library. It uses the standard 14 fonts
// (Helvetica, Helvetica-Bold, Courier) with WinAnsiEncoding, performs simple
// word wrapping and pagination, and emits an uncompressed cross-reference
// table so that any conforming reader accepts the output.
//
// It is intentionally small: there is no layout engine, no images and no
// compression. The goal is to give a browser engine a Page.pdf-like
// capability from an already extracted text outline of a page.
package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Line is one logical block of text to render.
//
// Kind selects the style. Recognised kinds are "h1", "h2", "h3", "p", "li",
// "link", "input", "button", "pre", "hr" and "" (treated like "p"). Indent is
// a nesting depth that shifts the block to the right. Href is used for "link"
// lines; an absolute http(s) URL produces a clickable /Link annotation.
type Line struct {
	Text   string
	Kind   string
	Indent int
	Href   string
}

// Options controls page geometry and decoration.
type Options struct {
	Title        string
	URL          string
	PageWidthPt  float64
	PageHeightPt float64
	MarginPt     float64
	BaseFontPt   float64
	Landscape    bool
	HeaderFooter bool
}

// Default geometry (A4 portrait).
const (
	DefaultPageWidthPt  = 595.28
	DefaultPageHeightPt = 841.89
	DefaultMarginPt     = 48
	DefaultBaseFontPt   = 10.5
)

// DefaultOptions returns the recommended options: A4, 48 pt margins, 10.5 pt
// Helvetica, header and footer enabled. Render fills in zero numeric fields
// with these defaults; HeaderFooter is a plain bool, so a zero Options
// renders without the running header and footer.
func DefaultOptions() Options {
	return Options{
		PageWidthPt:  DefaultPageWidthPt,
		PageHeightPt: DefaultPageHeightPt,
		MarginPt:     DefaultMarginPt,
		BaseFontPt:   DefaultBaseFontPt,
		HeaderFooter: true,
	}
}

// font identifiers used as PDF resource names.
const (
	fontHelvetica     = "F1"
	fontHelveticaBold = "F2"
	fontCourier       = "F3"
)

// style describes how a line kind is drawn.
type style struct {
	font        string
	size        float64 // multiplier of the base font size, or absolute when abs is true
	abs         bool
	bold        bool
	spaceBefore float64 // in units of the line's font size
	spaceAfter  float64
	color       [3]float64
	bullet      bool
	box         bool // stroked box (input)
	fill        bool // filled box (button)
}

var (
	colorBlack = [3]float64{0, 0, 0}
	colorBlue  = [3]float64{0.05, 0.25, 0.75}
	colorGray  = [3]float64{0.35, 0.35, 0.35}
)

func styleFor(kind string, base float64) style {
	switch kind {
	case "h1":
		return style{font: fontHelveticaBold, size: 18, abs: true, bold: true, spaceBefore: 0.9, spaceAfter: 0.45, color: colorBlack}
	case "h2":
		return style{font: fontHelveticaBold, size: 15, abs: true, bold: true, spaceBefore: 0.8, spaceAfter: 0.4, color: colorBlack}
	case "h3":
		return style{font: fontHelveticaBold, size: 12.5, abs: true, bold: true, spaceBefore: 0.7, spaceAfter: 0.35, color: colorBlack}
	case "li":
		return style{font: fontHelvetica, size: base, abs: true, spaceAfter: 0.25, color: colorBlack, bullet: true}
	case "link":
		return style{font: fontHelvetica, size: base, abs: true, spaceAfter: 0.4, color: colorBlue}
	case "input":
		return style{font: fontHelvetica, size: base, abs: true, spaceBefore: 0.3, spaceAfter: 0.6, color: colorGray, box: true}
	case "button":
		return style{font: fontHelveticaBold, size: base, abs: true, bold: true, spaceBefore: 0.3, spaceAfter: 0.6, color: colorBlack, fill: true}
	case "pre":
		return style{font: fontCourier, size: base * 0.9, abs: true, spaceAfter: 0.5, color: colorBlack}
	default: // "p", "" and unknown kinds
		return style{font: fontHelvetica, size: base, abs: true, spaceAfter: 0.5, color: colorBlack}
	}
}

// textRun is a positioned piece of text on a page.
type textRun struct {
	x, y  float64
	font  string
	size  float64
	color [3]float64
	text  []byte // WinAnsi encoded
}

// shape is a stroked or filled rectangle/line.
type shape struct {
	x, y, w, h float64
	fill       bool
	gray       float64 // fill or stroke gray level
	width      float64 // stroke width (0 for fill-only)
}

type annot struct {
	x1, y1, x2, y2 float64
	uri            string
}

type page struct {
	runs   []textRun
	shapes []shape
	annots []annot
}

// Render lays out lines and returns the bytes of a PDF 1.4 document.
func Render(lines []Line, o Options) ([]byte, error) {
	o = normalize(o)
	if o.PageWidthPt <= 0 || o.PageHeightPt <= 0 {
		return nil, errors.New("pdf: page size must be positive")
	}
	if 2*o.MarginPt >= o.PageWidthPt || 2*o.MarginPt >= o.PageHeightPt {
		return nil, errors.New("pdf: margins exceed page size")
	}
	if o.BaseFontPt <= 0 {
		return nil, errors.New("pdf: base font size must be positive")
	}

	pages := layout(lines, o)
	return emit(pages, o), nil
}

func normalize(o Options) Options {
	d := DefaultOptions()
	if o.PageWidthPt == 0 {
		o.PageWidthPt = d.PageWidthPt
	}
	if o.PageHeightPt == 0 {
		o.PageHeightPt = d.PageHeightPt
	}
	if o.MarginPt == 0 {
		o.MarginPt = d.MarginPt
	}
	if o.BaseFontPt == 0 {
		o.BaseFontPt = d.BaseFontPt
	}
	if o.Landscape && o.PageWidthPt < o.PageHeightPt {
		o.PageWidthPt, o.PageHeightPt = o.PageHeightPt, o.PageWidthPt
	}
	return o
}

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

type layoutState struct {
	o      Options
	pages  []*page
	cur    *page
	y      float64 // current baseline cursor (top of next line)
	top    float64
	bottom float64
	left   float64
	width  float64
}

func layout(lines []Line, o Options) []*page {
	ls := &layoutState{o: o}
	ls.left = o.MarginPt
	ls.width = o.PageWidthPt - 2*o.MarginPt
	ls.top = o.PageHeightPt - o.MarginPt
	ls.bottom = o.MarginPt
	if o.HeaderFooter {
		// Reserve a strip for the running header and footer.
		ls.top -= 14
		ls.bottom += 14
	}
	ls.newPage()

	const indentStep = 18.0
	for _, ln := range lines {
		st := styleFor(ln.Kind, o.BaseFontPt)
		size := st.size
		leading := size * 1.35
		indent := float64(ln.Indent) * indentStep
		if indent > ls.width/2 {
			indent = ls.width / 2
		}
		x := ls.left + indent
		avail := ls.width - indent

		if ln.Kind == "hr" {
			ls.ensure(leading)
			ls.y -= leading * 0.5
			ls.cur.shapes = append(ls.cur.shapes, shape{x: x, y: ls.y, w: avail, h: 0, gray: 0.6, width: 0.75})
			ls.y -= leading * 0.5
			continue
		}

		text := strings.TrimRight(ln.Text, " \t\r\n")
		if text == "" && !st.box && !st.fill {
			// Blank paragraph: a small vertical gap.
			ls.y -= leading * 0.5
			continue
		}

		// Space before the block, but not at the top of a page.
		if st.spaceBefore > 0 && ls.y < ls.top-0.01 {
			ls.y -= st.spaceBefore * size
		}

		bulletW := 0.0
		if st.bullet {
			bulletW = size * 1.2
		}
		padX := 0.0
		if st.box || st.fill {
			padX = size * 0.5
		}
		textX := x + bulletW + padX
		textAvail := avail - bulletW - 2*padX
		if textAvail < size*3 {
			textAvail = size * 3
		}

		var wrapped [][]byte
		if ln.Kind == "pre" {
			for _, raw := range strings.Split(text, "\n") {
				raw = strings.ReplaceAll(raw, "\t", "    ")
				wrapped = append(wrapped, wrapChars(encodeWinAnsi(raw), st.font, size, textAvail)...)
			}
		} else {
			wrapped = wrapWords(text, st.font, size, textAvail)
		}
		if len(wrapped) == 0 {
			wrapped = [][]byte{{}}
		}

		linkURI := ""
		if ln.Kind == "link" && isAbsoluteHTTP(ln.Href) {
			linkURI = ln.Href
		}

		// Box / button background is drawn per page segment of the block.
		boxStartY := 0.0
		boxPage := (*page)(nil)
		flushBox := func(lastBaseline float64) {
			if !(st.box || st.fill) || boxPage == nil {
				return
			}
			top := boxStartY
			bot := lastBaseline - size*0.35
			w := textAvail + 2*padX
			if st.fill {
				// Fit the button to its text when it is short.
				maxW := 0.0
				for _, wl := range wrapped {
					if ww := textWidth(wl, st.font, size); ww > maxW {
						maxW = ww
					}
				}
				if maxW+2*padX < w {
					w = maxW + 2*padX
				}
				boxPage.shapes = append(boxPage.shapes, shape{x: x, y: bot, w: w, h: top - bot, fill: true, gray: 0.88})
			}
			boxPage.shapes = append(boxPage.shapes, shape{x: x, y: bot, w: w, h: top - bot, gray: 0.55, width: 0.6})
		}

		for i, wl := range wrapped {
			if ls.y-leading < ls.bottom {
				if boxPage != nil {
					flushBox(ls.y + leading - size)
				}
				ls.newPage()
				boxPage = nil
			}
			if (st.box || st.fill) && boxPage == nil {
				boxPage = ls.cur
				boxStartY = ls.y - size*0.15
			}
			baseline := ls.y - size
			if i == 0 && st.bullet {
				ls.cur.runs = append(ls.cur.runs, textRun{x: x, y: baseline, font: fontHelvetica, size: size, color: st.color, text: []byte{0x95}})
			}
			ls.cur.runs = append(ls.cur.runs, textRun{x: textX, y: baseline, font: st.font, size: size, color: st.color, text: wl})
			if linkURI != "" {
				w := textWidth(wl, st.font, size)
				ls.cur.annots = append(ls.cur.annots, annot{x1: textX, y1: baseline - size*0.25, x2: textX + w, y2: baseline + size*0.95, uri: linkURI})
				// Underline the link.
				ls.cur.shapes = append(ls.cur.shapes, shape{x: textX, y: baseline - size*0.12, w: w, h: 0, gray: 0.3, width: 0.5})
			}
			ls.y -= leading
		}
		flushBox(ls.y + leading - size)
		ls.y -= st.spaceAfter * size
	}
	return ls.pages
}

func (ls *layoutState) newPage() {
	p := &page{}
	ls.pages = append(ls.pages, p)
	ls.cur = p
	ls.y = ls.top
}

// ensure starts a new page if h points do not fit on the current one.
func (ls *layoutState) ensure(h float64) {
	if ls.y-h < ls.bottom && ls.y < ls.top-0.01 {
		ls.newPage()
	}
}

func isAbsoluteHTTP(u string) bool {
	lu := strings.ToLower(u)
	return (strings.HasPrefix(lu, "http://") && len(u) > 7) || (strings.HasPrefix(lu, "https://") && len(u) > 8)
}

// ---------------------------------------------------------------------------
// Text measurement and wrapping
// ---------------------------------------------------------------------------

// helveticaWidths holds the AFM advance widths (1/1000 em) for the ASCII
// printable range 0x20..0x7E of Helvetica.
var helveticaWidths = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // ' '..'/'
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, // '0'..'?'
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, // '@'..'O'
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, // 'P'..'_'
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, // '`'..'o'
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, // 'p'..'~'
}

// helveticaBoldWidths holds the AFM advance widths for Helvetica-Bold.
var helveticaBoldWidths = [95]int{
	278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
	975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
	333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
	611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584,
}

const defaultGlyphWidth = 556

// glyphWidth returns the advance width of a WinAnsi byte in 1/1000 em.
func glyphWidth(b byte, font string) int {
	if font == fontCourier {
		return 600
	}
	if b >= 0x20 && b <= 0x7E {
		if font == fontHelveticaBold {
			return helveticaBoldWidths[b-0x20]
		}
		return helveticaWidths[b-0x20]
	}
	// A few common WinAnsi punctuation glyphs.
	switch b {
	case 0x91, 0x92: // quoteleft, quoteright
		if font == fontHelveticaBold {
			return 278
		}
		return 222
	case 0x93, 0x94: // quotedblleft, quotedblright
		if font == fontHelveticaBold {
			return 500
		}
		return 333
	case 0x96: // endash
		return 556
	case 0x97: // emdash
		return 1000
	case 0x85: // ellipsis
		return 1000
	case 0x95: // bullet
		return 350
	case 0xA0: // nbsp
		return 278
	}
	return defaultGlyphWidth
}

// textWidth measures WinAnsi-encoded text in points.
func textWidth(b []byte, font string, size float64) float64 {
	total := 0
	for _, c := range b {
		total += glyphWidth(c, font)
	}
	return float64(total) * size / 1000
}

// wrapWords breaks text into lines no wider than maxW points. Words wider
// than the line are split by character.
func wrapWords(text string, font string, size float64, maxW float64) [][]byte {
	words := strings.Fields(text)
	var out [][]byte
	var line []byte
	lineW := 0.0
	spaceW := textWidth([]byte{' '}, font, size)
	for _, w := range words {
		enc := encodeWinAnsi(w)
		ww := textWidth(enc, font, size)
		if ww > maxW {
			// Flush the current line and split the long word by characters.
			if len(line) > 0 {
				out = append(out, line)
				line, lineW = nil, 0
			}
			parts := wrapChars(enc, font, size, maxW)
			if len(parts) > 1 {
				out = append(out, parts[:len(parts)-1]...)
			}
			last := parts[len(parts)-1]
			line = append([]byte(nil), last...)
			lineW = textWidth(last, font, size)
			continue
		}
		if len(line) == 0 {
			line = append(line, enc...)
			lineW = ww
			continue
		}
		if lineW+spaceW+ww <= maxW {
			line = append(line, ' ')
			line = append(line, enc...)
			lineW += spaceW + ww
			continue
		}
		out = append(out, line)
		line = append([]byte(nil), enc...)
		lineW = ww
	}
	if len(line) > 0 || len(out) == 0 {
		out = append(out, line)
	}
	return out
}

// wrapChars splits already-encoded text by characters to fit maxW.
func wrapChars(enc []byte, font string, size float64, maxW float64) [][]byte {
	if len(enc) == 0 {
		return [][]byte{{}}
	}
	var out [][]byte
	var line []byte
	lineW := 0.0
	for _, c := range enc {
		cw := textWidth([]byte{c}, font, size)
		if len(line) > 0 && lineW+cw > maxW {
			out = append(out, line)
			line, lineW = nil, 0
		}
		line = append(line, c)
		lineW += cw
	}
	out = append(out, line)
	return out
}

// ---------------------------------------------------------------------------
// Encoding
// ---------------------------------------------------------------------------

// winAnsiHigh maps Unicode code points to WinAnsi bytes in the 0x80..0x9F
// range (the part that differs from Latin-1).
var winAnsiHigh = map[rune]byte{
	'€': 0x80, // euro
	'‚': 0x82, // quotesinglbase
	'ƒ': 0x83, // florin
	'„': 0x84, // quotedblbase
	'…': 0x85, // ellipsis
	'†': 0x86, // dagger
	'‡': 0x87, // daggerdbl
	'ˆ': 0x88, // circumflex
	'‰': 0x89, // perthousand
	'Š': 0x8A, // Scaron
	'‹': 0x8B, // guilsinglleft
	'Œ': 0x8C, // OE
	'Ž': 0x8E, // Zcaron
	'‘': 0x91, // quoteleft
	'’': 0x92, // quoteright
	'“': 0x93, // quotedblleft
	'”': 0x94, // quotedblright
	'•': 0x95, // bullet
	'–': 0x96, // endash
	'—': 0x97, // emdash
	'˜': 0x98, // tilde
	'™': 0x99, // trademark
	'š': 0x9A, // scaron
	'›': 0x9B, // guilsinglright
	'œ': 0x9C, // oe
	'ž': 0x9E, // zcaron
	'Ÿ': 0x9F, // Ydieresis
	// Approximations for common characters outside WinAnsi.
	'‐': '-',  // hyphen
	'‑': '-',  // non-breaking hyphen
	'‒': 0x96, // figure dash
	'―': 0x97, // horizontal bar
	'′': 0x27, // prime
	'″': 0x22, // double prime
	'⁃': 0x95, // hyphen bullet
	'●': 0x95, // black circle
	'◦': 0x95, // white bullet
	'∙': 0x95, // bullet operator
	' ': 0xA0, // nbsp (identity, listed for clarity)
	' ': ' ',  // thin space
	' ': ' ',  // hair space
	' ': ' ',  // en space
	' ': ' ',  // em space
	'　': ' ',  // ideographic space
	'→': '>',  // rightwards arrow
	'←': '<',  // leftwards arrow
	'−': '-',  // minus sign
	'­': 0xAD, // soft hyphen (identity)
	'ʼ': 0x92, // modifier letter apostrophe
}

// encodeWinAnsi converts a UTF-8 string to WinAnsi bytes. Characters outside
// WinAnsi become '?'. Zero-width characters are dropped.
func encodeWinAnsi(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n <= 1:
			out = append(out, '?')
		case r == '\t':
			out = append(out, ' ', ' ', ' ', ' ')
		case r == '\n' || r == '\r':
			out = append(out, ' ')
		case r < 0x20:
			// drop control characters
		case r < 0x7F:
			out = append(out, byte(r))
		case r == 0x7F:
			// drop DEL
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		case r == '\u200B' || r == '\u200C' || r == '\u200D' || r == '\uFEFF' || r == '\u2060':
			// zero width: drop
		default:
			if b, ok := winAnsiHigh[r]; ok {
				out = append(out, b)
			} else {
				out = append(out, '?')
			}
		}
	}
	return out
}

// pdfString escapes bytes for a PDF literal string in parentheses.
func pdfString(b []byte) string {
	var sb strings.Builder
	sb.WriteByte('(')
	for _, c := range b {
		switch {
		case c == '\\' || c == '(' || c == ')':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c == '\r':
			sb.WriteString("\\r")
		case c == '\n':
			sb.WriteString("\\n")
		case c < 0x20 || c >= 0x7F:
			sb.WriteString("\\")
			sb.WriteString(fmt.Sprintf("%03o", c))
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte(')')
	return sb.String()
}

// pdfTextString encodes a Go string for use in the Info dictionary or a
// /URI entry. Non-ASCII text in the Info dictionary uses PDFDocEncoding,
// which coincides with WinAnsi for the characters we map.
func pdfTextString(s string) string {
	return pdfString(encodeWinAnsi(s))
}

// uriString escapes a URL as an ASCII PDF string, percent-encoding bytes
// outside the printable ASCII range.
func uriString(u string) string {
	var buf []byte
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c < 0x21 || c > 0x7E {
			buf = append(buf, fmt.Sprintf("%%%02X", c)...)
		} else {
			buf = append(buf, c)
		}
	}
	return pdfString(buf)
}

func num(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" || s == "-0" {
		return "0"
	}
	return s
}

// ---------------------------------------------------------------------------
// Emission
// ---------------------------------------------------------------------------

// emit serialises the laid-out pages into a complete PDF file.
func emit(pages []*page, o Options) []byte {
	// Object numbering:
	//   1 Catalog, 2 Pages, 3 Helvetica, 4 Helvetica-Bold, 5 Courier, 6 Info,
	//   then for each page: page object, content stream, annotation objects.
	type obj struct {
		body string
	}
	objs := []obj{{}} // index 0 unused (placeholder for the free object)
	add := func(body string) int {
		objs = append(objs, obj{body: body})
		return len(objs) - 1
	}

	// Reserve numbers for the fixed objects first.
	catalogNum := add("")
	pagesNum := add("")
	helvNum := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	helvBoldNum := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	courierNum := add("<< /Type /Font /Subtype /Type1 /BaseFont /Courier /Encoding /WinAnsiEncoding >>")
	infoNum := add("")

	resources := fmt.Sprintf("<< /Font << /%s %d 0 R /%s %d 0 R /%s %d 0 R >> /ProcSet [/PDF /Text] >>",
		fontHelvetica, helvNum, fontHelveticaBold, helvBoldNum, fontCourier, courierNum)

	total := len(pages)
	pageNums := make([]int, 0, total)
	for i, p := range pages {
		content := buildContent(p, i+1, total, o)
		contentNum := add(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))

		annotRefs := make([]string, 0, len(p.annots))
		for _, a := range p.annots {
			n := add(fmt.Sprintf("<< /Type /Annot /Subtype /Link /Rect [%s %s %s %s] /Border [0 0 0] /A << /Type /Action /S /URI /URI %s >> >>",
				num(a.x1), num(a.y1), num(a.x2), num(a.y2), uriString(a.uri)))
			annotRefs = append(annotRefs, fmt.Sprintf("%d 0 R", n))
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %s %s] /Resources %s /Contents %d 0 R",
			pagesNum, num(o.PageWidthPt), num(o.PageHeightPt), resources, contentNum)
		if len(annotRefs) > 0 {
			fmt.Fprintf(&sb, " /Annots [%s]", strings.Join(annotRefs, " "))
		}
		sb.WriteString(" >>")
		pageNums = append(pageNums, add(sb.String()))
	}

	kids := make([]string, len(pageNums))
	for i, n := range pageNums {
		kids[i] = fmt.Sprintf("%d 0 R", n)
	}
	objs[pagesNum].body = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), total)
	objs[catalogNum].body = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum)

	var info strings.Builder
	info.WriteString("<< /Producer (sweef internal/pdf)")
	if o.Title != "" {
		info.WriteString(" /Title ")
		info.WriteString(pdfTextString(o.Title))
	}
	if o.URL != "" {
		info.WriteString(" /Subject ")
		info.WriteString(pdfTextString(o.URL))
	}
	info.WriteString(" >>")
	objs[infoNum].body = info.String()

	// Serialise.
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	out.Write([]byte{'%', 0xE2, 0xE3, 0xCF, 0xD3, '\n'}) // binary marker comment
	offsets := make([]int, len(objs))
	for i := 1; i < len(objs); i++ {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i, objs[i].body)
	}
	xrefPos := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", len(objs))
	out.WriteString("0000000000 65535 f \n")
	for i := 1; i < len(objs); i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs), catalogNum, infoNum, xrefPos)
	return out.Bytes()
}

// buildContent produces the content stream for one page.
func buildContent(p *page, pageNo, total int, o Options) string {
	var sb strings.Builder

	// Shapes first so text draws over fills.
	for _, s := range p.shapes {
		if s.fill {
			fmt.Fprintf(&sb, "q %s g %s %s %s %s re f Q\n", num(s.gray), num(s.x), num(s.y), num(s.w), num(s.h))
			continue
		}
		if s.h == 0 {
			fmt.Fprintf(&sb, "q %s G %s w %s %s m %s %s l S Q\n", num(s.gray), num(s.width), num(s.x), num(s.y), num(s.x+s.w), num(s.y))
		} else {
			fmt.Fprintf(&sb, "q %s G %s w %s %s %s %s re S Q\n", num(s.gray), num(s.width), num(s.x), num(s.y), num(s.w), num(s.h))
		}
	}

	if o.HeaderFooter {
		hf := 8.0
		headerY := o.PageHeightPt - o.MarginPt*0.5
		footerY := o.MarginPt*0.5 - hf*0.3
		ruleY := o.PageHeightPt - o.MarginPt + 6
		fmt.Fprintf(&sb, "q 0.75 G 0.5 w %s %s m %s %s l S Q\n", num(o.MarginPt), num(ruleY), num(o.PageWidthPt-o.MarginPt), num(ruleY))

		contentW := o.PageWidthPt - 2*o.MarginPt
		title := strings.TrimSpace(o.Title)
		url := strings.TrimSpace(o.URL)
		if title != "" {
			t := truncateToWidth(encodeWinAnsi(title), fontHelvetica, hf, contentW*0.6)
			writeText(&sb, textRun{x: o.MarginPt, y: headerY, font: fontHelvetica, size: hf, color: colorGray, text: t})
		}
		if url != "" {
			maxW := contentW * 0.38
			if title == "" {
				maxW = contentW
			}
			t := truncateToWidth(encodeWinAnsi(url), fontHelvetica, hf, maxW)
			w := textWidth(t, fontHelvetica, hf)
			writeText(&sb, textRun{x: o.PageWidthPt - o.MarginPt - w, y: headerY, font: fontHelvetica, size: hf, color: colorGray, text: t})
		}
		label := encodeWinAnsi(fmt.Sprintf("page %d of %d", pageNo, total))
		w := textWidth(label, fontHelvetica, hf)
		writeText(&sb, textRun{x: (o.PageWidthPt - w) / 2, y: footerY, font: fontHelvetica, size: hf, color: colorGray, text: label})
	}

	for _, r := range p.runs {
		writeText(&sb, r)
	}
	return sb.String()
}

func writeText(sb *strings.Builder, r textRun) {
	fmt.Fprintf(sb, "BT /%s %s Tf %s %s %s rg %s %s Td %s Tj ET\n",
		r.font, num(r.size), num(r.color[0]), num(r.color[1]), num(r.color[2]), num(r.x), num(r.y), pdfString(r.text))
}

// truncateToWidth shortens encoded text with an ellipsis to fit maxW.
func truncateToWidth(b []byte, font string, size, maxW float64) []byte {
	if textWidth(b, font, size) <= maxW {
		return b
	}
	ell := []byte{0x85}
	ellW := textWidth(ell, font, size)
	w := 0.0
	for i, c := range b {
		cw := textWidth([]byte{c}, font, size)
		if w+cw+ellW > maxW {
			return append(append([]byte(nil), b[:i]...), ell...)
		}
		w += cw
	}
	return b
}
