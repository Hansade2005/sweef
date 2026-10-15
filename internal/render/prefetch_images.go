package render

import "strings"

// ImagePrefetcher is an optional extension of Env. PrefetchImages starts loading images a paint pass is about
// to ask for through Env.Image, one at a time, so their downloads overlap instead of running back to back (a
// page with twenty CSS icons paid twenty round trips in every layout). It must not block; Image then finds each
// request in flight or done.
type ImagePrefetcher interface {
	PrefetchImages(urls []string)
}

// prefetchPaintImages hands the embedder the CSS images (backgrounds, masks, list markers) of every rendered
// element before paintAll asks for them.
func (c *ctx) prefetchPaintImages() {
	pf, ok := c.env.(ImagePrefetcher)
	if !ok {
		return
	}
	var urls []string
	seen := map[string]bool{}
	add := func(u string) {
		if u == "" || seen[u] || strings.HasPrefix(u, "data:") || strings.HasPrefix(u, "#") {
			return
		}
		seen[u] = true
		urls = append(urls, u)
	}
	for n, st := range c.styles {
		if st == nil || st.Display == "none" || len(c.byNode[n]) == 0 {
			continue
		}
		for _, ly := range st.BgLayers {
			if ly.Image.Grad == nil {
				add(ly.Image.URL)
			}
		}
		for _, ly := range st.Mask {
			if ly.Image.Grad == nil {
				add(ly.Image.URL)
			}
		}
		if st.ListStyleImage.Grad == nil {
			add(st.ListStyleImage.URL)
		}
	}
	if len(urls) > 1 {
		pf.PrefetchImages(urls)
	}
}
