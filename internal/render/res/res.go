// Package res connects layout and painting to the resources pages draw:
// decoded images by URL, and "replaced" bitmaps owned by other subsystems
// (a canvas element's backing store, a WebGL drawing buffer, a video's
// current frame). Providers register at init; the engine supplies a
// Loader per tab for network images.
package res

import (
	"image"
	"sync"

	"golang.org/x/net/html"
)

// Replaced is what a provider reports for an element it owns.
type Replaced struct {
	// Image is the current bitmap (nil = nothing to draw yet).
	Image image.Image
	// IntrinsicW/H are the element's natural size in CSS px (canvas
	// width/height attributes, video dimensions); 0 when unknown.
	IntrinsicW, IntrinsicH float64
}

// Provider answers for elements it owns; ok=false means "not mine".
type Provider func(n *html.Node) (Replaced, bool)

var (
	mu        sync.RWMutex
	providers []Provider
)

// RegisterProvider adds a replaced-element provider (call from init).
func RegisterProvider(p Provider) {
	mu.Lock()
	providers = append(providers, p)
	mu.Unlock()
}

// Lookup asks providers about n in registration order.
func Lookup(n *html.Node) (Replaced, bool) {
	mu.RLock()
	ps := providers
	mu.RUnlock()
	for _, p := range ps {
		if r, ok := p(n); ok {
			return r, true
		}
	}
	return Replaced{}, false
}

// Loader fetches and decodes images for one page. Image returns the
// decoded image for an absolute URL, or ok=false when it is not (yet)
// available; implementations may load synchronously with a short budget.
type Loader interface {
	Image(url string) (img image.Image, ok bool)
	// ResolveURL makes a reference absolute against the document base.
	ResolveURL(ref string) string
}

// NoLoader is a Loader with no images.
type NoLoader struct{}

func (NoLoader) Image(string) (image.Image, bool) { return nil, false }
func (NoLoader) ResolveURL(r string) string      { return r }
