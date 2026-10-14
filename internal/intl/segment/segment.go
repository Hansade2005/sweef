// Package segment implements the Unicode text segmentation rules of UAX #29 (grapheme clusters, words, sentences)
// for Intl.Segmenter. The rules are the default ones of Unicode 16; there is no dictionary, so scripts written
// without spaces (Thai, Lao, Khmer, Burmese, Japanese, Chinese) are cut where UAX #29 cuts them, not at the word
// boundaries ICU finds with its dictionaries.
package segment

import "sort"

func lookup(t []rng, r rune) uint8 {
	i := sort.Search(len(t), func(i int) bool { return t[i].hi >= r })
	if i < len(t) && t[i].lo <= r {
		return t[i].c
	}
	return 0
}

func isExtPict(r rune) bool {
	i := sort.Search(len(extPictTable)/2, func(i int) bool { return extPictTable[2*i+1] >= r })
	return i < len(extPictTable)/2 && extPictTable[2*i] <= r
}

// Boundaries returns the break positions (indexes into rs, including 0 and len(rs)) of kind "grapheme", "word" or
// "sentence". An empty input has no boundaries.
func Boundaries(kind string, rs []rune) []int {
	if len(rs) == 0 {
		return nil
	}
	switch kind {
	case "word":
		return words(rs)
	case "sentence":
		return sentences(rs)
	}
	return graphemes(rs)
}

// ---- grapheme clusters (UAX #29 rules GB1-GB999) --------------------------------------------------------------

func graphemes(rs []rune) []int {
	out := []int{0}
	n := len(rs)
	// state for GB9c, GB11, GB12/13 over the characters before the boundary
	ri := 0    // regional indicators in a row ending at i-1
	emoji := 0 // 1: ExtPict Extend* ended at i-1; 2: ... ZWJ
	incb := 0  // 1: Consonant (Extend|Linker)* with no linker yet; 2: with a linker
	update := func(r rune, c uint8) {
		if c == gcbRegionalIndicator {
			ri++
		} else {
			ri = 0
		}
		switch {
		case isExtPict(r):
			emoji = 1
		case c == gcbExtend && emoji == 1:
		case c == gcbZWJ && emoji == 1:
			emoji = 2
		default:
			emoji = 0
		}
		ib := lookup(incbTable, r)
		switch {
		case ib == incbConsonant:
			incb = 1
		case incb != 0 && ib == incbLinker:
			incb = 2
		case incb != 0 && (ib == incbExtend || c == gcbExtend || c == gcbZWJ):
		default:
			incb = 0
		}
	}
	prev := lookup(gcbTable, rs[0])
	update(rs[0], prev)
	for i := 1; i < n; i++ {
		cur := lookup(gcbTable, rs[i])
		brk := true
		switch {
		case prev == gcbCR && cur == gcbLF:
			brk = false
		case prev == gcbControl || prev == gcbCR || prev == gcbLF:
		case cur == gcbControl || cur == gcbCR || cur == gcbLF:
		case prev == gcbL && (cur == gcbL || cur == gcbV || cur == gcbLV || cur == gcbLVT):
			brk = false
		case (prev == gcbLV || prev == gcbV) && (cur == gcbV || cur == gcbT):
			brk = false
		case (prev == gcbLVT || prev == gcbT) && cur == gcbT:
			brk = false
		case cur == gcbExtend || cur == gcbZWJ:
			brk = false
		case cur == gcbSpacingMark:
			brk = false
		case prev == gcbPrepend:
			brk = false
		case incb == 2 && lookup(incbTable, rs[i]) == incbConsonant: // GB9c
			brk = false
		case emoji == 2 && isExtPict(rs[i]): // GB11
			brk = false
		case prev == gcbRegionalIndicator && cur == gcbRegionalIndicator && ri%2 == 1: // GB12, GB13
			brk = false
		}
		if brk {
			out = append(out, i)
		}
		update(rs[i], cur)
		prev = cur
	}
	return append(out, n)
}

// ---- words (UAX #29 rules WB1-WB999) ----------------------------------------------------------------------------

func isAH(c uint8) bool         { return c == wbALetter || c == wbHebrewLetter }
func isMidNumLetQ(c uint8) bool { return c == wbMidNumLet || c == wbSingleQuote }
func isNL(c uint8) bool         { return c == wbNewline || c == wbCR || c == wbLF }
func isIgn(c uint8) bool        { return c == wbExtend || c == wbFormat || c == wbZWJ }

