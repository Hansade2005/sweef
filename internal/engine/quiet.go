package engine

import "sync"

// quiet.go: changes that only touch part of the page.
//
// Most changes to a document (a script moved a node, a style changed) can alter any pixel, and the whole picture
// is made again. A few cannot: moving the selection or the caret, highlighting a find result, scrolling an
// overflow container. Those say which rows of the page they can touch and are counted here. A window that
// compares the document's generation with how many quiet changes there were since its last paint knows that
// nothing else happened, and repaints only those rows.

type quietState struct {
	mu       sync.Mutex
	n        uint64
	top, bot float64 // document rows (CSS px) touched since they were last taken
	has      bool
}

// addRows records a quiet change that can touch the document rows [top, bot).
func (q *quietState) addRows(top, bot float64) {
	q.mu.Lock()
	q.n++
	if !q.has {
		q.top, q.bot, q.has = top, bot, true
	} else {
		if top < q.top {
			q.top = top
		}
		if bot > q.bot {
			q.bot = bot
		}
	}
	q.mu.Unlock()
}

// QuietCount is how many quiet changes the document has had. Generation moves by one for each of them (and by
// more for anything else).
func (t *Tab) QuietCount() uint64 {
	t.quiet.mu.Lock()
	defer t.quiet.mu.Unlock()
	return t.quiet.n
}

// TakeQuietRows returns the document rows the quiet changes since the last call can have touched.
func (t *Tab) TakeQuietRows() (top, bottom float64, ok bool) {
	q := &t.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.has {
		return 0, 0, false
	}
	top, bottom = q.top, q.bot
	q.has = false
	return top, bottom, true
}
