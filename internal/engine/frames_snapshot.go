package engine

// frames_snapshot.go: the accessibility snapshot of a page includes its frames. Each iframe line is followed by the
// frame document's own tree, with the refs inside it qualified by the frame's path (f1e5, f1f2e5), so one snapshot
// covers the page and a click, fill or type by ref reaches the element inside the frame (routeFrameSel).

import (
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

var (
	refAttrRE   = regexp.MustCompile(`ref=((?:f\d+)*e\d+)\]`)
	frameAttrRE = regexp.MustCompile(`frame=((?:f\d+)+)\]`)
)

// qualifyRefs rewrites "[ref=e5]" to "[ref=f1e5]" (and "f2e5" to "f1f2e5", "[frame=f2]" to "[frame=f1f2]") in a
// snapshot line.
func qualifyRefs(line, prefix string) string {
	line = refAttrRE.ReplaceAllString(line, "ref="+prefix+"$1]")
	return frameAttrRE.ReplaceAllString(line, "frame="+prefix+"$1]")
}

// snapshotFrame returns, for the iframe element n, the frame's accessibility tree with every ref qualified by the
// frame's prefix, and the prefix.
func (t *Tab) snapshotFrame(n *html.Node, o fnet.SnapshotOptions) (string, []string) {
	var elem int64
	_ = t.withDoc(func(d *fnet.Doc) error { elem = d.IDOf(n); return nil })
	c := t.frameKid(elem)
	if c == nil || c.closed.Load() || !t.eng.frameReadable(c.URL()) {
		return "", nil
	}
	idx := c.frameIndex()
	if idx == 0 {
		return "", nil
	}
	c.WaitFrames(2 * time.Second)
	o.Selector = ""
	o.Frame = func(m *html.Node) (string, []string) { return c.snapshotFrame(m, o) }
	var y string
	err := c.withDoc(func(d *fnet.Doc) error {
		var e error
		y, e = d.AriaSnapshotV2(o)
		return e
	})
	if err != nil || strings.HasPrefix(y, "- text: (no interactive elements") || strings.HasPrefix(y, "- text: (page has no visible content") {
		return "", nil
	}
	prefix := "f" + itoa(idx)
	lines := strings.Split(strings.TrimRight(y, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = qualifyRefs(ln, prefix)
	}
	return prefix, lines
}

// OriginOrNull is the document's origin as scripts see it ("null" when opaque).
func (t *Tab) OriginOrNull() string { return originOrNull(t) }

// FrameRefs lists the frames of the document (direct children) with the ref prefix their snapshot refs carry.
type FrameRef struct {
	Ref string
	Tab *Tab
}

// FrameRefs lists the readable child frames in tree order with their refs (f1, f2, ...).
func (t *Tab) FrameRefs() []FrameRef {
	var out []FrameRef
	for i, k := range t.FrameTabs() {
		if t.eng.frameReadable(k.URL()) {
			out = append(out, FrameRef{Ref: "f" + itoa(i+1), Tab: k})
		}
	}
	return out
}
