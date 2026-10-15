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
