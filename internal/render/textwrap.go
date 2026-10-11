package render

import (
	"math"
	"sort"
)

// text-wrap: balance and pretty.
//
// balance spreads the text over the number of lines the greedy breaker
// produced, minimising the sum of squared slack of all lines (like Blink's
// score-based breaker); pretty only changes the last (up to four) lines, and
// only to avoid a last line holding a single word, minimising the slack of
// the lines before it. The chosen breaks are enforced on a second layout pass
// through lbox.force, so text-align applies within the full line width.

// planBreaks returns the atom indices that must start a line, nil when the
// greedy breaks stay. ok is false when the content cannot be planned
// (floats, atomic inlines, forced breaks, no-wrap text).
func (c *ctx) planBreaks(b *lbox, atoms []atom, lstarts []int, cw, indent float64, mode string, bfc *bfcState) (map[int]bool, bool) {
	K := len(lstarts)
	n := len(atoms)
	if K < 2 || n == 0 || len(bfc.floats) > 0 || (mode == "balance" && K > 6) {
		return nil, false
	}
	w := make([]float64, n)
	for k, a := range atoms {
		switch a.kind {
		case aText:
			if a.tab || a.it.st.WhiteSpace == "pre" || a.it.st.WhiteSpace == "nowrap" || a.it.st.WhiteSpace == "pre-wrap" || a.it.st.WhiteSpace == "pre-line" || a.it.st.WhiteSpace == "break-spaces" {
				return nil, false
			}
			w[k] = a.w
		case aOpen:
			w[k] = a.box.mar[3] + a.box.bor[3] + a.box.pad[3]
		case aClose:
			w[k] = a.box.mar[1] + a.box.bor[1] + a.box.pad[1]
		default:
			return nil, false
		}
	}
	// break positions: index of the first atom of the next line
	var pos []int
	pos = append(pos, 0)
	for j := 1; j < n; j++ {
		if atoms[j-1].kind == aText && atoms[j-1].brk {
			pos = append(pos, j)
		}
	}
	pos = append(pos, n)
	pi := map[int]int{}
	for i, p := range pos {
		pi[p] = i
	}
	for _, s := range lstarts {
		if _, ok := pi[s]; !ok {
			return nil, false
		}
	}
	lineW := func(s, e int) float64 {
		x := 0.0
		k := s
		for k < e && atoms[k].kind == aText && atoms[k].space {
			k++ // collapsible space at line start is skipped
		}
		for ; k < e; k++ {
			x += w[k]
		}
		for k = e - 1; k >= s; k-- {
			if atoms[k].kind == aText {
				x -= atoms[k].trail
				break
			}
			if atoms[k].kind != aClose && atoms[k].kind != aOpen {
				break
			}
		}
		return x
	}
	words := func(s, e int) int {
		cnt := 0
		for k := s; k < e; k++ {
			if atoms[k].kind == aText && !atoms[k].space {
				cnt++
			}
		}
		return cnt
	}
	avail := func(first bool) float64 {
		if first {
			return cw - indent
		}
		return cw
	}
	const eps = 0.01
	inf := math.Inf(1)
	// partition positions pos[from..] into m lines ending at n
	solve := func(from, m int, firstIsLine0 bool, lastMinWords int, includeLast bool) []int {
		P := len(pos)
		f := make([][]float64, m+1)
		bk := make([][]int, m+1)
		for k := range f {
			f[k] = make([]float64, P)
			bk[k] = make([]int, P)
			for j := range f[k] {
				f[k][j] = inf
			}
		}
		f[0][from] = 0
		for k := 1; k <= m; k++ {
			for j := from + 1; j < P; j++ {
				for i := from; i < j; i++ {
					if f[k-1][i] == inf {
						continue
					}
					s, e := pos[i], pos[j]
					av := avail(firstIsLine0 && k == 1)
					lw := lineW(s, e)
					if lw > av+eps {
						continue
					}
					cost := 0.0
					last := j == P-1
					if !last || includeLast {
						cost = (av - lw) * (av - lw)
					}
					if last && lastMinWords > 0 && words(s, e) < lastMinWords {
						continue
					}
					if f[k-1][i]+cost < f[k][j]-1e-9 {
						f[k][j] = f[k-1][i] + cost
						bk[k][j] = i
					}
				}
			}
		}
		if f[m][P-1] == inf {
			return nil
		}
		out := make([]int, m)
		j := P - 1
		for k := m; k >= 1; k-- {
			out[k-1] = pos[bk[k][j]]
			j = bk[k][j]
		}
		return out // start atom index of each of the m lines
	}
	var starts []int
	switch mode {
	case "balance":
		starts = solve(0, K, true, 0, true)
	default:
		// an orphan: a single word on the last line, shorter than a third of the line
		if words(lstarts[K-1], n) != 1 || lineW(lstarts[K-1], n) >= avail(false)/3 {
			return nil, true
		}
		ws := K - 4
		if ws < 0 {
			ws = 0
		}
		tail := solve(pi[lstarts[ws]], K-ws, ws == 0, 2, false)
		if tail == nil {
			return nil, true
		}
		starts = append(append([]int(nil), lstarts[:ws]...), tail...)
	}
	if starts == nil || len(starts) != K {
		return nil, true
	}
	same := true
	for i := range starts {
		if starts[i] != lstarts[i] {
			same = false
		}
	}
	if same {
		return nil, true
	}
	sort.Ints(starts)
	plan := map[int]bool{}
	for _, s := range starts[1:] {
		plan[s] = true
	}
	return plan, true
}
