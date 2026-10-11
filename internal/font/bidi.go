package font

import (
	"sort"
	"unicode"

	"golang.org/x/text/unicode/bidi"
)

// Basic bidirectional text and Arabic joining support for callers that lay
// out text themselves (the canvas in internal/raster uses it; the HTML
// layout can adopt BidiRuns and ShapeArabic the same way).
//
// Not covered: mark positioning (GPOS mark/mkmk), contextual alternates
// beyond the four joining forms, Syriac/N'Ko/Mongolian joining, Indic and
// other complex scripts.

// BidiRun is a span of runes [Start, End) in one direction. For an RTL run
// the characters are displayed in reverse logical order.
type BidiRun struct {
	Start, End int
	RTL        bool
}

// BidiRuns resolves one line of text with the Unicode Bidirectional
// Algorithm and returns its runs in visual (left to right) order. baseRTL
// selects the paragraph direction (CSS direction: rtl).
func BidiRuns(text []rune, baseRTL bool) []BidiRun {
	if len(text) == 0 {
		return nil
	}
	levels := BidiLevels(text, baseRTL)
	order := reorderVisual(levels)
	var out []BidiRun
	for k := 0; k < len(order); {
		i := order[k]
		rtl := levels[i]&1 == 1
		j := k + 1
		for j < len(order) {
			d := order[j] - order[j-1]
			if levels[order[j]]&1 == 1 != rtl || (!rtl && d != 1) || (rtl && d != -1) {
				break
			}
			j++
		}
		a, b := order[k], order[j-1]
		if a > b {
			a, b = b, a
		}
		out = append(out, BidiRun{Start: a, End: b + 1, RTL: rtl})
		k = j
	}
	return out
}

// ReorderVisual returns the logical index shown at each visual position for
// per-element embedding levels (rule L2).
func ReorderVisual(levels []uint8) []int { return reorderVisual(levels) }

// reorderVisual returns the logical index shown at each visual position
// (rule L2).
func reorderVisual(levels []uint8) []int {
	order := make([]int, len(levels))
	for i := range order {
		order[i] = i
	}
	var hi, lo uint8 = 0, 255
	for _, l := range levels {
		if l > hi {
			hi = l
		}
		if l&1 == 1 && l < lo {
			lo = l
		}
	}
	for l := hi; l >= lo && l > 0; l-- {
		for i := 0; i < len(order); {
			if levels[order[i]] < l {
				i++
				continue
			}
			j := i
			for j < len(order) && levels[order[j]] >= l {
				j++
			}
			for a, b := i, j-1; a < b; a, b = a+1, b-1 {
				order[a], order[b] = order[b], order[a]
			}
			i = j
		}
	}
	return order
}

var bracketPairs = map[rune]rune{'(': ')', '[': ']', '{': '}', '«': '»', '‹': '›', '⟨': '⟩', '「': '」', '『': '』', '【': '】', '〔': '〕'}

func bidiClass(r rune) bidi.Class {
	p, _ := bidi.LookupRune(r)
	return p.Class()
}

func isNI(c bidi.Class) bool {
	switch c {
	case bidi.B, bidi.S, bidi.WS, bidi.ON, bidi.FSI, bidi.LRI, bidi.RLI, bidi.PDI:
		return true
	}
	return false
}

