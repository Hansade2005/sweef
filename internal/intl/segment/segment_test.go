package segment

import (
	"bufio"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The Unicode Consortium's conformance files for UAX #29 (version 16.0.0): every line lists code points with a break
// (÷) or no break (×) between them.
func runConformance(t *testing.T, file, kind string) {
	f, err := os.Open("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	lines, bad := 0, 0
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rs []rune
		var want []int
		for _, tok := range strings.Fields(line) {
			switch tok {
			case "÷":
				want = append(want, len(rs))
			case "×":
			default:
				v, err := strconv.ParseInt(tok, 16, 32)
				if err != nil {
					t.Fatalf("%s: bad token %q", file, tok)
				}
				rs = append(rs, rune(v))
			}
		}
		got := Boundaries(kind, rs)
		lines++
		if !reflect.DeepEqual(got, want) {
			bad++
			if bad <= 8 {
				t.Errorf("%s line %d: %U\n got  %v\n want %v", file, lines, rs, got, want)
			}
		}
	}
	if lines < 100 {
		t.Fatalf("%s: only %d test lines", file, lines)
	}
	if bad > 0 {
		t.Errorf("%s: %d of %d lines differ", file, bad, lines)
	}
	t.Logf("%s: %d cases", file, lines)
}

func TestGraphemeConformance(t *testing.T) { runConformance(t, "GraphemeBreakTest.txt", "grapheme") }
func TestWordConformance(t *testing.T)     { runConformance(t, "WordBreakTest.txt", "word") }
func TestSentenceConformance(t *testing.T) { runConformance(t, "SentenceBreakTest.txt", "sentence") }

func TestBoundariesBasics(t *testing.T) {
	cases := []struct {
		kind, s string
		want    []int
	}{
		{"grapheme", "", nil},
		{"grapheme", "abc", []int{0, 1, 2, 3}},
		{"grapheme", "éa", []int{0, 2, 3}},
		{"grapheme", "👨‍👩‍👧x", []int{0, 5, 6}},
		{"grapheme", "🇫🇷🇩🇪", []int{0, 2, 4}},
		{"word", "Hello, world! 3.14", []int{0, 5, 6, 7, 12, 13, 14, 18}},
		{"sentence", "Hi there. How are you? Fine.", []int{0, 10, 23, 28}},
	}
	for _, c := range cases {
		if got := Boundaries(c.kind, []rune(c.s)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %q: got %v, want %v", c.kind, c.s, got, c.want)
		}
	}
}
