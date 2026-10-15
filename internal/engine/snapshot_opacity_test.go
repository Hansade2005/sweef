package engine

import (
	"strings"
	"testing"
)

// Content that animates in from opacity 0 is in the accessibility tree (as in Chromium) and so in the snapshot;
// display:none and visibility:hidden content is not.
func TestSnapshotKeepsOpacityZero(t *testing.T) {
	e := New(Options{CacheDir: "off"})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetDocument(`<!doctype html><body><h1 style="opacity:0">Fading in</h1><p style="display:none">Gone</p><p style="visibility:hidden">Hidden</p></body>`, "https://op.test/")
	snap, err := tab.AriaSnapshotYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap, "Fading in") || strings.Contains(snap, "Gone") || strings.Contains(snap, "Hidden") {
		t.Fatalf("snapshot:\n%s", snap)
	}
}