// BidiLevels computes the embedding level of every rune of one paragraph
// (UAX #9 rules X1-X8, W1-W7, N0-N2, I1-I2 and L1).
func BidiLevels(rs []rune, baseRTL bool) []uint8 {
	n := len(rs)
	levels := make([]uint8, n)
	cls := make([]bidi.Class, n)
	var base uint8
	if baseRTL {
		base = 1
	}
	for i, r := range rs {
		cls[i] = bidiClass(r)
	}
	// ---- explicit levels (X1-X8)
	type entry struct {
		level    uint8
		override bidi.Class // L, R or 0xff for neutral
		isolate  bool
	}
	const none = bidi.Class(0xff)
	stack := []entry{{base, none, false}}
	overIso, overEmb, validIso := 0, 0, 0
	nextOdd := func(l uint8) uint8 { return (l + 1) | 1 }
	nextEven := func(l uint8) uint8 { return (l + 2) &^ 1 }
	removed := make([]bool, n)
	for i := 0; i < n; i++ {
		top := stack[len(stack)-1]
		switch c := cls[i]; c {
		case bidi.RLE, bidi.LRE, bidi.RLO, bidi.LRO:
			levels[i] = top.level
			removed[i] = true
			var nl uint8
			if c == bidi.RLE || c == bidi.RLO {
				nl = nextOdd(top.level)
			} else {
				nl = nextEven(top.level)
			}
			if nl <= 125 && overIso == 0 && overEmb == 0 {
				ov := none
				if c == bidi.RLO {
					ov = bidi.R
				} else if c == bidi.LRO {
					ov = bidi.L
				}
				stack = append(stack, entry{nl, ov, false})
			} else if overIso == 0 {
				overEmb++
			}
		case bidi.RLI, bidi.LRI, bidi.FSI:
			levels[i] = top.level
			if top.override != none {
				cls[i] = top.override
			}
			isRTL := c == bidi.RLI
			if c == bidi.FSI {
				isRTL = firstStrongRTL(rs, cls, i+1)
			}
			var nl uint8
			if isRTL {
				nl = nextOdd(top.level)
			} else {
				nl = nextEven(top.level)
			}
			if nl <= 125 && overIso == 0 && overEmb == 0 {
				validIso++
				stack = append(stack, entry{nl, none, true})
			} else {
				overIso++
			}
		case bidi.PDI:
			if overIso > 0 {
				overIso--
			} else if validIso > 0 {
				overEmb = 0
				for !stack[len(stack)-1].isolate {
					stack = stack[:len(stack)-1]
				}
				stack = stack[:len(stack)-1]
				validIso--
			}
			top = stack[len(stack)-1]
			levels[i] = top.level
			if top.override != none {
				cls[i] = top.override
			}
		case bidi.PDF:
			levels[i] = top.level
			removed[i] = true
			if overIso > 0 {
			} else if overEmb > 0 {
				overEmb--
			} else if !top.isolate && len(stack) >= 2 {
				stack = stack[:len(stack)-1]
			}
		case bidi.B:
			levels[i] = base
		case bidi.BN:
			levels[i] = top.level
			removed[i] = true
		default:
			levels[i] = top.level
			if top.override != none {
				cls[i] = top.override
			}
		}
	}
	// ---- level runs over the kept characters
	var kept []int
	for i := 0; i < n; i++ {
		if !removed[i] {
			kept = append(kept, i)
		}
	}
	for a := 0; a < len(kept); {
		b := a + 1
		for b < len(kept) && levels[kept[b]] == levels[kept[a]] {
			b++
		}
		lvl := levels[kept[a]]
		prev, next := base, base
		if a > 0 {
			prev = levels[kept[a-1]]
		}
		if b < len(kept) {
			next = levels[kept[b]]
		}
		sos, eos := bidi.L, bidi.L
		if maxU8(prev, lvl)&1 == 1 {
			sos = bidi.R
		}
		if maxU8(next, lvl)&1 == 1 {
			eos = bidi.R
		}
		resolveSequence(rs, cls, levels, kept[a:b], lvl, sos, eos)
		a = b
	}
	// ---- L1: trailing whitespace and isolate formatting take the base level
	i := n - 1
	for ; i >= 0; i-- {
		c := bidiClass(rs[i])
		if c == bidi.WS || c == bidi.FSI || c == bidi.LRI || c == bidi.RLI || c == bidi.PDI || removed[i] {
			levels[i] = base
			continue
		}
		break
	}
	for i := 0; i < n; i++ {
		switch bidiClass(rs[i]) {
		case bidi.B, bidi.S:
			levels[i] = base
			for j := i - 1; j >= 0; j-- {
				c := bidiClass(rs[j])
				if c == bidi.WS || c == bidi.FSI || c == bidi.LRI || c == bidi.RLI || c == bidi.PDI || removed[j] {
					levels[j] = base
					continue
				}
				break
			}
		}
	}
	// removed characters share the level of the preceding kept one
	for i := 0; i < n; i++ {
		if removed[i] {
			if i > 0 {
				levels[i] = levels[i-1]
			} else {
				levels[i] = base
			}
		}
	}
	return levels
}

func maxU8(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}

