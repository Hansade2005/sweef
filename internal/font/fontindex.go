package font

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The font index caches what probing learned about each system font file
// (family, style, names), keyed by path, size and modification time, so a new
// process learns the system font set from a few stat calls instead of opening
// every file. It lives in the engine's cache directory ($SWEEF_CACHE_DIR, else
// the user cache dir + /sweef; "off" disables it).

const fontIndexName = "fontindex-v1.json"

type indexFace struct {
	Family string `json:"f"`
	Weight int    `json:"w"`
	Italic bool   `json:"i,omitempty"`
	Mono   bool   `json:"m,omitempty"`
	Idx    int    `json:"x"`
	Full   string `json:"n,omitempty"`
	PS     string `json:"p,omitempty"`
}

type indexFile struct {
	Size  int64       `json:"s"`
	MTime int64       `json:"t"`
	Faces []indexFace `json:"faces"`
}

func fontIndexPath() string {
	d := os.Getenv("SWEEF_CACHE_DIR")
	if d == "off" {
		return ""
	}
	if d == "" {
		c, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		d = filepath.Join(c, "sweef")
	}
	return filepath.Join(d, fontIndexName)
}

func readFontIndex(path string) map[string]indexFile {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]indexFile
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func writeFontIndex(path string, m map[string]indexFile) {
	if path == "" {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// cachedEntries returns the index's faces of file p when its size and mtime match.
func cachedEntries(idx map[string]indexFile, p string, fi os.FileInfo) ([]*entry, bool) {
	c, ok := idx[p]
	if !ok || fi == nil || c.Size != fi.Size() || c.MTime != fi.ModTime().UnixNano() {
		return nil, false
	}
	out := make([]*entry, 0, len(c.Faces))
	for _, f := range c.Faces {
		out = append(out, &entry{family: f.Family, style: Style{Weight: f.Weight, Italic: f.Italic}, path: p, mono: f.Mono, idx: f.Idx, full: f.Full, ps: f.PS})
	}
	return out, true
}

func indexEntry(fi os.FileInfo, es []*entry) indexFile {
	c := indexFile{Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Faces: []indexFace{}}
	for _, e := range es {
		c.Faces = append(c.Faces, indexFace{Family: e.family, Weight: e.style.Weight, Italic: e.style.Italic, Mono: e.mono, Idx: e.idx, Full: e.full, PS: e.ps})
	}
	return c
}
