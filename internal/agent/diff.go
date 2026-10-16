package agent

import (
	"fmt"
	"strings"
)

// diff.go - snapshot deltas, truncation and ref-label extraction.
//
// A snapshot enters the conversation once in full. Later observations of the
// same page are sent as a delta computed here against the text the model has
// actually seen. We do not reuse rpc.LineDiff: it allocates an (n+1)x(m+1)
// int table (128 MB at its own 16M-cell limit), while real page updates
// change a few lines inside a large unchanged tree. Trimming the common
// prefix/suffix first reduces the DP to the changed middle.

const noChangeText = DeltaPrefix + ": no changes since the last snapshot"

// DeltaText renders the difference between two snapshots. Format:
//
//	# delta (+ added, - removed; unchanged lines omitted)
//	@ <closest unchanged line above the change>
//	- <removed line>
//	+ <added line>
//
// It returns noChangeText when the snapshots are equal.
func DeltaText(prev, cur string) string {
	if prev == cur {
		return noChangeText
	}
	a := splitLines(prev)
	b := splitLines(cur)
	ops := diffLines(a, b)
	var sb strings.Builder
	sb.Grow(len(cur) / 8)
	sb.WriteString(DeltaPrefix + " (+ added, - removed; unchanged lines omitted)\n")
	lastCtx := ""
	inHunk := false
	for _, op := range ops {
		if op.kind == ' ' {
			lastCtx = op.line
			inHunk = false
			continue
		}
		if !inHunk {
			if lastCtx != "" {
				sb.WriteString("@ ")
				sb.WriteString(clip(strings.TrimSpace(lastCtx), 160))
				sb.WriteByte('\n')
			}
			inHunk = true
		}
		sb.WriteByte(op.kind)
		sb.WriteByte(' ')
		sb.WriteString(op.line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// IsDelta reports whether s is a delta rendering.
func IsDelta(s string) bool { return strings.HasPrefix(s, DeltaPrefix) }

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

type diffOp struct {
	kind byte // ' ', '+', '-'
	line string
}

// diffLines returns a full edit script. Common prefix/suffix are trimmed; the
// middle uses an LCS table when small and a multiset diff otherwise.
func diffLines(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ops := make([]diffOp, 0, len(b)+8)
	for i := 0; i < pre; i++ {
		ops = append(ops, diffOp{' ', a[i]})
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(am)*len(bm) <= 4_000_000 {
		ops = append(ops, lcsOps(am, bm)...)
	} else {
		ops = append(ops, multisetOps(am, bm)...)
	}
	for i := len(a) - suf; i < len(a); i++ {
		ops = append(ops, diffOp{' ', a[i]})
	}
	return ops
}

func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		out := make([]diffOp, 0, n+m)
		for _, l := range a {
			out = append(out, diffOp{'-', l})
		}
		for _, l := range b {
			out = append(out, diffOp{'+', l})
		}
		return out
	}
	w := m + 1
	dp := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i*w+j] = dp[(i+1)*w+j+1] + 1
			case dp[(i+1)*w+j] >= dp[i*w+j+1]:
				dp[i*w+j] = dp[(i+1)*w+j]
			default:
				dp[i*w+j] = dp[i*w+j+1]
			}
		}
	}
	out := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffOp{' ', a[i]})
			i++
			j++
		case dp[(i+1)*w+j] >= dp[i*w+j+1]:
			out = append(out, diffOp{'-', a[i]})
			i++
		default:
			out = append(out, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffOp{'+', b[j]})
	}
	return out
}

// multisetOps is the large-input fallback: order-preserving, no alignment.
func multisetOps(a, b []string) []diffOp {
	cnt := make(map[string]int, len(a))
	for _, l := range a {
		cnt[l]++
	}
	var out []diffOp
	for _, l := range b {
		if cnt[l] > 0 {
			cnt[l]--
			out = append(out, diffOp{' ', l})
		} else {
			out = append(out, diffOp{'+', l})
		}
	}
	for _, l := range a { // removed lines, in original order
		if cnt[l] > 0 {
			cnt[l]--
			out = append(out, diffOp{'-', l})
		}
	}
	return out
}