func firstStrongRTL(rs []rune, cls []bidi.Class, from int) bool {
	depth := 0
	for i := from; i < len(rs); i++ {
		switch cls[i] {
		case bidi.LRI, bidi.RLI, bidi.FSI:
			depth++
		case bidi.PDI:
			if depth == 0 {
				return false
			}
			depth--
		case bidi.L:
			if depth == 0 {
				return false
			}
		case bidi.R, bidi.AL:
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

// resolveSequence applies the weak, neutral and implicit rules to one level
// run (idx lists its characters in logical order).
func resolveSequence(rs []rune, cls []bidi.Class, levels []uint8, idx []int, lvl uint8, sos, eos bidi.Class) {
	m := len(idx)
	t := make([]bidi.Class, m)
	for k, i := range idx {
		t[k] = cls[i]
	}
	// W1
	prev := sos
	for k := range t {
		if t[k] == bidi.NSM {
			if prev == bidi.LRI || prev == bidi.RLI || prev == bidi.FSI || prev == bidi.PDI {
				t[k] = bidi.ON
			} else {
				t[k] = prev
			}
		}
		prev = t[k]
	}
	// W2, W3
	last := sos
	for k := range t {
		switch t[k] {
		case bidi.L, bidi.R, bidi.AL:
			last = t[k]
		case bidi.EN:
			if last == bidi.AL {
				t[k] = bidi.AN
			}
		}
	}
	for k := range t {
		if t[k] == bidi.AL {
			t[k] = bidi.R
		}
	}
	// W4
	for k := 1; k+1 < m; k++ {
		if t[k] == bidi.ES && t[k-1] == bidi.EN && t[k+1] == bidi.EN {
			t[k] = bidi.EN
		} else if t[k] == bidi.CS && (t[k-1] == bidi.EN && t[k+1] == bidi.EN || t[k-1] == bidi.AN && t[k+1] == bidi.AN) {
			t[k] = t[k-1]
		}
	}
	// W5
	for k := 0; k < m; {
		if t[k] != bidi.ET {
			k++
			continue
		}
		e := k
		for e < m && t[e] == bidi.ET {
			e++
		}
		if k > 0 && t[k-1] == bidi.EN || e < m && t[e] == bidi.EN {
			for q := k; q < e; q++ {
				t[q] = bidi.EN
			}
		}
		k = e
	}
	// W6
	for k := range t {
		switch t[k] {
		case bidi.ES, bidi.ET, bidi.CS:
			t[k] = bidi.ON
		}
	}
	// W7
	last = sos
	for k := range t {
		switch t[k] {
		case bidi.L, bidi.R:
			last = t[k]
		case bidi.EN:
			if last == bidi.L {
				t[k] = bidi.L
			}
		}
	}
	e := bidi.L
	if lvl&1 == 1 {
		e = bidi.R
	}
	strong := func(c bidi.Class) bidi.Class { // direction of a resolved class for N rules
		switch c {
		case bidi.L:
			return bidi.L
		case bidi.R, bidi.EN, bidi.AN:
			return bidi.R
		}
		return bidi.ON
	}
	// N0: paired brackets
	type open struct {
		r rune
		k int
	}
	var stack []open
	type pair struct{ a, b int }
	var pairs []pair
	for k, i := range idx {
		if t[k] != bidi.ON {
			continue
		}
		r := rs[i]
		if cl, ok := bracketPairs[r]; ok {
			if len(stack) < 63 {
				stack = append(stack, open{cl, k})
			}
		} else {
			for q := len(stack) - 1; q >= 0; q-- {
				if stack[q].r == r {
					pairs = append(pairs, pair{stack[q].k, k})
					stack = stack[:q]
					break
				}
			}
		}
	}
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].a < pairs[b].a })
	for _, pr := range pairs {
		foundE, foundO := false, false
		for k := pr.a + 1; k < pr.b; k++ {
			switch strong(t[k]) {
			case e:
				foundE = true
			case bidi.ON:
			default:
				foundO = true
			}
		}
		set := bidi.ON
		switch {
		case foundE:
			set = e
		case foundO:
			ctx := strong(sos)
			for k := pr.a - 1; k >= 0; k-- {
				if c := strong(t[k]); c != bidi.ON {
					ctx = c
					break
				}
			}
			if ctx != e {
				set = ctx // the context matches the opposite direction
			} else {
				set = e
			}
		}
		if set != bidi.ON {
			t[pr.a], t[pr.b] = set, set
			for _, q := range []int{pr.a, pr.b} {
				for k := q + 1; k < m && cls[idx[k]] == bidi.NSM; k++ {
					t[k] = set
				}
			}
		}
	}
	// N1, N2
	for k := 0; k < m; {
		if !isNI(t[k]) {
			k++
			continue
		}
		b := k
		for b < m && isNI(t[b]) {
			b++
		}
		before, after := sos, eos
		if k > 0 {
			before = strong(t[k-1])
		}
		if b < m {
			after = strong(t[b])
		}
		d := e
		if before == after && before != bidi.ON {
			d = before
		}
		for q := k; q < b; q++ {
			t[q] = d
		}
		k = b
	}
	// I1, I2
	for k, i := range idx {
		l := lvl
		switch t[k] {
		case bidi.L:
			if lvl&1 == 1 {
				l++
			}
		case bidi.R:
			if lvl&1 == 0 {
				l++
			}
		case bidi.EN, bidi.AN:
			if lvl&1 == 0 {
				l += 2
			} else {
				l++
			}
		}
		levels[i] = l
	}
}

