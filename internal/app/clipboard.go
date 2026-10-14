package app

// clipboard.go: the system clipboard as the engine sees it (engine.Clipboard) and as the copy and paste keys use it.
//
// The window's clipboard (shell.Window) carries text. A page can also put HTML and images on it through the Async
// Clipboard API or a copy event; those flavours are kept here, next to the text they were written with, and are
// offered again only while the system clipboard still holds exactly that text. Copying something else, in this
// browser or in another application, replaces the text and so retires them. Images from other applications are not
// read: the shell has no image clipboard.

import (
	"context"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

type appClip struct {
	a      *App
	mu     sync.Mutex
	rich   []engine.ClipboardEntry
	richOS string // the system text at the time the rich flavours were written
}

func newAppClip(a *App) *appClip { return &appClip{a: a} }

// readNow returns the flavours on the clipboard. UI loop only.
func (c *appClip) readNow() []engine.ClipboardEntry {
	text := c.a.win.Clipboard()
	c.mu.Lock()
	rich, at := c.rich, c.richOS
	c.mu.Unlock()
	if len(rich) > 0 && text == at {
		out := make([]engine.ClipboardEntry, len(rich))
		copy(out, rich)
		return out
	}
	if text == "" {
		return nil
	}
	return []engine.ClipboardEntry{{Type: "text/plain", Data: []byte(text)}}
}

// writeNow replaces the clipboard. UI loop only.
func (c *appClip) writeNow(es []engine.ClipboardEntry) error {
	var text string
	hasText := false
	for _, e := range es {
		if e.Type == "text/plain" {
			text, hasText = string(e.Data), true
		}
	}
	if hasText {
		c.a.win.SetClipboard(text)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(es) == 1 && hasText {
		c.rich, c.richOS = nil, "" // plain text: the system clipboard has all of it
		return nil
	}
	c.rich = append([]engine.ClipboardEntry(nil), es...)
	c.richOS = c.a.win.Clipboard()
	return nil
}

// Read implements engine.Clipboard (called from page goroutines).
func (c *appClip) Read() (out []engine.ClipboardEntry, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = c.a.onLoop(ctx, func() { out = c.readNow() })
	return
}

// Write implements engine.Clipboard (called from page goroutines).
func (c *appClip) Write(es []engine.ClipboardEntry) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	werr := c.a.onLoop(ctx, func() { err = c.writeNow(es) })
	if werr != nil {
		return werr
	}
	return err
}
