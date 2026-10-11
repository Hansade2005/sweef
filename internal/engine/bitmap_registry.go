package engine

// bitmap_registry.go — how live pixel sources (2D canvas, WebGL drawing
// buffers, video frames) publish their current contents so layout can size
// them and screenshots, PDF, drawImage and toDataURL can read them.
//
// A source is registered per (tab, document, node id). The node id is the DOM
// id the prelude sees through __swIdOf(element) / the Go side through
// Doc.IDOf(node). Registering again for the same key replaces the source.

import (
	"image"
	"sync"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// BitmapSource is anything that can report its current pixels.
type BitmapSource interface {
	// Bitmap returns the current contents (a snapshot is fine; callers must
	// not mutate it). Nil means "nothing drawn yet".
	Bitmap() image.Image
	// Size is the intrinsic size in pixels (canvas width/height attributes,
	// video videoWidth/videoHeight); 0,0 when unknown.
	Size() (w, h int)
}

type bitmapKey struct {
	tab  string
	doc  *fnet.Doc
	node int64
}

var (
	bitmapMu      sync.RWMutex
	bitmapSources = map[bitmapKey]BitmapSource{}
)

// SetBitmapSource registers or replaces the source for a node of the tab's
// current document; a nil src removes it.
func (t *Tab) SetBitmapSource(nodeID int64, src BitmapSource) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	k := bitmapKey{t.ID, d, nodeID}
	bitmapMu.Lock()
	if src == nil {
		delete(bitmapSources, k)
	} else {
		bitmapSources[k] = src
	}
	bitmapMu.Unlock()
}

// BitmapSourceFor returns the registered source for a node, or nil.
func (t *Tab) BitmapSourceFor(n *html.Node) BitmapSource {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil || n == nil {
		return nil
	}
	bitmapMu.RLock()
	defer bitmapMu.RUnlock()
	return bitmapSources[bitmapKey{t.ID, d, d.IDOf(n)}]
}

// dropBitmapSources forgets every source of a closed tab.
func dropBitmapSources(tabID string) {
	bitmapMu.Lock()
	for k := range bitmapSources {
		if k.tab == tabID {
			delete(bitmapSources, k)
		}
	}
	bitmapMu.Unlock()
}