// IsRTLRune reports whether r is a strong right-to-left character (Hebrew,
// Arabic and friends) or a bidi control; text without any can skip bidi.
func IsRTLRune(r rune) bool {
	if r < 0x590 {
		return r == 0x200f || r == 0x202b || r == 0x202e || r == 0x2067
	}
	switch {
	case r <= 0x8ff, r >= 0xfb1d && r <= 0xfdff, r >= 0xfe70 && r <= 0xfeff, r >= 0x10800 && r <= 0x10fff, r >= 0x1e800 && r <= 0x1efff:
		return true
	}
	return r == 0x200f || r == 0x202b || r == 0x202e || r == 0x2067
}

var arabicForms = map[rune][4]rune{ // isolated, final, initial, medial
	0x0621: {0xFE80, 0x0000, 0x0000, 0x0000},
	0x0622: {0xFE81, 0xFE82, 0x0000, 0x0000},
	0x0623: {0xFE83, 0xFE84, 0x0000, 0x0000},
	0x0624: {0xFE85, 0xFE86, 0x0000, 0x0000},
	0x0625: {0xFE87, 0xFE88, 0x0000, 0x0000},
	0x0626: {0xFE89, 0xFE8A, 0xFE8B, 0xFE8C},
	0x0627: {0xFE8D, 0xFE8E, 0x0000, 0x0000},
	0x0628: {0xFE8F, 0xFE90, 0xFE91, 0xFE92},
	0x0629: {0xFE93, 0xFE94, 0x0000, 0x0000},
	0x062A: {0xFE95, 0xFE96, 0xFE97, 0xFE98},
	0x062B: {0xFE99, 0xFE9A, 0xFE9B, 0xFE9C},
	0x062C: {0xFE9D, 0xFE9E, 0xFE9F, 0xFEA0},
	0x062D: {0xFEA1, 0xFEA2, 0xFEA3, 0xFEA4},
	0x062E: {0xFEA5, 0xFEA6, 0xFEA7, 0xFEA8},
	0x062F: {0xFEA9, 0xFEAA, 0x0000, 0x0000},
	0x0630: {0xFEAB, 0xFEAC, 0x0000, 0x0000},
	0x0631: {0xFEAD, 0xFEAE, 0x0000, 0x0000},
	0x0632: {0xFEAF, 0xFEB0, 0x0000, 0x0000},
	0x0633: {0xFEB1, 0xFEB2, 0xFEB3, 0xFEB4},
	0x0634: {0xFEB5, 0xFEB6, 0xFEB7, 0xFEB8},
	0x0635: {0xFEB9, 0xFEBA, 0xFEBB, 0xFEBC},
	0x0636: {0xFEBD, 0xFEBE, 0xFEBF, 0xFEC0},
	0x0637: {0xFEC1, 0xFEC2, 0xFEC3, 0xFEC4},
	0x0638: {0xFEC5, 0xFEC6, 0xFEC7, 0xFEC8},
	0x0639: {0xFEC9, 0xFECA, 0xFECB, 0xFECC},
	0x063A: {0xFECD, 0xFECE, 0xFECF, 0xFED0},
	0x0641: {0xFED1, 0xFED2, 0xFED3, 0xFED4},
	0x0642: {0xFED5, 0xFED6, 0xFED7, 0xFED8},
	0x0643: {0xFED9, 0xFEDA, 0xFEDB, 0xFEDC},
	0x0644: {0xFEDD, 0xFEDE, 0xFEDF, 0xFEE0},
	0x0645: {0xFEE1, 0xFEE2, 0xFEE3, 0xFEE4},
	0x0646: {0xFEE5, 0xFEE6, 0xFEE7, 0xFEE8},
	0x0647: {0xFEE9, 0xFEEA, 0xFEEB, 0xFEEC},
	0x0648: {0xFEED, 0xFEEE, 0x0000, 0x0000},
	0x0649: {0xFEEF, 0xFEF0, 0xFBE8, 0xFBE9},
	0x064A: {0xFEF1, 0xFEF2, 0xFEF3, 0xFEF4},
	0x0671: {0xFB50, 0xFB51, 0x0000, 0x0000},
	0x0677: {0xFBDD, 0x0000, 0x0000, 0x0000},
	0x0679: {0xFB66, 0xFB67, 0xFB68, 0xFB69},
	0x067A: {0xFB5E, 0xFB5F, 0xFB60, 0xFB61},
	0x067B: {0xFB52, 0xFB53, 0xFB54, 0xFB55},
	0x067E: {0xFB56, 0xFB57, 0xFB58, 0xFB59},
	0x067F: {0xFB62, 0xFB63, 0xFB64, 0xFB65},
	0x0680: {0xFB5A, 0xFB5B, 0xFB5C, 0xFB5D},
	0x0683: {0xFB76, 0xFB77, 0xFB78, 0xFB79},
	0x0684: {0xFB72, 0xFB73, 0xFB74, 0xFB75},
	0x0686: {0xFB7A, 0xFB7B, 0xFB7C, 0xFB7D},
	0x0687: {0xFB7E, 0xFB7F, 0xFB80, 0xFB81},
	0x0688: {0xFB88, 0xFB89, 0x0000, 0x0000},
	0x068C: {0xFB84, 0xFB85, 0x0000, 0x0000},
	0x068D: {0xFB82, 0xFB83, 0x0000, 0x0000},
	0x068E: {0xFB86, 0xFB87, 0x0000, 0x0000},
	0x0691: {0xFB8C, 0xFB8D, 0x0000, 0x0000},
	0x0698: {0xFB8A, 0xFB8B, 0x0000, 0x0000},
	0x06A4: {0xFB6A, 0xFB6B, 0xFB6C, 0xFB6D},
	0x06A6: {0xFB6E, 0xFB6F, 0xFB70, 0xFB71},
	0x06A9: {0xFB8E, 0xFB8F, 0xFB90, 0xFB91},
	0x06AD: {0xFBD3, 0xFBD4, 0xFBD5, 0xFBD6},
	0x06AF: {0xFB92, 0xFB93, 0xFB94, 0xFB95},
	0x06B1: {0xFB9A, 0xFB9B, 0xFB9C, 0xFB9D},
	0x06B3: {0xFB96, 0xFB97, 0xFB98, 0xFB99},
	0x06BA: {0xFB9E, 0xFB9F, 0x0000, 0x0000},
	0x06BB: {0xFBA0, 0xFBA1, 0xFBA2, 0xFBA3},
	0x06BE: {0xFBAA, 0xFBAB, 0xFBAC, 0xFBAD},
	0x06C0: {0xFBA4, 0xFBA5, 0x0000, 0x0000},
	0x06C1: {0xFBA6, 0xFBA7, 0xFBA8, 0xFBA9},
	0x06C5: {0xFBE0, 0xFBE1, 0x0000, 0x0000},
	0x06C6: {0xFBD9, 0xFBDA, 0x0000, 0x0000},
	0x06C7: {0xFBD7, 0xFBD8, 0x0000, 0x0000},
	0x06C8: {0xFBDB, 0xFBDC, 0x0000, 0x0000},
	0x06C9: {0xFBE2, 0xFBE3, 0x0000, 0x0000},
	0x06CB: {0xFBDE, 0xFBDF, 0x0000, 0x0000},
	0x06CC: {0xFBFC, 0xFBFD, 0xFBFE, 0xFBFF},
	0x06D0: {0xFBE4, 0xFBE5, 0xFBE6, 0xFBE7},
	0x06D2: {0xFBAE, 0xFBAF, 0x0000, 0x0000},
	0x06D3: {0xFBB0, 0xFBB1, 0x0000, 0x0000},
}

