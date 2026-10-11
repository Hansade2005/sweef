package pdf

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const headingText = "Quarterly Engineering Report"

func sampleLines() []Line {
	lines := []Line{
		{Kind: "h1", Text: headingText},
		{Kind: "p", Text: "This document exercises the renderer with \u201ccurly quotes\u201d, an em\u2014dash, an ellipsis\u2026 and a bullet \u2022 plus a snowman \u2603 that falls back to a question mark."},
		{Kind: "link", Text: "Visit the example site", Href: "https://example.com/path?q=1&r=(2)"},
		{Kind: "link", Text: "Relative link gets no annotation", Href: "/relative"},
		{Kind: "h2", Text: "Section with a list"},
		{Kind: "li", Text: "First item", Indent: 1},
		{Kind: "li", Text: "Second item with a very long run of text that must wrap because it exceeds the width of the page by quite a large margin indeed, more than once", Indent: 1},
		{Kind: "li", Text: "Nested item", Indent: 2},
		{Kind: "input", Text: "Search the site"},
		{Kind: "button", Text: "Submit"},
		{Kind: "hr"},
		{Kind: "h3", Text: "Code"},
		{Kind: "pre", Text: "func main() {\n\tfmt.Println(\"hello (world)\\n\")\n}"},
		{Kind: "p", Text: "Averyveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryverylongwordwithoutspaces"},
	}
	for i := 0; i < 120; i++ {
		lines = append(lines, Line{Kind: "p", Text: fmt.Sprintf("Paragraph %d. The quick brown fox jumps over the lazy dog while the five boxing wizards jump quickly, and the document keeps growing until it spills onto more pages.", i)})
	}
	return lines
}

func render(t *testing.T, lines []Line, o Options) []byte {
	t.Helper()
	b, err := Render(lines, o)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return b
}

