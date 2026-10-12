package state

import "reflect"

// Diff returns the RFC 7386 JSON merge patch that turns old into new. Both
// must be normalised (see Normalize). Arrays are replaced wholesale, as the
// RFC requires. The result is nil when nothing changed.
func Diff(old, new map[string]any) map[string]any {
	patch := map[string]any{}
	for k, nv := range new {
		ov, had := old[k]
		if !had {
			patch[k] = nv
			continue
		}
		om, ok1 := ov.(map[string]any)
		nm, ok2 := nv.(map[string]any)
		if ok1 && ok2 {
			if sub := Diff(om, nm); sub != nil {
				patch[k] = sub
			}
			continue
		}
		if !reflect.DeepEqual(ov, nv) {
			patch[k] = nv
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			patch[k] = nil
		}
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
}

// Apply applies a merge patch to target in place and returns it.
func Apply(target, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, pv := range patch {
		if pv == nil {
			delete(target, k)
			continue
		}
		if pm, ok := pv.(map[string]any); ok {
			tm, ok := target[k].(map[string]any)
			if !ok {
				tm = map[string]any{}
			}
			target[k] = Apply(tm, pm)
			continue
		}
		target[k] = pv
	}
	return target
}
