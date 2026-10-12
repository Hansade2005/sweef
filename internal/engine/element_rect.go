package engine

// RectByRef returns the layout rectangle {x, y, w, h} (CSS px, document
// space) of the element a snapshot ref such as "e12" points to. The GUI uses
// it to place the agent's cursor; ok is false when the ref is unknown or the
// element has no box.
func (t *Tab) RectByRef(ref string) (r [4]float64, ok bool) {
	t.mu.RLock()
	dom := t.dom
	t.mu.RUnlock()
	if dom == nil {
		return r, false
	}
	infos, err := dom.ResolveEngine(ref)
	if err != nil || len(infos) == 0 {
		return r, false
	}
	id := infos[0].BackendID
	if g, found := dom.GeometryOf(id); found {
		c := g.Client
		return c, c[2] > 0 && c[3] > 0
	}
	b, err := dom.BoundingBox(id)
	if err != nil {
		return r, false
	}
	return [4]float64{float64(b[0]), float64(b[1]), float64(b[2]), float64(b[3])}, b[2] > 0 && b[3] > 0
}
