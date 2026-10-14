package segment

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestNodeSegments compares the boundaries with what Node's Intl.Segmenter (ICU 77) finds in a set of texts
// (tools/intlgen/segcases.js). The texts that need ICU's break dictionaries (Thai, Lao, Khmer, Burmese, Chinese, Japanese) are only
// counted: Sweef cuts them where UAX #29 does, see docs/web-platform.md.
func TestNodeSegments(t *testing.T) {
	raw, err := os.ReadFile("testdata/node_segments.json")
	if err != nil {
		t.Fatal(err)
	}
	type testCase struct {
		Text        string `json:"text"`
		Granularity string `json:"granularity"`
		Boundaries  []int  `json:"boundaries"`
	}
	var data struct {
		Cases      []testCase `json:"cases"`
		Dictionary []testCase `json:"dictionary"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	// Node lists the start of every segment and then the length of the text; Boundaries gives UTF-32 offsets of the same
	run := func(c testCase) []int {
		rs := []rune(c.Text)
		units := make([]int, len(rs)+1)
		for i, r := range rs {
			units[i+1] = units[i] + 1
			if r >= 0x10000 {
				units[i+1]++
			}
		}
		var out []int
		for _, x := range Boundaries(c.Granularity, rs) {
			out = append(out, units[x])
		}
		return out
	}
	for _, c := range data.Cases {
		if got := run(c); !reflect.DeepEqual(got, c.Boundaries) {
			t.Errorf("%s %q: got %v, want %v", c.Granularity, c.Text, got, c.Boundaries)
		}
	}
	same := 0
	for _, c := range data.Dictionary {
		if reflect.DeepEqual(run(c), c.Boundaries) {
			same++
		}
	}
	t.Logf("texts that need a dictionary: %d of %d segmented as Node does", same, len(data.Dictionary))
}