func TestRenderStructure(t *testing.T) {
	b := render(t, sampleLines(), Options{Title: "Report (test)", URL: "https://example.com/report", HeaderFooter: true})

	if !bytes.HasPrefix(b, []byte("%PDF-1.4\n")) {
		t.Fatalf("output does not start with %%PDF-1.4 header: %q", b[:16])
	}
	if !bytes.HasSuffix(bytes.TrimRight(b, "\n"), []byte("%%EOF")) {
		t.Fatalf("output does not end with %%%%EOF: %q", b[len(b)-16:])
	}

	// Page count: the /Pages object's /Count must equal the number of page objects.
	pageObjs := bytes.Count(b, []byte("/Type /Page "))
	if pageObjs < 2 {
		t.Fatalf("expected a multi-page document, got %d pages", pageObjs)
	}
	m := regexp.MustCompile(`/Type /Pages /Kids \[([^\]]*)\] /Count (\d+)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("no /Pages object found")
	}
	count, _ := strconv.Atoi(string(m[2]))
	if count != pageObjs {
		t.Fatalf("/Count %d != %d page objects", count, pageObjs)
	}
	if kids := strings.Fields(string(m[1])); len(kids) != 3*count {
		t.Fatalf("/Kids has %d tokens, want %d", len(kids), 3*count)
	}
	if !bytes.Contains(b, []byte(fmt.Sprintf("(page %d of %d)", count, count))) {
		t.Fatalf("footer for last page not found")
	}
	if !bytes.Contains(b, []byte("(page 1 of ")) {
		t.Fatalf("footer for first page not found")
	}

	// Link annotation present with the absolute URL, and not for the relative one.
	if !bytes.Contains(b, []byte("/Subtype /Link")) {
		t.Fatalf("no /Link annotation")
	}
	if !bytes.Contains(b, []byte("/URI (https://example.com/path?q=1&r=\\(2\\))")) {
		t.Fatalf("link URI not found or not escaped")
	}
	if bytes.Contains(b, []byte("/URI (/relative)")) {
		t.Fatalf("relative href must not produce an annotation")
	}
	if bytes.Count(b, []byte("/Subtype /Link")) != 1 {
		t.Fatalf("expected exactly one link annotation, got %d", bytes.Count(b, []byte("/Subtype /Link")))
	}
	// Every /Annots array entry must reference an existing object.
	for _, am := range regexp.MustCompile(`/Annots \[([^\]]*)\]`).FindAllSubmatch(b, -1) {
		for _, ref := range regexp.MustCompile(`(\d+) 0 R`).FindAllSubmatch(am[1], -1) {
			if !bytes.Contains(b, []byte("\n"+string(ref[1])+" 0 obj\n<< /Type /Annot")) {
				t.Fatalf("annotation object %s missing", ref[1])
			}
		}
	}

	// Fonts and encoding.
	for _, want := range []string{"/BaseFont /Helvetica ", "/BaseFont /Helvetica-Bold", "/BaseFont /Courier", "/Encoding /WinAnsiEncoding"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("missing %q", want)
		}
	}
	// Curly quotes etc. map to WinAnsi octal escapes; the snowman becomes '?'.
	for _, want := range []string{`\223curly quotes\224`, `em\227dash`, `ellipsis\205`, `bullet \225`, `snowman ?`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("expected encoded text %q in content", want)
		}
	}
	// Pre text is escaped and uses Courier.
	if !bytes.Contains(b, []byte(`/F3 `)) || !bytes.Contains(b, []byte(`hello \(world\)\\n`)) {
		t.Fatalf("pre block not rendered with Courier and escaped parentheses")
	}
	if !bytes.Contains(b, []byte(" re S Q")) || !bytes.Contains(b, []byte(" re f Q")) {
		t.Fatalf("expected stroked (input) and filled (button) rectangles")
	}
}

func TestXrefOffsets(t *testing.T) {
	b := render(t, sampleLines(), Options{Title: "xref"})

	// Locate startxref.
	idx := bytes.LastIndex(b, []byte("startxref\n"))
	if idx < 0 {
		t.Fatalf("no startxref")
	}
	rest := b[idx+len("startxref\n"):]
	nl := bytes.IndexByte(rest, '\n')
	xrefPos, err := strconv.Atoi(string(rest[:nl]))
	if err != nil {
		t.Fatalf("bad startxref value: %v", err)
	}
	if !bytes.HasPrefix(b[xrefPos:], []byte("xref\n")) {
		t.Fatalf("startxref %d does not point at xref keyword", xrefPos)
	}

	// Parse the subsection header "0 N".
	hdrStart := xrefPos + len("xref\n")
	hdrEnd := hdrStart + bytes.IndexByte(b[hdrStart:], '\n')
	hdr := strings.Fields(string(b[hdrStart:hdrEnd]))
	if len(hdr) != 2 || hdr[0] != "0" {
		t.Fatalf("bad xref subsection header %q", hdr)
	}
	n, _ := strconv.Atoi(hdr[1])
	if n < 10 {
		t.Fatalf("too few objects in xref: %d", n)
	}
	entries := b[hdrEnd+1:]
	for i := 0; i < n; i++ {
		e := entries[i*20 : i*20+20]
		if len(e) != 20 || e[18] != ' ' || e[19] != '\n' {
			t.Fatalf("xref entry %d is not 20 bytes with ' \\n' terminator: %q", i, e)
		}
		off, err := strconv.Atoi(string(e[:10]))
		if err != nil {
			t.Fatalf("entry %d offset: %v", i, err)
		}
		gen := string(e[11:16])
		typ := e[17]
		if i == 0 {
			if typ != 'f' || gen != "65535" {
				t.Fatalf("entry 0 must be free with gen 65535: %q", e)
			}
			continue
		}
		if typ != 'n' || gen != "00000" {
			t.Fatalf("entry %d must be in use with gen 0: %q", i, e)
		}
		marker := fmt.Sprintf("%d 0 obj\n", i)
		if !bytes.HasPrefix(b[off:], []byte(marker)) {
			t.Fatalf("xref entry %d offset %d does not point at %q (got %q)", i, off, marker, b[off:off+min(len(b)-off, 20)])
		}
	}
	// Trailer /Size matches the entry count.
	if !bytes.Contains(b[xrefPos:], []byte(fmt.Sprintf("/Size %d", n))) {
		t.Fatalf("trailer /Size does not match %d", n)
	}
	// Each stream's /Length is exact.
	for _, m := range regexp.MustCompile(`<< /Length (\d+) >>\nstream\n`).FindAllSubmatchIndex(b, -1) {
		length, _ := strconv.Atoi(string(b[m[2]:m[3]]))
		start := m[1]
		if !bytes.HasPrefix(b[start+length:], []byte("endstream")) {
			t.Fatalf("stream /Length %d is wrong", length)
		}
	}
}

func TestOptionsAndEdgeCases(t *testing.T) {
	// Empty input still produces one valid page; DefaultOptions is accepted.
	b := render(t, nil, DefaultOptions())
	if !bytes.Contains(b, []byte("(page 1 of 1)")) {
		t.Fatalf("DefaultOptions should enable the footer")
	}
	if bytes.Count(b, []byte("/Type /Page ")) != 1 {
		t.Fatalf("empty document should have one page")
	}
	if !bytes.Contains(b, []byte("/MediaBox [0 0 595.28 841.89]")) {
		t.Fatalf("default A4 media box missing")
	}

	// Landscape swaps dimensions; HeaderFooter false omits the footer.
	b = render(t, []Line{{Text: "hi"}}, Options{Landscape: true, HeaderFooter: false})
	if !bytes.Contains(b, []byte("/MediaBox [0 0 841.89 595.28]")) {
		t.Fatalf("landscape media box missing")
	}
	if bytes.Contains(b, []byte("(page 1 of 1)")) {
		t.Fatalf("footer rendered although HeaderFooter is false")
	}

	// Custom page size (US Letter) is respected.
	b = render(t, []Line{{Text: "hi"}}, Options{PageWidthPt: 612, PageHeightPt: 792})
	if !bytes.Contains(b, []byte("/MediaBox [0 0 612 792]")) {
		t.Fatalf("custom media box missing")
	}

	// Invalid geometry errors.
	if _, err := Render(nil, Options{MarginPt: 400}); err == nil {
		t.Fatalf("expected error for oversized margins")
	}
	if _, err := Render(nil, Options{PageWidthPt: -1}); err == nil {
		t.Fatalf("expected error for negative page width")
	}
	if _, err := Render(nil, Options{BaseFontPt: -1}); err == nil {
		t.Fatalf("expected error for negative font size")
	}
}

func TestWrapWords(t *testing.T) {
	text := strings.Repeat("word ", 50)
	lines := wrapWords(text, fontHelvetica, 10, 200)
	if len(lines) < 2 {
		t.Fatalf("expected wrapping, got %d lines", len(lines))
	}
	for _, l := range lines {
		if w := textWidth(l, fontHelvetica, 10); w > 200 {
			t.Fatalf("line %q is %.2f pt wide, exceeds 200", l, w)
		}
	}
	if textWidth([]byte("W"), fontHelvetica, 1000) != 944 {
		t.Fatalf("Helvetica W width wrong")
	}
	if textWidth([]byte("W"), fontHelveticaBold, 1000) != 944 || textWidth([]byte("i"), fontHelveticaBold, 1000) != 278 {
		t.Fatalf("Helvetica-Bold widths wrong")
	}
	if textWidth([]byte("abc"), fontCourier, 10) != 18 {
		t.Fatalf("Courier width wrong")
	}
}

func TestEncodeWinAnsi(t *testing.T) {
	got := encodeWinAnsi("a\u2018b\u2019\u201cc\u201d\u2013\u2014\u2026\u2022\u00e9\u20ac\u2603\u200b")
	want := []byte{'a', 0x91, 'b', 0x92, 0x93, 'c', 0x94, 0x96, 0x97, 0x85, 0x95, 0xE9, 0x80, '?'}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeWinAnsi = % x, want % x", got, want)
	}
	if s := pdfString([]byte("a(b)\\c\x91")); s != `(a\(b\)\\c\221)` {
		t.Fatalf("pdfString = %s", s)
	}
}

func writeTemp(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "out.pdf")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQpdfCheck(t *testing.T) {
	qpdf, err := exec.LookPath("qpdf")
	if err != nil {
		t.Skip("qpdf not on PATH")
	}
	p := writeTemp(t, render(t, sampleLines(), Options{Title: "qpdf", URL: "https://example.com", HeaderFooter: true}))
	out, err := exec.Command(qpdf, "--check", p).CombinedOutput()
	if err != nil {
		t.Fatalf("qpdf --check failed: %v\n%s", err, out)
	}
}

func TestPoppler(t *testing.T) {
	pdftotext, errT := exec.LookPath("pdftotext")
	pdfinfo, errI := exec.LookPath("pdfinfo")
	if errT != nil && errI != nil {
		t.Skip("pdftotext/pdfinfo not on PATH")
	}
	b := render(t, sampleLines(), Options{Title: "Poppler Test", URL: "https://example.com/x", HeaderFooter: true})
	pages := bytes.Count(b, []byte("/Type /Page "))
	p := writeTemp(t, b)

	if errI == nil {
		out, err := exec.Command(pdfinfo, p).CombinedOutput()
		if err != nil {
			t.Fatalf("pdfinfo failed: %v\n%s", err, out)
		}
		if !regexp.MustCompile(`(?m)^Pages:\s+` + strconv.Itoa(pages) + `$`).Match(out) {
			t.Fatalf("pdfinfo did not report %d pages:\n%s", pages, out)
		}
		if !bytes.Contains(out, []byte("Poppler Test")) {
			t.Fatalf("pdfinfo did not report the title:\n%s", out)
		}
	}
	if errT == nil {
		out, err := exec.Command(pdftotext, p, "-").CombinedOutput()
		if err != nil {
			t.Fatalf("pdftotext failed: %v\n%s", err, out)
		}
		if !bytes.Contains(out, []byte(headingText)) {
			t.Fatalf("pdftotext output lacks heading %q:\n%s", headingText, out)
		}
		if !bytes.Contains(out, []byte("\u201ccurly quotes\u201d")) {
			t.Fatalf("pdftotext did not recover curly quotes:\n%s", out[:min(len(out), 600)])
		}
		if !bytes.Contains(out, []byte(fmt.Sprintf("page %d of %d", pages, pages))) {
			t.Fatalf("pdftotext did not recover the footer")
		}
	}
}
