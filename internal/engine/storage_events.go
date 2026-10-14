package engine

// storage_events.go: the "storage" event. A change to localStorage fires an event in the other documents that share
// the storage area (same partitioned storage key, same browser context): other tabs, popups and frames. The writer
// itself gets none. The event travels on the frame event channel (frame_prelude.js, __swFrameEvent).

import "encoding/json"

// storageChanged tells the other documents that localStorage of key lk changed. key == nil means clear().
func (j *jsVM) storageChanged(lk string, key, oldV, newV *string) {
	me := j.tab
	if me == nil {
		return
	}
	msg := map[string]any{"t": "storage", "key": key, "old": oldV, "new": newV, "url": me.URL()}
	if _, err := json.Marshal(msg); err != nil {
		return
	}
	winReg.Range(func(_, v any) bool {
		w, ok := v.(*Tab)
		if !ok || w == me || w.closed.Load() || w.Context() != me.Context() {
			return true
		}
		o := w.docOrigin()
		if o == "" || w.storageKeyFor(o) != lk {
			return true
		}
		w.mu.RLock()
		wj := w.js
		w.mu.RUnlock()
		if wj != nil {
			pushFrame(wj, msg)
		}
		return true
	})
}