// ---- token estimate & truncation ------------------------------------------

// EstimateTokens is a cheap token estimate (about 4 bytes per token for
// English/markup, which slightly over-estimates snapshot YAML).
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

var refRe = lazyRE(`ref=(e\d+)`)

func hasRef(line string) bool { return strings.Contains(line, "ref=e") }

const maxLineChars = 240

// TruncateSnapshot caps a snapshot at about maxTokens tokens. Interactive
// lines (those carrying a [ref=eN]) are kept first, in document order, which
// approximates "near the top of the viewport first" because the engine emits
// the tree in layout order; the rest of the budget goes to non-interactive
// lines (headings, text) in document order. Output preserves the original
// order and ends with a notice naming what was dropped.
func TruncateSnapshot(s string, maxTokens int) (string, bool) {
	if maxTokens <= 0 || EstimateTokens(s) <= maxTokens {
		return s, false
	}
	lines := splitLines(s)
	cost := make([]int, len(lines))
	for i, l := range lines {
		if len(l) > maxLineChars {
			l = l[:maxLineChars]
		}
		cost[i] = EstimateTokens(l) + 1
	}
	keep := make([]bool, len(lines))
	budget := maxTokens - 40 // room for the notice
	used := 0
	// pass 1: interactive lines, up to 85% of the budget
	limit1 := budget * 85 / 100
	for i, l := range lines {
		if hasRef(l) && used+cost[i] <= limit1 {
			keep[i] = true
			used += cost[i]
		}
	}
	// pass 2: everything else in document order
	for i := range lines {
		if !keep[i] && used+cost[i] <= budget {
			keep[i] = true
			used += cost[i]
		}
	}
	var sb strings.Builder
	kept := 0
	droppedRefs := 0
	for i, l := range lines {
		if !keep[i] {
			if hasRef(l) {
				droppedRefs++
			}
			continue
		}
		kept++
		if len(l) > maxLineChars {
			l = l[:maxLineChars] + "..."
		}
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	fmt.Fprintf(&sb, "[snapshot truncated: kept %d of %d lines (interactive elements first); %d interactive elements not shown - use find(query) or scroll and read_page]\n",
		kept, len(lines), droppedRefs)
	return sb.String(), true
}

// ---- ref labels ------------------------------------------------------------

var lineRe = lazyRE(`^\s*(?:[+-] )?- ([A-Za-z][\w-]*)(?: "((?:[^"\\]|\\.)*)")?.*?ref=(e\d+)`)
var urlRe = lazyRE(`^\s*(?:[+-] )?- /url: (\S+)`)

// refInfo is what we remember about a ref from the latest snapshot text.
type refInfo struct {
	Role  string
	Name  string
	Href  string
	Extra string // the raw bracket attributes, e.g. "checked", "type=password"
}

func (r refInfo) label() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Role
}

// parseRefs extracts ref -> info from a full snapshot or delta.
func parseRefs(s string, into map[string]refInfo) {
	lastRef := ""
	for _, line := range strings.Split(s, "\n") {
		if m := lineRe().FindStringSubmatch(line); m != nil {
			info := refInfo{Role: m[1], Name: strings.ReplaceAll(m[2], `\"`, `"`)}
			if i := strings.Index(line, "["); i >= 0 {
				if j := strings.LastIndex(line, "]"); j > i {
					info.Extra = line[i+1 : j]
				}
			}
			into[m[3]] = info
			lastRef = m[3]
			continue
		}
		if m := urlRe().FindStringSubmatch(line); m != nil && lastRef != "" {
			info := into[lastRef]
			info.Href = m[1]
			into[lastRef] = info
		}
	}
}