func words(rs []rune) []int {
	n := len(rs)
	cls := make([]uint8, n)
	for i, r := range rs {
		cls[i] = lookup(wbTable, r)
	}
	// units: a character with the Extend, Format and ZWJ characters that follow it (WB4), except after newlines
	var start []int
	for i := 0; i < n; i++ {
		if i > 0 && isIgn(cls[i]) && !isNL(cls[start[len(start)-1]]) {
			continue
		}
		start = append(start, i)
	}
	unitOf := make(map[int]int, len(start))
	for u, s := range start {
		unitOf[s] = u
	}
	ucls := func(u int) uint8 {
		if u < 0 || u >= len(start) {
			return 0
		}
		return cls[start[u]]
	}
	out := []int{0}
	for i := 1; i < n; i++ {
		a, b := cls[i-1], cls[i]
		brk := true
		switch {
		case a == wbCR && b == wbLF:
			brk = false
		case isNL(a), isNL(b):
		case a == wbZWJ && isExtPict(rs[i]):
			brk = false
		case a == wbWSegSpace && b == wbWSegSpace:
			brk = false
		case isIgn(b):
			brk = false
		default:
			u, ok := unitOf[i]
			if !ok {
				break
			}
			A, B := ucls(u-1), ucls(u)
			Ap, Bn := ucls(u-2), ucls(u+1)
			switch {
			case isAH(A) && isAH(B):
				brk = false
			case isAH(A) && (B == wbMidLetter || isMidNumLetQ(B)) && isAH(Bn):
				brk = false
			case isAH(Ap) && (A == wbMidLetter || isMidNumLetQ(A)) && isAH(B):
				brk = false
			case A == wbHebrewLetter && B == wbSingleQuote:
				brk = false
			case A == wbHebrewLetter && B == wbDoubleQuote && Bn == wbHebrewLetter:
				brk = false
			case Ap == wbHebrewLetter && A == wbDoubleQuote && B == wbHebrewLetter:
				brk = false
			case A == wbNumeric && B == wbNumeric:
				brk = false
			case isAH(A) && B == wbNumeric:
				brk = false
			case A == wbNumeric && isAH(B):
				brk = false
			case Ap == wbNumeric && (A == wbMidNum || isMidNumLetQ(A)) && B == wbNumeric:
				brk = false
			case A == wbNumeric && (B == wbMidNum || isMidNumLetQ(B)) && Bn == wbNumeric:
				brk = false
			case A == wbKatakana && B == wbKatakana:
				brk = false
			case (isAH(A) || A == wbNumeric || A == wbKatakana || A == wbExtendNumLet) && B == wbExtendNumLet:
				brk = false
			case A == wbExtendNumLet && (isAH(B) || B == wbNumeric || B == wbKatakana):
				brk = false
			case A == wbRegionalIndicator && B == wbRegionalIndicator:
				cnt := 0
				for v := u - 1; v >= 0 && ucls(v) == wbRegionalIndicator; v-- {
					cnt++
				}
				if cnt%2 == 1 {
					brk = false
				}
			}
		}
		if brk {
			out = append(out, i)
		}
	}
	return append(out, n)
}

// ---- sentences (UAX #29 rules SB1-SB998) --------------------------------------------------------------------------

func isParaSep(c uint8) bool { return c == sbSep || c == sbCR || c == sbLF }
func isSATerm(c uint8) bool  { return c == sbSTerm || c == sbATerm }

func sentences(rs []rune) []int {
	n := len(rs)
	cls := make([]uint8, n)
	for i, r := range rs {
		cls[i] = lookup(sbTable, r)
	}
	var start []int
	for i := 0; i < n; i++ {
		if i > 0 && (cls[i] == sbExtend || cls[i] == sbFormat) && !isParaSep(cls[start[len(start)-1]]) {
			continue
		}
		start = append(start, i)
	}
	unitOf := make(map[int]int, len(start))
	for u, s := range start {
		unitOf[s] = u
	}
	ucls := func(u int) uint8 {
		if u < 0 || u >= len(start) {
			return 0
		}
		return cls[start[u]]
	}
	out := []int{0}
	for i := 1; i < n; i++ {
		a, b := cls[i-1], cls[i]
		brk := false
		switch {
		case a == sbCR && b == sbLF:
		case isParaSep(a): // SB4
			brk = true
		case b == sbExtend || b == sbFormat: // SB5
		default:
			u, ok := unitOf[i]
			if !ok {
				break
			}
			A, B := ucls(u-1), ucls(u)
			// SATerm Close* Sp*
			j := u - 1
			for j >= 0 && ucls(j) == sbSp {
				j--
			}
			sp := j != u-1
			for j >= 0 && ucls(j) == sbClose {
				j--
			}
			close := j
			haveTerm := j >= 0 && isSATerm(ucls(j))
			switch {
			case A == sbATerm && B == sbNumeric: // SB6
			case (ucls(u-2) == sbUpper || ucls(u-2) == sbLower) && A == sbATerm && B == sbUpper: // SB7
			case haveTerm && ucls(j) == sbATerm && sb8(ucls, start, u, len(start)): // SB8
			case haveTerm && (B == sbSContinue || isSATerm(B)): // SB8a
			case haveTerm && !sp && (B == sbClose || B == sbSp || isParaSep(B)): // SB9
			case haveTerm && (B == sbSp || isParaSep(B)): // SB10
			case haveTerm: // SB11
				brk = true
			}
			_ = close
		}
		if brk {
			out = append(out, i)
		}
	}
	return append(out, n)
}

// sb8: after ATerm Close* Sp*, a run of anything but OLetter, Upper, Lower, ParaSep and SATerm that ends in Lower.
func sb8(ucls func(int) uint8, start []int, u, nu int) bool {
	for v := u; v < nu; v++ {
		c := ucls(v)
		switch {
		case c == sbLower:
			return true
		case c == sbOLetter || c == sbUpper || isParaSep(c) || isSATerm(c):
			return false
		}
	}
	return false
}