// lam followed by an alef variant forms a mandatory ligature.
var lamAlef = map[rune][2]rune{ // alef -> isolated, final ligature
	0x0622: {0xFEF5, 0xFEF6},
	0x0623: {0xFEF7, 0xFEF8},
	0x0625: {0xFEF9, 0xFEFA},
	0x0627: {0xFEFB, 0xFEFC},
}

func arabicTransparent(r rune) bool {
	return unicode.Is(unicode.Mn, r) && (r >= 0x0610 && r <= 0x061a || r >= 0x064b && r <= 0x065f || r == 0x0670 ||
		r >= 0x06d6 && r <= 0x06dc || r >= 0x06df && r <= 0x06e4 || r == 0x06e7 || r == 0x06e8 || r >= 0x06ea && r <= 0x06ed)
}

// joinsNext reports whether r can join to the letter after it (dual or
// join-causing), and joinsPrev whether it can join to the letter before.
func joinsNext(r rune) bool {
	if r == 0x0640 || r == 0x200d {
		return true
	}
	f, ok := arabicForms[r]
	return ok && f[2] != 0
}

func joinsPrev(r rune) bool {
	if r == 0x0640 || r == 0x200d {
		return true
	}
	f, ok := arabicForms[r]
	return ok && f[1] != 0
}

