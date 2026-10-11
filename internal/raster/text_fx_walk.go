package raster

import (
	"slices"
	"sort"
)

// fxWalk accumulates the coverage of edges row segment by row segment the way
// SkScan_AAAPath's aaa_walk_edges does: the active edges are ordered by x, the
// winding number decides which pairs bound an interval, and every interval of
// a segment contributes the exact area of its trapezoid. Unlike plain signed
// area accumulation this takes the union of overlapping shapes (a glyph and
// the stroke around it) instead of adding their partial coverages.
func fxWalk(r *rasterizer, edges []fxEdge, ox, oy float64) {
	if len(edges) == 0 {
		return
	}
	ys := make([]int, 0, len(edges)*2+64)
	minY, maxY := edges[0].y0, edges[0].y1
	for _, e := range edges {
		ys = append(ys, e.y0, e.y1)
		minY = fxMin(minY, e.y0)
		maxY = fxMax(maxY, e.y1)
	}
	for y := (minY>>16 + 1) << 16; y < maxY; y += fxOne {
		ys = append(ys, y)
	}
	sort.Ints(ys)
	ys = slices.Compact(ys)
	const k = 1.0 / fxOne
	order := make([]int, len(edges))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return edges[order[a]].y0 < edges[order[b]].y0 })
	type act struct {
		xa, xb int
		e      *fxEdge
	}
	next := 0
	var live []int
	var cur []act
	for n := 0; n+1 < len(ys); n++ {
		ya, yb := ys[n], ys[n+1]
		for next < len(order) && edges[order[next]].y0 <= ya {
			live = append(live, order[next])
			next++
		}
		cur = cur[:0]
		keep := live[:0]
		for _, i := range live {
			e := &edges[i]
			if e.y1 <= ya {
				continue
			}
			keep = append(keep, i)
			cur = append(cur, act{e.x + fxMul(e.slope, ya-e.y0), e.x + fxMul(e.slope, yb-e.y0), e})
		}
		live = keep
		if len(cur) < 2 {
			continue
		}
		sort.SliceStable(cur, func(i, j int) bool {
			if cur[i].xa != cur[j].xa {
				return cur[i].xa < cur[j].xa
			}
			return cur[i].e.slope < cur[j].e.slope
		})
		w, left := 0, -1
		for i := range cur {
			prev := w
			w += cur[i].e.wind
			if prev == 0 && w != 0 {
				left = i
			} else if prev != 0 && w == 0 && left >= 0 {
				l, rt := cur[left], cur[i]
				left = -1
				if l.xa > rt.xa {
					continue
				}
				fy0, fy1 := float64(ya)*k+oy, float64(yb)*k+oy
				r.line(float64(l.xa)*k+ox, fy0, float64(l.xb)*k+ox, fy1)
				r.line(float64(rt.xb)*k+ox, fy1, float64(rt.xa)*k+ox, fy0)
			}
		}
	}
}
