package font

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lazy probe (ReaderAt, a few tables) must learn exactly what the
// whole-file probe learns, for every font installed on the machine.
func TestLazyProbeMatchesFullProbe(t *testing.T) {
	n := 0
	for _, d := range systemDirs() {
		filepath.WalkDir(d, func(p string, de os.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".ttf" && ext != ".otf" && ext != ".ttc" {
				return nil
			}
			idxs := []int{-1}
			if ext == ".ttc" {
				idxs = nil
				for _, e := range probeAll(p, true) {
					idxs = append(idxs, e.idx)
				}
			}
			for _, i := range idxs {
				lazy, full := probe(p, i), probeFull(p, i)
				if (lazy == nil) != (full == nil) || lazy != nil && (lazy.family != full.family || lazy.style != full.style ||
					lazy.mono != full.mono || lazy.full != full.full || lazy.ps != full.ps) {
					t.Errorf("%s #%d: lazy %+v, full %+v", p, i, lazy, full)
				}
				n++
			}
			return nil
		})
	}
	if n == 0 {
		t.Skip("no system fonts")
	}
	t.Logf("%d faces compared", n)
}

// The font index gives a later process the same entries as probing.
func TestFontIndexRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SWEEF_CACHE_DIR", dir)
	var p string
	var fi os.FileInfo
	for _, d := range systemDirs() {
		filepath.WalkDir(d, func(q string, de os.DirEntry, err error) error {
			if p == "" && err == nil && !de.IsDir() && strings.EqualFold(filepath.Ext(q), ".ttf") {
				p = q
				fi, _ = os.Stat(q)
			}
			return nil
		})
	}
	if p == "" {
		t.Skip("no system .ttf")
	}
	es := probeAll(p, false)
	writeFontIndex(fontIndexPath(), map[string]indexFile{p: indexEntry(fi, es)})
	got, ok := cachedEntries(readFontIndex(fontIndexPath()), p, fi)
	if !ok || len(got) != len(es) || got[0].family != es[0].family || got[0].style != es[0].style || got[0].full != es[0].full {
		t.Fatalf("index round trip: ok=%v got %+v want %+v", ok, got, es)
	}
}