// ShapeArabic maps Arabic letters in logical order to their contextual
// presentation forms (isolated, initial, medial, final) and combines
// lam-alef into the mandatory ligature. The result is still in logical
// order and may be shorter than the input; non-Arabic runes pass through.
func ShapeArabic(rs []rune) []rune {
	out, _ := shapeArabic(rs, false)
	return out
}

// ShapeArabicKeep is ShapeArabic that preserves the rune count: a lam-alef
// ligature takes the lam's place and the alef position becomes 0 (a rune the
// caller skips), so indexes into the input stay valid.
func ShapeArabicKeep(rs []rune) []rune {
	out, _ := shapeArabic(rs, true)
	return out
}

func shapeArabic(rs []rune, keep bool) ([]rune, bool) {
	has := false
	for _, r := range rs {
		if r >= 0x0600 && r <= 0x06ff {
			has = true
			break
		}
	}
	if !has {
		return rs, false
	}
	out := make([]rune, 0, len(rs))
	prevJoins := func(i int) bool { // does the previous non-transparent letter join forward?
		for j := i - 1; j >= 0; j-- {
			if arabicTransparent(rs[j]) {
				continue
			}
			return joinsNext(rs[j])
		}
		return false
	}
	nextJoins := func(i int) bool {
		for j := i + 1; j < len(rs); j++ {
			if arabicTransparent(rs[j]) {
				continue
			}
			return joinsPrev(rs[j])
		}
		return false
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		forms, ok := arabicForms[r]
		if !ok {
			out = append(out, r)
			continue
		}
		pj := prevJoins(i) && forms[1] != 0
		// lam + alef ligature
		if r == 0x0644 {
			j := i + 1
			for j < len(rs) && arabicTransparent(rs[j]) {
				j++
			}
			if j < len(rs) {
				if lig, ok := lamAlef[rs[j]]; ok {
					if pj {
						out = append(out, lig[1])
					} else {
						out = append(out, lig[0])
					}
					out = append(out, rs[i+1:j]...) // marks between stay after the ligature
					if keep {
						out = append(out, 0)
					}
					i = j
					continue
				}
			}
		}
		nj := nextJoins(i) && forms[2] != 0
		var f rune
		switch {
		case pj && nj:
			f = forms[3]
		case nj:
			f = forms[2]
		case pj:
			f = forms[1]
		default:
			f = forms[0]
		}
		if f == 0 {
			f = r
		}
		out = append(out, f)
	}
	return out, true
}
